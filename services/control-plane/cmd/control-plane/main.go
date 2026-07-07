// Command control-plane is a scaffolding stub for the control-plane subsystem
// (node registry, per-user REALITY id issuance, config assembly). It serves only
// /healthz today. Real behavior is built against the frozen contracts package by
// the control-plane team. See CLAUDE.md and docs/contracts.md.
package main

import (
	"log"
	"net/http"
	"os"

	"github.com/caspervpn/contracts"
)

const serviceName = "control-plane"

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
