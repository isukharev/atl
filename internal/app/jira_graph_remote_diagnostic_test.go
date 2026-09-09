package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	jiraadapter "github.com/isukharev/atl/internal/adapter/jira"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

type remoteLinkDiagnosticHTTPError struct {
	status int
	cause  error
}

func (e remoteLinkDiagnosticHTTPError) Error() string   { return "PRIVATE-REMOTE-LINK-ERROR" }
func (e remoteLinkDiagnosticHTTPError) HTTPStatus() int { return e.status }
func (e remoteLinkDiagnosticHTTPError) Unwrap() error   { return e.cause }

type remoteLinkDiagnosticMarkerError struct {
	status    int
	transport bool
}

func (remoteLinkDiagnosticMarkerError) Error() string { return "PRIVATE-MARKER-ERROR" }
func (e remoteLinkDiagnosticMarkerError) HTTPStatus() int {
	return e.status
}
func (e remoteLinkDiagnosticMarkerError) DiagnosticTransportFailure() bool {
	return e.transport
}

type remoteLinkDiagnosticCycleError struct{}

func (*remoteLinkDiagnosticCycleError) Error() string   { return "PRIVATE-CYCLE-ERROR" }
func (e *remoteLinkDiagnosticCycleError) Unwrap() error { return e }

type remoteLinkDiagnosticOpaqueIsError struct{ cause error }

func (remoteLinkDiagnosticOpaqueIsError) Error() string { return "PRIVATE-OPAQUE-ERROR" }
func (e remoteLinkDiagnosticOpaqueIsError) Is(target error) bool {
	return errors.Is(e.cause, target)
}

func TestRemoteLinkFailureDiagnosticUsesOnlyClosedStructuralEvidence(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		class      domain.ArtifactGraphSourceFailureClass
		httpStatus int
	}{
		{"HTTP authentication", remoteLinkDiagnosticHTTPError{status: 401, cause: domain.ErrAuth}, domain.ArtifactSourceFailureAuthentication, 401},
		{"HTTP permission", remoteLinkDiagnosticHTTPError{status: 403, cause: domain.ErrForbidden}, domain.ArtifactSourceFailurePermission, 403},
		{"HTTP not found", remoteLinkDiagnosticHTTPError{status: 404, cause: domain.ErrNotFound}, domain.ArtifactSourceFailureNotFound, 404},
		{"HTTP rate limit", remoteLinkDiagnosticHTTPError{status: 429}, domain.ArtifactSourceFailureHTTP, 429},
		{"HTTP server failure", remoteLinkDiagnosticHTTPError{status: 500}, domain.ArtifactSourceFailureHTTP, 500},
		{"status-less authentication", fmt.Errorf("wrapped: %w", domain.ErrAuth), domain.ArtifactSourceFailureAuthentication, 0},
		{"status-less permission", fmt.Errorf("wrapped: %w", domain.ErrForbidden), domain.ArtifactSourceFailurePermission, 0},
		{"status-less not found", fmt.Errorf("wrapped: %w", domain.ErrNotFound), domain.ArtifactSourceFailureNotFound, 0},
		{"transport", &httpx.TransportError{Method: http.MethodGet, Category: "network"}, domain.ArtifactSourceFailureTransport, 0},
		{"generic request", errors.New("PRIVATE-GENERIC-REQUEST"), domain.ArtifactSourceFailureRequest, 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			failure := remoteLinkFailureDiagnostic(test.err)
			if failure == nil || failure.Class != test.class || (failure.HTTPStatus == nil) != (test.httpStatus == 0) {
				t.Fatalf("failure=%+v", failure)
			}
			if test.httpStatus != 0 && *failure.HTTPStatus != test.httpStatus {
				t.Fatalf("HTTP status=%d want=%d", *failure.HTTPStatus, test.httpStatus)
			}
			encoded, err := json.Marshal(failure)
			if err != nil || bytes.Contains(encoded, []byte("PRIVATE")) {
				t.Fatalf("diagnostic exposed error text: %s err=%v", encoded, err)
			}
		})
	}

	cycle := &remoteLinkDiagnosticCycleError{}
	invalid := []error{
		nil,
		domain.ErrCheckFailed,
		domain.ErrUsage,
		domain.ErrReadAttemptBudgetExhausted,
		context.Canceled,
		remoteLinkDiagnosticHTTPError{status: 200},
		remoteLinkDiagnosticHTTPError{status: 700},
		remoteLinkDiagnosticHTTPError{status: 401, cause: domain.ErrForbidden},
		remoteLinkDiagnosticHTTPError{status: 401, cause: domain.ErrCheckFailed},
		errors.Join(domain.ErrAuth, domain.ErrUsage),
		errors.Join(domain.ErrAuth, domain.ErrCheckFailed),
		errors.Join(remoteLinkDiagnosticHTTPError{status: 401}, remoteLinkDiagnosticHTTPError{status: 403}),
		errors.Join(domain.ErrAuth, domain.ErrForbidden),
		remoteLinkDiagnosticMarkerError{status: 500, transport: true},
		remoteLinkDiagnosticMarkerError{transport: false},
		cycle,
	}
	for index, err := range invalid {
		if failure := remoteLinkFailureDiagnostic(err); failure != nil {
			t.Fatalf("invalid evidence %d produced %+v", index, failure)
		}
	}
}

