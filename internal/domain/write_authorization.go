package domain

import "context"

// WriteVerb describes what a governed remote operation changes. It is
// independent from the HTTP method used to perform that operation.
type WriteVerb string

const (
	WriteVerbCreate     WriteVerb = "create"
	WriteVerbUpdate     WriteVerb = "update"
	WriteVerbComment    WriteVerb = "comment"
	WriteVerbTransition WriteVerb = "transition"
	WriteVerbMove       WriteVerb = "move"
	WriteVerbDelete     WriteVerb = "delete"
)

// ValidWriteVerb reports whether verb belongs to the closed governed-write
// vocabulary.
func ValidWriteVerb(verb WriteVerb) bool {
	switch verb {
	case WriteVerbCreate, WriteVerbUpdate, WriteVerbComment,
		WriteVerbTransition, WriteVerbMove, WriteVerbDelete:
		return true
	}
	return false
}

// WriteVerbSet is the non-empty set of verbs one remote operation requires.
// Callers preserve set semantics: every member is valid and appears once.
type WriteVerbSet []WriteVerb

// ValidWriteVerbSet validates the closed, non-empty set representation.
func ValidWriteVerbSet(verbs WriteVerbSet) bool {
	if len(verbs) == 0 {
		return false
	}
	seen := make(map[WriteVerb]struct{}, len(verbs))
	for _, verb := range verbs {
		if !ValidWriteVerb(verb) {
			return false
		}
		if _, duplicate := seen[verb]; duplicate {
			return false
		}
		seen[verb] = struct{}{}
	}
	return true
}

// WriteTarget is one backend-canonical identity affected by a governed remote
// operation. Empty scalar attributes are unresolved or inapplicable. A non-nil
// empty AncestorIDs slice is a resolved root-level/treeless identity, while a
// nil slice means hierarchy identity was not resolved.
type WriteTarget struct {
	Service     string
	Kind        string
	ID          string
	Project     string
	Key         string
	Space       string
	AncestorIDs []string
}

// WriteAuthorizationRequest describes the complete verb and target
// conjunction for one governed remote operation.
type WriteAuthorizationRequest struct {
	Verbs   WriteVerbSet
	Targets []WriteTarget
}

// WriteAuthorizer is the transport-neutral last-hop authorization port.
// An admitted request returns the context that must be threaded into the
// transport; implementations use it to carry write clearance. An error denies
// the operation before the mutating request is constructed.
type WriteAuthorizer interface {
	Authorize(context.Context, WriteAuthorizationRequest) (context.Context, error)
}
