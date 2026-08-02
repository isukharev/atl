package app

import (
	"errors"

	"github.com/isukharev/atl/internal/domain"
)

// ambiguousWriteError marks an outcome that must be reconciled before any
// replay. Its message is supplied by the guarded use case and remains the
// existing human diagnostic; recovery classification reads only the closed
// marker and the ErrCheckFailed identity, never that prose.
type ambiguousWriteError struct{ message string }

func (e *ambiguousWriteError) Error() string                  { return e.message }
func (e *ambiguousWriteError) Unwrap() error                  { return domain.ErrCheckFailed }
func (e *ambiguousWriteError) DiagnosticAmbiguousWrite() bool { return e != nil }

func ambiguousWriteFailure(message string) error {
	return &ambiguousWriteError{message: message}
}

// definitiveWriteRejection reports HTTP outcomes known not to have applied the
// mutation. Timeout/early-data/throttling statuses remain ambiguous and require
// a reconciliation read; transport errors have no HTTP status and are likewise
// ambiguous. Callers must never replay either class automatically.
func definitiveWriteRejection(err error) bool {
	var statusErr interface{ HTTPStatus() int }
	if !errors.As(err, &statusErr) {
		return false
	}
	status := statusErr.HTTPStatus()
	return status >= 400 && status < 500 && status != 408 && status != 425 && status != 429
}

// sanitizeRemoteWriteCause preserves only typed classification and HTTP status
// while dropping response bodies, request paths, and backend-derived detail.
func sanitizeRemoteWriteCause(err error) error {
	if err == nil {
		return nil
	}
	var causes []error
	for _, sentinel := range []error{
		domain.ErrUsage, domain.ErrAuth, domain.ErrNotFound, domain.ErrVersionConflict,
		domain.ErrForbidden, domain.ErrConfig, domain.ErrCheckFailed,
	} {
		if errors.Is(err, sentinel) {
			causes = append(causes, sentinel)
		}
	}
	var statusErr interface{ HTTPStatus() int }
	if errors.As(err, &statusErr) {
		causes = append(causes, remoteWriteHTTPStatus(statusErr.HTTPStatus()))
	}
	if len(causes) == 0 {
		return errors.New("request failed")
	}
	return errors.Join(causes...)
}

type remoteWriteHTTPStatus int

func (e remoteWriteHTTPStatus) Error() string   { return "request failed" }
func (e remoteWriteHTTPStatus) HTTPStatus() int { return int(e) }
