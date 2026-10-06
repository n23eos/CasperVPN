package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/control-plane/internal/adapters/httpapi"
	"github.com/caspervpn/control-plane/internal/authz"
)

func TestOperatorSummaryOnlyAdminAndNoContactOrCredentials(t *testing.T) {
	router := newTestRouter(t)
	for _, test := range []struct {
		token string
		want  int
	}{{"", 401}, {deliveryTok, 403}, {subTok, 403}, {billTok, 403}, {orchTok, 403}, {teleTok, 403}, {adminTok, 200}} {
		req := httptest.NewRequest(http.MethodGet, "/v1/operator/summary", nil)
		if test.token != "" {
			req.Header.Set("Authorization", "Bearer "+test.token)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != test.want {
			t.Fatalf("token role status=%d want=%d", rec.Code, test.want)
		}
		if test.want == 200 {
			var result contracts.ControlPlaneOperatorSummary
			if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Users == nil || result.Subscriptions == nil || result.Recent == nil {
				t.Fatal("empty overview must use initialized maps and arrays")
			}
			if rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("private overview must not be cached")
			}
			for _, field := range []string{"telegram_id", "email", "private_key", "uuid", "reality_short_id", "hysteria2_password", "token"} {
				if strings.Contains(rec.Body.String(), field) {
					t.Fatalf("unexpected private field %q", field)
				}
			}
		}
	}
}

func TestOperatorSummaryUnavailableDoesNotLeakStorageError(t *testing.T) {
	tokens := authz.NewTokenStore(map[string]authz.Role{adminTok: authz.RoleAdmin})
	handler := httpapi.New(nil, nil, nil, nil, nil, nil, tokens).WithOperatorSummary(func(context.Context) (contracts.ControlPlaneOperatorSummary, error) {
		return contracts.ControlPlaneOperatorSummary{}, errors.New("postgres://secret-data storage failed")
	})
	req := httptest.NewRequest(http.MethodGet, "/v1/operator/summary", nil)
	req.Header.Set("Authorization", "Bearer "+adminTok)
	rec := httptest.NewRecorder()
	handler.Router().ServeHTTP(rec, req)
	if rec.Code != 503 || strings.Contains(rec.Body.String(), "secret-data") {
		t.Fatalf("unsafe unavailable response: %d %s", rec.Code, rec.Body.String())
	}
}
