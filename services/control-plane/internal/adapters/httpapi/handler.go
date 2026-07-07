// Package httpapi implements the control-plane HTTP API (the frozen OpenAPI plus
// additive internal endpoints) over the usecase services.
package httpapi

import (
	"encoding/json"
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
	tokens  *authz.TokenStore
}

// New builds a Handler.
func New(
	nodes *usecase.NodeService,
	users *usecase.UserService,
	subs *usecase.SubscriptionService,
	bundles *usecase.BundleService,
	signals *usecase.SignalService,
	tokens *authz.TokenStore,
) *Handler {
	return &Handler{nodes: nodes, users: users, subs: subs, bundles: bundles, signals: signals, tokens: tokens}
}

func (h *Handler) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "control-plane"})
}

// decodeJSON reads and strictly decodes a JSON body.
func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}
