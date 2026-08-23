package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

type guardedFieldPortStub struct {
	domain.Tracker
	issues           []domain.JiraGuardedFieldIssue
	issueErrors      map[int]error
	catalogCalls     int
	issueCalls       int
	prepareCalls     int
	writeCalls       int
	broadFieldCalls  int
	broadIssueCalls  int
	broadWriteCalls  int
	writeErr         error
	prepareDriftCall int
	mutateRequest    bool
	mutatePrepared   func(*domain.JiraGuardedFieldPreparation)
	lastWrite        domain.JiraGuardedFieldWrite
	references       []string
}

func (p *guardedFieldPortStub) ReadGuardedFieldCatalog(_ context.Context, selected []string) (domain.JiraGuardedFieldCatalog, error) {
	p.catalogCalls++
	fields := make([]domain.JiraGuardedFieldCatalogEntry, len(selected))
	for index, field := range selected {
		fields[index] = domain.JiraGuardedFieldCatalogEntry{ID: field, Custom: true}
	}
	return domain.JiraGuardedFieldCatalog{Fields: fields, Complete: true}, nil
}

func (p *guardedFieldPortStub) ReadGuardedFieldIssue(_ context.Context, reference string, _ []string) (domain.JiraGuardedFieldIssue, error) {
	p.issueCalls++
	p.references = append(p.references, reference)
	if err := p.issueErrors[p.issueCalls]; err != nil {
		return domain.JiraGuardedFieldIssue{}, err
	}
	index := p.issueCalls - 1
	if index >= len(p.issues) {
		index = len(p.issues) - 1
	}
	return p.issues[index], nil
}

func (p *guardedFieldPortStub) PrepareGuardedFields(request domain.JiraGuardedFieldPreparationRequest) (domain.JiraGuardedFieldPreparation, error) {
	p.prepareCalls++
	if p.mutateRequest {
		request.Values["plugin.vendor"].(map[string]any)["id"] = "hostile"
	}
	values := make(map[string]any, len(request.Values))
	for field, value := range request.Values {
		values[field] = value
	}
	if p.prepareCalls == p.prepareDriftCall {
		values[request.Qualified[0].ID] = "drifted"
	}
	payload, _ := json.Marshal(map[string]any{"fields": values})
	fields := make([]domain.JiraGuardedFieldPreparedProjection, 0, len(values))
	for _, qualified := range request.Qualified {
		value := values[qualified.ID]
		kind := ""
		var encoded []byte
		switch typed := value.(type) {
		case string:
			kind, encoded = "string", []byte(typed)
		case map[string]any:
			kind, encoded = "object", mustGuardedFieldJSON(value)
		case []any:
			kind, encoded = "array", mustGuardedFieldJSON(value)
		}
		sum := sha256.Sum256(encoded)
		fields = append(fields, domain.JiraGuardedFieldPreparedProjection{FieldID: qualified.ID, Kind: kind, Bytes: len(encoded), SHA256: hex.EncodeToString(sum[:])})
	}
	prepared := domain.JiraGuardedFieldPreparation{Payload: payload, Fields: fields}
	if p.mutatePrepared != nil {
		p.mutatePrepared(&prepared)
	}
	return prepared, nil
}

func (p *guardedFieldPortStub) WriteGuardedFields(ctx context.Context, write domain.JiraGuardedFieldWrite) error {
	p.writeCalls++
	if !domain.SingleAttempt(ctx) {
		return fmt.Errorf("writer context was replayable")
	}
	p.lastWrite = write
	return p.writeErr
}

func (p *guardedFieldPortStub) Fields(context.Context) ([]domain.FieldDef, error) {
	p.broadFieldCalls++
	return nil, errors.New("broad Fields must not be called")
}
func (p *guardedFieldPortStub) GetIssue(context.Context, string, []string) (*domain.Issue, error) {
	p.broadIssueCalls++
	return nil, errors.New("broad GetIssue must not be called")
}
func (p *guardedFieldPortStub) SetFields(context.Context, string, map[string]any) error {
	p.broadWriteCalls++
	return errors.New("broad SetFields must not be called")
}

