// Package onboarding connects the Telegram bot to control-plane and billing.
package onboarding

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/delivery/internal/channel/telegram"
)

const (
	maxResponseBytes = 64 << 10
	maxAttempts      = 3
)

type Service struct {
	cp            serviceClient
	billing       serviceClient
	publicSubBase string
	now           func() time.Time
}

func New(controlPlaneBase, controlPlaneToken, billingBase, billingToken, publicSubBase string, httpClient *http.Client, retryDelay time.Duration) *Service {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &Service{
		cp:            serviceClient{base: strings.TrimRight(controlPlaneBase, "/"), token: controlPlaneToken, http: httpClient, retryDelay: retryDelay},
		billing:       serviceClient{base: strings.TrimRight(billingBase, "/"), token: billingToken, http: httpClient, retryDelay: retryDelay},
		publicSubBase: strings.TrimRight(publicSubBase, "/"),
		now:           time.Now,
	}
}

func (s *Service) EnsureUser(ctx context.Context, senderID int64) error {
	_, err := s.ensureUser(ctx, senderID)
	return err
}

func (s *Service) Catalog(ctx context.Context) ([]telegram.Plan, error) {
	var response contracts.BillingPlanOffers
	if err := s.billing.doJSON(ctx, http.MethodGet, "/v1/plans", nil, nil, &response, http.StatusOK); err != nil {
		return nil, translateReadError(err)
	}
	plans := make([]telegram.Plan, 0, len(response.Items))
	for _, item := range response.Items {
		if !item.ID.Valid() || item.DurationSeconds <= 0 || item.GraceSeconds < 0 || len(item.Prices) == 0 {
			return nil, errors.New("onboarding: invalid billing catalog")
		}
		prices := make(map[string]string, len(item.Prices))
		for currency, amount := range item.Prices {
			currency = strings.ToUpper(strings.TrimSpace(currency))
			amount = strings.TrimSpace(amount)
			if currency == "" || amount == "" {
				return nil, errors.New("onboarding: invalid billing catalog price")
			}
			prices[currency] = amount
		}
		plans = append(plans, telegram.Plan{
			ID: item.ID, DurationSeconds: item.DurationSeconds,
			GraceSeconds: item.GraceSeconds, Prices: prices,
		})
	}
	return plans, nil
}

func (s *Service) CreateInvoice(ctx context.Context, senderID, updateID int64, plan contracts.SubscriptionPlan, currency string) (telegram.Invoice, error) {
	user, err := s.ensureUser(ctx, senderID)
	if err != nil {
		return telegram.Invoice{}, err
	}
	request := struct {
		AnonUserID string `json:"anon_user_id"`
		Plan       string `json:"plan"`
		Currency   string `json:"currency"`
	}{AnonUserID: user.ID, Plan: string(plan), Currency: currency}
	var response struct {
		InvoiceID   string `json:"invoice_id"`
		Provider    string `json:"provider"`
		PayAddress  string `json:"pay_address"`
		CheckoutURL string `json:"checkout_url"`
		Amount      string `json:"amount"`
		Currency    string `json:"currency"`
		ExpiresAt   string `json:"expires_at"`
	}
	headers := map[string]string{"Idempotency-Key": "tg-update-" + strconv.FormatInt(updateID, 10)}
	if err := s.billing.doJSON(ctx, http.MethodPost, "/v1/invoices", request, headers, &response, http.StatusCreated, http.StatusOK); err != nil {
		var statusErr *statusError
		if errors.As(err, &statusErr) {
			switch statusErr.code {
			case http.StatusBadRequest, http.StatusUnprocessableEntity:
				return telegram.Invoice{}, telegram.ErrUnsupportedCatalog
			case http.StatusTooManyRequests:
				return telegram.Invoice{}, telegram.ErrRateLimited
			}
		}
		return telegram.Invoice{}, err
	}
	expiresAt, err := time.Parse(time.RFC3339, response.ExpiresAt)
	if err != nil || response.InvoiceID == "" || response.Provider == "" || response.Amount == "" || response.Currency == "" || (response.PayAddress == "" && response.CheckoutURL == "") {
		return telegram.Invoice{}, errors.New("onboarding: invalid billing response")
	}
	return telegram.Invoice{
		ID: response.InvoiceID, Provider: response.Provider, PayAddress: response.PayAddress,
		CheckoutURL: response.CheckoutURL, Amount: response.Amount, Currency: response.Currency, ExpiresAt: expiresAt,
	}, nil
}

