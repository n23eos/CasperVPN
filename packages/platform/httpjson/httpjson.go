// Package httpjson writes uniform JSON responses. Before it existed each
// service carried its own writeJSON/writeError pair, and the error shape
// diverged ({"error": msg} vs {code, detail}) so no client could parse errors
// uniformly. The {"error": msg} shape here is the monorepo default; the
// control-plane keeps its richer {code, detail} contract in its own adapter.
package httpjson

import (
	"encoding/json"
	"net/http"
)

// Write serializes v with the given status. Encoding errors are ignored: the
// header is already out, and v is always a service-controlled type.
func Write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Error writes {"error": msg} with the given status.
func Error(w http.ResponseWriter, status int, msg string) {
	Write(w, status, map[string]string{"error": msg})
}