type guardedFieldNoAttemptStub struct{ cause error }

func (e guardedFieldNoAttemptStub) Error() string                  { return "refused before dispatch" }
func (e guardedFieldNoAttemptStub) Unwrap() error                  { return e.cause }
func (e guardedFieldNoAttemptStub) DiagnosticWriteAttempted() bool { return false }

type fieldSetHTTPError int

func (e fieldSetHTTPError) Error() string   { return "HTTP write rejected" }
func (e fieldSetHTTPError) HTTPStatus() int { return int(e) }

func mustGuardedFieldJSON(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}

func guardedFieldIssue(updated string, values map[string]any) domain.JiraGuardedFieldIssue {
	fields := make(map[string]domain.JiraGuardedFieldEvidence, len(values))
	for field, value := range values {
		fields[field] = domain.JiraGuardedFieldEvidence{Present: true, Value: value}
	}
	return domain.JiraGuardedFieldIssue{ID: "10001", Key: "PROJ-1", Project: "PROJ", Updated: updated, Fields: fields, Complete: true}
}

func guardedFieldProposals() []JiraFieldProposal {
	return []JiraFieldProposal{
		{Field: "customfield_1", Source: "markdown", Value: "h2. Progress", InputBytes: 12},
		{Field: "plugin.vendor", Source: "raw", Value: map[string]any{"id": "2", "large": json.Number("9007199254740993")}, InputBytes: 43},
	}
}

func guardedFieldNestedArray(depth int) any {
	var value any = json.Number("0")
	for range depth {
		value = []any{value}
	}
	return value
}

