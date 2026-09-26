// Package money does exact decimal comparison for crypto amounts. Amounts are
// decimal strings (e.g. "0.00012") to avoid float rounding; math/big.Rat gives
// exact arithmetic with no external dependency.
package money

import (
	"fmt"
	"math/big"
)

func parse(s string) (*big.Rat, error) {
	r, ok := new(big.Rat).SetString(s)
	if !ok || r.Sign() < 0 {
		return nil, fmt.Errorf("money: invalid amount %q", s)
	}
	return r, nil
}

// Valid reports whether s is a well-formed, non-negative decimal amount.
func Valid(s string) bool {
	_, err := parse(s)
	return err == nil
}

// Equal reports exact numeric equality while allowing harmless decimal scale
// differences such as 0.0001 and 0.00010000.
func Equal(a, b string) (bool, error) {
	left, err := parse(a)
	if err != nil {
		return false, err
	}
	right, err := parse(b)
	if err != nil {
		return false, err
	}
	return left.Cmp(right) == 0, nil
}

// GTE reports whether paid >= expected. Used for the underpayment guard.
func GTE(paid, expected string) (bool, error) {
	p, err := parse(paid)
	if err != nil {
		return false, err
	}
	e, err := parse(expected)
	if err != nil {
		return false, err
	}
	return p.Cmp(e) >= 0, nil
}
