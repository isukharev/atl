package grading

import "errors"

type ErrorCode string

const (
	ErrorContract    ErrorCode = "grader_contract_invalid"
	ErrorUnsupported ErrorCode = "grader_unsupported"
	ErrorPolicy      ErrorCode = "grader_policy_denied"
	ErrorEvidence    ErrorCode = "grader_evidence_invalid"
	ErrorExecution   ErrorCode = "grader_execution_failed"
	ErrorInterrupted ErrorCode = "grader_interrupted"
)

type Error struct {
	code  ErrorCode
	cause error
}

func (e *Error) Error() string   { return string(e.code) }
func (e *Error) Unwrap() error   { return e.cause }
func (e *Error) Code() ErrorCode { return e.code }

func newError(code ErrorCode, cause error) error { return &Error{code: code, cause: cause} }

func CodeOf(err error) (ErrorCode, bool) {
	var target *Error
	if !errors.As(err, &target) {
		return "", false
	}
	return target.Code(), true
}
