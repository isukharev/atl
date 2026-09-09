package app

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

type jiraProjectPageClientStub struct {
	calls     int
	arguments domain.BrokerProjectPageArguments
	result    domain.BrokerJiraProjectPageResultV2
	err       error
}

func (stub *jiraProjectPageClientStub) ReadJiraProjectIssuePage(_ context.Context, arguments domain.BrokerProjectPageArguments) (domain.BrokerJiraProjectPageResultV2, error) {
	stub.calls++
	stub.arguments = arguments
	stub.arguments.Fields = append([]domain.BrokerProjectPageField{}, arguments.Fields...)
	return stub.result, stub.err
}

func TestJiraProjectIssuePageMapsEveryContractTruthAndPreservesOrder(t *testing.T) {
	stub := &jiraProjectPageClientStub{result: domain.BrokerJiraProjectPageResultV2{
		SchemaVersion: 2, ArgumentsSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ConsistencyProfile: domain.BrokerReadConsistencyIdentitySnapshotV1, ProjectID: "7", ProjectKey: "EXAMPLE",
		Issues: []domain.BrokerJiraProjectPageResultIssueV2{
			{ID: "20", Key: "EXAMPLE-20", ProjectID: "7", ProjectKey: "EXAMPLE", Updated: "2026-09-08T10:00:00.000+0000", Fields: []domain.BrokerJiraIssueReadField{
				{Field: domain.BrokerJiraIssueFieldDescription, Present: true, Null: true},
				{Field: domain.BrokerJiraIssueFieldSummary, Present: true, Value: "First"},
			}},
			{ID: "3", Key: "EXAMPLE-3", ProjectID: "7", ProjectKey: "EXAMPLE", Updated: "2026-09-08T11:00:00.000+0000", Fields: []domain.BrokerJiraIssueReadField{
				{Field: domain.BrokerJiraIssueFieldDescription, Present: true, Value: "Second body"},
				{Field: domain.BrokerJiraIssueFieldSummary, Present: true, Value: "Second"},
			}},
		},
		Page: domain.BrokerJiraProjectPageResultPageV2{
			StartAt: 15, MaxResults: 2, Total: 18, Count: 2, NextCursor: "17", NextCursorPresent: true,
			CoordinateExhausted: false, SelectionComplete: false, PartialReason: "",
		},
		Complete: true,
	}}
	service := NewJiraService(JiraDependencies{ProjectPages: stub})
	arguments, err := NewJiraProjectIssuePageArguments("EXAMPLE", []string{"summary", "description"}, 2, "15")
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.ProjectIssuePage(context.Background(), arguments)
	if err != nil {
		t.Fatal(err)
	}
	if stub.calls != 1 || stub.arguments.ProjectKey != "EXAMPLE" || stub.arguments.StartAt != 15 ||
		!slices.Equal(stub.arguments.Fields, []domain.BrokerProjectPageField{domain.BrokerProjectPageFieldDescription, domain.BrokerProjectPageFieldSummary}) ||
		len(result.Issues) != 2 || result.Issues[0].Key != "EXAMPLE-20" || result.Issues[1].Key != "EXAMPLE-3" ||
		!result.Issues[0].Fields[0].Present || !result.Issues[0].Fields[0].Null || result.Issues[0].Fields[0].Value != "" ||
		result.ArgumentsSHA256 != stub.result.ArgumentsSHA256 || result.ConsistencyProfile != stub.result.ConsistencyProfile ||
		!result.Page.NextCursorPresent || result.Page.NextCursor != "17" || result.Page.CoordinateExhausted || result.Page.SelectionComplete || !result.Complete {
		t.Fatalf("arguments=%+v result=%+v", stub.arguments, result)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		`"arguments_sha256"`, `"consistency_profile"`, `"project_id"`, `"updated"`,
		`"present":true`, `"null":true`, `"value":""`, `"next_cursor":"17"`,
		`"next_cursor_present":true`, `"coordinate_exhausted":false`, `"selection_complete":false`,
		`"partial_reason":""`, `"complete":true`,
	} {
		if !jsonContains(encoded, required) {
			t.Fatalf("JSON omitted %s: %s", required, encoded)
		}
	}
}

func TestJiraProjectIssuePageRejectsInvalidSelectorsBeforeReader(t *testing.T) {
	stub := &jiraProjectPageClientStub{}
	service := NewJiraService(JiraDependencies{ProjectPages: stub})
	for _, test := range []struct {
		project string
		fields  []string
		limit   int
		cursor  string
	}{
		{project: "example", fields: []string{"summary"}, limit: 1, cursor: "0"},
		{project: "EXAMPLE", fields: []string{"status"}, limit: 1, cursor: "0"},
		{project: "EXAMPLE", fields: []string{"summary", "summary"}, limit: 1, cursor: "0"},
		{project: "EXAMPLE", fields: []string{"summary"}, limit: 0, cursor: "0"},
		{project: "EXAMPLE", fields: []string{"summary"}, limit: 16, cursor: "0"},
		{project: "EXAMPLE", fields: []string{"summary"}, limit: 1, cursor: "01"},
		{project: "EXAMPLE", fields: []string{"summary"}, limit: 1, cursor: "1000001"},
	} {
		if _, err := NewJiraProjectIssuePageArguments(test.project, test.fields, test.limit, test.cursor); !errors.Is(err, domain.ErrUsage) {
			t.Fatalf("selector=%+v err=%v", test, err)
		}
	}
	unsorted := domain.BrokerProjectPageArguments{
		ProjectKey: "EXAMPLE", Fields: []domain.BrokerProjectPageField{domain.BrokerProjectPageFieldSummary, domain.BrokerProjectPageFieldDescription}, MaxResults: 1,
	}
	if _, err := service.ProjectIssuePage(context.Background(), unsorted); !errors.Is(err, domain.ErrUsage) || stub.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, stub.calls)
	}
}

func TestJiraProjectIssuePageNilPortIsClosedUnsupported(t *testing.T) {
	result, err := NewJiraService(JiraDependencies{}).ProjectIssuePage(context.Background(), domain.BrokerProjectPageArguments{})
	reason, _ := brokercontract.Reason(err)
	if result != nil || reason != domain.BrokerReasonUnsupported {
		t.Fatalf("result=%+v reason=%s err=%v", result, reason, err)
	}
}

func jsonContains(encoded []byte, value string) bool {
	return strings.Contains(string(encoded), value)
}
