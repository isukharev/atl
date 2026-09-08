package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/strictjson"
)

type guardedUpdatePortStub struct {
	domain.Tracker
	issues         []domain.JiraGuardedFieldIssue
	issueCalls     int
	catalogCalls   int
	prepareCalls   int
	writeCalls     int
	writeErr       error
	mutatePrepared bool
	references     []string
	lastWrite      domain.JiraGuardedUpdateWrite
}

func (p *guardedUpdatePortStub) ReadGuardedUpdateFieldCatalog(_ context.Context, fields []string) (domain.JiraGuardedFieldCatalog, error) {
	p.catalogCalls++
	return domain.JiraGuardedFieldCatalog{Fields: optimisticUpdateQualification(fields), Complete: true}, nil
}

func (p *guardedUpdatePortStub) ReadGuardedUpdateIssue(_ context.Context, reference string, _ []string) (domain.JiraGuardedFieldIssue, error) {
	p.issueCalls++
	p.references = append(p.references, reference)
	index := p.issueCalls - 1
	if index >= len(p.issues) {
		index = len(p.issues) - 1
	}
	return p.issues[index], nil
}

func (p *guardedUpdatePortStub) PrepareGuardedUpdate(request domain.JiraGuardedUpdatePreparationRequest) (domain.JiraGuardedUpdatePreparation, error) {
	p.prepareCalls++
	values := make(map[string]any, len(request.Fields)+2)
	kinds := make(map[string]string, len(request.Fields)+2)
	if request.SummaryPresent {
		values["summary"], kinds["summary"] = request.Summary, "summary"
	}
	if request.DescriptionPresent {
		values["description"], kinds["description"] = string(request.Description), request.DescriptionSource
	}
	for field, input := range request.Fields {
		value := any(input.Value)
		if input.ExplicitJSON {
			decoded, err := strictjson.DecodeValue([]byte(input.Value))
			if err != nil {
				return domain.JiraGuardedUpdatePreparation{}, err
			}
			value, kinds[field] = decoded, "explicit_json"
		} else {
			kinds[field] = "legacy"
		}
		values[field] = value
	}
	if p.mutatePrepared {
		values["summary"] = "hostile replacement"
	}
	ids := make([]string, 0, len(values))
	for field := range values {
		ids = append(ids, field)
	}
	sort.Strings(ids)
	fields := make([]domain.JiraGuardedUpdatePreparedField, len(ids))
	for index, field := range ids {
		encoded, _ := json.Marshal(values[field])
		sum := sha256.Sum256(encoded)
		fields[index] = domain.JiraGuardedUpdatePreparedField{
			FieldID: field, InputKind: kinds[field], JSONKind: guardedFieldJSONKind(values[field]),
			Bytes: len(encoded), SHA256: hex.EncodeToString(sum[:]),
		}
	}
	payload, _ := json.Marshal(map[string]any{"fields": values})
	return domain.JiraGuardedUpdatePreparation{Payload: payload, Fields: fields, Values: values}, nil
}

func (p *guardedUpdatePortStub) WriteGuardedUpdate(ctx context.Context, write domain.JiraGuardedUpdateWrite) error {
	p.writeCalls++
	if !domain.SingleAttempt(ctx) {
		return fmt.Errorf("writer context was replayable")
	}
	p.lastWrite = write
	return p.writeErr
}

type guardedUpdateHTTPError int

func (e guardedUpdateHTTPError) Error() string   { return "rejected" }
func (e guardedUpdateHTTPError) HTTPStatus() int { return int(e) }

func guardedUpdateIssue(updated string, values map[string]any) domain.JiraGuardedFieldIssue {
	fields := make(map[string]domain.JiraGuardedFieldEvidence, len(values))
	for field, value := range values {
		fields[field] = domain.JiraGuardedFieldEvidence{Present: true, Value: value}
	}
	return domain.JiraGuardedFieldIssue{ID: "10001", Key: "PROJ-1", Project: "PROJ", Updated: updated, Fields: fields, Complete: true}
}