func guardedFieldPreview(t *testing.T, proposals []JiraFieldProposal, current map[string]any) *JiraFieldSetResult {
	t.Helper()
	port := &guardedFieldPortStub{issues: []domain.JiraGuardedFieldIssue{guardedFieldIssue("2026-08-23T10:00:00.000+0000", current)}}
	result, err := (&JiraService{tr: port, baseURL: "https://jira.example.test"}).SetFieldsGuarded(t.Context(), "PROJ-1", JiraFieldSetOpts{
		Proposals: proposals, AllowFields: []string{"customfield_1", "plugin.vendor"},
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	return result
}

func guardedFieldApplyOpts(t *testing.T, proposals []JiraFieldProposal, current map[string]any) JiraFieldSetOpts {
	t.Helper()
	preview := guardedFieldPreview(t, proposals, current)
	return JiraFieldSetOpts{Proposals: proposals, AllowFields: []string{"plugin.vendor", "customfield_1"}, Apply: true, ExpectedUpdated: preview.ActualUpdated, ExpectedProposalHash: preview.ProposalHash}
}

func TestJiraGuardedFieldErrorTerminalAndCauseContract(t *testing.T) {
	for _, cause := range []error{domain.ErrAuth, domain.ErrConfig, domain.ErrForbidden, domain.ErrUsage, domain.ErrCheckFailed} {
		err := guardedFieldFailure("blocked", cause, true, false)
		var terminal interface{ DiagnosticTerminalCheckFailure() bool }
		if !errors.Is(err, domain.ErrCheckFailed) || !errors.Is(err, cause) || !errors.As(err, &terminal) || !terminal.DiagnosticTerminalCheckFailure() {
			t.Fatalf("closed cause=%v err=%v terminal=%v", cause, err, terminal)
		}
	}
	unknown := guardedFieldFailure("unknown", nil, false, true)
	var ambiguous interface{ DiagnosticAmbiguousWrite() bool }
	if errors.Is(unknown, domain.ErrCheckFailed) || !errors.As(unknown, &ambiguous) || !ambiguous.DiagnosticAmbiguousWrite() {
		t.Fatalf("unknown error identity=%v", unknown)
	}
	definitive := guardedFieldFailure("failed", domain.ErrUsage, false, false)
	if !errors.Is(definitive, domain.ErrUsage) || errors.Is(definitive, domain.ErrCheckFailed) {
		t.Fatalf("definitive cause identity=%v", definitive)
	}
}

func TestSetFieldsGuardedPreviewUsesStrictPortAndContentFreeEvidence(t *testing.T) {
	proposals := guardedFieldProposals()
	port := &guardedFieldPortStub{issues: []domain.JiraGuardedFieldIssue{guardedFieldIssue("2026-08-23T10:00:00.000+0000", map[string]any{"customfield_1": "old", "plugin.vendor": map[string]any{"id": "1"}})}}
	result, err := (&JiraService{tr: port, baseURL: "https://jira.example.test"}).SetFieldsGuarded(t.Context(), "proj-1", JiraFieldSetOpts{Proposals: proposals, AllowFields: []string{"customfield_1", "plugin.vendor", "surplus.vendor"}})
	if err != nil || result.Status != "would_apply" || result.Mode != "dry-run" || !result.Complete || result.WriteAttempted || result.Reconciled {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if port.catalogCalls != 1 || port.issueCalls != 1 || port.prepareCalls != 1 || port.writeCalls != 0 {
		t.Fatalf("strict calls catalog/issue/prepare/write=%d/%d/%d/%d", port.catalogCalls, port.issueCalls, port.prepareCalls, port.writeCalls)
	}
	if port.broadFieldCalls+port.broadIssueCalls+port.broadWriteCalls != 0 {
		t.Fatal("guarded workflow called a broad Tracker field method")
	}
	if len(result.ProposalHash) != 64 || result.Prepared.Bytes == 0 || len(result.Current) != 2 || len(result.Catalog) != 2 {
		t.Fatalf("guarded evidence=%+v", result)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), `"old"`) || strings.Contains(string(encoded), `"name"`) {
		t.Fatalf("current/catalog values leaked: %s", encoded)
	}
	if !strings.Contains(string(encoded), "h2. Progress") {
		t.Fatalf("released desired value missing: %s", encoded)
	}
}

func TestSetFieldsGuardedApplyUsesImmutablePrewriteAndReadback(t *testing.T) {
	proposals := guardedFieldProposals()
	current := map[string]any{"customfield_1": "old", "plugin.vendor": map[string]any{"id": "1"}}
	desired := map[string]any{"customfield_1": "h2. Progress", "plugin.vendor": map[string]any{"id": "2", "large": json.Number("9007199254740993")}}
	port := &guardedFieldPortStub{issues: []domain.JiraGuardedFieldIssue{
		guardedFieldIssue("2026-08-23T10:00:00.000+0000", current),
		guardedFieldIssue("2026-08-23T10:00:00.000+0000", current),
		guardedFieldIssue("2026-08-23T10:01:00.000+0000", desired),
	}}
	result, err := (&JiraService{tr: port, baseURL: "https://jira.example.test"}).SetFieldsGuarded(t.Context(), "PROJ-1", guardedFieldApplyOpts(t, proposals, current))
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

func TestSetFieldsGuardedReviewAndPrewriteDriftBlock(t *testing.T) {
	proposals := guardedFieldProposals()
	current := map[string]any{"customfield_1": "old", "plugin.vendor": map[string]any{"id": "1"}}
	for _, test := range []struct {
		name         string
		opts         func(t *testing.T) JiraFieldSetOpts
		port         *guardedFieldPortStub
		wantComplete bool
	}{
		{name: "hash mismatch", opts: func(*testing.T) JiraFieldSetOpts {
			return JiraFieldSetOpts{Proposals: proposals, AllowFields: []string{"customfield_1", "plugin.vendor"}, Apply: true, ExpectedUpdated: "2026-08-23T10:00:00.000+0000", ExpectedProposalHash: strings.Repeat("0", 64)}
		}, port: &guardedFieldPortStub{issues: []domain.JiraGuardedFieldIssue{guardedFieldIssue("2026-08-23T10:00:00.000+0000", current)}}, wantComplete: true},
		{name: "updated mismatch", opts: func(t *testing.T) JiraFieldSetOpts {
			opts := guardedFieldApplyOpts(t, proposals, current)
			opts.ExpectedUpdated = "2026-08-23T09:59:00.000+0000"
			return opts
		}, port: &guardedFieldPortStub{issues: []domain.JiraGuardedFieldIssue{guardedFieldIssue("2026-08-23T10:00:00.000+0000", current)}}, wantComplete: true},
		{name: "prepared drift", opts: func(t *testing.T) JiraFieldSetOpts { return guardedFieldApplyOpts(t, proposals, current) }, port: &guardedFieldPortStub{issues: []domain.JiraGuardedFieldIssue{guardedFieldIssue("2026-08-23T10:00:00.000+0000", current), guardedFieldIssue("2026-08-23T10:00:00.000+0000", current)}, prepareDriftCall: 2}},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := (&JiraService{tr: test.port, baseURL: "https://jira.example.test"}).SetFieldsGuarded(t.Context(), "PROJ-1", test.opts(t))
			if !errors.Is(err, domain.ErrCheckFailed) || result == nil || result.Status != "blocked" || result.WriteAttempted || result.Complete != test.wantComplete || test.port.writeCalls != 0 {
				t.Fatalf("result=%+v err=%v writeCalls=%d", result, err, test.port.writeCalls)
			}
			if test.name == "updated mismatch" && (result.ExpectedUpdated != "2026-08-23T09:59:00.000+0000" || result.ActualUpdated != "2026-08-23T10:00:00.000+0000") {
				t.Fatalf("review audit expected=%q actual=%q", result.ExpectedUpdated, result.ActualUpdated)
			}
		})
	}
}

func TestSetFieldsGuardedRejectsHostilePreparationBeforePreviewOrWrite(t *testing.T) {
	proposals := guardedFieldProposals()
	current := map[string]any{"customfield_1": "old", "plugin.vendor": map[string]any{"id": "1"}}
	mutations := map[string]func(*guardedFieldPortStub){
		"request shallow alias drift": func(port *guardedFieldPortStub) { port.mutateRequest = true },
		"payload bytes": func(port *guardedFieldPortStub) {
			port.mutatePrepared = func(prepared *domain.JiraGuardedFieldPreparation) { prepared.Payload = append(prepared.Payload, ' ') }
		},
		"projection order": func(port *guardedFieldPortStub) {
			port.mutatePrepared = func(prepared *domain.JiraGuardedFieldPreparation) {
				prepared.Fields[0], prepared.Fields[1] = prepared.Fields[1], prepared.Fields[0]
			}
		},
		"projection kind": func(port *guardedFieldPortStub) {
			port.mutatePrepared = func(prepared *domain.JiraGuardedFieldPreparation) { prepared.Fields[0].Kind = "object" }
		},
		"projection length": func(port *guardedFieldPortStub) {
			port.mutatePrepared = func(prepared *domain.JiraGuardedFieldPreparation) { prepared.Fields[0].Bytes++ }
		},
		"projection uppercase digest": func(port *guardedFieldPortStub) {
			port.mutatePrepared = func(prepared *domain.JiraGuardedFieldPreparation) {
				prepared.Fields[0].SHA256 = strings.ToUpper(prepared.Fields[0].SHA256)
			}
		},
		"projection wrong digest": func(port *guardedFieldPortStub) {
			port.mutatePrepared = func(prepared *domain.JiraGuardedFieldPreparation) {
				prepared.Fields[0].SHA256 = strings.Repeat("0", 64)
			}
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			port := &guardedFieldPortStub{issues: []domain.JiraGuardedFieldIssue{guardedFieldIssue("2026-08-23T10:00:00.000+0000", current)}}
			mutate(port)
			result, err := (&JiraService{tr: port, baseURL: "https://jira.example.test"}).SetFieldsGuarded(t.Context(), "PROJ-1", JiraFieldSetOpts{
				Proposals: proposals, AllowFields: []string{"customfield_1", "plugin.vendor"},
			})
			if !errors.Is(err, domain.ErrCheckFailed) || result == nil || result.Status != "blocked" || result.Complete || result.WriteAttempted || port.writeCalls != 0 {
				t.Fatalf("result=%+v err=%v writes=%d", result, err, port.writeCalls)
			}
			if proposals[1].Value.(map[string]any)["id"] != "2" {
				t.Fatal("hostile preparation mutated caller-owned desired value")
			}
		})
	}
}

