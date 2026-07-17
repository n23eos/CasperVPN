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
	"github.com/caspervpn/delivery/internal/config"
)

const (
	serviceName       = "delivery"
	readHeaderTimeout = 10 * time.Second
	shutdownTimeout   = 15 * time.Second
)

func main() {
	cfg := config.Load()
	a, err := app.Build(cfg)
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

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           a.Handler,
		ReadHeaderTimeout: readHeaderTimeout, // slow-loris guard on a public endpoint
	}

	go func() {
		log.Printf("%s listening on %s (contracts %s)", serviceName, srv.Addr, contracts.TransportVersionV1)
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
}