func (s *Service) Status(ctx context.Context, senderID int64) (telegram.AccountStatus, error) {
	user, err := s.ensureUser(ctx, senderID)
	if err != nil {
		return telegram.AccountStatus{}, translateReadError(err)
	}
	result := telegram.AccountStatus{UserStatus: user.Status}
	var latest contracts.BillingInvoiceStatus
	invoicePath := "/v1/accounts/" + url.PathEscape(user.ID) + "/invoices/latest"
	err = s.billing.doJSON(ctx, http.MethodGet, invoicePath, nil, nil, &latest, http.StatusOK)
	if err != nil {
		var statusErr *statusError
		if !errors.As(err, &statusErr) || statusErr.code != http.StatusNotFound {
			return telegram.AccountStatus{}, translateReadError(err)
		}
	} else {
		result.Invoice = &telegram.LatestInvoice{
			ID: latest.InvoiceID, Plan: latest.Plan, Status: string(latest.Status),
			Amount: latest.Amount, Currency: latest.Currency,
			CreatedAt: latest.CreatedAt, ExpiresAt: latest.ExpiresAt,
		}
	}

	if user.SubscriptionID == nil || *user.SubscriptionID == "" {
		return result, nil
	}
	subscription, found, err := s.subscription(ctx, user, *user.SubscriptionID)
	if err != nil {
		return telegram.AccountStatus{}, translateReadError(err)
	}
	if !found {
		return result, nil
	}
	result.Subscription = &subscription
	result.Eligible = user.Status == contracts.UserStatusActive && eligible(subscription, s.now())
	return result, nil
}

func (s *Service) SubscriptionLink(ctx context.Context, senderID int64) (telegram.Link, error) {
	user, err := s.ensureUser(ctx, senderID)
	if err != nil {
		return telegram.Link{}, err
	}
	if user.Status == contracts.UserStatusSuspended || user.Status == contracts.UserStatusBanned {
		return telegram.Link{}, telegram.ErrAccountSuspended
	}
	if user.Status != contracts.UserStatusActive || user.SubscriptionID == nil || *user.SubscriptionID == "" {
		return telegram.Link{}, telegram.ErrNoSubscription
	}

	subscription, found, err := s.subscription(ctx, user, *user.SubscriptionID)
	if err != nil {
		return telegram.Link{}, err
	}
	if !found {
		return telegram.Link{}, telegram.ErrNoSubscription
	}
	if !eligible(subscription, s.now()) {
		return telegram.Link{}, telegram.ErrNotEligible
	}

	path := "/v1/subscriptions/" + url.PathEscape(subscription.ID)
	var link contracts.DeliveryLink
	if err := s.cp.doJSON(ctx, http.MethodPost, path+"/delivery-link", struct{}{}, nil, &link, http.StatusOK, http.StatusCreated); err != nil {
		return telegram.Link{}, err
	}
	if link.Token == "" || link.SubscriptionID != subscription.ID {
		return telegram.Link{}, errors.New("onboarding: invalid delivery link response")
	}
	subscriptionURL := s.publicSubBase + "/sub/" + url.PathEscape(link.Token)
	return telegram.Link{
		SubscriptionURL: subscriptionURL,
		HappDeepLink:    "happ://add/" + base64.StdEncoding.EncodeToString([]byte(subscriptionURL)),
	}, nil
}

