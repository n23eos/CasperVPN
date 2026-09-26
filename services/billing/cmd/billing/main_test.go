package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNormalizeMode(t *testing.T) {
	tests := []struct {
		input string
		want  string
		ok    bool
	}{
		{input: "", want: "production", ok: true},
		{input: "production", want: "production", ok: true},
		{input: "prod", want: "production", ok: true},
		{input: "dev", want: "dev", ok: true},
		{input: "test", want: "test", ok: true},
		{input: "staging", want: "", ok: false},
	}
	for _, tt := range tests {
		got, ok := normalizeMode(tt.input)
		if got != tt.want || ok != tt.ok {
			t.Errorf("normalizeMode(%q) = %q,%t; want %q,%t", tt.input, got, ok, tt.want, tt.ok)
		}
	}
}

func TestWithRequestTimeoutCancelsHandlerContext(t *testing.T) {
	const timeout = 20 * time.Millisecond
	var handlerErr error
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.Context().Deadline(); !ok {
			handlerErr = errors.New("request context has no deadline")
			return
		}
		<-r.Context().Done()
		handlerErr = r.Context().Err()
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	started := time.Now()
	response := httptest.NewRecorder()
	withRequestTimeout(handler, timeout).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if !errors.Is(handlerErr, context.DeadlineExceeded) {
		t.Fatalf("handler context error = %v, want deadline exceeded", handlerErr)
	}
	if elapsed := time.Since(started); elapsed < timeout || elapsed > time.Second {
		t.Fatalf("handler cancellation took %s, want between %s and 1s", elapsed, timeout)
	}
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("response code = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
}
