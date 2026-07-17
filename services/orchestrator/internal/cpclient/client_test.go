package cpclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/orchestrator/internal/ports"
)

func node(id string) contracts.Node {
	return contracts.Node{
		ID: id, Role: contracts.NodeRoleEntry, Status: contracts.NodeStatusActive,
		Provider: "hetzner", Cloud: "cloud-a", Region: "eu-central",
		EntryIP: "192.0.2.10", EphemeralEntryIP: true, CreatedAt: time.Now().UTC(),
	}
}

func TestListNodesWalksPagination(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("Authorization = %q", got)
		}
		if r.URL.Query().Get("status") != "active" {
			t.Errorf("status filter not propagated: %s", r.URL.RawQuery)
		}
		switch r.URL.Query().Get("cursor") {
		case "":
			_ = json.NewEncoder(w).Encode(nodesPage{Items: []contracts.Node{node("a")}, NextCursor: "p2"})
		case "p2":
			_ = json.NewEncoder(w).Encode(nodesPage{Items: []contracts.Node{node("b")}})
		default:
			t.Errorf("unexpected cursor %q", r.URL.Query().Get("cursor"))
		}
	}))
	defer srv.Close()

	got, err := New(srv.URL, "tok", time.Second).ListNodes(context.Background(),
		ports.NodeFilter{Status: contracts.NodeStatusActive})
	if err != nil {
		t.Fatalf("ListNodes() error: %v", err)
	}
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "b" {
		t.Fatalf("nodes = %+v, want [a b]", got)
	}
}

func TestListNodesBreaksCursorLoop(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(nodesPage{Items: []contracts.Node{node("x")}, NextCursor: "same"})
	}))
	defer srv.Close()

	if _, err := New(srv.URL, "tok", time.Second).ListNodes(context.Background(), ports.NodeFilter{}); err == nil {
		t.Fatal("want error on unbounded pagination, got nil")
	}
}

func TestGetNodeErrors(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, `{"error":"not_found"}`, http.StatusNotFound)
	}))
	defer srv.Close()

	if _, err := New(srv.URL, "tok", time.Second).GetNode(context.Background(), "ghost"); err == nil {
		t.Fatal("want error on 404")
	}
	if calls.Load() != 1 {
		t.Fatalf("404 retried %d times — 4xx must not be retried", calls.Load())
	}
}

func TestUpdateNodePatchesFullObjectWithBoundedRetry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError) // transient
			return
		}
		if r.Method != http.MethodPatch || r.URL.Path != "/v1/nodes/node-1" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var n contracts.Node
		if err := json.NewDecoder(r.Body).Decode(&n); err != nil || n.Status != contracts.NodeStatusDraining {
			t.Errorf("bad PATCH body: %v %+v", err, n)
		}
		_ = json.NewEncoder(w).Encode(n)
	}))
	defer srv.Close()

	n := node("node-1")
	n.Status = contracts.NodeStatusDraining
	got, err := New(srv.URL, "tok", time.Second).UpdateNode(context.Background(), n)
	if err != nil {
		t.Fatalf("UpdateNode() error: %v", err)
	}
	if got.Status != contracts.NodeStatusDraining {
		t.Fatalf("status = %s, want draining", got.Status)
	}
	if calls.Load() != 2 {
		t.Fatalf("attempts = %d, want 2 (one retry after transient 500)", calls.Load())
	}
}

func TestUpdateNodeRejectsInvalidNode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("invalid node must not reach the wire")
	}))
	defer srv.Close()

	if _, err := New(srv.URL, "tok", time.Second).UpdateNode(context.Background(), contracts.Node{}); err == nil {
		t.Fatal("want validation error")
	}
}

func TestMalformedListResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"items": [{"id": 42}]}`)) // id must be a string
	}))
	defer srv.Close()

	if _, err := New(srv.URL, "tok", time.Second).ListNodes(context.Background(), ports.NodeFilter{}); err == nil {
		t.Fatal("want error on malformed JSON")
	}
}
