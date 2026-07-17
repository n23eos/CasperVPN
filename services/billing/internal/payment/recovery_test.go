package payment

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/caspervpn/billing/internal/model"
	"github.com/caspervpn/billing/internal/store"
)

func TestRecoveryReport_ShouldLog(t *testing.T) {
	cases := []struct {
		name string
		r    RecoveryReport
		want bool
	}{
		{"empty", RecoveryReport{}, false},
		{"recovered", RecoveryReport{Recovered: 1}, true},
		{"failed", RecoveryReport{Failed: 1}, true},
		{"leased", RecoveryReport{Leased: 1}, true},
		{"canceled", RecoveryReport{Canceled: true}, true},
		{"stage only (lease_query)", RecoveryReport{Stages: map[string]int{stageLeaseQuery: 1}}, true},
	}
	for _, c := range cases {
		if got := c.r.ShouldLog(); got != c.want {
			t.Errorf("%s: ShouldLog = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestRecoveryReport_StringStableAndSorted(t *testing.T) {
	r := RecoveryReport{
		Leased: 5, Recovered: 3, Failed: 2, Canceled: false,
		FailedIDs: []string{"inv-a", "inv-b"},
		Stages:    map[string]int{stageSetStatus: 1, stageActivate: 1},
	}
	want := "leased=5 recovered=3 failed=2 canceled=false ids=[inv-a,inv-b] stages=[activate:1,set_status:1]"
	if got := r.String(); got != want {
		t.Fatalf("String()\n got: %s\nwant: %s", got, want)
	}
}

// A mass failure must not blow up the ID list: at most maxReportedIDs are kept, but the
// full Failed count is preserved.
func TestRecoveryReport_CardinalityBound(t *testing.T) {
	var r RecoveryReport
	for i := 0; i < 100; i++ {
		r.Failed++
		r.note(&recoveryError{stage: stageActivate, invoiceID: fmt.Sprintf("inv-%d", i), err: errors.New("x")})
	}
	if len(r.FailedIDs) != maxReportedIDs {
		t.Fatalf("FailedIDs len = %d, want %d", len(r.FailedIDs), maxReportedIDs)
	}
	if r.Failed != 100 {
		t.Fatalf("Failed = %d, want 100", r.Failed)
	}
	if r.Stages[stageActivate] != 100 {
		t.Fatalf("stage count = %d, want 100", r.Stages[stageActivate])
	}
}

// The report must never leak the raw error (response body, email, amount, token) and
// must bound/neutralize an oversized or delimiter-bearing invoice ID.
func TestRecoveryReport_NoPIILeak(t *testing.T) {
	secrets := []string{"user@example.com", "sk_live_DEADBEEF", "amount=0.5BTC", "<html>body</html>", "Authorization: Bearer xyz"}
	rawBody := strings.Join(secrets, " ") + strings.Repeat(" filler", 600) // ~4 KiB-ish
	oversizedID := strings.Repeat("A", 500) + ",]evil-suffix"

	var r RecoveryReport
	r.Failed++
	r.note(&recoveryError{stage: stageActivate, invoiceID: oversizedID, err: errors.New(rawBody)})
	out := r.String()

	for _, s := range secrets {
		if strings.Contains(out, s) {
			t.Errorf("recovery log leaked %q:\n%s", s, out)
		}
	}
	id := r.FailedIDs[0]
	if len([]rune(id)) > maxIDLen+1 {
		t.Errorf("ID not truncated: %d runes", len([]rune(id)))
	}
	if strings.ContainsAny(id, ",[]") {
		t.Errorf("ID retained format delimiters: %q", id)
	}
}

func TestSanitizeID(t *testing.T) {
	if got := sanitizeID("a,b]c[d"); strings.ContainsAny(got, ",[]") {
		t.Errorf("delimiters survived: %q", got)
	}
	long := strings.Repeat("x", maxIDLen+50)
	if got := sanitizeID(long); len([]rune(got)) != maxIDLen+1 {
		t.Errorf("truncated len = %d, want %d", len([]rune(got)), maxIDLen+1)
	}
	if got := sanitizeID("normal-id"); got != "normal-id" {
		t.Errorf("mangled a clean id: %q", got)
	}
}

func TestJoinBounded(t *testing.T) {
	if joinBounded(nil) != nil {
		t.Fatal("empty must be nil")
	}
	sentinel := errors.New("kept-error-3")
	var errs []error
	for i := 0; i < 25; i++ {
		if i == 3 {
			errs = append(errs, sentinel)
			continue
		}
		errs = append(errs, fmt.Errorf("e%d", i))
	}
	agg := joinBounded(errs)
	if !errors.Is(agg, sentinel) {
		t.Fatal("kept error not discoverable via errors.Is")
	}
	if !strings.Contains(agg.Error(), "+15 more recovery errors") {
		t.Fatalf("no bounded remainder summary: %s", agg.Error())
	}
}

// leaseErrStore fails only LeaseStuckSettlements; everything else delegates.
type leaseErrStore struct {
	store.Repository
	err error
}

func (s *leaseErrStore) LeaseStuckSettlements(context.Context, time.Time, time.Duration, int) ([]store.StuckSettlement, error) {
	return nil, s.err
}

func TestReconcile_LeaseQueryError(t *testing.T) {
	boom := errors.New("lease query boom")
	repo := &leaseErrStore{Repository: store.NewMemory(), err: boom}
	proc := NewProcessor(repo, nil, 0, func() time.Time { return time.Unix(0, 0) })

	rep, err := proc.Reconcile(context.Background(), time.Unix(0, 0), leaseFor, batch)
	if !errors.Is(err, boom) {
		t.Fatalf("aggregate = %v, want wraps boom", err)
	}
	if rep.Stages[stageLeaseQuery] != 1 {
		t.Fatalf("stages = %v, want lease_query:1", rep.Stages)
	}
	if rep.Leased != 0 || rep.Failed != 0 {
		t.Fatalf("report = %+v, want no per-invoice work", rep)
	}
	if !rep.ShouldLog() {
		t.Fatal("a lease-query failure must be logged")
	}
}

// A canceled context stops the batch, keeps what was done, and flags the cycle.
func TestReconcile_ContextCanceledStopsAndReports(t *testing.T) {
	h := newRecoverHarness(t, seededFakeCP(t, "acct-1", "acct-2"))
	seedPending(t, h.repo, "inv-1", "acct-1", h.clk.Add(time.Hour))
	seedPending(t, h.repo, "inv-2", "acct-2", h.clk.Add(time.Hour))
	for _, id := range []string{"inv-1", "inv-2"} {
		if _, err := h.repo.ClaimSettlement(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // canceled before the loop starts

	rep, _ := h.proc.Reconcile(ctx, *h.clk, leaseFor, batch)
	if !rep.Canceled {
		t.Fatal("report must be flagged Canceled")
	}
	if rep.Recovered != 0 {
		t.Fatalf("Recovered = %d, want 0 (stopped immediately)", rep.Recovered)
	}
	if rep.Leased != 2 {
		t.Fatalf("Leased = %d, want 2", rep.Leased)
	}
	if got := statusOf(t, h.repo, "inv-1"); got != model.StatusPending {
		t.Fatalf("inv-1 = %q, want still pending (no work done)", got)
	}
}
