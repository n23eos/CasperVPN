// Package httpapi implements the delivery HTTP surface defined in
// packages/contracts/openapi/delivery.yaml (frozen): liveness, channel listing,
// and per-channel fetch. Handlers move already-signed blobs; they never mint or
// verify signatures (that is the client's job) — delivery only transports.
package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/caspervpn/delivery/internal/channel"
)

// API holds the dependencies the handlers need.
type API struct {
	reg *channel.Registry
}

// New builds the API over a channel registry.
func New(reg *channel.Registry) *API { return &API{reg: reg} }

// Routes registers every handler on a mux and returns it.
func (a *API) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", a.health)
	mux.HandleFunc("/v1/channels", a.channels)
	mux.HandleFunc("/d/", a.fetchViaChannel) // /d/{channel}/{token}
	return mux
}

func (a *API) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "delivery"})
}

// channelDTO mirrors the openapi Channel schema.
type channelDTO struct {
	ID       string `json:"id,omitempty"`
	Kind     string `json:"kind"`
	Enabled  bool   `json:"enabled"`
	Endpoint string `json:"endpoint,omitempty"`
	Priority int    `json:"priority,omitempty"`
	Healthy  bool   `json:"healthy"`
}

// channels handles GET (list) and POST (register-intent).
func (a *API) channels(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.listChannels(w, r)
	case http.MethodPost:
		a.createChannel(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *API) listChannels(w http.ResponseWriter, r *http.Request) {
	health := a.reg.Health(r.Context())
	kinds := a.reg.Kinds()
	items := make([]channelDTO, 0, len(kinds))
	for i, k := range kinds {
		items = append(items, channelDTO{
			Kind:     string(k),
			Enabled:  true,
			Priority: i,
			Healthy:  health[k],
		})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"items": items})
}

// createChannel validates a channel descriptor. Live channel construction is
// config/boot-time (endpoints and keys never come from an unauthenticated POST);
// this records operator intent and returns the normalized descriptor. It rejects
// an unknown kind so callers get immediate feedback.
func (a *API) createChannel(w http.ResponseWriter, r *http.Request) {
	var dto channelDTO
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&dto); err != nil {
		writeError(w, http.StatusBadRequest, "invalid channel body")
		return
	}
	if !knownKind(channel.Kind(dto.Kind)) {
		writeError(w, http.StatusBadRequest, "unknown channel kind")
		return
	}
	if _, exists := a.reg.Get(channel.Kind(dto.Kind)); exists {
		writeError(w, http.StatusConflict, "channel already exists")
		return
	}
	dto.Enabled = true
	writeJSON(w, http.StatusCreated, dto)
}

// fetchViaChannel serves GET /d/{channel}/{token}: fetch the signed blob a
// specific channel holds for token and return it base64-encoded. The client
// verifies the signature; delivery does not.
func (a *API) fetchViaChannel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/d/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		writeError(w, http.StatusNotFound, "expected /d/{channel}/{token}")
		return
	}
	kind := channel.Kind(parts[0])
	token := parts[1]

	ch, ok := a.reg.Get(kind)
	if !ok {
		writeError(w, http.StatusNotFound, "channel not found")
		return
	}
	blob, err := ch.Fetch(r.Context(), token)
	if err != nil {
		writeFetchError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(base64.StdEncoding.EncodeToString(blob)))
}

func writeFetchError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, channel.ErrNotFound):
		writeError(w, http.StatusNotFound, "subscription not found on channel")
	case errors.Is(err, channel.ErrUnavailable):
		writeError(w, http.StatusServiceUnavailable, "channel temporarily unavailable")
	default:
		writeError(w, http.StatusServiceUnavailable, "channel error")
	}
}

func knownKind(k channel.Kind) bool {
	switch k {
	case channel.KindHTTPS, channel.KindDoH, channel.KindDNSTXT,
		channel.KindTelegram, channel.KindGitHubRaw, channel.KindSteg:
		return true
	}
	return false
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
