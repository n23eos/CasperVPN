// Command control-plane is the CasperVPN control plane: the node registry,
// per-user secret issuance, subscription-set assembly, and the block→rebuild
// feedback loop. It serves the frozen control-plane OpenAPI plus additive
// internal endpoints. See docs/control-plane.md.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/caspervpn/control-plane/internal/adapters/httpapi"
	"github.com/caspervpn/control-plane/internal/adapters/postgres"
	"github.com/caspervpn/control-plane/internal/adapters/rebuild"
	"github.com/caspervpn/control-plane/internal/adapters/subnotify"
	"github.com/caspervpn/control-plane/internal/authz"
	"github.com/caspervpn/control-plane/internal/config"
	"github.com/caspervpn/control-plane/internal/domain"
	"github.com/caspervpn/control-plane/internal/migrate"
	"github.com/caspervpn/control-plane/internal/seed"
	"github.com/caspervpn/control-plane/internal/usecase"
)

func main() {
	logger := log.New(os.Stdout, "control-plane ", log.LstdFlags|log.Lmsgprefix)

	cfg, err := config.Load()
	if err != nil {
		logger.Fatalf("config: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := postgres.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Fatalf("db: %v", err)
	}
	defer pool.Close()

	if err := migrate.Up(ctx, pool); err != nil {
		logger.Fatalf("migrate: %v", err)
	}

	// Repositories.
	nodeStore := postgres.NewNodeStore(pool)
	userStore := postgres.NewUserStore(pool)
	subStore := postgres.NewSubscriptionStore(pool)
	rotStore := postgres.NewRotationStore(pool)
	setStore := postgres.NewSetStore(pool)
	signalStore := postgres.NewSignalStore(pool)

	// Usecases + async rebuild queue (the "issue a fresh config" loop). The
	// durable queue (REBUILD_DURABLE=true) persists jobs in Postgres so pending
	// rebuilds survive restarts and can be drained by several instances; the
	// default in-memory queue is single-instance and loses pending work on exit.
	bundleSvc := usecase.NewBundleService(userStore, nodeStore, setStore)
	var queue domain.RebuildQueue
	if cfg.RebuildDurable {
		dq := rebuild.NewDurable(postgres.NewRebuildJobStore(pool), setStore, bundleSvc,
			rebuild.DurableConfig{Workers: cfg.RebuildWorkers}, logger)
		dq.Start(ctx)
		queue = dq
		logger.Printf("rebuild queue: durable (Postgres), workers=%d", cfg.RebuildWorkers)
	} else {
		q := rebuild.New(setStore, bundleSvc, cfg.RebuildBuffer, cfg.RebuildWorkers, logger)
		q.Start(ctx)
		queue = q
		logger.Printf("rebuild queue: in-memory, workers=%d buffer=%d", cfg.RebuildWorkers, cfg.RebuildBuffer)
	}

	// Revocation propagation into the subscription service (TZ-token-revocation).
	// No-op unless SUBSCRIPTION_INTERNAL_URL is configured, so dev doesn't break.
	var revoker domain.SubscriptionRevoker = subnotify.Noop{}
	if cfg.SubscriptionInternalURL != "" {
		revoker = subnotify.New(cfg.SubscriptionInternalURL, cfg.SubscriptionInternalToken,
			time.Duration(cfg.SubscriptionTimeoutSeconds)*time.Second)
		logger.Printf("subscription revocation notifier enabled: %s", cfg.SubscriptionInternalURL)
	}

	nodeSvc := usecase.NewNodeService(nodeStore, rotStore, queue)
	userSvc := usecase.NewUserService(userStore, rotStore, queue).WithRevoker(revoker)
	subSvc := usecase.NewSubscriptionService(subStore, userStore).WithRevoker(revoker)
	signalSvc := usecase.NewSignalService(nodeStore, signalStore, nodeSvc)

	if os.Getenv("SEED") == "true" {
		if err := seed.Run(ctx, seed.Services{Nodes: nodeSvc, NodesRepo: nodeStore, Users: userSvc, Subs: subSvc, Bundles: bundleSvc}); err != nil {
			logger.Printf("seed: %v", err)
		} else {
			logger.Printf("seed: dev data loaded")
		}
	}

	tokens := authz.NewTokenStore(cfg.Tokens)
	if cfg.Env == "dev" {
		logger.Printf("WARNING: dev env; ensure CONTROL_PLANE_TOKENS is set for non-local use")
	}
	handler := httpapi.New(nodeSvc, userSvc, subSvc, bundleSvc, signalSvc, tokens)

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           handler.Router(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		logger.Printf("listening on %s (env=%s)", srv.Addr, cfg.Env)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatalf("serve: %v", err)
		}
	}()

	<-ctx.Done()
	logger.Printf("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Printf("graceful shutdown failed: %v", err)
	}
}