func TestJiraFieldProposalHashBindsEverySchemaV3MemberAndIsOrderStable(t *testing.T) {
	proposals := guardedFieldProposals()
	current := map[string]any{"customfield_1": "old", "plugin.vendor": map[string]any{"id": "1"}}
	base := guardedFieldPreview(t, proposals, current)
	preimage, err := jiraFieldProposalPreimage(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"h2. Progress", "9007199254740993", `"large"`} {
		if bytes.Contains(preimage, []byte(raw)) {
			t.Fatalf("proposal hash preimage contains raw desired value marker %q: %s", raw, preimage)
		}
	}
	clone := func() *JiraFieldSetResult {
		encoded, _ := json.Marshal(base)
		var out JiraFieldSetResult
		_ = json.Unmarshal(encoded, &out)
		return &out
	}
	mutations := map[string]func(*JiraFieldSetResult){
		"backend":          func(value *JiraFieldSetResult) { value.BackendSHA256 = "sha256:" + strings.Repeat("f", 64) },
		"requested key":    func(value *JiraFieldSetResult) { value.RequestedKey = "PROJ-2" },
		"issue id":         func(value *JiraFieldSetResult) { value.IssueID = "10002" },
		"key":              func(value *JiraFieldSetResult) { value.Key = "PROJ-2" },
		"project":          func(value *JiraFieldSetResult) { value.Project = "OTHER" },
		"updated":          func(value *JiraFieldSetResult) { value.ActualUpdated += "x" },
		"catalog id":       func(value *JiraFieldSetResult) { value.Catalog[0].ID += ".changed" },
		"catalog custom":   func(value *JiraFieldSetResult) { value.Catalog[0].Custom = !value.Catalog[0].Custom },
		"catalog complete": func(value *JiraFieldSetResult) { value.Complete = !value.Complete },
		"current field":    func(value *JiraFieldSetResult) { value.Current[0].Field += ".changed" },
		"current present":  func(value *JiraFieldSetResult) { value.Current[0].Present = !value.Current[0].Present },
		"current kind":     func(value *JiraFieldSetResult) { value.Current[0].Kind = "object" },
		"current length":   func(value *JiraFieldSetResult) { value.Current[0].Bytes++ },
		"current digest":   func(value *JiraFieldSetResult) { value.Current[0].SHA256 = strings.Repeat("f", 64) },
		"desired field":    func(value *JiraFieldSetResult) { value.Fields[0].Field += ".changed" },
		"desired source":   func(value *JiraFieldSetResult) { value.Fields[0].Source = "raw" },
		"desired kind":     func(value *JiraFieldSetResult) { value.Fields[0].Kind = "object" },
		"desired length":   func(value *JiraFieldSetResult) { value.Fields[0].Bytes++ },
		"desired digest":   func(value *JiraFieldSetResult) { value.Fields[0].SHA256 = strings.Repeat("f", 64) },
		"prepared length":  func(value *JiraFieldSetResult) { value.Prepared.Bytes++ },
		"prepared digest":  func(value *JiraFieldSetResult) { value.Prepared.SHA256 = strings.Repeat("f", 64) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := clone()
			mutate(changed)
			hash, hashErr := jiraFieldProposalHash(changed)
			if hashErr != nil || hash == base.ProposalHash {
				t.Fatalf("hash=%q err=%v", hash, hashErr)
			}
		})
	}
	canonicalBase := jiraFieldProposalCanonicalValue(base)
	if canonicalBase.SchemaVersion != 3 || canonicalBase.Operation != "jira_issue_field_set" || canonicalBase.CatalogEndpoint != "/rest/api/2/field" || !canonicalBase.CatalogComplete || !canonicalBase.Desired[0].Present {
		t.Fatalf("fixed proposal members=%+v", canonicalBase)
	}
	canonicalMutations := map[string]func(*jiraFieldProposalCanonical){
		"schema version":       func(value *jiraFieldProposalCanonical) { value.SchemaVersion++ },
		"operation":            func(value *jiraFieldProposalCanonical) { value.Operation += ".changed" },
		"catalog endpoint":     func(value *jiraFieldProposalCanonical) { value.CatalogEndpoint += ".changed" },
		"catalog completeness": func(value *jiraFieldProposalCanonical) { value.CatalogComplete = false },
		"desired present":      func(value *jiraFieldProposalCanonical) { value.Desired[0].Present = false },
	}
	for name, mutate := range canonicalMutations {
		t.Run("fixed/"+name, func(t *testing.T) {
			changed := canonicalBase
			changed.Desired = append([]jiraFieldDesiredHashProjection(nil), canonicalBase.Desired...)
			mutate(&changed)
			encoded, marshalErr := json.Marshal(changed)
			if marshalErr != nil || guardedProposalDigest(encoded) == base.ProposalHash {
				t.Fatalf("fixed member mutation was not bound: err=%v", marshalErr)
			}
		})
	}
	boundType := reflect.TypeOf(base.Bounds)
	for index := 0; index < boundType.NumField(); index++ {
		index := index
		t.Run("bound/"+boundType.Field(index).Name, func(t *testing.T) {
			changed := clone()
			field := reflect.ValueOf(&changed.Bounds).Elem().Field(index)
			field.SetInt(field.Int() + 1)
			hash, hashErr := jiraFieldProposalHash(changed)
			if hashErr != nil || hash == base.ProposalHash {
				t.Fatalf("hash=%q err=%v", hash, hashErr)
			}
		})
	}
	reordered := []JiraFieldProposal{proposals[1], proposals[0]}
	if got := guardedFieldPreview(t, reordered, current).ProposalHash; got != base.ProposalHash {
		t.Fatalf("input order changed proposal hash: %s != %s", got, base.ProposalHash)
	}
}

