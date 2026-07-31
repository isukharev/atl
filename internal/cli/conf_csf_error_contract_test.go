package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/diagnostic"
	"github.com/isukharev/atl/internal/domain"
)

func overDepthCSF() string {
	depth := csf.MaxNestingDepth + 1
	return strings.Repeat("<p>", depth) + "x" + strings.Repeat("</p>", depth)
}

func TestConfCSFValidationErrorsShareCheckFailedContract(t *testing.T) {
	validationCases := []struct {
		name string
		body string
		rule string
	}{
		{name: "malformed_xml", body: "<p>unbalanced <strong>oops</p>", rule: "well-formedness"},
		{name: "max_depth", body: overDepthCSF(), rule: "max-depth"},
	}
	commands := []string{"validate", "push", "page_create", "blog_create"}

	for _, validation := range validationCases {
		for _, command := range commands {
			t.Run(validation.name+"/"+command, func(t *testing.T) {
				server := newConfServer(t)
				var args []string
				switch command {
				case "validate":
					args = []string{"conf", "validate", writeCSF(t, validation.body)}
				case "push":
					root, path := dirtyMirror(t, server, 7)
					if err := os.WriteFile(path, []byte(validation.body), 0o644); err != nil {
						t.Fatal(err)
					}
					args = []string{"conf", "push", path, "--into", root}
				case "page_create":
					args = []string{"conf", "page", "create", "--space", "DOC", "--title", "T", "--from-file", writeCSF(t, validation.body)}
				case "blog_create":
					args = []string{"conf", "blog", "create", "--space", "DOC", "--title", "T", "--from-file", writeCSF(t, validation.body)}
				default:
					t.Fatalf("unknown command case %q", command)
				}

				requestsBefore := len(server.requests())
				stdout, cobraStderr, err := executeCLIRaw(t, confEnv(server.srv), args...)
				if !errors.Is(err, domain.ErrCheckFailed) {
					t.Fatalf("error=%v, want ErrCheckFailed", err)
				}
				if code := codeFor(err); code != exitCheckFailed {
					t.Fatalf("exit=%d, want %d (stdout=%q)", code, exitCheckFailed, stdout)
				}
				if cobraStderr != "" {
					t.Fatalf("Cobra stderr=%q, want empty", cobraStderr)
				}
				if requestsAfter := len(server.requests()); requestsAfter != requestsBefore {
					t.Fatalf("validation reached backend: requests=%d->%d", requestsBefore, requestsAfter)
				}

				problems := validationProblems(t, command, stdout)
				if len(problems) == 0 || !csf.HasErrors(problems) {
					t.Fatalf("stdout omitted blocking problems: %s", stdout)
				}
				if problems[0].Rule != validation.rule {
					t.Fatalf("problem rule=%q, want %q: %+v", problems[0].Rule, validation.rule, problems)
				}

				var errorOut bytes.Buffer
				writeError(&errorOut, "json", err, codeFor(err))
				var envelope struct {
					Code        int                 `json:"code"`
					Kind        string              `json:"kind"`
					Remediation string              `json:"remediation"`
					Recovery    diagnostic.Recovery `json:"recovery"`
				}
				if decodeErr := json.Unmarshal(errorOut.Bytes(), &envelope); decodeErr != nil {
					t.Fatalf("decode structured error: %v\n%s", decodeErr, errorOut.String())
				}
				if envelope.Code != exitCheckFailed || envelope.Kind != "check_failed" ||
					envelope.Remediation != "review_failed_check" || envelope.Recovery.Action != diagnostic.RecoveryInspectFailure ||
					envelope.Recovery.RetrySafe || !diagnostic.ValidateRecovery(envelope.Recovery) {
					t.Fatalf("structured error=%+v, want code=%d kind=check_failed remediation=review_failed_check and closed inspect_failure recovery", envelope, exitCheckFailed)
				}
			})
		}
	}
}

func TestConfPushCSFPreflightRunsBeforeConfiguration(t *testing.T) {
	validationCases := []struct {
		name string
		body string
		rule string
	}{
		{name: "malformed_xml", body: "<p>broken", rule: "well-formedness"},
		{name: "max_depth", body: overDepthCSF(), rule: "max-depth"},
	}
	for _, validation := range validationCases {
		for _, directory := range []bool{false, true} {
			name := "single"
			if directory {
				name = "directory"
			}
			t.Run(validation.name+"/"+name, func(t *testing.T) {
				server := newConfServer(t)
				root, path := dirtyMirror(t, server, 7)
				if err := os.WriteFile(path, []byte(validation.body), 0o644); err != nil {
					t.Fatal(err)
				}
				target := path
				if directory {
					target = root
				}
				requestsBefore := len(server.requests())
				stdout, _, err := executeCLIRaw(t, nil, "conf", "push", target, "--into", root)
				if !errors.Is(err, domain.ErrCheckFailed) || codeFor(err) != exitCheckFailed {
					t.Fatalf("error=%v exit=%d, want ErrCheckFailed/exit 8", err, codeFor(err))
				}
				problems := validationProblems(t, "push", stdout)
				if len(problems) == 0 || problems[0].Rule != validation.rule {
					t.Fatalf("problems=%+v, want %s", problems, validation.rule)
				}
				if requestsAfter := len(server.requests()); requestsAfter != requestsBefore {
					t.Fatalf("offline preflight reached backend: requests=%d->%d", requestsBefore, requestsAfter)
				}
			})
		}
	}
}

func TestConfPushValidTargetsFallThroughToConfiguration(t *testing.T) {
	for _, directory := range []bool{false, true} {
		name := "single"
		if directory {
			name = "directory"
		}
		t.Run(name, func(t *testing.T) {
			server := newConfServer(t)
			root, path := dirtyMirror(t, server, 7)
			target := path
			if directory {
				target = root
			}
			requestsBefore := len(server.requests())
			stdout, _, err := executeCLIRaw(t, nil, "conf", "push", target, "--into", root)
			if !errors.Is(err, domain.ErrConfig) || codeFor(err) != exitConfig {
				t.Fatalf("error=%v exit=%d, want configured-push fallthrough", err, codeFor(err))
			}
			if strings.TrimSpace(stdout) != "" {
				t.Fatalf("valid preflight emitted stdout: %q", stdout)
			}
			if requestsAfter := len(server.requests()); requestsAfter != requestsBefore {
				t.Fatalf("unconfigured fallthrough reached backend: requests=%d->%d", requestsBefore, requestsAfter)
			}
		})
	}
}

func validationProblems(t *testing.T, command, stdout string) []csf.Problem {
	t.Helper()
	var result struct {
		OK       *bool         `json:"ok"`
		Problems []csf.Problem `json:"problems"`
		Items    []struct {
			Problems []csf.Problem `json:"problems"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("decode %s validation result: %v\n%s", command, err, stdout)
	}
	if command == "push" {
		if len(result.Items) != 1 {
			t.Fatalf("push items=%d, want 1: %s", len(result.Items), stdout)
		}
		return result.Items[0].Problems
	}
	if command == "validate" && (result.OK == nil || *result.OK) {
		t.Fatalf("validate ok=%v, want false: %s", result.OK, stdout)
	}
	if command != "validate" && result.OK != nil {
		t.Fatalf("%s unexpectedly changed result shape: %s", command, stdout)
	}
	return result.Problems
}
