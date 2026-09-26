package httpapi_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/control-plane/internal/secret"
)

func launchAccount(t *testing.T, r http.Handler) (contracts.User, contracts.Subscription) {
	t.Helper()
	rec := do(t, r, "POST", "/v1/users/ensure-telegram", deliveryTok, `{"telegram_id":4321}`)
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body)
	}
	var u contracts.User
	_ = json.Unmarshal(rec.Body.Bytes(), &u)
	rec = do(t, r, "POST", "/v1/users/"+u.ID+"/ensure-subscription", billTok, `{"plan":"basic"}`)
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body)
	}
	var s contracts.Subscription
	_ = json.Unmarshal(rec.Body.Bytes(), &s)
	return u, s
}
func putState(t *testing.T, r http.Handler, id string, state contracts.BillingState, want int) contracts.Subscription {
	t.Helper()
	body, _ := json.Marshal(state)
	rec := do(t, r, "PUT", "/v1/subscriptions/"+id+"/billing-state", billTok, string(body))
	if rec.Code != want {
		t.Fatalf("billing state: %d want %d: %s", rec.Code, want, rec.Body)
	}
	var s contracts.Subscription
	_ = json.Unmarshal(rec.Body.Bytes(), &s)
	return s
}
func TestLaunchIdentityAndUnpaidSubscription(t *testing.T) {
	r := newTestRouter(t)
	u, s := launchAccount(t, r)
	u2, s2 := launchAccount(t, r)
	if u.ID != u2.ID || s.ID != s2.ID || s.Status != contracts.SubscriptionStatusExpired || s.ExpiresAt == nil || s.ExpiresAt.After(time.Now()) {
		t.Fatal("onboarding is not idempotent and unpaid")
	}
	if u.PrivateKey != "" || u.Hysteria2Password != "" || u.UUID != "" {
		t.Fatal("delivery received transport secrets")
	}
	rec := do(t, r, "POST", "/v1/subscriptions/"+s.ID+"/delivery-link", deliveryTok, `{}`)
	if rec.Code != 409 {
		t.Fatal("unpaid token issued", rec.Code)
	}
	if rec := do(t, r, "POST", "/v1/users/ensure-telegram", billTok, `{"telegram_id":1}`); rec.Code != 403 {
		t.Fatal("billing must not ensure Telegram identity")
	}
}
func TestLaunchBillingFenceAndStableTokens(t *testing.T) {
	r := newTestRouter(t)
	_, s := launchAccount(t, r)
	expiry := time.Now().UTC().Truncate(time.Second).Add(time.Hour)
	state := contracts.BillingState{Plan: contracts.SubscriptionPlanBasic, Revision: 2, Status: contracts.SubscriptionStatusActive, ExpiresAt: expiry, GraceUntil: expiry.Add(time.Hour)}
	putState(t, r, s.ID, state, 200)
	putState(t, r, s.ID, state, 200)
	stale := state
	stale.Revision = 1
	stale.ExpiresAt = expiry.Add(-time.Hour)
	got := putState(t, r, s.ID, stale, 200)
	if got.BillingRevision != 2 || !got.ExpiresAt.Equal(expiry) {
		t.Fatal("stale state changed entitlement")
	}
	conflict := state
	conflict.Status = contracts.SubscriptionStatusExpired
	putState(t, r, s.ID, conflict, 409)
	if rec := do(t, r, "PATCH", "/v1/subscriptions/"+s.ID, adminTok, `{"status":"expired"}`); rec.Code != 409 {
		t.Fatal("PATCH bypassed revision fence")
	}
	upgrade := state
	upgrade.Plan = contracts.SubscriptionPlanUnlimited
	putState(t, r, s.ID, upgrade, 409)
	upgrade.Revision = 3
	upgraded := putState(t, r, s.ID, upgrade, 200)
	if upgraded.Plan != contracts.SubscriptionPlanUnlimited || upgraded.DeviceLimit != 5 || upgraded.TrafficLimitBytes != 0 {
		t.Fatal("plan limits not upgraded atomically")
	}
	got = putState(t, r, s.ID, state, 200)
	if got.Plan != contracts.SubscriptionPlanUnlimited {
		t.Fatal("stale state downgraded plan")
	}
	var links []contracts.DeliveryLink
	for i := 0; i < 2; i++ {
		rec := do(t, r, "POST", "/v1/subscriptions/"+s.ID+"/delivery-link", deliveryTok, `{}`)
		if rec.Code != 200 {
			t.Fatal(rec.Code, rec.Body)
		}
		var link contracts.DeliveryLink
		_ = json.Unmarshal(rec.Body.Bytes(), &link)
		links = append(links, link)
	}
	if links[0].Token == "" || links[0].Token != links[1].Token {
		t.Fatal("delivery link rotated")
	}
	hash := secret.HashToken(links[0].Token)
	body := fmt.Sprintf(`{"token_hash":%q}`, hash)
	if rec := do(t, r, "POST", "/v1/subscription-tokens/resolve", subTok, body); rec.Code != 200 {
		t.Fatal("alias did not resolve")
	}
	if rec := do(t, r, "POST", "/v1/subscription-tokens/resolve", deliveryTok, body); rec.Code != 403 {
		t.Fatal("delivery resolved token hashes")
	}
	if rec := do(t, r, "POST", "/v1/subscriptions/"+s.ID+"/rotate-token", adminTok, `{}`); rec.Code != 200 {
		t.Fatal("rotate failed", rec.Code, rec.Body)
	}
	if rec := do(t, r, "POST", "/v1/subscription-tokens/resolve", subTok, body); rec.Code != 404 {
		t.Fatal("rotated alias remains valid")
	}
	if rec := do(t, r, "POST", "/v1/subscriptions/"+s.ID+"/cancel", adminTok, `{}`); rec.Code != 200 {
		t.Fatal("cancel revisioned subscription", rec.Code)
	}
	afterCancel := putState(t, r, s.ID, upgrade, 200)
	if afterCancel.Status != contracts.SubscriptionStatusCanceled {
		t.Fatal("replayed credit undid cancellation")
	}
}
func TestLaunchAccessRevisionAndGrace(t *testing.T) {
	r, nodes := newTestRouterWithNodes(t)
	id, _ := seedActivatable(t, r, nodes, vlessTr+","+hy2Tr)
	rec := do(t, r, "GET", "/v1/nodes/"+id+"/access-users", orchTok, "")
	var snapshot contracts.NodeAccessUsers
	_ = json.Unmarshal(rec.Body.Bytes(), &snapshot)
	if len(snapshot.Users) != 1 || snapshot.Users[0].Hysteria2Password == "" || !snapshot.ValidUntil.After(time.Now()) {
		t.Fatal("missing personal admission lease")
	}
	rec = do(t, r, "GET", "/v1/nodes/"+id+"/reality-users", orchTok, "")
	var old contracts.NodeRealityUsers
	_ = json.Unmarshal(rec.Body.Bytes(), &old)
	rec = do(t, r, "POST", "/v1/nodes/"+id+"/activate", orchTok, fmt.Sprintf(`{"expected_revision":%q}`, old.Revision))
	if rec.Code != 409 {
		t.Fatal("REALITY-only digest bypassed Hy2 gate")
	}
	if rec = activate(t, r, id, orchTok, snapshot.Revision); rec.Code != 200 {
		t.Fatal("combined activation", rec.Code, rec.Body)
	}
}

