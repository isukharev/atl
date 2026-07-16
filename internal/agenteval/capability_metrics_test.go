package agenteval

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCapabilityFamiliesAreGenericAndPrivacySafe(t *testing.T) {
	private := "SYNTHETIC-SENSITIVE-123"
	family, ok := CapabilityFamilyForCLI([]string{"jira", "epic", "digest", private, "--quarter", "2026-Q2"})
	if !ok || family != "jira.epic.digest" || strings.Contains(family, private) {
		t.Fatalf("family=%q ok=%t", family, ok)
	}
	if family, ok := CapabilityFamilyForMCP("jira_epic_digest"); !ok || family != "jira.epic.digest" {
		t.Fatalf("family=%q ok=%t", family, ok)
	}
	if family, ok := CapabilityFamilyForCLI([]string{"jira", "issue", "field", "get", private, "--field", "customfield_1"}); !ok || family != "jira.issue.field" {
		t.Fatalf("CLI field family=%q ok=%t", family, ok)
	}
	if family, ok := CapabilityFamilyForMCP("jira_issue_field_get"); !ok || family != "jira.issue.field" {
		t.Fatalf("MCP field family=%q ok=%t", family, ok)
	}
	if _, ok := CapabilityFamilyForMCP("private_" + private); ok {
		t.Fatal("unknown MCP tool was attributed")
	}
	encoded, _ := json.Marshal([]CapabilityFamilyMetric{{Family: family, Invocations: 1, Successes: 1, OutputBytes: 42}})
	if strings.Contains(string(encoded), private) {
		t.Fatalf("metric leaked input: %s", encoded)
	}
}

func TestCapabilityFamilyValidationFailsClosed(t *testing.T) {
	for _, values := range [][]CapabilityFamilyMetric{
		{{Family: "jira.epic.digest", Invocations: 1, Successes: 0, Failures: 0}},
		{{Family: "private value", Invocations: 1, Successes: 1}},
		{{Family: "jira.fields", Invocations: 1, Successes: 1}, {Family: "jira.fields", Invocations: 1, Successes: 1}},
	} {
		if _, err := normalizeCapabilityFamilies(values); err == nil {
			t.Fatalf("accepted %+v", values)
		}
	}
}
