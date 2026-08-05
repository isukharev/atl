package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWrapperOracleProxyRecordJSONBytes(t *testing.T) {
	for _, test := range []struct {
		name string
		in   proxyRecord
		want string
	}{
		{name: "zero", in: proxyRecord{}, want: `{"stdout_bytes":0,"stderr_bytes":0,"exit_code":0}` + "\n"},
		{name: "full", in: proxyRecord{CommandFamily: "jira.fields", CalibrationObservationSHA256: strings.Repeat("a", 64), ErrorKind: "not_found", ErrorRemediation: "verify_identifier_or_access", Denied: true, StdoutBytes: 12, StderrBytes: 34, ExitCode: 4}, want: `{"command_family":"jira.fields","calibration_observation_sha256":"` + strings.Repeat("a", 64) + `","error_kind":"not_found","error_remediation":"verify_identifier_or_access","denied":true,"stdout_bytes":12,"stderr_bytes":34,"exit_code":4}` + "\n"},
		{name: "denied", in: proxyRecord{Denied: true, ExitCode: 2}, want: `{"denied":true,"stdout_bytes":0,"stderr_bytes":0,"exit_code":2}` + "\n"},
		{name: "classified", in: proxyRecord{ErrorKind: "not_found", ErrorRemediation: "verify_identifier_or_access", StderrBytes: 10, ExitCode: 4}, want: `{"error_kind":"not_found","error_remediation":"verify_identifier_or_access","stdout_bytes":0,"stderr_bytes":10,"exit_code":4}` + "\n"},
		{name: "calibration", in: proxyRecord{CommandFamily: "atl_version", CalibrationObservationSHA256: strings.Repeat("b", 64), StdoutBytes: 8}, want: `{"command_family":"atl_version","calibration_observation_sha256":"` + strings.Repeat("b", 64) + `","stdout_bytes":8,"stderr_bytes":0,"exit_code":0}` + "\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "counter.jsonl")
			if err := appendProxyRecord(path, test.in); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != test.want {
				t.Fatalf("record=%q err=%v want=%q", got, err, test.want)
			}
		})
	}
}

func TestWrapperOracleWriteAuthorityRequiresExactOne(t *testing.T) {
	for _, variable := range []string{"reviewed", "synthetic"} {
		for _, test := range []struct {
			value string
			want  bool
		}{
			{value: ""}, {value: "0"}, {value: "true"}, {value: "01"}, {value: " 1"}, {value: "1 ", want: false}, {value: "1", want: true},
		} {
			t.Run(variable+"/"+test.value, func(t *testing.T) {
				t.Setenv("ATL_EVAL_ALLOW_REVIEWED_WRITES", "")
				t.Setenv("ATL_EVAL_ALLOW_SYNTHETIC_WRITES", "")
				if variable == "reviewed" {
					t.Setenv("ATL_EVAL_ALLOW_REVIEWED_WRITES", test.value)
				} else {
					t.Setenv("ATL_EVAL_ALLOW_SYNTHETIC_WRITES", test.value)
				}
				if got := reviewedWriteEnvironmentEnabled(); got != test.want {
					t.Fatalf("value=%q enabled=%v want=%v", test.value, got, test.want)
				}
			})
		}
	}
}

func TestWrapperOracleAllowedCommandArraySemantics(t *testing.T) {
	args := []string{"jira", "fields"}
	for _, test := range []struct {
		name string
		raw  string
		want bool
	}{
		{name: "malformed", raw: `[`},
		{name: "null", raw: `null`},
		{name: "empty", raw: `[]`},
		{name: "wrong scalar", raw: `"atl jira fields"`},
		{name: "empty member", raw: `[""]`},
		{name: "valid", raw: `["atl jira fields"]`, want: true},
		{name: "duplicate valid", raw: `["atl jira fields","atl jira fields"]`, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := allowedATLArgs(args, test.raw); got != test.want {
				t.Fatalf("allowedATLArgs(%q)=%v want=%v", test.raw, got, test.want)
			}
		})
	}
}

func TestWrapperOracleAllowedMCPArraySemantics(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  string
		want bool
	}{
		{name: "malformed", raw: `[`},
		{name: "null", raw: `null`},
		{name: "empty", raw: `[]`},
		{name: "wrong scalar", raw: `"mcp__atl__jira_fields"`},
		{name: "empty member", raw: `[""]`},
		{name: "valid", raw: `["mcp__atl__jira_fields"]`, want: true},
		{name: "duplicate valid", raw: `["mcp__atl__jira_fields","mcp__atl__jira_fields"]`, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := allowedMCPGuardTool("mcp__atl__jira_fields", test.raw); got != test.want {
				t.Fatalf("allowedMCPGuardTool(%q)=%v want=%v", test.raw, got, test.want)
			}
		})
	}
}

