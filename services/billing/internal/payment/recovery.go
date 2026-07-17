package payment

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Recovery failure stages — stable, PII-free categories safe to log. A stage names
// WHERE in the recovery of one settlement a failure happened, never WHAT the raw error
// said: a control-plane statusError can carry up to 4 KiB of response body, and an
// activation error may mention accounts/amounts — none of that may reach a log line.
const (
	stageLeaseQuery  = "lease_query"  // batch could not even be leased
	stageLoadInvoice = "load_invoice" // GetInvoice failed
	stageActivate    = "activate"     // control-plane activation failed
	stageSetStatus   = "set_status"   // final status flip failed
	stageUnknown     = "unknown"      // defensive: an unstaged error slipped through
)

// Report size bounds. A mass failure (DB down → the whole batch fails) must never
// produce an unbounded log line or aggregate-error string.
const (
	maxReportedIDs = 10  // at most this many invoice IDs listed in a report
	maxIDLen       = 64  // each rendered ID truncated to this many runes
	maxIDsStrLen   = 800 // hard cap on the total rendered ID-list length
)

// recoveryError tags a per-invoice recovery failure with a stable stage and the opaque
// invoice ID, wrapping the raw cause. Unwrap keeps errors.Is/As working so callers and
// tests can inspect the cause — but the raw cause is NEVER formatted into a recovery
// log; only the stage and (sanitized) ID are.
type recoveryError struct {
	stage     string
	invoiceID string
	err       error
}

func (e *recoveryError) Error() string {
	return fmt.Sprintf("recovery %s %q: %v", e.stage, e.invoiceID, e.err)
}

func (e *recoveryError) Unwrap() error { return e.err }

// RecoveryReport is the structured, safe-to-log outcome of one Reconcile cycle. It
// carries ONLY non-sensitive data: counts, a bounded and sanitized sample of invoice
// IDs, and per-stage failure tallies. It deliberately holds NO error value — the
// aggregate error is returned separately from Reconcile, so a raw error (and any
// response body inside it) can never be logged by way of the report.
type RecoveryReport struct {
	Leased    int            // rows leased this cycle
	Recovered int            // settlements finished
	Failed    int            // per-invoice recovery failures
	Canceled  bool           // the cycle stopped early on a canceled context
	FailedIDs []string       // bounded, sanitized sample; len may be < Failed
	Stages    map[string]int // stage → failure count
}

// ShouldLog reports whether this cycle is worth a log line. An all-clean cycle with no
// work (nothing leased, recovered, failed, canceled, or staged) logs nothing, so the
// poll loop does not emit constant noise on every idle tick.
func (r RecoveryReport) ShouldLog() bool {
	return r.Leased > 0 || r.Recovered > 0 || r.Failed > 0 || r.Canceled || len(r.Stages) > 0
}

// note records a per-invoice failure into the report: it tallies the stage and appends
// a sanitized invoice ID up to the cap. The raw error is not stored.
func (r *RecoveryReport) note(rerr error) {
	stage, id := stageUnknown, ""
	var re *recoveryError
	if errors.As(rerr, &re) {
		stage, id = re.stage, re.invoiceID
	}
	if r.Stages == nil {
		r.Stages = map[string]int{}
	}
	r.Stages[stage]++
	if id != "" && len(r.FailedIDs) < maxReportedIDs {
		r.FailedIDs = append(r.FailedIDs, sanitizeID(id))
	}
}

// String renders a stable, PII-free one-line summary. Stages are sorted for
// determinism; only counts, sanitized IDs, and stage categories appear — never raw
// error text or a response body.
func (r RecoveryReport) String() string {
	ids := strings.Join(r.FailedIDs, ",")
	if len(ids) > maxIDsStrLen {
		ids = ids[:maxIDsStrLen] + "~"
	}
	return fmt.Sprintf("leased=%d recovered=%d failed=%d canceled=%t ids=[%s] stages=[%s]",
		r.Leased, r.Recovered, r.Failed, r.Canceled, ids, renderStages(r.Stages))
}

func renderStages(m map[string]int) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s:%d", k, m[k]))
	}
	return strings.Join(parts, ",")
}

// sanitizeID bounds one invoice ID for logging: control characters and the log
// format's own delimiters are replaced, then it is truncated to maxIDLen runes with a
// marker. Even though IDs are opaque and billing-generated, this is defense in depth
// against an oversized or format-breaking ID reaching the log.
func sanitizeID(id string) string {
	id = strings.Map(func(r rune) rune {
		if r < 0x20 || r == ',' || r == '[' || r == ']' {
			return '_'
		}
		return r
	}, id)
	if rs := []rune(id); len(rs) > maxIDLen {
		return string(rs[:maxIDLen]) + "~"
	}
	return id
}

// joinBounded aggregates raw per-invoice errors into a single error, keeping at most
// maxReportedIDs of them (with errors.Is/As intact) and summarizing the remainder as a
// count, so a mass failure cannot produce an unbounded error string. Returned SEPARATELY
// from the report and never formatted into a recovery log.
func joinBounded(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	if len(errs) <= maxReportedIDs {
		return errors.Join(errs...)
	}
	kept := make([]error, 0, maxReportedIDs+1)
	kept = append(kept, errs[:maxReportedIDs]...)
	kept = append(kept, fmt.Errorf("+%d more recovery errors", len(errs)-maxReportedIDs))
	return errors.Join(kept...)
}

// RecoveryLogger receives the structured report of each Reconcile cycle that has
// something to say (see RecoveryReport.ShouldLog). It is REQUIRED by NewPoller — a nil
// logger is a programmer error, not a silent no-op, so recovery signal can never be
// accidentally dropped.
type RecoveryLogger func(RecoveryReport)
