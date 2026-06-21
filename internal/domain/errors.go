package domain

import "errors"

// Sentinel errors mapped to process exit codes by the CLI layer.
// Exit map: 0 ok · 2 usage · 3 auth · 4 not-found · 5 version-conflict ·
// 6 forbidden · 7 config · 1 generic.
var (
	ErrAuth            = errors.New("authentication failed")
	ErrNotFound        = errors.New("not found")
	ErrVersionConflict = errors.New("remote version moved (drift); refused")
	ErrForbidden       = errors.New("forbidden")
	ErrUsage           = errors.New("usage error")
	// ErrConfig marks a "not set up yet" condition: a missing backend URL or a
	// missing PAT — i.e. the operator has not finished configuring atl, as
	// opposed to ErrAuth (the server rejected a token that *was* supplied). It
	// maps to exit 7 so scripts/agents can distinguish "run setup" from "token
	// rejected" and react accordingly.
	ErrConfig = errors.New("not configured")
)
