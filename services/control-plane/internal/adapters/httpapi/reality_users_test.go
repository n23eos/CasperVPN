package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/control-plane/internal/adapters/memory"
)

// seedNodeAndEligibleUser seeds an active node (via the repo — Register refuses
// active) and an active user with a servable subscription, returning the node id
// and the user's uuid.
func seedNodeAndEligibleUser(t *testing.T, r http.Handler, nodes *memory.Nodes) (nodeID, userUUID string) {
	t.Helper()

	seedActive(t, nodes, `{"id":"nd-au-1","role":"entry","status":"active","provider":"local","cloud":"local","region":"eu","entry_ip":"1.1.1.1","ephemeral_entry_ip":false,"transports":[]}`)

	rec := do(t, r, http.MethodPost, "/v1/users", adminTok, `{"telegram_id":42}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("seed user: got %d: %s", rec.Code, rec.Body)
	}
	var u contracts.User
	if err := json.Unmarshal(rec.Body.Bytes(), &u); err != nil {
		t.Fatal(err)
	}
	rec = do(t, r, http.MethodPost, "/v1/subscriptions", adminTok, `{"user_id":"`+u.ID+`","plan":"basic"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("seed subscription: got %d: %s", rec.Code, rec.Body)
	}
	return "nd-au-1", u.UUID
}

func do(t *testing.T, r http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *strings.Reader
	if body == "" {
		rdr = strings.NewReader("")
	} else {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestRealityUsers_OrchestratorGetsAllowListWithNoStore(t *testing.T) {
	r, nodes := newTestRouterWithNodes(t)
	nodeID, userUUID := seedNodeAndEligibleUser(t, r, nodes)

	rec := do(t, r, http.MethodGet, "/v1/nodes/"+nodeID+"/reality-users", orchTok, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	var got contracts.NodeRealityUsers
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Revision == "" {
		t.Error("revision must be set")
	}
	found := false
	for _, ru := range got.Users {
		if ru.UUID == userUUID {
			found = true
			if ru.ShortID == "" {
				t.Error("short_id must be present")
			}
		}
	}
	if !found {
		t.Errorf("eligible user %s missing from allow-list: %+v", userUUID, got.Users)
	}
	// Revision must equal the digest of the returned set.
	if got.Revision != contracts.RealityUsersRevision(got.Users) {
		t.Error("revision must be the digest of the returned users")
	}
}

func TestRealityUsers_RBACAndMissingNode(t *testing.T) {
	r, nodes := newTestRouterWithNodes(t)
	nodeID, _ := seedNodeAndEligibleUser(t, r, nodes)

	// subscription role must NOT read the allow-list (live admission creds).
	if rec := do(t, r, http.MethodGet, "/v1/nodes/"+nodeID+"/reality-users", subTok, ""); rec.Code != http.StatusForbidden {
		t.Errorf("subscription role: got %d, want 403", rec.Code)
	}
	// unauthenticated -> 401.
	if rec := do(t, r, http.MethodGet, "/v1/nodes/"+nodeID+"/reality-users", "", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("unauth: got %d, want 401", rec.Code)
	}
	// missing node -> 404.
	if rec := do(t, r, http.MethodGet, "/v1/nodes/does-not-exist/reality-users", orchTok, ""); rec.Code != http.StatusNotFound {
		t.Errorf("missing node: got %d, want 404", rec.Code)
	}
}