func TestSetFieldsGuardedOutcomeTruthTable(t *testing.T) {
	proposals := guardedFieldProposals()
	current := map[string]any{"customfield_1": "old", "plugin.vendor": map[string]any{"id": "1"}}
	desired := map[string]any{"customfield_1": "h2. Progress", "plugin.vendor": map[string]any{"id": "2", "large": json.Number("9007199254740993")}}
	oldIssue := guardedFieldIssue("2026-08-23T10:00:00.000+0000", current)
	newIssue := guardedFieldIssue("2026-08-23T10:01:00.000+0000", desired)
	tests := []struct {
		name           string
		writeErr       error
		readback       domain.JiraGuardedFieldIssue
		readbackErr    error
		wantStatus     string
		wantErr        bool
		wantAttempted  bool
		wantReconciled bool
		wantComplete   bool
		wantAmbiguous  bool
	}{
		{name: "success proved", readback: newIssue, wantStatus: "applied", wantAttempted: true, wantReconciled: true, wantComplete: true},
		{name: "ambiguous proved", writeErr: errors.New("connection closed"), readback: newIssue, wantStatus: "applied", wantAttempted: true, wantReconciled: true, wantComplete: true},
		{name: "success old", readback: oldIssue, wantStatus: "unknown", wantErr: true, wantAttempted: true, wantReconciled: true, wantComplete: true, wantAmbiguous: true},
		{name: "ambiguous unreadable", writeErr: errors.New("connection closed"), readbackErr: errors.New("read failed"), wantStatus: "unknown", wantErr: true, wantAttempted: true, wantAmbiguous: true},
		{name: "definitive satisfying", writeErr: fieldSetHTTPError(400), readback: newIssue, wantStatus: "already_satisfied", wantAttempted: true, wantReconciled: true, wantComplete: true},
		{name: "definitive old", writeErr: fieldSetHTTPError(403), readback: oldIssue, wantStatus: "failed", wantErr: true, wantAttempted: true, wantReconciled: true, wantComplete: true},
		{name: "definitive unreadable", writeErr: fieldSetHTTPError(400), readbackErr: errors.New("read failed"), wantStatus: "failed", wantErr: true, wantAttempted: true},
		{name: "typed no attempt", writeErr: guardedFieldNoAttemptStub{cause: domain.ErrForbidden}, wantStatus: "blocked", wantErr: true, wantComplete: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			port := &guardedFieldPortStub{issues: []domain.JiraGuardedFieldIssue{oldIssue, oldIssue, test.readback}, writeErr: test.writeErr, issueErrors: map[int]error{3: test.readbackErr}}
			result, err := (&JiraService{tr: port, baseURL: "https://jira.example.test"}).SetFieldsGuarded(t.Context(), "PROJ-1", guardedFieldApplyOpts(t, proposals, current))
			if (err != nil) != test.wantErr || result.Status != test.wantStatus || result.WriteAttempted != test.wantAttempted || result.Reconciled != test.wantReconciled || result.Complete != test.wantComplete {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			var diagnostic interface{ DiagnosticAmbiguousWrite() bool }
			if errors.As(err, &diagnostic) && diagnostic.DiagnosticAmbiguousWrite() != test.wantAmbiguous || test.wantAmbiguous && !errors.As(err, &diagnostic) {
				t.Fatalf("ambiguous=%v err=%v", diagnostic != nil && diagnostic.DiagnosticAmbiguousWrite(), err)
			}
		})
	}
}

