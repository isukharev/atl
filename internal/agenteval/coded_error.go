package agenteval

// codedEvaluationError is one classified evaluation failure: a stable sentinel
// and a stable code for callers, operators, and exit-code mapping, plus the
// concrete causes that produced it.
//
// Error renders only the sentinel and the code and never cause text, so a
// configured private path, a workspace layout detail, or backend data cannot
// reach a log line, a CLI message, or an HTTP response body through an error
// string. Unwrap reports the sentinel together with every retained cause, so
// errors.Is keeps matching the sentinel while errors.As still reaches the
// concrete cause types (*fs.PathError and friends) that the redacted text
// deliberately omits.
type codedEvaluationError struct {
	code string
	// tree holds the sentinel first, then each non-nil cause, in the order
	// the causes were supplied.
	tree []error
}

// codedError classifies a failure under sentinel and code while keeping the
// causes already in hand. Nil causes are dropped, so a caller can pass an
// optional secondary failure without guarding it first.
func codedError(sentinel error, code string, causes ...error) error {
	tree := make([]error, 1, len(causes)+1)
	tree[0] = sentinel
	for _, cause := range causes {
		if cause != nil {
			tree = append(tree, cause)
		}
	}
	return &codedEvaluationError{code: code, tree: tree}
}

func (e *codedEvaluationError) Error() string {
	if e.code == "" {
		return e.tree[0].Error()
	}
	return e.tree[0].Error() + ": " + e.code
}

// Code returns the stable classification without exposing the cause text. The
// concrete type stays package-private, but callers can inspect this method via
// errors.As into an interface when they need structured diagnostics.
func (e *codedEvaluationError) Code() string { return e.code }

// Unwrap exposes the sentinel and the retained causes to errors.Is/errors.As.
func (e *codedEvaluationError) Unwrap() []error { return e.tree }