func TestRemoteLinkFailureDiagnosticValidatesOnlyExactFailedSourceShapes(t *testing.T) {
	status := 403
	valid := domain.ArtifactGraphSource{
		NodeID: "jira:issue:PROJ-1", NodeDepth: intPointer(0), Kind: "remote_links", Requested: true,
		Status: domain.ArtifactSourceForbidden, Complete: false, Stability: domain.ArtifactStabilityPublicAPI,
		Failure: &domain.ArtifactGraphSourceFailure{Class: domain.ArtifactSourceFailurePermission, HTTPStatus: &status},
	}
	if !validJiraGraphRemoteLinkFailure(valid) {
		t.Fatal("exact remote-link failure was rejected")
	}
	mutations := []func(*domain.ArtifactGraphSource){
		func(source *domain.ArtifactGraphSource) { source.Kind = "comments" },
		func(source *domain.ArtifactGraphSource) { source.Complete = true },
		func(source *domain.ArtifactGraphSource) { source.Count = 1 },
		func(source *domain.ArtifactGraphSource) { source.Truncated = true },
		func(source *domain.ArtifactGraphSource) { source.Status = domain.ArtifactSourcePartial },
		func(source *domain.ArtifactGraphSource) { source.PartialReason = domain.ArtifactPartialRequestFailed },
		func(source *domain.ArtifactGraphSource) { source.Failure.Class = "unknown" },
		func(source *domain.ArtifactGraphSource) { changed := 401; source.Failure.HTTPStatus = &changed },
		func(source *domain.ArtifactGraphSource) { changed := 0; source.Failure.HTTPStatus = &changed },
	}
	for index, mutate := range mutations {
		candidate := cloneJiraIssueGraphSource(valid)
		mutate(&candidate)
		if validJiraGraphRemoteLinkFailure(candidate) {
			t.Fatalf("mutation %d was accepted: %+v", index, candidate)
		}
	}
}

func TestJiraGraphRemoteLinkDiagnosticNeverInvalidatesLegacyPartialResult(t *testing.T) {
	tests := []struct {
		name          string
		err           error
		status        domain.ArtifactGraphSourceStatus
		partialReason string
	}{
		{"opaque authentication", remoteLinkDiagnosticOpaqueIsError{cause: domain.ErrAuth}, domain.ArtifactSourceForbidden, ""},
		{"opaque check failure", remoteLinkDiagnosticOpaqueIsError{cause: domain.ErrCheckFailed}, domain.ArtifactSourcePartial, domain.ArtifactPartialMalformed},
		{"status without sentinel", remoteLinkDiagnosticHTTPError{status: 403}, domain.ArtifactSourcePartial, domain.ArtifactPartialRequestFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tracker := completeGraphFixture()
			tracker.remoteErr = test.err
			result, err := (&JiraService{tr: tracker, baseURL: "https://jira.example.test"}).IssueGraphWithOptions(
				t.Context(), "PROJ-1", JiraIssueGraphOptions{},
			)
			if err != nil {
				t.Fatalf("legacy partial result became an error: %v", err)
			}
			source := graphSourceByKind(t, result, "remote_links")
			if result.Complete || result.RootID != "jira:issue:PROJ-1" || len(result.Nodes) == 0 ||
				source.Status != test.status || source.PartialReason != test.partialReason || source.Failure != nil {
				t.Fatalf("result=%+v source=%+v", result, source)
			}
		})
	}
}

