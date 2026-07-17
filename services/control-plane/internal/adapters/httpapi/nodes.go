package httpapi

import (
	"net/http"
	"strconv"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/control-plane/internal/domain"
	"github.com/go-chi/chi/v5"
)

func (h *Handler) listNodes(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := domain.NodeFilter{
		Status: contracts.NodeStatus(q.Get("status")),
		Role:   contracts.NodeRole(q.Get("role")),
		Region: q.Get("region"),
		Cursor: q.Get("cursor"),
	}
	if l := q.Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			f.Limit = n
		}
	}
	nodes, next, err := h.nodes.List(r.Context(), f)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if nodes == nil {
		nodes = []contracts.Node{}
	}
	writeJSON(w, http.StatusOK, nodesListResponse{Items: nodes, NextCursor: next})
}

func (h *Handler) createNode(w http.ResponseWriter, r *http.Request) {
	var n contracts.Node
	if err := decodeJSON(r, &n); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	created, err := h.nodes.Register(r.Context(), n, actorFromContext(r.Context()))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (h *Handler) getNode(w http.ResponseWriter, r *http.Request) {
	n, err := h.nodes.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, n)
}

func (h *Handler) updateNode(w http.ResponseWriter, r *http.Request) {
	var n contracts.Node
	if err := decodeJSON(r, &n); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	updated, err := h.nodes.Update(r.Context(), chi.URLParam(r, "id"), n, actorFromContext(r.Context()))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (h *Handler) deleteNode(w http.ResponseWriter, r *http.Request) {
	if err := h.nodes.Retire(r.Context(), chi.URLParam(r, "id"), actorFromContext(r.Context())); err != nil {
		writeDomainError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// demoteNode is an additive endpoint: atomically set status=provisioning. Unlike
// a full-body PATCH it is a single status-only update, so it cannot clobber a
// concurrent rotation's IP/keys/transports. The reconciler's hard barrier.
func (h *Handler) demoteNode(w http.ResponseWriter, r *http.Request) {
	n, err := h.nodes.Demote(r.Context(), chi.URLParam(r, "id"), actorFromContext(r.Context()))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, n)
}

// rotateNode is an additive endpoint: advertise a new ingress IP for a node.
func (h *Handler) rotateNode(w http.ResponseWriter, r *http.Request) {
	var req rotateNodeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	n, err := h.nodes.Rotate(r.Context(), chi.URLParam(r, "id"), req.NewEntryIP, req.Reason, actorFromContext(r.Context()))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, n)
}
