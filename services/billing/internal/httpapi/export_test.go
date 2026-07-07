package httpapi

import "time"

// SetInvoiceLimiterForTest replaces the invoice rate limiter with a deterministic
// one so tests can assert the 429 path without hammering wall-clock time.
func (a *API) SetInvoiceLimiterForTest(capacity, refillPerSec float64, now func() time.Time) {
	a.invoiceLimiter = newStripedLimiter(capacity, refillPerSec, now)
}
