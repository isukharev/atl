package agenteval

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validCLICommandPolicy() CLICommandPolicy {
	return CLICommandPolicy{
		SchemaVersion: CLICommandPolicySchemaVersion,
		Rules: []CLICommandRule{{
			Name:    "jira_digest",
			Command: []string{"jira", "epic", "digest"},
			Positionals: []CLIArgumentRule{{
				Values: []string{"PROJ-1"},
			}},
			Flags: []CLIFlagRule{
				{Name: "--quarter", Values: []string{"2026-Q2"}, Required: true},
				{Name: "--status-field", Values: []string{"Delivery Notes"}},
				{Name: "--read-only"},
				{Name: "-o", Values: []string{"json", "text"}},
			},
			MaxInvocations: 2,
		}},
	}
}

func TestCLICommandPolicyMatchesOnlyReviewedArguments(t *testing.T) {
	policy := validCLICommandPolicy()
	match, err := policy.Match([]string{
		"jira", "epic", "digest", "--read-only", "PROJ-1", "-o", "json",
		"--quarter", "2026-Q2", "--status-field", "Delivery Notes",
	})
	if err != nil {
		t.Fatal(err)
	}
	if match.Name != "jira_digest" || match.MaxInvocations != 2 {
		t.Fatalf("match=%+v", match)
	}
	if _, err := policy.Match([]string{"--read-only", "jira", "epic", "digest", "PROJ-1", "--quarter", "2026-Q2"}); err != nil {
		t.Fatalf("monotonic global read-only flag: %v", err)
	}

	for name, args := range map[string][]string{
		"changed target":   {"jira", "epic", "digest", "PROJ-2", "--quarter", "2026-Q2"},
		"unknown flag":     {"jira", "epic", "digest", "PROJ-1", "--quarter", "2026-Q2", "--raw"},
		"missing flag":     {"jira", "epic", "digest", "PROJ-1"},
		"duplicate flag":   {"jira", "epic", "digest", "PROJ-1", "--quarter", "2026-Q2", "--quarter", "2026-Q2"},
		"extra positional": {"jira", "epic", "digest", "PROJ-1", "extra", "--quarter", "2026-Q2"},
		"joined flag":      {"jira", "epic", "digest", "PROJ-1", "--quarter=2026-Q2"},
		"separator":        {"jira", "epic", "digest", "PROJ-1", "--quarter", "2026-Q2", "--"},
		"wrong value":      {"jira", "epic", "digest", "PROJ-1", "--quarter", "2026-Q3"},
		"duplicate global": {"--read-only", "--read-only", "jira", "epic", "digest", "PROJ-1", "--quarter", "2026-Q2"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := policy.Match(args); err == nil {
				t.Fatal("command outside the reviewed policy matched")
			}
		})
	}
}

func TestCLICommandPolicyRejectsAmbiguousAndInvalidRules(t *testing.T) {
	policy := validCLICommandPolicy()
	policy.Rules = append(policy.Rules, policy.Rules[0])
	policy.Rules[1].Name = "same_command"
	if _, err := policy.Match([]string{"jira", "epic", "digest", "PROJ-1", "--quarter", "2026-Q2"}); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("err=%v", err)
	}

	for name, mutate := range map[string]func(*CLICommandPolicy){
		"schema":            func(p *CLICommandPolicy) { p.SchemaVersion++ },
		"no rules":          func(p *CLICommandPolicy) { p.Rules = nil },
		"duplicate name":    func(p *CLICommandPolicy) { p.Rules = append(p.Rules, p.Rules[0]) },
		"bad command":       func(p *CLICommandPolicy) { p.Rules[0].Command[0] = "../jira" },
		"bad flag":          func(p *CLICommandPolicy) { p.Rules[0].Flags[0].Name = "quarter" },
		"bad value":         func(p *CLICommandPolicy) { p.Rules[0].Positionals[0].Values[0] = "PROJ-1\nsecret" },
		"bad value format":  func(p *CLICommandPolicy) { p.Rules[0].Flags[0].ValueFormat = "hex" },
		"values and format": func(p *CLICommandPolicy) { p.Rules[0].Flags[0].ValueFormat = "sha256" },
		"zero budget":       func(p *CLICommandPolicy) { p.Rules[0].MaxInvocations = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := validCLICommandPolicy()
			mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("invalid policy passed")
			}
		})
	}
}

