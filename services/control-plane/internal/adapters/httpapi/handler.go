// Package httpapi implements the control-plane HTTP API (the frozen OpenAPI plus
// additive internal endpoints) over the usecase services.
package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/caspervpn/control-plane/internal/authz"
	"github.com/caspervpn/control-plane/internal/usecase"
)

// Handler holds the usecase services and the token store.
type Handler struct {
	nodes   *usecase.NodeService
	users   *usecase.UserService
	subs    *usecase.SubscriptionService
	bundles *usecase.BundleService
	signals *usecase.SignalService
	allow   *usecase.AllowListService
	tokens  *authz.TokenStore
	ready   func(context.Context) error
}

// New builds a Handler.
func New(
	nodes *usecase.NodeService,
	users *usecase.UserService,
	subs *usecase.SubscriptionService,
	bundles *usecase.BundleService,
	signals *usecase.SignalService,
	allow *usecase.AllowListService,
	tokens *authz.TokenStore,
) *Handler {
	return &Handler{nodes: nodes, users: users, subs: subs, bundles: bundles, signals: signals, allow: allow, tokens: tokens}
}

func (h *Handler) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "control-plane"})
}

// decodeJSON reads and strictly decodes a JSON body.
func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("request must contain one JSON value")
	}
	return nil
}
