package agenteval

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
)

// codedErrorTestPath stands in for a configured private workspace path that a
// cause carries and the rendered text must never repeat.
const codedErrorTestPath = "/private/workspace/runs/run-1/audit.jsonl"

func TestCodedErrorRendersSentinelAndCodeOnly(t *testing.T) {
	sentinel := errors.New("evaluation rejected")
	pathCause := &fs.PathError{Op: "write", Path: codedErrorTestPath, Err: fs.ErrPermission}
	for _, test := range []struct {
		name   string
		code   string
		causes []error
		want   string
	}{
		{name: "zero causes", code: "cap", want: "evaluation rejected: cap"},
		{name: "only nil causes", code: "cap", causes: []error{nil, nil}, want: "evaluation rejected: cap"},
		{name: "one cause", code: "write", causes: []error{pathCause}, want: "evaluation rejected: write"},
		{name: "multiple causes", code: "state", causes: []error{pathCause, errors.New("persist state: " + codedErrorTestPath)},
			want: "evaluation rejected: state"},
		{name: "empty code", causes: []error{pathCause}, want: "evaluation rejected"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := codedError(sentinel, test.code, test.causes...)
			if got := err.Error(); got != test.want {
				t.Fatalf("Error()=%q, want %q", got, test.want)
			}
			if strings.Contains(err.Error(), codedErrorTestPath) {
				t.Fatalf("rendered text leaked cause detail: %q", err.Error())
			}
			if !errors.Is(err, sentinel) {
				t.Fatalf("error %v lost its sentinel", err)
			}
		})
	}
}

func TestCodedErrorExposesEveryNonNilCause(t *testing.T) {
	sentinel := errors.New("evaluation rejected")
	pathCause := &fs.PathError{Op: "write", Path: codedErrorTestPath, Err: fs.ErrPermission}
	stateCause := errors.New("state not persisted")
	err := codedError(sentinel, "state", nil, pathCause, nil, stateCause, nil)

	for _, target := range []error{sentinel, pathCause, stateCause, fs.ErrPermission} {
		if !errors.Is(err, target) {
			t.Fatalf("errors.Is lost %v in %v", target, err)
		}
	}
	var typed *fs.PathError
	if !errors.As(err, &typed) {
		t.Fatalf("errors.As did not reach the typed cause in %v", err)
	}
	if typed.Path != codedErrorTestPath || typed.Op != "write" {
		t.Fatalf("typed cause=%+v, want the original path error", typed)
	}
	var classified interface{ Code() string }
	if !errors.As(err, &classified) || classified.Code() != "state" {
		t.Fatalf("coded cause=%v, want state classification", classified)
	}
	unwrapped, ok := err.(interface{ Unwrap() []error })
	if !ok {
		t.Fatalf("%T does not unwrap to multiple errors", err)
	}
	tree := unwrapped.Unwrap()
	if len(tree) != 3 {
		t.Fatalf("unwrap tree=%v, want the sentinel plus two non-nil causes", tree)
	}
	if tree[0] != sentinel || tree[1] != error(pathCause) || tree[2] != stateCause {
		t.Fatalf("unwrap tree=%v, want sentinel first then causes in order", tree)
	}
}

func TestCodedErrorWithoutCausesStillUnwrapsToTheSentinel(t *testing.T) {
	sentinel := errors.New("evaluation rejected")
	unwrapped, ok := codedError(sentinel, "latched").(interface{ Unwrap() []error })
	if !ok {
		t.Fatal("coded error does not unwrap to multiple errors")
	}
	if tree := unwrapped.Unwrap(); len(tree) != 1 || tree[0] != sentinel {
		t.Fatalf("unwrap tree=%v, want only the sentinel", tree)
	}
}