func TestCLICommandPolicyMatchesSHA256FlagValue(t *testing.T) {
	policy := CLICommandPolicy{SchemaVersion: CLICommandPolicySchemaVersion, Rules: []CLICommandRule{{
		Name: "apply", Command: []string{"conf", "plan", "apply"},
		Positionals:    []CLIArgumentRule{{Values: []string{"plan.json"}}},
		Flags:          []CLIFlagRule{{Name: "--expected-proposal-hash", ValueFormat: "sha256", Required: true}},
		MaxInvocations: 1,
	}}}
	valid := strings.Repeat("a", 64)
	if _, err := policy.Match([]string{"conf", "plan", "apply", "plan.json", "--expected-proposal-hash", valid}); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{strings.Repeat("a", 63), strings.Repeat("A", 64), strings.Repeat("g", 64)} {
		if _, err := policy.Match([]string{"conf", "plan", "apply", "plan.json", "--expected-proposal-hash", invalid}); err == nil {
			t.Fatalf("invalid sha256 %q matched", invalid)
		}
	}
}

func TestCLICommandPolicyMatchesExactRepeatedFlagValues(t *testing.T) {
	policy := CLICommandPolicy{SchemaVersion: CLICommandPolicySchemaVersion, Rules: []CLICommandRule{{
		Name: "create", Command: []string{"jira", "issue", "create"},
		Flags: []CLIFlagRule{{
			Name: "--field", Values: []string{"customfield_10001=Platform A", "customfield_10002=Research Group"}, Required: true, Occurrences: 2,
		}},
		MaxInvocations: 1,
	}}}
	if _, err := policy.Match([]string{"jira", "issue", "create", "--field", "customfield_10002=Research Group", "--field", "customfield_10001=Platform A"}); err != nil {
		t.Fatal(err)
	}
	for name, args := range map[string][]string{
		"missing":   {"jira", "issue", "create", "--field", "customfield_10001=Platform A"},
		"duplicate": {"jira", "issue", "create", "--field", "customfield_10001=Platform A", "--field", "customfield_10001=Platform A"},
		"changed":   {"jira", "issue", "create", "--field", "customfield_10001=Platform A", "--field", "customfield_10002=Another Group"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := policy.Match(args); err == nil {
				t.Fatal("command outside the exact repeated-value policy matched")
			}
		})
	}
}

func TestCLICommandPolicyRepeatedFlagsRequireCurrentSchemaAndExactValues(t *testing.T) {
	for name, mutate := range map[string]func(*CLICommandPolicy){
		"legacy schema": func(p *CLICommandPolicy) { p.SchemaVersion = LegacyCLICommandPolicySchemaVersion },
		"not required":  func(p *CLICommandPolicy) { p.Rules[0].Flags[0].Required = false },
		"value count":   func(p *CLICommandPolicy) { p.Rules[0].Flags[0].Values = p.Rules[0].Flags[0].Values[:1] },
		"too many":      func(p *CLICommandPolicy) { p.Rules[0].Flags[0].Occurrences = 33 },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := CLICommandPolicy{SchemaVersion: CLICommandPolicySchemaVersion, Rules: []CLICommandRule{{
				Name: "create", Command: []string{"jira", "issue", "create"},
				Flags: []CLIFlagRule{{Name: "--field", Values: []string{"a=1", "b=2"}, Required: true, Occurrences: 2}}, MaxInvocations: 1,
			}}}
			mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("invalid repeated flag policy passed")
			}
		})
	}
	legacy := validCLICommandPolicy()
	legacy.SchemaVersion = LegacyCLICommandPolicySchemaVersion
	if err := legacy.Validate(); err != nil {
		t.Fatalf("legacy policy without repeated flags: %v", err)
	}
}

func TestCLICommandPolicyFileIsStrictAndOwnerOnly(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "policy.json")
	data, err := EncodeCLICommandPolicy(validCLICommandPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadCLICommandPolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeCLICommandPolicy(loaded)
	if err != nil || !bytes.Equal(data, encoded) {
		t.Fatalf("round trip err=%v\n%s", err, encoded)
	}

	if err := os.WriteFile(path, append(bytes.TrimSpace(data), []byte(` {}`)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCLICommandPolicy(path); err == nil {
		t.Fatal("trailing JSON data passed")
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCLICommandPolicy(path); err == nil {
		t.Fatal("group-readable policy passed")
	}

	symlink := filepath.Join(directory, "policy-link.json")
	if err := os.Symlink(path, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCLICommandPolicy(symlink); err == nil {
		t.Fatal("symlink policy passed")
	}
}
