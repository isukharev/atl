package domain

// PageUpdateUnconfirmedError records the version sent in a page update whose
// acknowledgement could not be qualified. The write may have committed. Only
// an exact readback can establish success; the write must not be replayed.
type PageUpdateUnconfirmedError struct {
	ExpectedVersion int
}

func (*PageUpdateUnconfirmedError) Error() string {
	return "page update acknowledgement is unconfirmed; reconcile the outcome without replaying the write"
}

func (*PageUpdateUnconfirmedError) Unwrap() error                  { return ErrCheckFailed }
func (*PageUpdateUnconfirmedError) DiagnosticAmbiguousWrite() bool { return true }
