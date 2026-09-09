package agenteval

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestDecodeJiraIssueGraphViewAcceptsClosedRemoteLinkFailureMatrix(t *testing.T) {
	tests := []struct {
		class         string
		status        int
		sourceStatus  string
		partialReason string
	}{
		{"authentication", 0, "forbidden", ""},
		{"authentication", 401, "forbidden", ""},
		{"permission", 0, "forbidden", ""},
		{"permission", 403, "forbidden", ""},
		{"not_found", 0, "unsupported", ""},
		{"not_found", 404, "unsupported", ""},
		{"http", 429, "partial", "request_failed"},
		{"http", 500, "partial", "malformed_response"},
		{"transport", 0, "partial", "request_failed"},
		{"request", 0, "partial", "request_failed"},
	}
	for _, test := range tests {
		t.Run(test.class+test.sourceStatus, func(t *testing.T) {
			view := jiraGraphWireRemoteFailureView(t, test.class, test.status, test.sourceStatus, test.partialReason)
			encoded := jiraGraphWireEncode(t, view)
			decoded, err := DecodeJiraIssueGraphView(bytes.NewReader(encoded))
			if err != nil {
				t.Fatalf("decode diagnostic: %v\n%s", err, encoded)
			}
			failure := jiraGraphWireRemoteSource(t, &decoded).Failure
			if failure == nil || failure.Class != test.class || (failure.HTTPStatus == nil) != (test.status == 0) {
				t.Fatalf("failure=%+v", failure)
			}
		})
	}
}

func TestDecodeJiraIssueGraphViewRejectsRemoteLinkFailureContradictions(t *testing.T) {
	mutations := []struct {
		name   string
		mutate func(*JiraIssueGraphSource)
	}{
		{"successful source", func(source *JiraIssueGraphSource) { source.Status, source.Complete = "complete", true }},
		{"nonzero count", func(source *JiraIssueGraphSource) { source.Count = 1 }},
		{"truncated", func(source *JiraIssueGraphSource) { source.Truncated = true }},
		{"unknown class", func(source *JiraIssueGraphSource) { source.Failure.Class = "unknown" }},
		{"mismatched status", func(source *JiraIssueGraphSource) { status := 401; source.Failure.HTTPStatus = &status }},
		{"explicit zero status", func(source *JiraIssueGraphSource) { status := 0; source.Failure.HTTPStatus = &status }},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			view := jiraGraphWireRemoteFailureView(t, "permission", 403, "forbidden", "")
			test.mutate(jiraGraphWireRemoteSource(t, &view))
			if _, err := DecodeJiraIssueGraphView(bytes.NewReader(jiraGraphWireEncode(t, view))); err == nil {
				t.Fatal("contradictory diagnostic was accepted")
			}
		})
	}
	t.Run("wrong source", func(t *testing.T) {
		view := jiraGraphWireRemoteFailureView(t, "permission", 403, "forbidden", "")
		remote := jiraGraphWireRemoteSource(t, &view)
		failure := remote.Failure
		remote.Status, remote.Complete, remote.Failure = "empty", true, nil
		for index := range view.Sources {
			if view.Sources[index].Kind == "comments" {
				view.Sources[index].Status, view.Sources[index].Complete, view.Sources[index].Failure = "forbidden", false, failure
			}
		}
		if _, err := DecodeJiraIssueGraphView(bytes.NewReader(jiraGraphWireEncode(t, view))); err == nil {
			t.Fatal("diagnostic on comments was accepted")
		}
	})

	valid := jiraGraphWireEncode(t, jiraGraphWireRemoteFailureView(t, "permission", 403, "forbidden", ""))
	var document map[string]any
	if err := json.Unmarshal(valid, &document); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"null failure", func(source map[string]any) { source["failure"] = nil }},
		{"null HTTP status", func(source map[string]any) { source["failure"].(map[string]any)["http_status"] = nil }},
		{"unknown failure member", func(source map[string]any) { source["failure"].(map[string]any)["message"] = "PRIVATE" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			var clone map[string]any
			encoded, _ := json.Marshal(document)
			_ = json.Unmarshal(encoded, &clone)
			candidate := jiraGraphWireRawRemoteSource(t, clone)
			test.mutate(candidate)
			if _, err := DecodeJiraIssueGraphView(bytes.NewReader(jiraGraphWireEncode(t, clone))); err == nil {
				t.Fatal("invalid diagnostic wire was accepted")
			}
		})
	}
}

func jiraGraphWireRemoteFailureView(t *testing.T, class string, status int, sourceStatus, partialReason string) JiraIssueGraphView {
	t.Helper()
	view := jiraGraphWireBaseView()
	source := jiraGraphWireRemoteSource(t, &view)
	source.Status, source.Complete, source.PartialReason = sourceStatus, false, partialReason
	source.Failure = &JiraIssueGraphSourceFailure{Class: class}
	if status != 0 {
		source.Failure.HTTPStatus = &status
	}
	view.Complete = false
	view.Summary.IncompleteSourceCount = 1
	view.Summary.SourceStatusCounts["empty"]--
	view.Summary.SourceStatusCounts[sourceStatus]++
	view.Warnings = []string{"one or more requested graph sources are incomplete"}
	return view
}

func jiraGraphWireRemoteSource(t *testing.T, view *JiraIssueGraphView) *JiraIssueGraphSource {
	t.Helper()
	for index := range view.Sources {
		if view.Sources[index].Kind == "remote_links" {
			return &view.Sources[index]
		}
	}
	t.Fatal("wire fixture omitted remote_links")
	return nil
}

func jiraGraphWireRawRemoteSource(t *testing.T, document map[string]any) map[string]any {
	t.Helper()
	sources, ok := document["sources"].([]any)
	if !ok {
		t.Fatal("wire document omitted sources")
	}
	for _, value := range sources {
		source, ok := value.(map[string]any)
		if ok && source["kind"] == "remote_links" {
			return source
		}
	}
	t.Fatal("wire document omitted remote_links")
	return nil
}
