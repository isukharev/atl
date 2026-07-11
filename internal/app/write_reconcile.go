package app

import "errors"

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