func (s *Service) subscription(ctx context.Context, user contracts.User, subscriptionID string) (contracts.Subscription, bool, error) {
	var subscription contracts.Subscription
	path := "/v1/subscriptions/" + url.PathEscape(subscriptionID)
	if err := s.cp.doJSON(ctx, http.MethodGet, path, nil, nil, &subscription, http.StatusOK); err != nil {
		var statusErr *statusError
		if errors.As(err, &statusErr) && statusErr.code == http.StatusNotFound {
			return contracts.Subscription{}, false, nil
		}
		return contracts.Subscription{}, false, err
	}
	if subscription.ID != subscriptionID || subscription.UserID != user.ID {
		return contracts.Subscription{}, false, errors.New("onboarding: inconsistent subscription identity")
	}
	return subscription, true, nil
}

func translateReadError(err error) error {
	var statusErr *statusError
	if errors.As(err, &statusErr) && statusErr.code == http.StatusTooManyRequests {
		return telegram.ErrRateLimited
	}
	return err
}

func (s *Service) ensureUser(ctx context.Context, senderID int64) (contracts.User, error) {
	var user contracts.User
	request := contracts.EnsureTelegram{TelegramID: senderID}
	if err := s.cp.doJSON(ctx, http.MethodPost, "/v1/users/ensure-telegram", request, nil, &user, http.StatusOK, http.StatusCreated); err != nil {
		return contracts.User{}, err
	}
	if user.ID == "" || user.TelegramID == nil || *user.TelegramID != senderID {
		return contracts.User{}, errors.New("onboarding: invalid control-plane identity response")
	}
	return user, nil
}

func eligible(subscription contracts.Subscription, now time.Time) bool {
	switch subscription.Status {
	case contracts.SubscriptionStatusActive, contracts.SubscriptionStatusTrialing, contracts.SubscriptionStatusPastDue:
	default:
		return false
	}
	until := subscription.AccessUntil()
	return until == nil || until.After(now)
}

type serviceClient struct {
	base       string
	token      string
	http       *http.Client
	retryDelay time.Duration
}

type statusError struct{ code int }

func (e *statusError) Error() string { return fmt.Sprintf("upstream status %d", e.code) }

func (c serviceClient) doJSON(ctx context.Context, method, path string, body interface{}, headers map[string]string, dst interface{}, accepted ...int) error {
	encoded, err := encodeBody(body)
	if err != nil {
		return err
	}
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(encoded))
		if err != nil {
			return errors.New("onboarding: create upstream request")
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		for name, value := range headers {
			req.Header.Set(name, value)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			if attempt < maxAttempts && wait(ctx, c.retryDelay) {
				continue
			}
			return errors.New("onboarding: upstream unavailable")
		}
		responseBody, readErr := readResponse(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return readErr
		}
		if acceptedStatus(resp.StatusCode, accepted) {
			if dst == nil || len(responseBody) == 0 {
				return nil
			}
			if err := json.Unmarshal(responseBody, dst); err != nil {
				return errors.New("onboarding: invalid upstream response")
			}
			return nil
		}
		if retryableStatus(resp.StatusCode) && attempt < maxAttempts && wait(ctx, c.retryDelay) {
			continue
		}
		return &statusError{code: resp.StatusCode}
	}
	return errors.New("onboarding: upstream unavailable")
}

func encodeBody(body interface{}) ([]byte, error) {
	if body == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, errors.New("onboarding: encode request")
	}
	return encoded, nil
}

func readResponse(reader io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, maxResponseBytes+1))
	if err != nil {
		return nil, errors.New("onboarding: read upstream response")
	}
	if len(body) > maxResponseBytes {
		return nil, errors.New("onboarding: upstream response too large")
	}
	return body, nil
}

func acceptedStatus(got int, accepted []int) bool {
	for _, code := range accepted {
		if got == code {
			return true
		}
	}
	return false
}

func retryableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable:
		return true
	}
	return false
}

func wait(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
