// Command telemetry runs the CasperVPN feedback-loop service: it ingests anonymous
// FieldSignals (and authoritative HealthEvents), aggregates them per region and
// transport, detects "this transport died in this region", and serves the
// control-plane rotation/prioritization recommendations plus Prometheus metrics.
//
// Anonymity by construction, resistance to data poisoning, and turning every
// blocked verdict into an actionable rotation signal are the design invariants —
// see docs/telemetry.md and CLAUDE.md [ANTI-BLOCK].
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver

	"github.com/caspervpn/telemetry/internal/api"
	"github.com/caspervpn/telemetry/internal/config"
	"github.com/caspervpn/telemetry/internal/ingest"
	"github.com/caspervpn/telemetry/internal/metrics"
	"github.com/caspervpn/telemetry/internal/query"
	"github.com/caspervpn/telemetry/internal/store"
)

const (
	serviceName     = "telemetry"
	memoryHardCap   = 500_000 // max in-memory points per series (memory-leak guard)
	shutdownTimeout = 15 * time.Second
	dedupTTLFactor  = 2 // dedup TTL = window * factor, so replays within a verdict window are caught
)

func main() {
	cfg := config.Load()
	now := time.Now

	st, err := newStore(cfg)
	if err != nil {
		log.Fatalf("%s: %v", serviceName, err)
	}

	coll := metrics.NewCollector()
	limiter := ingest.NewRateLimiter(cfg.RatePerSec, cfg.RateBurst, cfg.GlobalRate, cfg.GlobalBurst, now)
	dedup := ingest.NewDeduper(cfg.Window*dedupTTLFactor, now)
	ingestor := ingest.NewIngestor(st, limiter, dedup, coll, cfg.MaxBatch, cfg.Retention, now)
	queries := query.NewHandler(st, cfg.Window, cfg.Verdict, now)

	if cfg.InternalToken == "" {
		log.Printf("%s: WARNING TELEMETRY_INTERNAL_TOKEN unset — internal endpoints are UNAUTHENTICATED (dev only)", serviceName)
	}
	handler := api.Router(ingestor, queries, coll, cfg.InternalToken)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go retentionLoop(ctx, st, cfg.Retention, cfg.PruneEvery, now)

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("%s listening on %s (window=%s retention=%s)", serviceName, srv.Addr, cfg.Window, cfg.Retention)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("%s: %v", serviceName, err)
		}
	}()

	<-ctx.Done()
	log.Printf("%s: shutting down", serviceName)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("%s: graceful shutdown failed: %v", serviceName, err)
	}
	_ = st.Close()
}

// newStore selects the persistence backend: Postgres when DATABASE_URL is set
// (durable — survives restart), otherwise the in-memory store (dev default,
// state lost on restart). Schema and migrations are applied OUT OF BAND, never
// on startup, to avoid races between instances (Production Checklist); startup
// only opens the pool and pings for readiness. See docs/telemetry.md.
func newStore(cfg config.Config) (store.Store, error) {
	if cfg.DatabaseURL == "" {
		log.Printf("%s: DATABASE_URL unset; using in-memory store (state lost on restart)", serviceName)
		return store.NewMemoryStore(memoryHardCap), nil
	}
	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres unreachable (readiness ping): %w", err)
	}
	log.Printf("%s: using Postgres store (DATABASE_URL set)", serviceName)
	return store.NewPostgresStore(db), nil
}

// retentionLoop prunes points older than the retention horizon on a timer.
func retentionLoop(ctx context.Context, st store.Store, retention, every time.Duration, now func() time.Time) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			removed, err := st.Prune(context.Background(), now().Add(-retention))
			if err != nil {
				log.Printf("%s: prune error: %v", serviceName, err)
				continue
			}
			if removed > 0 {
				log.Printf("%s: pruned %d expired points", serviceName, removed)
			}
		}
	}
}