func guardedUpdateOpts() JiraGuardedUpdateOpts {
	return JiraGuardedUpdateOpts{
		Summary: "New summary", SummaryPresent: true,
		Description: []byte("h2. New body"), DescriptionPresent: true, DescriptionSource: "wiki",
		Fields: map[string]domain.JiraFieldInput{"customfield_1": {Value: "9007199254740993", ExplicitJSON: true}},
	}
}

func guardedUpdateCurrent() map[string]any {
	return map[string]any{"summary": "Old summary", "description": "old body", "customfield_1": json.Number("1")}
}

func guardedUpdateDesired() map[string]any {
	return map[string]any{"summary": "New summary", "description": "h2. New body", "customfield_1": json.Number("9007199254740993")}
}

func guardedUpdatePreview(t *testing.T) *JiraGuardedUpdateResult {
	t.Helper()
	port := &guardedUpdatePortStub{issues: []domain.JiraGuardedFieldIssue{guardedUpdateIssue("2026-09-08T10:00:00.000+0000", guardedUpdateCurrent())}}
	result, err := (&JiraService{tr: port, baseURL: "https://jira.example.test"}).UpdateIssueGuarded(t.Context(), "PROJ-1", guardedUpdateOpts())
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestUpdateIssueGuardedPreviewUsesContentFreeEvidence(t *testing.T) {
	port := &guardedUpdatePortStub{issues: []domain.JiraGuardedFieldIssue{guardedUpdateIssue("2026-09-08T10:00:00.000+0000", guardedUpdateCurrent())}}
	result, err := (&JiraService{tr: port, baseURL: "https://jira.example.test"}).UpdateIssueGuarded(t.Context(), "proj-1", guardedUpdateOpts())
	if err != nil || result.Status != "would_apply" || result.Mode != "dry-run" || !result.Complete || result.WriteAttempted || result.Reconciled {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if port.catalogCalls != 1 || port.issueCalls != 1 || port.prepareCalls != 1 || port.writeCalls != 0 {
		t.Fatalf("strict calls catalog/issue/prepare/write=%d/%d/%d/%d", port.catalogCalls, port.issueCalls, port.prepareCalls, port.writeCalls)
	}
	if len(result.ProposalHash) != 64 || len(result.Desired) != 3 || len(result.Current) != 3 || result.Source.Kind != "wiki" {
		t.Fatalf("evidence=%+v", result)
	}
	encoded, _ := json.Marshal(result)
	for _, secret := range []string{"Old summary", "old body", "New summary", "h2. New body", "9007199254740993"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("value %q leaked in result: %s", secret, encoded)
		}
	}
}

func TestUpdateIssueGuardedApplyUsesImmutablePrewriteAndReadback(t *testing.T) {
	preview := guardedUpdatePreview(t)
	opts := guardedUpdateOpts()
	opts.Apply, opts.ExpectedProposalHash = true, preview.ProposalHash
	port := &guardedUpdatePortStub{issues: []domain.JiraGuardedFieldIssue{
		guardedUpdateIssue("2026-09-08T10:00:00.000+0000", guardedUpdateCurrent()),
		guardedUpdateIssue("2026-09-08T10:00:00.000+0000", guardedUpdateCurrent()),
		guardedUpdateIssue("2026-09-08T10:01:00.000+0000", guardedUpdateDesired()),
	}}
	result, err := (&JiraService{tr: port, baseURL: "https://jira.example.test"}).UpdateIssueGuarded(t.Context(), "PROJ-1", opts)
	if err != nil || result.Status != "applied" || !result.WriteAttempted || !result.Reconciled || !result.Complete {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if port.catalogCalls != 2 || port.issueCalls != 3 || port.prepareCalls != 2 || port.writeCalls != 1 || !reflect.DeepEqual(port.references, []string{"PROJ-1", "10001", "10001"}) {
		t.Fatalf("geometry catalog/issue/prepare/write=%d/%d/%d/%d refs=%v", port.catalogCalls, port.issueCalls, port.prepareCalls, port.writeCalls, port.references)
	}
	if port.lastWrite.ID != "10001" || port.lastWrite.Key != "PROJ-1" || port.lastWrite.Project != "PROJ" {
		t.Fatalf("write identity=%+v", port.lastWrite)
	}
}

func TestUpdateIssueGuardedPrewriteDriftBlocks(t *testing.T) {
	preview := guardedUpdatePreview(t)
	opts := guardedUpdateOpts()
	opts.Apply, opts.ExpectedProposalHash = true, preview.ProposalHash
	drifted := guardedUpdateCurrent()
	drifted["summary"] = "concurrent edit"
	port := &guardedUpdatePortStub{issues: []domain.JiraGuardedFieldIssue{
		guardedUpdateIssue("2026-09-08T10:00:00.000+0000", guardedUpdateCurrent()),
		guardedUpdateIssue("2026-09-08T10:01:00.000+0000", drifted),
	}}
	result, err := (&JiraService{tr: port, baseURL: "https://jira.example.test"}).UpdateIssueGuarded(t.Context(), "PROJ-1", opts)
	if err == nil || !errors.Is(err, domain.ErrCheckFailed) || result.Status != "blocked" || result.WriteAttempted || port.writeCalls != 0 {
		t.Fatalf("result=%+v err=%v writes=%d", result, err, port.writeCalls)
	}
}

func TestUpdateIssueGuardedRejectsAdapterCandidateSubstitution(t *testing.T) {
	port := &guardedUpdatePortStub{
		issues:         []domain.JiraGuardedFieldIssue{guardedUpdateIssue("2026-09-08T10:00:00.000+0000", guardedUpdateCurrent())},
		mutatePrepared: true,
	}
	result, err := (&JiraService{tr: port, baseURL: "https://jira.example.test"}).UpdateIssueGuarded(t.Context(), "PROJ-1", guardedUpdateOpts())
	if err == nil || !errors.Is(err, domain.ErrCheckFailed) || result.Status != "blocked" || port.writeCalls != 0 {
		t.Fatalf("result=%+v err=%v writes=%d", result, err, port.writeCalls)
	}
}

func TestUpdateIssueGuardedPostDispatchClassifications(t *testing.T) {
	preview := guardedUpdatePreview(t)
	baseOpts := guardedUpdateOpts()
	baseOpts.Apply, baseOpts.ExpectedProposalHash = true, preview.ProposalHash
	for _, test := range []struct {
		name      string
		writeErr  error
		readback  map[string]any
		updated   string
		want      string
		wantErr   bool
		wantReads int
	}{
		{name: "recovered", writeErr: errors.New("connection lost"), readback: guardedUpdateDesired(), updated: "2026-09-08T10:01:00.000+0000", want: "recovered", wantReads: 3},
		{name: "definitive", writeErr: guardedUpdateHTTPError(400), want: "not_applied", wantErr: true, wantReads: 2},
		{name: "unknown", writeErr: errors.New("connection lost"), readback: guardedUpdateCurrent(), updated: "2026-09-08T10:00:00.000+0000", want: "outcome_unknown", wantErr: true, wantReads: 3},
		{name: "HTTP 408 recovered", writeErr: guardedUpdateHTTPError(408), readback: guardedUpdateDesired(), updated: "2026-09-08T10:01:00.000+0000", want: "recovered", wantReads: 3},
		{name: "HTTP 425 recovered", writeErr: guardedUpdateHTTPError(425), readback: guardedUpdateDesired(), updated: "2026-09-08T10:01:00.000+0000", want: "recovered", wantReads: 3},
		{name: "HTTP 429 recovered", writeErr: guardedUpdateHTTPError(429), readback: guardedUpdateDesired(), updated: "2026-09-08T10:01:00.000+0000", want: "recovered", wantReads: 3},
		{name: "HTTP 500 recovered", writeErr: guardedUpdateHTTPError(500), readback: guardedUpdateDesired(), updated: "2026-09-08T10:01:00.000+0000", want: "recovered", wantReads: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			issues := []domain.JiraGuardedFieldIssue{
				guardedUpdateIssue("2026-09-08T10:00:00.000+0000", guardedUpdateCurrent()),
				guardedUpdateIssue("2026-09-08T10:00:00.000+0000", guardedUpdateCurrent()),
			}
			if test.wantReads == 3 {
				issues = append(issues, guardedUpdateIssue(test.updated, test.readback))
			}
			port := &guardedUpdatePortStub{issues: issues, writeErr: test.writeErr}
			result, err := (&JiraService{tr: port, baseURL: "https://jira.example.test"}).UpdateIssueGuarded(t.Context(), "PROJ-1", baseOpts)
			if (err != nil) != test.wantErr || result.Status != test.want || port.issueCalls != test.wantReads {
				t.Fatalf("result=%+v err=%v reads=%d", result, err, port.issueCalls)
			}
			if test.want == "outcome_unknown" {
				var ambiguous interface{ DiagnosticAmbiguousWrite() bool }
				if !errors.Is(err, domain.ErrCheckFailed) || !errors.As(err, &ambiguous) || !ambiguous.DiagnosticAmbiguousWrite() {
					t.Fatalf("missing ambiguous marker: %v", err)
				}
			}
		})
	}
}

func TestUpdateIssueGuardedRejectsStructuredNativeSummaryAndDescription(t *testing.T) {
	for _, field := range []string{"summary", "description"} {
		t.Run(field, func(t *testing.T) {
			current := guardedUpdateCurrent()
			current[field] = map[string]any{"type": "doc"}
			port := &guardedUpdatePortStub{issues: []domain.JiraGuardedFieldIssue{guardedUpdateIssue("2026-09-08T10:00:00.000+0000", current)}}
			result, err := (&JiraService{tr: port, baseURL: "https://jira.example.test"}).UpdateIssueGuarded(t.Context(), "PROJ-1", guardedUpdateOpts())
			if err == nil || !errors.Is(err, domain.ErrCheckFailed) || result.Complete || port.writeCalls != 0 {
				t.Fatalf("result=%+v err=%v writes=%d", result, err, port.writeCalls)
			}
		})
	}
}

func TestJiraGuardedUpdateMarkdownErrorIsContentFree(t *testing.T) {
	const privateText = "PRIVATE-CANARY.example.invalid/secret"
	err := ValidateJiraGuardedUpdateOpts(JiraGuardedUpdateOpts{
		Description: []byte("h2. " + privateText), DescriptionPresent: true, DescriptionSource: "markdown",
	})
	if err == nil || !errors.Is(err, domain.ErrCheckFailed) || strings.Contains(err.Error(), privateText) {
		t.Fatalf("error=%q", err)
	}
}

func TestJiraGuardedUpdateAggregateInputCapIncludesSummaryAndDescription(t *testing.T) {
	description := make([]byte, domain.JiraGuardedUpdateMaxInputBytes)
	err := ValidateJiraGuardedUpdateOpts(JiraGuardedUpdateOpts{
		Summary: "x", SummaryPresent: true,
		Description: description, DescriptionPresent: true, DescriptionSource: "wiki",
	})
	if err == nil || !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("aggregate overflow error=%v", err)
	}
}

