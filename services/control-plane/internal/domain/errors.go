package domain

import "errors"

// Sentinel errors surfaced across layers. Adapters map these to transport codes
// (e.g. HTTP 404/409/422); usecases return them; repos raise them.
var (
	ErrNotFound     = errors.New("not found")
	ErrConflict     = errors.New("conflict")
	ErrValidation   = errors.New("validation failed")
	ErrUnauthorized = errors.New("unauthorized")
	ErrForbidden    = errors.New("forbidden")
)