func TestSetFieldsGuardedRejectsReservedBeforePortSelection(t *testing.T) {
	for _, field := range []string{"project", "IssueType", " summary ", "DESCRIPTION", "labels", "Assignee"} {
		_, err := (&JiraService{}).SetFieldsGuarded(t.Context(), "PROJ-1", JiraFieldSetOpts{Proposals: []JiraFieldProposal{{Field: field, Source: "raw", Value: "x"}}, AllowFields: []string{field}})
		if !errors.Is(err, domain.ErrUsage) {
			t.Fatalf("field=%q err=%v", field, err)
		}
	}
}

func TestSetFieldsGuardedEnforcesReleasedValueEnvelopeDepthBeforePreview(t *testing.T) {
	field := "plugin.vendor"
	exact := guardedFieldNestedArray(domain.JiraGuardedFieldMaxValueNestingDepth)
	port := &guardedFieldPortStub{issues: []domain.JiraGuardedFieldIssue{guardedFieldIssue("2026-08-23T10:00:00.000+0000", map[string]any{field: "old"})}}
	result, err := (&JiraService{tr: port, baseURL: "https://jira.example.test"}).SetFieldsGuarded(t.Context(), "PROJ-1", JiraFieldSetOpts{
		Proposals:   []JiraFieldProposal{{Field: field, Source: "raw", Value: exact, InputBytes: domain.JiraGuardedFieldMaxValueNestingDepth*2 + 1}},
		AllowFields: []string{field},
	})
	if err != nil || !result.Complete || result.Bounds.MaxJSONNestingDepth != 10_000 || result.Bounds.MaxValueNestingDepth != 9_997 || port.prepareCalls != 1 {
		t.Fatalf("result=%+v err=%v prepare=%d", result, err, port.prepareCalls)
	}
	emitted, marshalErr := json.Marshal(result)
	if marshalErr != nil || !json.Valid(emitted) {
		t.Fatalf("emit exact-depth result: bytes=%d err=%v", len(emitted), marshalErr)
	}

	overPort := &guardedFieldPortStub{}
	_, err = (&JiraService{tr: overPort, baseURL: "https://jira.example.test"}).SetFieldsGuarded(t.Context(), "PROJ-1", JiraFieldSetOpts{
		Proposals:   []JiraFieldProposal{{Field: field, Source: "raw", Value: guardedFieldNestedArray(domain.JiraGuardedFieldMaxValueNestingDepth + 1)}},
		AllowFields: []string{field},
	})
	if !errors.Is(err, domain.ErrUsage) || overPort.catalogCalls != 0 || overPort.prepareCalls != 0 {
		t.Fatalf("over-depth err=%v catalog=%d prepare=%d", err, overPort.catalogCalls, overPort.prepareCalls)
	}
}

