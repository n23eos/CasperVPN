// Command billing is the crypto-first subscription billing service. It accepts
// payment across several self-hosted / on-chain gateways (no custodial single point
// of pressure), confirms via signed webhooks or polling, and activates/renews
// subscriptions through the control-plane API. It stores no PII: accounts are
// referenced by an opaque anon_user_id only. See docs/billing.md and
// docs/decisions/ADR-004-crypto-billing.md.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/caspervpn/billing/internal/controlplane"
	"github.com/caspervpn/billing/internal/expiry"
	"github.com/caspervpn/billing/internal/httpapi"
	"github.com/caspervpn/billing/internal/idgen"
	"github.com/caspervpn/billing/internal/payment"
	"github.com/caspervpn/billing/internal/payment/btcpay"
	"github.com/caspervpn/billing/internal/payment/mock"
	"github.com/caspervpn/billing/internal/plan"
	"github.com/caspervpn/billing/internal/store"
	"github.com/caspervpn/billing/internal/subscription"
	"github.com/caspervpn/contracts"
	"github.com/jackc/pgx/v5/pgxpool"
)

const serviceName = "billing"

func main() {
	log.SetPrefix(serviceName + ": ")

	catalog := loadCatalog()
	repo, closeStore := newStore()
	defer closeStore()
	registry := buildRegistry()

	cp := controlplane.NewHTTPClient(
		getenv("CONTROL_PLANE_URL", "http://control-plane:8081"),
		os.Getenv("CONTROL_PLANE_TOKEN"),
	)
	activator := subscription.NewActivator(cp, catalog, repo, time.Now)
	processor := payment.NewProcessor(repo, activator, replayWindow(), time.Now)
	// On-chain poll/expire policy (variant A). SETTLEMENT_GRACE has no default — it is
	// a per-chain policy and is REQUIRED (fail-fast) when an on-chain gateway is live.
	onchainCfg := payment.OnChain{
		Grace:        interval("SETTLEMENT_GRACE", 0),
		PollLease:    interval("POLL_LEASE", 30*time.Second),
		ChainTimeout: interval("CHAIN_CHECK_TIMEOUT", 10*time.Second),
		Owner:        getenv("BILLING_INSTANCE_ID", idgen.New()), // observability only; unique per process
	}
	if registryHasOnChain(registry) {
		if err := onchainCfg.Validate(); err != nil {
			log.Fatalf("on-chain config: %v", err)
		}
	}
	// Structured, PII-free recovery log: counts, bounded invoice IDs, and stable stage
	// categories only — never a raw error or response body.
	recoveryLog := func(r payment.RecoveryReport) { log.Printf("recovery %s", r.String()) }
	poller := payment.NewPoller(repo, registry, processor, payment.Recovery{
		// Must exceed the activation HTTP timeout (control-plane client is 10s, and an
		// Activate makes up to a few calls) so recovery never pre-empts a live settle.
		StaleThreshold: interval("SETTLEMENT_STALE_THRESHOLD", 2*time.Minute),
	}, onchainCfg, recoveryLog, time.Now)
	sweeper := expiry.NewSweeper(repo, cp, time.Now)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Background loops: expiry sweep + settlement polling.
	go runLoop(ctx, "sweeper", interval("BILLING_SWEEP_INTERVAL", time.Minute), func() {
		if err := sweeper.RunOnce(ctx); err != nil {
			log.Printf("sweeper: %v", err)
		}
	})
	go runLoop(ctx, "poller", interval("BILLING_POLL_INTERVAL", 30*time.Second), func() {
		if err := poller.RunOnce(ctx); err != nil {
			log.Printf("poller: %v", err)
		}
	})

	api := httpapi.New(registry, processor, repo, catalog)
	srv := &http.Server{
		Addr:              ":" + getenv("PORT", "8084"),
		Handler:           api.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("listening on %s (contracts %s)", srv.Addr, contracts.TransportVersionV1)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("serve: %v", err)
		}
	}()

	<-ctx.Done()
	log.Printf("shutting down")
	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

// buildRegistry assembles the active gateways from env. Diversity is the point:
// register everything configured, so the failure or seizure of one never stops
// sales. On-chain gateways need operator-provided ChainClient/AddressPool impls and
// are wired by the operator (see docs/billing.md), not from env alone.
// registryHasOnChain reports whether any registered gateway is poll-based on-chain —
// used to require the on-chain config (SETTLEMENT_GRACE) only when it actually applies.
func registryHasOnChain(reg *payment.Registry) bool {
	for _, g := range reg.Gateways() {
		if _, ok := g.(payment.OnChainGateway); ok {
			return true
		}
	}
	return false
}

func buildRegistry() *payment.Registry {
	reg := payment.NewRegistry()

	if base := os.Getenv("BTCPAY_BASE_URL"); base != "" {
		cfg := btcpay.Config{
			BaseURL:       base,
			APIKey:        os.Getenv("BTCPAY_API_KEY"),
			StoreID:       os.Getenv("BTCPAY_STORE_ID"),
			WebhookSecret: os.Getenv("BTCPAY_WEBHOOK_SECRET"),
			Currencies:    splitCSV(getenv("BTCPAY_CURRENCIES", "BTC")),
		}
		// Fail fast: a configured gateway with no webhook secret would verify
		// forged "settled" webhooks against an empty HMAC key.
		if err := cfg.Validate(); err != nil {
			log.Fatalf("btcpay config: %v", err)
		}
		reg.Register(btcpay.New(cfg))
		log.Printf("registered gateway: btcpay")
	}

	if secret := os.Getenv("BILLING_MOCK_SECRET"); secret != "" {
		reg.Register(mock.New(secret, splitCSV(getenv("BILLING_MOCK_CURRENCIES", "BTC,XMR"))))
		log.Printf("registered gateway: mock (dev)")
	}

	if len(reg.Gateways()) == 0 {
		log.Printf("WARNING: no payment gateways configured; invoice creation will fail")
	}
	return reg
}

// newStore selects the persistence backend: Postgres when DATABASE_URL is set
// (durable — invoices survive restarts), otherwise the in-memory store for
// dev/offline. It returns a close func the caller defers to release the pool.
func newStore() (store.Repository, func()) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		log.Printf("WARNING: DATABASE_URL unset; using in-memory store (state lost on restart)")
		return store.NewMemory(), func() {}
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		log.Fatalf("connect postgres: %v", err)
	}
	log.Printf("using postgres store")
	return store.NewPostgres(pool), pool.Close
}

func loadCatalog() *plan.Catalog {
	path := os.Getenv("BILLING_PLAN_CATALOG")
	if path == "" {
		log.Printf("WARNING: BILLING_PLAN_CATALOG unset; no plans priced")
		return plan.NewCatalog()
	}
	f, err := os.Open(path)
	if err != nil {
		log.Fatalf("open catalog: %v", err)
	}
	defer f.Close()
	c, err := plan.Load(f)
	if err != nil {
		log.Fatalf("load catalog: %v", err)
	}
	return c
}

func runLoop(ctx context.Context, name string, every time.Duration, fn func()) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fn()
		}
	}
}

func replayWindow() time.Duration { return interval("BILLING_REPLAY_WINDOW", time.Hour) }

func interval(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
