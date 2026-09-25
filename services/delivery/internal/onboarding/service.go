// Package onboarding connects the Telegram bot to control-plane and billing.
package onboarding

import (
	"bytes"
	"context"
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
		InvoiceID  string `json:"invoice_id"`
		Provider   string `json:"provider"`
		PayAddress string `json:"pay_address"`
		CheckoutURL string `json:"checkout_url"`
		Amount     string `json:"amount"`
		Currency   string `json:"currency"`
		ExpiresAt  string `json:"expires_at"`
	}
	headers := map[string]string{"Idempotency-Key": "tg-update-" + strconv.FormatInt(updateID, 10)}
	if err := s.billing.doJSON(ctx, http.MethodPost, "/v1/invoices", request, headers, &response, http.StatusCreated, http.StatusOK); err != nil {
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

func (s *Service) SubscriptionLink(ctx context.Context, senderID int64) (telegram.Link, error) {
	user, err := s.ensureUser(ctx, senderID)
	if err != nil {
		return telegram.Link{}, err
	}
	if user.Status != contracts.UserStatusActive || user.SubscriptionID == nil || *user.SubscriptionID == "" {
		return telegram.Link{}, telegram.ErrNoSubscription
	}

	var subscription contracts.Subscription
	path := "/v1/subscriptions/" + url.PathEscape(*user.SubscriptionID)
	if err := s.cp.doJSON(ctx, http.MethodGet, path, nil, nil, &subscription, http.StatusOK); err != nil {
		var statusErr *statusError
		if errors.As(err, &statusErr) && statusErr.code == http.StatusNotFound {
			return telegram.Link{}, telegram.ErrNoSubscription
		}
		return telegram.Link{}, err
	}
	if !eligible(subscription, s.now()) {
		return telegram.Link{}, telegram.ErrNotEligible
	}

	var link contracts.DeliveryLink
	if err := s.cp.doJSON(ctx, http.MethodPost, path+"/delivery-link", struct{}{}, nil, &link, http.StatusOK, http.StatusCreated); err != nil {
		return telegram.Link{}, err
	}
	if link.Token == "" || link.SubscriptionID != subscription.ID {
		return telegram.Link{}, errors.New("onboarding: invalid delivery link response")
	}
	return telegram.Link{SubscriptionURL: s.publicSubBase + "/sub/" + url.PathEscape(link.Token)}, nil
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
