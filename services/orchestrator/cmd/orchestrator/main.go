// Command orchestrator closes the anti-fragility loop: it consumes telemetry
// recommendations, confirms suspicions with its own probes, decides via the
// policy engine (anti-poisoning gated) and drives node rotation/replacement
// through the infra lifecycle scripts and the control-plane API.
//
// SAFETY DEFAULT: DRY_RUN=true. Out of the box the orchestrator only plans
// and logs. Set DRY_RUN=false (plus TELEMETRY_URL / CONTROL_PLANE_URL and
// tokens) to let it act. See docs/orchestrator.md.
package main

import (
	"context"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/orchestrator/internal/config"
	"github.com/caspervpn/orchestrator/internal/cpclient"
	"github.com/caspervpn/orchestrator/internal/policy"
	"github.com/caspervpn/orchestrator/internal/probe"
	"github.com/caspervpn/orchestrator/internal/provision"
	"github.com/caspervpn/orchestrator/internal/reconcile"
	"github.com/caspervpn/orchestrator/internal/telemetryclient"
)

const serviceName = "orchestrator"

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("%s: %v", serviceName, err)
	}
	if err := cfg.Validate(); err != nil {
		log.Fatalf("%s: %v", serviceName, err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	const clientTimeout = 30 * time.Second // per attempt
	deps := reconcile.Deps{
		Telemetry: telemetryclient.New(cfg.TelemetryURL, cfg.TelemetryToken, clientTimeout),
		CP:        cpclient.New(cfg.ControlPlaneURL, cfg.ControlPlaneToken, clientTimeout),
		Prov: &provision.ScriptRunner{
			Dir:     cfg.ScriptsDir,
			Timeout: cfg.ScriptTimeout,
			Logf:    log.Printf,
		},
		Prober: &probe.TCPProber{Source: cfg.ProbeSource, Timeout: cfg.ProbeTimeout},
		Logf:   log.Printf,
	}
	opts := reconcile.Options{
		DryRun: cfg.DryRun,
		Thresholds: policy.Thresholds{
			RecommendationMaxAge: cfg.RecommendationMaxAge,
			ProbeMaxAge:          cfg.ProbeMaxAge,
			DrainGrace:           cfg.DrainGrace,
			MaxActionsPerCycle:   cfg.MaxActionsPerCycle,
		},
		ProbeEnabled:  cfg.ProbeEnabled,
		DefaultRegion: cfg.DefaultRegion,
		DefaultCloud:  cfg.DefaultCloud,
		Interval:      cfg.ReconcileInterval,
	}

	// The loop needs both seams to observe; without them (dry-run without
	// URLs) we only serve /healthz — still useful in dev compose.
	if cfg.TelemetryURL != "" && cfg.ControlPlaneURL != "" {
		go reconcile.New(deps, opts).Run(ctx)
		log.Printf("%s: reconcile loop started (dry_run=%v, interval=%s, probe=%v)",
			serviceName, cfg.DryRun, cfg.ReconcileInterval, cfg.ProbeEnabled)
	} else {
		log.Printf("%s: TELEMETRY_URL/CONTROL_PLANE_URL not set — reconcile loop disabled, serving /healthz only", serviceName)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", healthz)
	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	log.Printf("%s listening on %s (contracts %s)", serviceName, srv.Addr, contracts.TransportVersionV1)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("%s: %v", serviceName, err)
	}
}

func healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok","service":"` + serviceName + `"}`))
}