func TestWrapperOracleReadRootArraySemantics(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "inside.txt")
	if err := os.WriteFile(inside, []byte("synthetic"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("synthetic"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, raw := range []string{`[`, `null`, `[]`, `"` + root + `"`} {
		if _, allowed, err := resolveAllowedReadPath(inside, raw, false); err == nil || allowed {
			t.Fatalf("invalid roots %q produced allowed=%v err=%v", raw, allowed, err)
		}
	}
	valid := `[` + quotedJSONOracle(root) + `]`
	duplicate := `[` + quotedJSONOracle(root) + `,` + quotedJSONOracle(root) + `]`
	for _, raw := range []string{valid, duplicate} {
		resolved, allowed, err := resolveAllowedReadPath(inside, raw, false)
		if err != nil || !allowed || resolved != inside {
			t.Fatalf("roots=%q resolved=%q allowed=%v err=%v", raw, resolved, allowed, err)
		}
	}
	if _, allowed, err := resolveAllowedReadPath(outside, valid, false); err != nil || allowed {
		t.Fatalf("outside allowed=%v err=%v", allowed, err)
	}
	if resolved, allowed, err := resolveAllowedReadPath("", `[`, true); err != nil || allowed || resolved != "" {
		t.Fatalf("empty target resolved=%q allowed=%v err=%v", resolved, allowed, err)
	}

	t.Setenv("ATL_EVAL_WORKSPACE_ROOT", "")
	if _, allowed, err := resolveAllowedReadPath("inside.txt", valid, true); err != nil || allowed {
		t.Fatalf("private relative path without workspace allowed=%v err=%v", allowed, err)
	}
	t.Setenv("ATL_EVAL_WORKSPACE_ROOT", root)
	resolved, allowed, err := resolveAllowedReadPath("inside.txt", valid, true)
	if err != nil || !allowed || resolved != inside {
		t.Fatalf("workspace relative resolved=%q allowed=%v err=%v", resolved, allowed, err)
	}

	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err == nil {
		if _, allowed, err := resolveAllowedReadPath(link, valid, false); err != nil || allowed {
			t.Fatalf("escaping symlink allowed=%v err=%v", allowed, err)
		}
	}
}

func TestWrapperOracleEmptyAndUnknownGuardModesKeepLegacyFallback(t *testing.T) {
	for _, mode := range []string{"", "unknown-mode"} {
		t.Run(mode, func(t *testing.T) {
			counter := filepath.Join(t.TempDir(), "guard.jsonl")
			t.Setenv("ATL_EVAL_GUARD_MODE", mode)
			t.Setenv("ATL_EVAL_ALLOWED_COMMANDS", `["atl jira fields"]`)
			t.Setenv("ATL_EVAL_GUARD_COUNTER", counter)
			input := `{"tool_name":"Bash","tool_input":{"command":"atl jira fields"}}`
			var output, errorOutput bytes.Buffer
			if code := runClaudeBashGuard(strings.NewReader(input), &output, &errorOutput); code != 0 || !strings.Contains(output.String(), `"permissionDecision":"allow"`) {
				t.Fatalf("mode=%q code=%d output=%s stderr=%s", mode, code, output.String(), errorOutput.String())
			}
			record, err := os.ReadFile(counter)
			if err != nil || string(record) != `{"decision":"allow","family":"atl"}`+"\n" {
				t.Fatalf("mode=%q record=%q err=%v", mode, record, err)
			}
		})
	}

	for _, raw := range []string{"", `null`, `[]`, `[`} {
		t.Run("missing-policy/"+raw, func(t *testing.T) {
			t.Setenv("ATL_EVAL_GUARD_MODE", "unknown-mode")
			t.Setenv("ATL_EVAL_ALLOWED_COMMANDS", raw)
			t.Setenv("ATL_EVAL_GUARD_COUNTER", filepath.Join(t.TempDir(), "guard.jsonl"))
			input := `{"tool_name":"Bash","tool_input":{"command":"atl jira fields"}}`
			var output, errorOutput bytes.Buffer
			if code := runClaudeBashGuard(strings.NewReader(input), &output, &errorOutput); code != 2 || !strings.Contains(errorOutput.String(), "has no command policy") {
				t.Fatalf("policy=%q code=%d output=%s stderr=%s", raw, code, output.String(), errorOutput.String())
			}
		})
	}
}

func TestWrapperOracleDelegationAtoiAndBounds(t *testing.T) {
	for _, test := range []struct {
		name    string
		raw     string
		allowed bool
		wantErr bool
	}{
		{name: "empty", raw: "", wantErr: true},
		{name: "space", raw: " 1", wantErr: true},
		{name: "malformed", raw: "one", wantErr: true},
		{name: "negative", raw: "-1", wantErr: true},
		{name: "zero", raw: "0"},
		{name: "plus", raw: "+1", allowed: true},
		{name: "leading zero", raw: "01", allowed: true},
		{name: "upper bound", raw: "3", allowed: true},
		{name: "too large", raw: "4", wantErr: true},
		{name: "overflow", raw: "999999999999999999999999999999", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			counter := filepath.Join(t.TempDir(), "guard.jsonl")
			allowed, err := reserveDelegationSlot(counter, test.raw)
			if allowed != test.allowed || (err != nil) != test.wantErr {
				t.Fatalf("raw=%q allowed=%v err=%v wantAllowed=%v wantErr=%v", test.raw, allowed, err, test.allowed, test.wantErr)
			}
		})
	}

	if allowed, err := reserveDelegationSlot("", "1"); err == nil || allowed {
		t.Fatalf("missing counter allowed=%v err=%v", allowed, err)
	}
	counter := filepath.Join(t.TempDir(), "guard.jsonl")
	first, err := reserveDelegationSlot(counter, "1")
	if err != nil || !first {
		t.Fatalf("first slot allowed=%v err=%v", first, err)
	}
	second, err := reserveDelegationSlot(counter, "1")
	if err != nil || second {
		t.Fatalf("exhausted slot allowed=%v err=%v", second, err)
	}
}

func quotedJSONOracle(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
}
