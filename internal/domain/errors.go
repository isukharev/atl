package domain

import "errors"

// Sentinel errors mapped to process exit codes by the CLI layer.
// Exit map: 0 ok · 2 usage · 3 auth · 4 not-found · 5 version-conflict ·
// 6 forbidden · 1 generic.
var (
	ErrAuth            = errors.New("authentication failed")
	ErrNotFound        = errors.New("not found")
	ErrVersionConflict = errors.New("remote version moved (drift); refused")
	ErrForbidden       = errors.New("forbidden")
	ErrUsage           = errors.New("usage error")
)
