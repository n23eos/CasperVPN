package controlplane

import (
	"errors"
	"fmt"
)

// ErrSubscriptionExists is returned by CreateSubscription when the control-plane
// rejects the create because the user already has a subscription (HTTP 409). The
// caller re-reads the user and continues with the existing subscription_id — this
// is the recovery path when a concurrent settlement won the create.
var ErrSubscriptionExists = errors.New("controlplane: user already has a subscription")

// statusError carries the HTTP status of a non-2xx control-plane response so callers
// can branch on it (e.g. map 409 to ErrSubscriptionExists) without string matching.
type statusError struct {
	method, path string
	code         int
	body         string
}

func (e *statusError) Error() string {
	return fmt.Sprintf("controlplane: %s %s status %d: %s", e.method, e.path, e.code, e.body)
}

// statusCode returns the HTTP status carried by err, or 0 if err is not a statusError.
func statusCode(err error) int {
	var se *statusError
	if errors.As(err, &se) {
		return se.code
	}
	return 0
}