func TestSetFieldsGuardedNoopStopsAfterInitialQualification(t *testing.T) {
	proposals := guardedFieldProposals()
	desired := map[string]any{"customfield_1": "h2. Progress", "plugin.vendor": map[string]any{"id": "2", "large": json.Number("9007199254740993")}}
	port := &guardedFieldPortStub{issues: []domain.JiraGuardedFieldIssue{guardedFieldIssue("2026-08-23T10:00:00.000+0000", desired)}}
	result, err := (&JiraService{tr: port, baseURL: "https://jira.example.test"}).SetFieldsGuarded(t.Context(), "PROJ-1", guardedFieldApplyOpts(t, proposals, desired))
	if err != nil || result.Status != "already_satisfied" || port.catalogCalls != 1 || port.issueCalls != 1 || port.writeCalls != 0 {
		t.Fatalf("result=%+v err=%v calls=%d/%d/%d", result, err, port.catalogCalls, port.issueCalls, port.writeCalls)
	}
}

func TestGuardedFieldCanonicalProjectionIsStableAndBounded(t *testing.T) {
	value := map[string]any{"z": "<>&\u2028\u2029", "a": []any{json.Number("9007199254740993"), true, nil}}
	kind, bytes, digest, err := canonicalGuardedFieldValue(value, 1024)
	if err != nil || kind != "object" || bytes == 0 || len(digest) != 64 {
		t.Fatalf("kind=%q bytes=%d digest=%q err=%v", kind, bytes, digest, err)
	}
	reordered := map[string]any{"a": []any{json.Number("9007199254740993"), true, nil}, "z": "<>&\u2028\u2029"}
	_, otherBytes, otherDigest, _ := canonicalGuardedFieldValue(reordered, 1024)
	if bytes != otherBytes || digest != otherDigest {
		t.Fatalf("map order changed projection: %d/%s != %d/%s", bytes, digest, otherBytes, otherDigest)
	}
	if _, _, _, err := canonicalGuardedFieldValue(value, int64(bytes-1)); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("exact bound err=%v", err)
	}
}

