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
