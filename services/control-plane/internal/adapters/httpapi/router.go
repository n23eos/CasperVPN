package httpapi

import (
	"net/http"

	"github.com/caspervpn/control-plane/internal/authz"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// Router builds the chi mux. /healthz is public; everything under /v1 requires a
// valid service token, with per-operation RBAC.
func (h *Handler) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)

	r.Get("/healthz", h.healthz)

	// Role sets. Reads are open to any authenticated service; writes are scoped.
	anyService := []authz.Role{authz.RoleAdmin, authz.RoleOrchestrator, authz.RoleTelemetry, authz.RoleSubscription, authz.RoleBilling}
	nodeWriters := []authz.Role{authz.RoleAdmin, authz.RoleOrchestrator}
	accountWriters := []authz.Role{authz.RoleAdmin, authz.RoleBilling}
	accountReaders := []authz.Role{authz.RoleAdmin, authz.RoleBilling, authz.RoleSubscription}

	r.Route("/v1", func(r chi.Router) {
		r.Use(h.authenticate)

		// Nodes (dynamic registry).
		r.Group(func(r chi.Router) {
			r.With(requireRole(anyService...)).Get("/nodes", h.listNodes)
			r.With(requireRole(anyService...)).Get("/nodes/{id}", h.getNode)
			r.With(requireRole(nodeWriters...)).Post("/nodes", h.createNode)
			r.With(requireRole(nodeWriters...)).Patch("/nodes/{id}", h.updateNode)
			r.With(requireRole(nodeWriters...)).Delete("/nodes/{id}", h.deleteNode)
			r.With(requireRole(nodeWriters...)).Post("/nodes/{id}/rotate", h.rotateNode)
			// REALITY allow-list — live admission creds; orchestrator/admin only.
			r.With(requireRole(authz.RoleOrchestrator, authz.RoleAdmin)).Get("/nodes/{id}/reality-users", h.getNodeRealityUsers)
			// Guarded provisioning->active; orchestrator/admin only.
			r.With(requireRole(authz.RoleOrchestrator, authz.RoleAdmin)).Post("/nodes/{id}/activate", h.activateNode)
		})

		// Users + their personal secrets.
		r.Group(func(r chi.Router) {
			r.With(requireRole(accountWriters...)).Post("/users", h.createUser)
			r.With(requireRole(accountReaders...)).Get("/users/{id}", h.getUser)
			r.With(requireRole(accountWriters...)).Patch("/users/{id}", h.updateUser)
			r.With(requireRole(accountWriters...)).Post("/users/{id}/rotate-secrets", h.rotateUserSecrets)
			// Structured Node×Transport set for the subscription service (Agent C).
			r.With(requireRole(authz.RoleSubscription, authz.RoleAdmin)).Get("/users/{id}/subscription-set", h.getSubscriptionSet)
		})

		// Subscriptions (entitlement only — no card/payment data here).
		r.Group(func(r chi.Router) {
			r.With(requireRole(accountWriters...)).Post("/subscriptions", h.createSubscription)
			r.With(requireRole(accountReaders...)).Get("/subscriptions/{id}", h.getSubscription)
			// Additive Wave-2 endpoints (TZ-contract-changes §1–2): billing
			// activation/renewal + leaked-link revocation.
			r.With(requireRole(accountWriters...)).Patch("/subscriptions/{id}", h.patchSubscription)
			r.With(requireRole(accountWriters...)).Post("/subscriptions/{id}/rotate-token", h.rotateSubscriptionToken)
			r.With(requireRole(accountWriters...)).Post("/subscriptions/{id}/cancel", h.cancelSubscription)
		})

		// Telemetry feedback loop: FieldSignal aggregates → degraded/blocked.
		r.With(requireRole(authz.RoleTelemetry, authz.RoleAdmin)).Post("/signals/aggregate", h.ingestSignalAggregates)
	})

	return r
}
