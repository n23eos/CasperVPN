// Command delivery is a scaffolding stub for the delivery subsystem
// (multi-channel subscription delivery: HTTPS, DoH, Telegram, GitHub raw, DNS
// TXT — routing around a blocked primary domain). It serves only /healthz
// today. See CLAUDE.md and docs/contracts.md.
package main

import (
	"log"
	"net/http"
	"os"

	"github.com/caspervpn/contracts"
)

const serviceName = "delivery"

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", healthz)

	addr := ":" + port()
	log.Printf("%s stub listening on %s (contracts %s)", serviceName, addr, contracts.TransportVersionV1)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("%s: %v", serviceName, err)
	}
}

func healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok","service":"` + serviceName + `"}`))
}

func port() string {
	if p := os.Getenv("PORT"); p != "" {
		return p
	}
	return "8080"
}