func TestBillingCannotUseLegacyGrantOrRotationRoutes(t *testing.T) {
	r := newTestRouter(t)
	for _, operation := range []struct{ method, path string }{
		{"POST", "/v1/users"}, {"POST", "/v1/subscriptions"},
		{"PATCH", "/v1/users/u1"}, {"PATCH", "/v1/subscriptions/s1"},
		{"POST", "/v1/users/u1/rotate-secrets"}, {"POST", "/v1/subscriptions/s1/rotate-token"},
		{"POST", "/v1/subscriptions/s1/cancel"},
	} {
		if rec := do(t, r, operation.method, operation.path, billTok, `{}`); rec.Code != 403 {
			t.Fatalf("billing legacy %s %s = %d, want 403", operation.method, operation.path, rec.Code)
		}
	}
}

func TestStatusPatchPreservesTelegramAccountIdentity(t *testing.T) {
	r := newTestRouter(t)
	var created contracts.User
	doJSON(t, r, "POST", "/v1/users", adminTok, map[string]any{"telegram_id": 789}, 201, &created)
	var banned contracts.User
	doJSON(t, r, "PATCH", "/v1/users/"+created.ID, adminTok, map[string]any{"status": "banned"}, 200, &banned)
	if banned.TelegramID == nil || *banned.TelegramID != 789 || banned.DeviceLimit != created.DeviceLimit || banned.UUID != created.UUID {
		t.Fatalf("status-only patch erased account fields: %+v", banned)
	}
	var same contracts.User
	doJSON(t, r, "POST", "/v1/users/ensure-telegram", deliveryTok, map[string]any{"telegram_id": 789}, 200, &same)
	if same.ID != created.ID || same.Status != contracts.UserStatusBanned {
		t.Fatal("ban created a new Telegram account")
	}
}
