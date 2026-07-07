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
	if !ok {
		return nil, fmt.Errorf("money: invalid amount %q", s)
	}
	return r, nil
}

// Valid reports whether s is a well-formed, non-negative decimal amount.
func Valid(s string) bool {
	r, ok := new(big.Rat).SetString(s)
	return ok && r.Sign() >= 0
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
