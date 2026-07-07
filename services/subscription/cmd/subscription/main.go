// Command subscription serves per-user subscription URLs for external clients
// (Happ / sing-box / Clash.Meta). It fetches the node set + user secrets from the
// control-plane, renders three representations of one canonical set, personalizes
// and caches them, and invalidates on control-plane signals. See CLAUDE.md and
// docs/subscription.md.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/caspervpn/subscription/internal/cache"
	"github.com/caspervpn/subscription/internal/config"
	"github.com/caspervpn/subscription/internal/controlplane"
	"github.com/caspervpn/subscription/internal/httpapi"
	"github.com/caspervpn/subscription/internal/render"
	"github.com/caspervpn/subscription/internal/resolve"
)

const serviceName = "subscription"

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("%s: config: %v", serviceName, err)
	}

	// The in-memory store always backs the subscription-owned token index. In
	// dev (no CONTROL_PLANE_URL) it also serves users/subscriptions/nodes from an
	// optional fixtures file.
	memStore := controlplane.NewMemory()
	var provider controlplane.Provider = memStore
	if cfg.ControlPlaneURL != "" {
		provider = controlplane.NewHTTPClient(cfg.ControlPlaneURL, cfg.ControlPlaneToken, cfg.ControlPlaneTimeout)
		log.Printf("%s: control-plane at %s", serviceName, cfg.ControlPlaneURL)
	} else if path := os.Getenv("SUBSCRIPTION_FIXTURES"); path != "" {
		if err := loadFixtures(memStore, path); err != nil {
			log.Fatalf("%s: fixtures: %v", serviceName, err)
		}
		log.Printf("%s: loaded dev fixtures from %s", serviceName, path)
	}

	resolver := resolve.New(memStore, provider, time.Now)
	renderer := render.New(cfg.Routing)
	payloadCache := cache.New(cfg.CacheTTL, time.Now)
	srv := httpapi.New(cfg, resolver, renderer, payloadCache, memStore, time.Now)

	httpServer := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		log.Printf("%s listening on %s (profile-update-interval %dh, cache TTL %s)",
			serviceName, httpServer.Addr, cfg.ProfileUpdateHours, cfg.CacheTTL)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("%s: %v", serviceName, err)
		}
	}()

	// Graceful shutdown: drain in-flight requests on SIGINT/SIGTERM.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Printf("%s: shutting down", serviceName)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		log.Printf("%s: shutdown: %v", serviceName, err)
	}
}
