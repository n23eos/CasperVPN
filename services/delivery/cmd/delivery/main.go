// Command delivery is the multi-channel subscription delivery service: it hands
// the SAME signed artifact (subscription payload, or an encrypted directory
// pointer) over several channels of different nature — messenger (Telegram/Max),
// DNS (DoH + TXT), git-raw mirrors, and a steganographic carrier — so a blocked
// primary domain never cuts off config delivery. Every artifact is Ed25519-signed
// and the client MUST verify it. See services/delivery/docs/delivery.md.
package main

import (
	"context"
	"encoding/base64"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/delivery/internal/app"
	"github.com/caspervpn/delivery/internal/botstore"
	"github.com/caspervpn/delivery/internal/channel/telegram"
	"github.com/caspervpn/delivery/internal/config"
	"github.com/caspervpn/delivery/internal/migrate"
	"github.com/caspervpn/delivery/internal/onboarding"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	serviceName       = "delivery"
	readHeaderTimeout = 10 * time.Second
	shutdownTimeout   = 15 * time.Second
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("%s: config: %v", serviceName, err)
	}

	var (
		pool       *pgxpool.Pool
		poller     *telegram.Poller
		appOptions []app.Option
	)
	if cfg.BotEnabled {
		startupCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		pool, err = pgxpool.New(startupCtx, cfg.DatabaseURL)
		if err != nil {
			log.Fatalf("%s: database configuration failed", serviceName)
		}
		defer pool.Close()
		if err := pool.Ping(startupCtx); err != nil {
			log.Fatalf("%s: database unavailable", serviceName)
		}
		if err := migrate.Up(startupCtx, pool); err != nil {
			log.Fatalf("%s: database migration failed: %v", serviceName, err)
		}

		store := botstore.NewPostgres(pool)
		appOptions = append(appOptions, app.WithReadiness(store))
		telegramHTTP := &http.Client{Timeout: cfg.PollTimeout + cfg.HTTPTimeout}
		serviceHTTP := &http.Client{Timeout: cfg.HTTPTimeout}
		botAPI := telegram.API{Base: cfg.TelegramBase, Token: cfg.TelegramToken, HTTP: telegramHTTP}
		onboardingService := onboarding.New(
			cfg.ControlPlaneBase, cfg.ControlPlaneToken,
			cfg.BillingBase, cfg.BillingToken,
			cfg.PublicSubBase, serviceHTTP, cfg.RetryDelay,
		)
		bot, err := telegram.NewBot(botAPI, onboardingService, telegram.BotConfig{
			RatePerSec: cfg.Bot.RatePerSec, Burst: cfg.Bot.Burst, Cooldown: cfg.Bot.Cooldown,
			DefaultPlan: cfg.Bot.DefaultPlan, DefaultCurrency: cfg.Bot.DefaultCurrency,
		})
		if err != nil {
			log.Fatalf("%s: bot configuration failed", serviceName)
		}
		poller = telegram.NewPoller(botAPI, bot, store, cfg.PollTimeout, cfg.RetryDelay)
	}

	a, err := app.Build(cfg, appOptions...)
	if err != nil {
		log.Fatalf("%s: build: %v", serviceName, err)
	}

	if a.EphemeralKeys {
		log.Printf("%s: WARNING ephemeral signing/seal keys generated (dev only). "+
			"Set DELIVERY_SIGN_SEED and DELIVERY_SEAL_KEY in production.", serviceName)
	}
	log.Printf("%s: signing key id=%q public=%s", serviceName,
		a.Signer.KeyID(), base64.StdEncoding.EncodeToString(a.Signer.PublicKey()))
	log.Printf("%s: channels registered: %v", serviceName, a.Registry.Kinds())

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           a.Handler,
		ReadHeaderTimeout: readHeaderTimeout, // slow-loris guard on a public endpoint
	}

	serverErr := make(chan error, 1)
	go func() {
		log.Printf("%s listening on %s (contracts %s)", serviceName, srv.Addr, contracts.TransportVersionV1)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
	}()
	pollerErr := make(chan error, 1)
	if poller != nil {
		go func() { pollerErr <- poller.Run(ctx) }()
	}

	select {
	case <-ctx.Done():
	case <-serverErr:
		log.Printf("%s: HTTP server stopped unexpectedly", serviceName)
		stop()
	case <-pollerErr:
		if ctx.Err() == nil {
			log.Printf("%s: Telegram polling stopped unexpectedly", serviceName)
			stop()
		}
	}
	log.Printf("%s: shutting down", serviceName)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("%s: graceful shutdown failed: %v", serviceName, err)
	}
}