func TestJiraGraphRemoteLinkHTTPFailuresAreSingleAttemptBoundAndContentFree(t *testing.T) {
	tests := []struct {
		name          string
		status        int
		drop          bool
		class         domain.ArtifactGraphSourceFailureClass
		sourceStatus  domain.ArtifactGraphSourceStatus
		partialReason string
	}{
		{"authentication", 401, false, domain.ArtifactSourceFailureAuthentication, domain.ArtifactSourceForbidden, ""},
		{"permission", 403, false, domain.ArtifactSourceFailurePermission, domain.ArtifactSourceForbidden, ""},
		{"not found", 404, false, domain.ArtifactSourceFailureNotFound, domain.ArtifactSourceUnsupported, ""},
		{"rate limit", 429, false, domain.ArtifactSourceFailureHTTP, domain.ArtifactSourcePartial, domain.ArtifactPartialRequestFailed},
		{"server failure", 500, false, domain.ArtifactSourceFailureHTTP, domain.ArtifactSourcePartial, domain.ArtifactPartialRequestFailed},
		{"connection loss", 0, true, domain.ArtifactSourceFailureTransport, domain.ArtifactSourcePartial, domain.ArtifactPartialRequestFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, snapshotCalls, remoteCalls, private := remoteLinkDiagnosticHTTPGraph(t, test.status, test.drop)
			source := graphSourceByKind(t, result, "remote_links")
			if result.Complete || len(result.Nodes) != 1 || result.RootID != "jira:issue:PROJ-1" ||
				result.Nodes[0].State != domain.ArtifactNodeResolved || snapshotCalls != 1 || remoteCalls != 1 ||
				result.Bounds.RequestsUsed != 2 || source.Status != test.sourceStatus || source.PartialReason != test.partialReason ||
				source.Failure == nil || source.Failure.Class != test.class {
				t.Fatalf("result=%+v source=%+v calls=%d/%d", result, source, snapshotCalls, remoteCalls)
			}
			if (source.Failure.HTTPStatus == nil) != test.drop || !test.drop && *source.Failure.HTTPStatus != test.status {
				t.Fatalf("failure=%+v", source.Failure)
			}
			encoded, err := json.Marshal(result)
			if err != nil || bytes.Contains(encoded, []byte(private)) || bytes.Contains(encoded, []byte("private-token")) {
				t.Fatalf("graph leaked private transport data: %s err=%v", encoded, err)
			}

			projection, err := NormalizeJiraIssueGraphProjection(JiraIssueGraphProjectionCompact, []string{"none"}, false)
			if err != nil {
				t.Fatal(err)
			}
			compact, err := ProjectJiraIssueGraphCompact(result, projection)
			if err != nil || len(compact.Sources) != 1 || compact.Sources[0].Failure == nil || compact.Sources[0].Failure.Class != test.class {
				t.Fatalf("compact=%+v err=%v", compact, err)
			}
			if compact.Sources[0].Failure == source.Failure ||
				source.Failure.HTTPStatus != nil && compact.Sources[0].Failure.HTTPStatus == source.Failure.HTTPStatus {
				t.Fatal("compact projection retained mutable diagnostic pointers")
			}
		})
	}
}

func TestJiraGraphRemoteLinkFailureTextIsAdditiveAndClosed(t *testing.T) {
	result, _, _, private := remoteLinkDiagnosticHTTPGraph(t, 403, false)
	text := JiraIssueGraphMarkdown(result)
	for _, want := range []string{"| Failure | HTTP status |", "| permission | 403 |"} {
		if !strings.Contains(text, want) {
			t.Fatalf("text omitted %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, private) || strings.Contains(text, "private-token") {
		t.Fatal("text exposed private transport data")
	}

	success := completeGraphFixture()
	complete, err := (&JiraService{tr: success, baseURL: "https://jira.example.test"}).IssueGraphWithOptions(t.Context(), "PROJ-1", JiraIssueGraphOptions{})
	if err != nil {
		t.Fatal(err)
	}
	successText := JiraIssueGraphMarkdown(complete)
	if strings.Contains(successText, "HTTP status") || strings.Contains(successText, "| Failure |") {
		t.Fatal("success text changed for absent diagnostics")
	}
}

func remoteLinkDiagnosticHTTPGraph(t *testing.T, status int, drop bool) (*JiraIssueGraphResult, int, int, string) {
	t.Helper()
	const privateBody = "PRIVATE-REMOTE-LINK-BODY-CANARY"
	var snapshotCalls, remoteCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/rest/api/2/issue/PROJ-1":
			snapshotCalls.Add(1)
			if request.Method != http.MethodGet || request.URL.Query().Get("expand") != "names,schema" || request.URL.Query().Get("fields") != "summary" || len(request.URL.Query()) != 2 {
				t.Errorf("snapshot request=%s %s", request.Method, request.URL.String())
			}
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"id":"101","key":"PROJ-1","fields":{"summary":"retained seed"},"names":{},"schema":{}}`))
		case "/rest/api/2/issue/PROJ-1/remotelink":
			remoteCalls.Add(1)
			if request.Method != http.MethodGet || request.URL.RawQuery != "" {
				t.Errorf("remote-link request=%s %s", request.Method, request.URL.String())
			}
			if drop {
				connection, _, err := http.NewResponseController(writer).Hijack()
				if err != nil {
					t.Errorf("hijack remote-link response: %v", err)
					return
				}
				_ = connection.Close()
				return
			}
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(status)
			_, _ = writer.Write([]byte(`{"error":"` + privateBody + `"}`))
		default:
			t.Errorf("unexpected request=%s %s", request.Method, request.URL.String())
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	tracker := jiraadapter.New(server.URL, "private-token", "test")
	result, err := (&JiraService{tr: tracker, baseURL: server.URL}).IssueGraphWithOptions(
		t.Context(), "PROJ-1", JiraIssueGraphOptions{IncludeSources: []string{"remote_links"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	return result, int(snapshotCalls.Load()), int(remoteCalls.Load()), privateBody
}

func intPointer(value int) *int { return &value }