func TestGuardedFieldCanonicalTraversalStackTracksDepthNotFlatArrayLength(t *testing.T) {
	flat := make([]any, 100_000)
	for index := range flat {
		flat[index] = json.Number("0")
	}
	maximumStack := 0
	kind, bytes, digest, err := canonicalGuardedFieldValueObserved(flat, 1<<20, &maximumStack)
	if err != nil || kind != "array" || bytes != len(flat)*2+1 || len(digest) != 64 {
		t.Fatalf("kind=%q bytes=%d digest=%q stack=%d err=%v", kind, bytes, digest, maximumStack, err)
	}
	if maximumStack != 2 {
		t.Fatalf("flat array traversal stack=%d want exactly container+one active child", maximumStack)
	}

	var deep any = json.Number("0")
	for range 64 {
		deep = []any{deep}
	}
	maximumStack = 0
	if _, _, _, err := canonicalGuardedFieldValueObserved(deep, 1024, &maximumStack); err != nil || maximumStack != 65 {
		t.Fatalf("deep traversal stack=%d err=%v", maximumStack, err)
	}
}

func TestJiraFieldProposalEqualityPreservesSubsetSemantics(t *testing.T) {
	if !jiraFieldProposalEqual(map[string]any{"id": "1", "name": "enriched"}, map[string]any{"id": "1"}) {
		t.Fatal("enriched response object did not satisfy desired subset")
	}
	if jiraFieldProposalEqual([]any{"a", "b"}, []any{"b", "a"}) || jiraFieldProposalEqual(" value ", "value") {
		t.Fatal("array order or exact string semantics changed")
	}
}