func TestUpdateIssueGuardedSourceBytesBindProposal(t *testing.T) {
	portA := &guardedUpdatePortStub{issues: []domain.JiraGuardedFieldIssue{guardedUpdateIssue("2026-09-08T10:00:00.000+0000", map[string]any{"description": "text"})}}
	portB := &guardedUpdatePortStub{issues: []domain.JiraGuardedFieldIssue{guardedUpdateIssue("2026-09-08T10:00:00.000+0000", map[string]any{"description": "text"})}}
	a, errA := (&JiraService{tr: portA, baseURL: "https://jira.example.test"}).UpdateIssueGuarded(t.Context(), "PROJ-1", JiraGuardedUpdateOpts{Description: []byte("text"), DescriptionPresent: true, DescriptionSource: "markdown"})
	b, errB := (&JiraService{tr: portB, baseURL: "https://jira.example.test"}).UpdateIssueGuarded(t.Context(), "PROJ-1", JiraGuardedUpdateOpts{Description: []byte("\ufefftext"), DescriptionPresent: true, DescriptionSource: "markdown"})
	if errA != nil || errB != nil || a.ProposalHash == b.ProposalHash || a.Status != "already_satisfied" || b.Status != "already_satisfied" {
		t.Fatalf("a=%+v errA=%v b=%+v errB=%v", a, errA, b, errB)
	}
}
