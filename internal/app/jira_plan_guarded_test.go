package app

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/domain"
)

const jiraPlanTestHeader = "schema_version,operation,source,target,type,field,value\n"

func writeJiraPlanTestFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plan.csv")
	if err := os.WriteFile(path, []byte(jiraPlanTestHeader+body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadJiraPlanDocumentStrictV2AndImmutableSingleOpen(t *testing.T) {
	path := writeJiraPlanTestFile(t, "2,label_add,ops-1,,,,alpha\n")
	document, err := ReadJiraPlanDocument(path)
	if err != nil {
		t.Fatal(err)
	}
	wantDigest := document.normalizedSHA256
	if len(document.rows) != 1 || document.rows[0].source != "OPS-1" || document.rows[0].value != "alpha" {
		t.Fatalf("normalized document=%+v", document.rows)
	}
	if err := os.WriteFile(path, []byte(jiraPlanTestHeader+"2,label_add,OPS-2,,,,replacement\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := BindJiraPlanDocument(document, "preview"); err != nil {
		t.Fatal(err)
	}
	first, err := JiraPlanDocumentPolicyRequests(document, "preview")
	if err != nil {
		t.Fatal(err)
	}
	first[0].Targets[0].Key = "MUTATED-1"
	second, err := JiraPlanDocumentPolicyRequests(document, "preview")
	if err != nil || second[0].Targets[0].Key != "OPS-1" || document.normalizedSHA256 != wantDigest || document.rows[0].value != "alpha" {
		t.Fatalf("defensive projection=%+v err=%v document=%+v", second, err, document.rows)
	}
	if err := BindJiraPlanDocument(document, "preview"); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("second bind err=%v", err)
	}
	if err := BindJiraPlanDocument(&JiraPlanDocument{}, "preview"); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("zero document err=%v", err)
	}
}

func TestJiraPlanDocumentRawPolicyGeometry(t *testing.T) {
	path := writeJiraPlanTestFile(t, "2,link,OPS-1,PROJ-2,Blocks,,\n2,comment,DOC-3,,,,body\n2,field,OPS-4,,,customfield_1,\"\"\"text\"\"\"\n")
	document, err := ReadJiraPlanDocument(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := BindJiraPlanDocument(document, "apply"); err != nil {
		t.Fatal(err)
	}
	requests, err := JiraPlanDocumentPolicyRequests(document, "apply")
	if err != nil || len(requests) != 3 {
		t.Fatalf("requests=%+v err=%v", requests, err)
	}
	if requests[0].Verbs[0] != domain.WriteVerbUpdate || len(requests[0].Targets) != 2 ||
		requests[0].Targets[0].Kind != "link" || requests[0].Targets[0].Key != "OPS-1" || requests[0].Targets[1].Kind != "link" || requests[0].Targets[1].Key != "PROJ-2" ||
		requests[1].Verbs[0] != domain.WriteVerbComment || requests[1].Targets[0].Kind != "issue" ||
		requests[2].Verbs[0] != domain.WriteVerbUpdate || requests[2].Targets[0].Kind != "issue" {
		t.Fatalf("raw policy geometry=%+v", requests)
	}
}

func TestReadJiraPlanDocumentExactBounds(t *testing.T) {
	t.Run("document bytes", func(t *testing.T) {
		base := jiraPlanTestHeader + "2,label_add,OPS-1,,,,alpha\n"
		exact := base + strings.Repeat("\n", int(JiraPlanMaxDocumentBytes)-len(base))
		path := filepath.Join(t.TempDir(), "exact.csv")
		if err := os.WriteFile(path, []byte(exact), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadJiraPlanDocument(path); err != nil {
			t.Fatalf("exact bound: %v", err)
		}
		if err := os.WriteFile(path, append([]byte(exact), '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadJiraPlanDocument(path); !errors.Is(err, domain.ErrUsage) {
			t.Fatalf("bound +1 err=%v", err)
		}
	})
	t.Run("rows", func(t *testing.T) {
		var rows strings.Builder
		for i := 0; i < JiraPlanMaxRows; i++ {
			rows.WriteString("2,label_add,OPS-1,,,,alpha\n")
		}
		path := writeJiraPlanTestFile(t, rows.String())
		if document, err := ReadJiraPlanDocument(path); err != nil || len(document.rows) != JiraPlanMaxRows {
			t.Fatalf("exact rows=%v err=%v", document, err)
		}
		if err := os.WriteFile(path, []byte(jiraPlanTestHeader+rows.String()+"2,label_add,OPS-1,,,,alpha\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadJiraPlanDocument(path); !errors.Is(err, domain.ErrUsage) {
			t.Fatalf("rows +1 err=%v", err)
		}
	})
	t.Run("field cell", func(t *testing.T) {
		for _, extra := range []int{0, 1} {
			var encoded bytes.Buffer
			writer := csv.NewWriter(&encoded)
			if err := writer.Write(strings.Split(strings.TrimSuffix(jiraPlanTestHeader, "\n"), ",")); err != nil {
				t.Fatal(err)
			}
			value := `"` + strings.Repeat("a", JiraPlanMaxFieldCellBytes-2+extra) + `"`
			if err := writer.Write([]string{"2", "field", "OPS-1", "", "", "customfield_1", value}); err != nil {
				t.Fatal(err)
			}
			writer.Flush()
			path := filepath.Join(t.TempDir(), "field.csv")
			if err := os.WriteFile(path, encoded.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := ReadJiraPlanDocument(path)
			if extra == 0 && err != nil || extra == 1 && !errors.Is(err, domain.ErrUsage) {
				t.Fatalf("extra=%d err=%v", extra, err)
			}
		}
	})
}

func TestReadJiraPlanDocumentRejectsWireAliasesAndStrictFieldJSON(t *testing.T) {
	for _, body := range []string{
		"version,operation,source,target,type,field,value\n2,label_add,OPS-1,,,,alpha\n",
		"\"schema_version\",operation,source,target,type,field,value\n2,label_add,OPS-1,,,,alpha\n",
		"schema_version,operation,source,target,type,field,value\r\n2,label_add,OPS-1,,,,alpha\n",
		jiraPlanTestHeader + "1,label_add,OPS-1,,,,alpha\n",
		jiraPlanTestHeader + "2,field,OPS-1,,,customfield_1,{\"a\":1\x00}\n",
		jiraPlanTestHeader + "2,field,OPS-1,,,customfield_1,\"{\"\"a\"\":1,\"\"a\"\":2}\"\n",
	} {
		path := filepath.Join(t.TempDir(), "invalid.csv")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadJiraPlanDocument(path); !errors.Is(err, domain.ErrUsage) {
			t.Fatalf("invalid document accepted: err=%v", err)
		}
	}
}

type jiraPlanLabelPort struct {
	domain.Tracker
	reads          int
	writes         int
	labels         []string
	updated        string
	charge         bool
	chargesPerRead int
}

type jiraPlanDenyAuthorizer struct {
	preflights int
	request    domain.WriteAuthorizationRequest
}

type jiraPlanRecordingAuthorizer struct {
	requests []domain.WriteAuthorizationRequest
}

func (authorizer *jiraPlanRecordingAuthorizer) Authorize(ctx context.Context, _ domain.WriteAuthorizationRequest) (context.Context, error) {
	return ctx, nil
}

func (authorizer *jiraPlanRecordingAuthorizer) Preflight(request domain.WriteAuthorizationRequest) error {
	authorizer.requests = append(authorizer.requests, request)
	return nil
}

func (authorizer *jiraPlanDenyAuthorizer) Authorize(ctx context.Context, _ domain.WriteAuthorizationRequest) (context.Context, error) {
	return ctx, nil
}

func (authorizer *jiraPlanDenyAuthorizer) Preflight(request domain.WriteAuthorizationRequest) error {
	authorizer.preflights++
	authorizer.request = request
	return domain.ErrCheckFailed
}

func (port *jiraPlanLabelPort) ReadGuardedLabelSnapshot(ctx context.Context, _ string) (domain.JiraGuardedLabelSnapshot, error) {
	port.reads++
	if port.charge {
		budget := domain.ReadBudgetFromContext(ctx)
		if budget == nil {
			return domain.JiraGuardedLabelSnapshot{}, domain.ErrCheckFailed
		}
		charges := port.chargesPerRead
		if charges == 0 {
			charges = 1
		}
		for range charges {
			if err := budget.TakeAttempt(); err != nil {
				return domain.JiraGuardedLabelSnapshot{}, err
			}
		}
		_, finish, err := budget.BeginResponse(ctx)
		if err != nil {
			return domain.JiraGuardedLabelSnapshot{}, err
		}
		finish(64)
	}
	return domain.JiraGuardedLabelSnapshot{ID: "10001", Key: "OPS-1", Project: "OPS", Labels: append([]string{}, port.labels...), Updated: port.updated, Complete: true}, nil
}

func (port *jiraPlanLabelPort) WriteGuardedLabelDelta(_ context.Context, write domain.JiraGuardedLabelWrite) error {
	port.writes++
	port.labels = append([]string(nil), write.Add...)
	port.updated = "2026-08-23T12:01:00.000+0000"
	return nil
}

func runJiraPlanLabel(t *testing.T, mode, expected string, port *jiraPlanLabelPort) (*JiraPlanResult, error) {
	t.Helper()
	document, err := ReadJiraPlanDocument(writeJiraPlanTestFile(t, "2,label_add,OPS-1,,,,alpha\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := BindJiraPlanDocument(document, mode); err != nil {
		t.Fatal(err)
	}
	return (&JiraService{tr: port, baseURL: "https://jira.example.test"}).RunJiraPlan(t.Context(), document, JiraPlanRunOpts{Mode: mode, ExpectedProposalHash: expected, AllowOps: []string{"label_add"}})
}

func TestRunJiraPlanHashBarrierAndClosedResult(t *testing.T) {
	previewPort := &jiraPlanLabelPort{updated: "2026-08-23T12:00:00.000+0000", labels: []string{}}
	preview, err := runJiraPlanLabel(t, "preview", "", previewPort)
	if err != nil || preview.Status != "would_apply" || !preview.Complete || preview.ProposalHash == "" || previewPort.writes != 0 || preview.ParentBudget.MaxRequests != 1 || preview.ParentBudget.MaxResponseBytes != 16<<20 {
		t.Fatalf("preview=%+v err=%v reads=%d writes=%d", preview, err, previewPort.reads, previewPort.writes)
	}
	wire, err := json.Marshal(preview)
	wantWire, readErr := os.ReadFile("testdata/jira_plan_preview_v2.json")
	if err != nil || !bytes.HasPrefix(wire, []byte(`{"schema_version":2,"operation":"jira_issue_plan","mode":"preview","status":"would_apply","complete":true,"row_count":1,`)) ||
		bytes.Contains(wire, []byte("plan.csv")) || bytes.Contains(wire, []byte(`"value"`)) || bytes.Contains(wire, []byte("alpha")) {
		t.Fatalf("closed preview wire=%s err=%v", wire, err)
	}
	if readErr != nil || !bytes.Equal(wire, bytes.TrimSpace(wantWire)) {
		t.Fatalf("preview golden mismatch: read=%v\ngot=%s\nwant=%s", readErr, wire, wantWire)
	}

	mismatchPort := &jiraPlanLabelPort{updated: "2026-08-23T12:00:00.000+0000", labels: []string{}}
	mismatch, err := runJiraPlanLabel(t, "apply", strings.Repeat("0", 64), mismatchPort)
	if !errors.Is(err, domain.ErrCheckFailed) || mismatch.Status != "blocked" || mismatchPort.writes != 0 {
		t.Fatalf("mismatch=%+v err=%v writes=%d", mismatch, err, mismatchPort.writes)
	}

	applyPort := &jiraPlanLabelPort{updated: "2026-08-23T12:00:00.000+0000", labels: []string{}}
	applied, err := runJiraPlanLabel(t, "apply", preview.ProposalHash, applyPort)
	if err != nil || applied.Status != "applied" || !applied.Complete || applyPort.writes != 1 || applied.ProposalHash != preview.ProposalHash {
		t.Fatalf("apply=%+v err=%v reads=%d writes=%d", applied, err, applyPort.reads, applyPort.writes)
	}
}

func TestRunJiraPlanCanonicalDenyStopsBeforeHashAndWriter(t *testing.T) {
	document, err := ReadJiraPlanDocument(writeJiraPlanTestFile(t, "2,label_add,OPS-1,,,,alpha\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := BindJiraPlanDocument(document, "preview"); err != nil {
		t.Fatal(err)
	}
	port := &jiraPlanLabelPort{updated: "2026-08-23T12:00:00.000+0000"}
	authorizer := &jiraPlanDenyAuthorizer{}
	result, err := (&JiraService{tr: port, baseURL: "https://jira.example.test", writeAuthorizer: authorizer}).RunJiraPlan(t.Context(), document, JiraPlanRunOpts{Mode: "preview", AllowOps: []string{"label_add"}})
	if !errors.Is(err, domain.ErrCheckFailed) || result == nil || result.Status != "blocked" || result.ProposalHash != "" || port.writes != 0 || authorizer.preflights != 1 {
		t.Fatalf("result=%+v err=%v writes=%d preflights=%d", result, err, port.writes, authorizer.preflights)
	}
	if len(authorizer.request.Targets) != 1 || authorizer.request.Targets[0].Key != "OPS-1" || authorizer.request.Targets[0].Project != "OPS" || authorizer.request.Targets[0].ID != "" {
		t.Fatalf("canonical request=%+v", authorizer.request)
	}
}

func TestRunJiraPlanChildrenShareOneExactParentBudget(t *testing.T) {
	document, err := ReadJiraPlanDocument(writeJiraPlanTestFile(t, "2,label_add,OPS-1,,,,alpha\n2,label_add,OPS-1,,,,beta\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := BindJiraPlanDocument(document, "preview"); err != nil {
		t.Fatal(err)
	}
	port := &jiraPlanLabelPort{updated: "2026-08-23T12:00:00.000+0000", charge: true}
	result, err := (&JiraService{tr: port, baseURL: "https://jira.example.test"}).RunJiraPlan(t.Context(), document, JiraPlanRunOpts{Mode: "preview", AllowOps: []string{"label_add"}})
	if err != nil || result.ParentBudget.MaxRequests != 2 || result.ParentBudget.MaxResponseBytes != 32<<20 || result.Usage.Requests != 2 || result.Usage.ResponseBytes != 128 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(result.Rows) != 2 || result.Rows[0].Usage.Requests != 1 || result.Rows[1].Usage.Requests != 1 {
		t.Fatalf("row usage=%+v", result.Rows)
	}
}

func TestJiraPlanAdmissionExactFormulasAndCaps(t *testing.T) {
	requests, responses, bounds, err := jiraPlanAdmit("preview", JiraPlanFamilyCounts{Links: 1, Label: 2, Comments: 1, Field: 1})
	if err != nil || requests != 109 || responses != 150994944 || bounds.Admitted.PreviewRequests != 109 || bounds.Admitted.ApplyRequests != 329 {
		t.Fatalf("mixed admission requests=%d responses=%d bounds=%+v err=%v", requests, responses, bounds, err)
	}
	for _, counts := range []JiraPlanFamilyCounts{{Field: 4}, {Comments: 11}, {Links: JiraPlanMaxRows + 1}} {
		if _, _, _, err := jiraPlanAdmit("preview", counts); !errors.Is(err, domain.ErrUsage) {
			t.Fatalf("counts=%+v err=%v", counts, err)
		}
	}
	requests, responses, _, err = jiraPlanAdmit("apply", JiraPlanFamilyCounts{Comments: 6})
	if err != nil || requests != 1836 || responses != 100663296 {
		t.Fatalf("apply cap edge requests=%d responses=%d err=%v", requests, responses, err)
	}
	if _, _, _, err := jiraPlanAdmit("apply", JiraPlanFamilyCounts{Comments: 7}); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("apply cap +1 err=%v", err)
	}
}

func TestJiraPlanAllowlistsAndEveryResolvedSelectorAreProposalBound(t *testing.T) {
	rows := []jiraPlanDocumentRow{
		{operation: "link", source: "OPS-1", target: "OPS-2", typeName: "Blocks"},
		{operation: "link", source: "OPS-3", target: "OPS-4", typeName: "Blocks"},
	}
	opts, counts, err := normalizeJiraPlanRunOpts(JiraPlanRunOpts{
		Mode: "preview", AllowOps: []string{"link"}, AllowLinkTypes: []string{"Blocks:outward"},
	}, rows)
	if err != nil || counts.Links != 2 {
		t.Fatalf("normalized opts=%+v counts=%+v err=%v", opts, counts, err)
	}
	result := &JiraPlanResult{
		SchemaVersion: 2, Operation: "jira_issue_plan", Document: JiraPlanDocumentProjection{NormalizedSHA256: strings.Repeat("a", 64)},
		FamilyCounts: counts, Bounds: JiraPlanBounds{}, Rows: []JiraPlanResultRow{
			{Row: 2, Family: "link", Requested: JiraPlanLinkRequested{SourceKey: "OPS-1", TargetKey: "OPS-2"}, Effect: JiraPlanLinkEffect{Action: "add"}, Qualified: JiraPlanLinkQualified{}, Authorization: &JiraPlanAuthorization{}},
			{Row: 3, Family: "link", Requested: JiraPlanLinkRequested{SourceKey: "OPS-3", TargetKey: "OPS-4"}, Effect: JiraPlanLinkEffect{Action: "add"}, Qualified: JiraPlanLinkQualified{}, Authorization: &JiraPlanAuthorization{}},
		},
	}
	prepared := []jiraPlanPreparedRow{
		{linkSelectors: []jiraPlanResolvedSelector{{Selector: "Blocks:outward", TypeID: "10000", Role: "outward"}}},
		{linkSelectors: []jiraPlanResolvedSelector{{Selector: "Blocks:outward", TypeID: "10000", Role: "outward"}}},
	}
	first, err := jiraPlanProposalHash(result, opts, prepared)
	if err != nil {
		t.Fatal(err)
	}
	prepared[1].linkSelectors[0].TypeID = "10001"
	second, err := jiraPlanProposalHash(result, opts, prepared)
	if err != nil || first == second {
		t.Fatalf("every row selector resolution must be bound: first=%s second=%s err=%v", first, second, err)
	}
	opts.AllowOps = append(opts.AllowOps, "label_add")
	third, err := jiraPlanProposalHash(result, opts, prepared)
	if err != nil || second == third {
		t.Fatalf("allow operations must be bound: second=%s third=%s err=%v", second, third, err)
	}
	if _, _, err := normalizeJiraPlanRunOpts(JiraPlanRunOpts{Mode: "preview", AllowOps: []string{"label_add"}, AllowLinkTypes: []string{"Blocks"}}, rows[:0]); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("unused link selectors err=%v", err)
	}
	if _, _, err := normalizeJiraPlanRunOpts(JiraPlanRunOpts{Mode: "preview", AllowOps: []string{"field"}, AllowFields: []string{"customfield_1"}}, rows[:0]); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("unused field selectors err=%v", err)
	}
	document, err := ReadJiraPlanDocument(writeJiraPlanTestFile(t, "2,link,APP-1,OPS-2,Blocks,,\n"))
	if err != nil || BindJiraPlanDocument(document, "preview") != nil {
		t.Fatalf("document err=%v", err)
	}
	port := guardedLinkFixture(false)
	result, runErr := (&JiraService{tr: port, baseURL: "https://jira.example.test"}).RunJiraPlan(t.Context(), document, JiraPlanRunOpts{Mode: "preview", AllowOps: []string{"link"}, AllowLinkTypes: []string{"Blocks:outward", "blocks"}})
	if !errors.Is(runErr, domain.ErrCheckFailed) || result.Status != "blocked" || result.ProposalHash != "" || port.writes != 0 {
		t.Fatalf("duplicate resolved selector result=%+v err=%v writes=%d", result, runErr, port.writes)
	}
}

func TestJiraPlanCanonicalLinkAuthorizationUsesStrictOutwardInwardOrder(t *testing.T) {
	base := JiraPlanResultRow{
		Family: "link", Requested: JiraPlanLinkRequested{SourceKey: "OPS-1", TargetKey: "PROJ-2"}, Effect: JiraPlanLinkEffect{},
		Qualified: JiraPlanLinkQualified{SourceID: "20", SourceProject: "OPS", TargetID: "10", TargetProject: "PROJ"},
	}
	prepared := jiraPlanPreparedRow{sourceKey: "MOVED-11", sourceProject: "MOVED", targetKey: "NEXT-22", targetProject: "NEXT"}
	for _, test := range []struct {
		role, want string
	}{
		{role: "outward", want: `{"verbs":["update"],"targets":[{"service":"jira","kind":"link","key":"MOVED-11","project":"MOVED"},{"service":"jira","kind":"link","key":"NEXT-22","project":"NEXT"}]}`},
		{role: "inward", want: `{"verbs":["update"],"targets":[{"service":"jira","kind":"link","key":"NEXT-22","project":"NEXT"},{"service":"jira","kind":"link","key":"MOVED-11","project":"MOVED"}]}`},
		{role: "neutral", want: `{"verbs":["update"],"targets":[{"service":"jira","kind":"link","key":"NEXT-22","project":"NEXT"},{"service":"jira","kind":"link","key":"MOVED-11","project":"MOVED"}]}`},
	} {
		row := base
		effect := row.Effect.(JiraPlanLinkEffect)
		effect.ResolvedRole = test.role
		row.Effect = effect
		request := jiraPlanCanonicalAuthorization(row, prepared)
		wire, err := json.Marshal(jiraPlanAuthorizationProjection(request))
		if err != nil || string(wire) != test.want {
			t.Fatalf("role=%s authorization=%s err=%v", test.role, wire, err)
		}
	}
}

func TestJiraPlanAggregateTruthTable(t *testing.T) {
	tests := []struct {
		name, mode, status string
		rows               []JiraPlanResultRow
		complete           bool
	}{
		{name: "preview satisfied", mode: "preview", status: "already_satisfied", complete: true, rows: []JiraPlanResultRow{{Status: "already_satisfied", Complete: true}}},
		{name: "preview would apply", mode: "preview", status: "would_apply", complete: true, rows: []JiraPlanResultRow{{Status: "would_apply", Complete: true}}},
		{name: "preview blocked", mode: "preview", status: "blocked", complete: true, rows: []JiraPlanResultRow{{Status: "blocked", Complete: true}}},
		{name: "apply complete", mode: "apply", status: "applied", complete: true, rows: []JiraPlanResultRow{{Status: "applied", Complete: true}, {Status: "already_satisfied", Complete: true}}},
		{name: "fully processed partial", mode: "apply", status: "partially_applied", complete: true, rows: []JiraPlanResultRow{{Status: "recovered", Complete: true}, {Status: "not_applied", Complete: true}}},
		{name: "fail fast partial", mode: "apply", status: "partially_applied", complete: false, rows: []JiraPlanResultRow{{Status: "applied", Complete: true}, {Status: "skipped", Complete: false}}},
		{name: "definitive no positive", mode: "apply", status: "not_applied", complete: true, rows: []JiraPlanResultRow{{Status: "not_applied", Complete: true}}},
		{name: "ambiguity dominates", mode: "apply", status: "outcome_unknown", complete: false, rows: []JiraPlanResultRow{{Status: "applied", Complete: true}, {Status: "outcome_unknown", Complete: false}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			budget, err := domain.NewReadBudget(10, 1024)
			if err != nil {
				t.Fatal(err)
			}
			result := &JiraPlanResult{Mode: test.mode, RowCount: len(test.rows), Rows: test.rows}
			jiraPlanFinalize(result, budget)
			if result.Status != test.status || result.Complete != test.complete {
				t.Fatalf("status=%s complete=%t result=%+v", result.Status, result.Complete, result)
			}
		})
	}
}

type jiraPlanAllFamilyPort struct {
	domain.Tracker
	link        *guardedLinkTracker
	labels      *guardedLabelStore
	comment     *jiraGuardedCommentStub
	field       *guardedFieldPortStub
	linkAliases map[string]string
}

func (p *jiraPlanAllFamilyPort) ReadStrictLinkTypes(ctx context.Context) (domain.JiraStrictLinkCatalog, error) {
	return p.link.ReadStrictLinkTypes(ctx)
}
func (p *jiraPlanAllFamilyPort) ReadStrictLinkEndpoint(ctx context.Context, reference string) (domain.JiraStrictLinkEndpoint, error) {
	if current := p.linkAliases[reference]; current != "" {
		reference = current
	}
	return p.link.ReadStrictLinkEndpoint(ctx, reference)
}
func (p *jiraPlanAllFamilyPort) AddGuardedLink(ctx context.Context, write domain.JiraGuardedLinkWrite) error {
	return p.link.AddGuardedLink(ctx, write)
}
func (p *jiraPlanAllFamilyPort) DeleteGuardedLink(ctx context.Context, write domain.JiraGuardedLinkWrite) error {
	return p.link.DeleteGuardedLink(ctx, write)
}
func (p *jiraPlanAllFamilyPort) ReadGuardedLabelSnapshot(ctx context.Context, reference string) (domain.JiraGuardedLabelSnapshot, error) {
	return p.labels.ReadGuardedLabelSnapshot(ctx, reference)
}
func (p *jiraPlanAllFamilyPort) WriteGuardedLabelDelta(ctx context.Context, write domain.JiraGuardedLabelWrite) error {
	return p.labels.WriteGuardedLabelDelta(ctx, write)
}
func (p *jiraPlanAllFamilyPort) ReadGuardedCommentIssue(ctx context.Context, reference string) (domain.JiraGuardedCommentIssue, error) {
	return p.comment.ReadGuardedCommentIssue(ctx, reference)
}
func (p *jiraPlanAllFamilyPort) ReadGuardedCommentActor(ctx context.Context) (domain.JiraGuardedCommentActor, error) {
	return p.comment.ReadGuardedCommentActor(ctx)
}
func (p *jiraPlanAllFamilyPort) ListJiraCommentsQualified(ctx context.Context, issueID string, opts domain.JiraCommentReadOptions) (domain.JiraCommentInventory, error) {
	return p.comment.ListJiraCommentsQualified(ctx, issueID, opts)
}
func (p *jiraPlanAllFamilyPort) WriteGuardedComment(ctx context.Context, write domain.JiraGuardedCommentWrite) (domain.JiraGuardedCommentAcknowledgement, error) {
	return p.comment.WriteGuardedComment(ctx, write)
}
func (p *jiraPlanAllFamilyPort) ReadGuardedFieldCatalog(ctx context.Context, selected []string) (domain.JiraGuardedFieldCatalog, error) {
	return p.field.ReadGuardedFieldCatalog(ctx, selected)
}
func (p *jiraPlanAllFamilyPort) ReadGuardedFieldIssue(ctx context.Context, reference string, selected []string) (domain.JiraGuardedFieldIssue, error) {
	return p.field.ReadGuardedFieldIssue(ctx, reference, selected)
}
func (p *jiraPlanAllFamilyPort) PrepareGuardedFields(request domain.JiraGuardedFieldPreparationRequest) (domain.JiraGuardedFieldPreparation, error) {
	return p.field.PrepareGuardedFields(request)
}
func (p *jiraPlanAllFamilyPort) WriteGuardedFields(ctx context.Context, write domain.JiraGuardedFieldWrite) error {
	return p.field.WriteGuardedFields(ctx, write)
}

func jiraPlanAllFamilyFixture(apply bool) *jiraPlanAllFamilyPort {
	const before = "2026-08-23T10:00:00Z"
	const after = "2026-08-23T10:00:01Z"
	link := guardedLinkFixture(false)
	labelSnapshots := []domain.JiraGuardedLabelSnapshot{{ID: "30", Key: "OPS-1", Project: "OPS", Labels: []string{"old"}, Updated: before, Complete: true}}
	commentIssue := domain.JiraGuardedCommentIssue{ID: "40", Key: "PROJ-1", Project: "PROJ", Updated: before, Complete: true}
	comment := guardedCommentFixture()
	comment.issueFn = func(call int) (domain.JiraGuardedCommentIssue, error) {
		issue := commentIssue
		if call == 3 {
			issue.Updated = after
		}
		return issue, nil
	}
	comment.inventoryFn = func(call int) (domain.JiraCommentInventory, error) {
		if call == 3 {
			return completeCommentInventory([]domain.Comment{{ID: "50", AuthorName: "writer", AuthorKey: "writer-key", Created: after, Updated: after, Body: "body"}}), nil
		}
		return domain.JiraCommentInventory{Comments: []domain.Comment{}, Complete: true, TotalKnown: true, PageCount: 1}, nil
	}
	comment.ack = domain.JiraGuardedCommentAcknowledgement{ID: "50"}
	oldField := guardedFieldIssue(before, map[string]any{"customfield_1": "old"})
	newField := guardedFieldIssue(after, map[string]any{"customfield_1": "new"})
	fieldIssues := []domain.JiraGuardedFieldIssue{oldField}
	if apply {
		labelSnapshots = append(labelSnapshots, labelSnapshots[0], domain.JiraGuardedLabelSnapshot{ID: "30", Key: "OPS-1", Project: "OPS", Labels: []string{"alpha", "old"}, Updated: after, Complete: true})
		fieldIssues = []domain.JiraGuardedFieldIssue{oldField, oldField, newField}
	}
	return &jiraPlanAllFamilyPort{
		link: link, labels: &guardedLabelStore{snapshots: labelSnapshots}, comment: comment,
		field: &guardedFieldPortStub{issues: fieldIssues, issueErrors: map[int]error{}}, linkAliases: map[string]string{},
	}
}

func runJiraPlanAllFamilies(t *testing.T, mode, expected string, port *jiraPlanAllFamilyPort) (*JiraPlanResult, error) {
	t.Helper()
	document, err := ReadJiraPlanDocument(writeJiraPlanTestFile(t, "2,link,APP-1,OPS-2,Blocks,,\n2,label_add,OPS-1,,,,alpha\n2,comment,PROJ-1,,,,body\n2,field,PROJ-1,,,customfield_1,\"\"\"new\"\"\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := BindJiraPlanDocument(document, mode); err != nil {
		t.Fatal(err)
	}
	return (&JiraService{tr: port, baseURL: "https://jira.example.test"}).RunJiraPlan(t.Context(), document, JiraPlanRunOpts{
		Mode: mode, ExpectedProposalHash: expected, AllowOps: []string{"link", "label_add", "comment", "field"},
		AllowFields: []string{"customfield_1"}, AllowLinkTypes: []string{"Blocks"},
	})
}

func TestRunJiraPlanDispatchesAllFourPreparedFamilies(t *testing.T) {
	preview, err := runJiraPlanAllFamilies(t, "preview", "", jiraPlanAllFamilyFixture(false))
	if err != nil || preview.Status != "would_apply" {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	assertWire := func(name string, got any, want string) {
		t.Helper()
		wire, marshalErr := json.Marshal(got)
		if marshalErr != nil || string(wire) != want {
			t.Fatalf("%s wire=%s want=%s err=%v", name, wire, want, marshalErr)
		}
	}
	assertWire("link requested", preview.Rows[0].Requested, `{"source_key":"APP-1","target_key":"OPS-2"}`)
	assertWire("link effect", preview.Rows[0].Effect, `{"action":"add","selector_bytes":6,"selector_sha256":"1cd5a6687bb76e3926f8c89a9fb290cd17d893ca944899952322d3fb5c9896d3","resolved_type_id":"7","resolved_role":"outward"}`)
	assertWire("link qualified", preview.Rows[0].Qualified, `{"source_id":"10","target_id":"20","source_project":"APP","target_project":"OPS","source_updated_sha256":"40d7a64322be295b56be1cb9f80cef0b7d3d5eec3ce888388b5f7a3bef664fab"}`)
	assertWire("label requested", preview.Rows[1].Requested, `{"source_key":"OPS-1"}`)
	assertWire("label effect", preview.Rows[1].Effect, `{"action":"add","count":1,"bytes":5,"sha256":"8ed3f6ad685b959ead7022518e1af76cd816f8e8ec7ccdda1ed4018e8f2223f8"}`)
	assertWire("label qualified", preview.Rows[1].Qualified, `{"source_id":"30","project":"OPS","updated_sha256":"40d7a64322be295b56be1cb9f80cef0b7d3d5eec3ce888388b5f7a3bef664fab"}`)
	assertWire("comment requested", preview.Rows[2].Requested, `{"source_key":"PROJ-1"}`)
	assertWire("comment effect", preview.Rows[2].Effect, `{"satisfaction_policy":"exact_body_present","body_bytes":4,"body_sha256":"230d8358dc8e8890b4c58deeb62912ee2f20357ae92a5cc861b98e68fe31acb5"}`)
	commentQualified := preview.Rows[2].Qualified.(JiraPlanCommentQualified)
	assertWire("comment qualified", commentQualified, fmt.Sprintf(`{"source_id":"40","project":"PROJ","updated_sha256":"40d7a64322be295b56be1cb9f80cef0b7d3d5eec3ce888388b5f7a3bef664fab","baseline_count":0,"baseline_sha256":%q,"actor_sha256":%q}`, commentQualified.BaselineSHA256, commentQualified.ActorSHA256))
	assertWire("field requested", preview.Rows[3].Requested, `{"source_key":"PROJ-1","field_id":"customfield_1"}`)
	fieldEffect := preview.Rows[3].Effect.(JiraPlanFieldEffect)
	assertWire("field effect", fieldEffect, fmt.Sprintf(`{"source":"raw","kind":"string","bytes":5,"sha256":"80270e39ab5a8e50f949b1287e9432cef723e843964056ef04e1f185a4d3b301","prepared_bytes":%d,"prepared_sha256":%q}`, fieldEffect.PreparedBytes, fieldEffect.PreparedSHA256))
	fieldQualified := preview.Rows[3].Qualified.(JiraPlanFieldQualified)
	assertWire("field qualified", fieldQualified, fmt.Sprintf(`{"source_id":"10001","project":"PROJ","updated_sha256":"40d7a64322be295b56be1cb9f80cef0b7d3d5eec3ce888388b5f7a3bef664fab","catalog_count":%d,"catalog_sha256":%q}`, fieldQualified.CatalogCount, fieldQualified.CatalogSHA256))
	applyPort := jiraPlanAllFamilyFixture(true)
	result, err := runJiraPlanAllFamilies(t, "apply", preview.ProposalHash, applyPort)
	if err != nil || result.Status != "applied" || !result.Complete || applyPort.link.writes != 1 || len(applyPort.labels.writes) != 1 || applyPort.comment.writeCalls != 1 || applyPort.field.writeCalls != 1 {
		t.Fatalf("result=%+v err=%v writes=%d/%d/%d/%d", result, err, applyPort.link.writes, len(applyPort.labels.writes), applyPort.comment.writeCalls, applyPort.field.writeCalls)
	}
	commentQualified, ok := result.Rows[2].Qualified.(JiraPlanCommentQualified)
	commentEffect, effectOK := result.Rows[2].Effect.(JiraPlanCommentEffect)
	if !ok || !effectOK || commentQualified.BaselineCount != 0 || commentEffect.SatisfactionPolicy != "exact_body_present" {
		t.Fatalf("comment qualified/effect=%+v/%+v", result.Rows[2].Qualified, result.Rows[2].Effect)
	}
}

func TestJiraPlanCanonicalPreflightUsesMovedQualifiedKeysForEveryFamily(t *testing.T) {
	port := jiraPlanAllFamilyFixture(false)
	port.link.left.Key, port.link.left.Project = "MOVED-11", "MOVED"
	port.link.right.Key, port.link.right.Project = "NEXT-22", "NEXT"
	port.linkAliases = map[string]string{"APP-1": "MOVED-11", "OPS-2": "NEXT-22"}
	port.labels.snapshots[0].Key, port.labels.snapshots[0].Project = "LABEL-31", "LABEL"
	port.comment.issueFn = func(int) (domain.JiraGuardedCommentIssue, error) {
		return domain.JiraGuardedCommentIssue{ID: "40", Key: "COMMENT-41", Project: "COMMENT", Updated: "2026-08-23T10:00:00Z", Complete: true}, nil
	}
	port.field.issues[0].Key, port.field.issues[0].Project = "FIELD-51", "FIELD"
	authorizer := &jiraPlanRecordingAuthorizer{}
	document, err := ReadJiraPlanDocument(writeJiraPlanTestFile(t, "2,link,APP-1,OPS-2,Blocks,,\n2,label_add,OPS-1,,,,alpha\n2,comment,PROJ-1,,,,body\n2,field,PROJ-1,,,customfield_1,\"\"\"new\"\"\"\n"))
	if err != nil || BindJiraPlanDocument(document, "preview") != nil {
		t.Fatalf("document err=%v", err)
	}
	result, err := (&JiraService{tr: port, baseURL: "https://jira.example.test", writeAuthorizer: authorizer}).RunJiraPlan(t.Context(), document, JiraPlanRunOpts{
		Mode: "preview", AllowOps: []string{"link", "label_add", "comment", "field"}, AllowFields: []string{"customfield_1"}, AllowLinkTypes: []string{"Blocks"},
	})
	if err != nil || len(authorizer.requests) != 4 || result.Authorization == nil || result.Authorization.RequestCount != 4 {
		t.Fatalf("result=%+v err=%v requests=%+v", result, err, authorizer.requests)
	}
	wants := []struct {
		verb domain.WriteVerb
		keys []string
	}{
		{domain.WriteVerbUpdate, []string{"MOVED-11", "NEXT-22"}},
		{domain.WriteVerbUpdate, []string{"LABEL-31"}},
		{domain.WriteVerbComment, []string{"COMMENT-41"}},
		{domain.WriteVerbUpdate, []string{"FIELD-51"}},
	}
	for i, want := range wants {
		request := authorizer.requests[i]
		if len(request.Verbs) != 1 || request.Verbs[0] != want.verb || len(request.Targets) != len(want.keys) {
			t.Fatalf("request[%d]=%+v", i, request)
		}
		for j, key := range want.keys {
			if request.Targets[j].Key != key || request.Targets[j].ID != "" {
				t.Fatalf("request[%d].targets=%+v", i, request.Targets)
			}
		}
	}
	linkRequested := result.Rows[0].Requested.(JiraPlanLinkRequested)
	if linkRequested.SourceKey != "APP-1" || linkRequested.TargetKey != "OPS-2" {
		t.Fatalf("requested keys changed: %+v", linkRequested)
	}
}

func TestRunJiraPlanExecutionFailureControlIsClosed(t *testing.T) {
	const before = "2026-08-23T10:00:00Z"
	body := "2,label_add,OPS-1,,,,alpha\n2,label_add,OPS-1,,,,beta\n"
	previewStore := &guardedLabelStore{snapshots: []domain.JiraGuardedLabelSnapshot{guardedLabelSnapshot(before, "old"), guardedLabelSnapshot(before, "old")}}
	document := func(t *testing.T, mode string) *JiraPlanDocument {
		doc, err := ReadJiraPlanDocument(writeJiraPlanTestFile(t, body))
		if err != nil || BindJiraPlanDocument(doc, mode) != nil {
			t.Fatalf("document err=%v", err)
		}
		return doc
	}
	preview, err := (&JiraService{tr: previewStore, baseURL: guardedLabelTestBackend}).RunJiraPlan(t.Context(), document(t, "preview"), JiraPlanRunOpts{Mode: "preview", AllowOps: []string{"label_add"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name            string
		continueOnError bool
		writeErr        error
		wantWrites      int
		wantStatus      string
		wantSecond      string
		wantComplete    bool
	}{
		{name: "fail fast", writeErr: &guardedLabelStatusError{status: 403}, wantWrites: 1, wantStatus: "not_applied", wantSecond: "skipped"},
		{name: "continue", continueOnError: true, writeErr: &guardedLabelStatusError{status: 403}, wantWrites: 2, wantStatus: "not_applied", wantSecond: "not_applied", wantComplete: true},
		{name: "ambiguity always stops", continueOnError: true, writeErr: errors.New("ambiguous transport"), wantWrites: 1, wantStatus: "outcome_unknown", wantSecond: "skipped"},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshots := []domain.JiraGuardedLabelSnapshot{guardedLabelSnapshot(before, "old"), guardedLabelSnapshot(before, "old"), guardedLabelSnapshot(before, "old"), guardedLabelSnapshot(before, "old"), guardedLabelSnapshot(before, "old")}
			store := &guardedLabelStore{snapshots: snapshots, writeErr: test.writeErr}
			result, err := (&JiraService{tr: store, baseURL: guardedLabelTestBackend}).RunJiraPlan(t.Context(), document(t, "apply"), JiraPlanRunOpts{Mode: "apply", ExpectedProposalHash: preview.ProposalHash, AllowOps: []string{"label_add"}, ContinueOnError: test.continueOnError})
			if !errors.Is(err, domain.ErrCheckFailed) || result.Status != test.wantStatus || result.Complete != test.wantComplete || len(store.writes) != test.wantWrites || result.Rows[1].Status != test.wantSecond {
				t.Fatalf("result=%+v err=%v writes=%d", result, err, len(store.writes))
			}
		})
	}
}

func TestJiraPlanGlobalQualificationBarrierStopsEveryWriter(t *testing.T) {
	const before = "2026-08-23T10:00:00Z"
	document, err := ReadJiraPlanDocument(writeJiraPlanTestFile(t, "2,label_add,OPS-1,,,,alpha\n2,label_add,OPS-1,,,,beta\n"))
	if err != nil || BindJiraPlanDocument(document, "apply") != nil {
		t.Fatalf("document err=%v", err)
	}
	store := &guardedLabelStore{snapshots: []domain.JiraGuardedLabelSnapshot{guardedLabelSnapshot(before, "old")}, readErr: errors.New("qualification failed"), readErrAt: 2}
	result, runErr := (&JiraService{tr: store, baseURL: guardedLabelTestBackend}).RunJiraPlan(t.Context(), document, JiraPlanRunOpts{Mode: "apply", ExpectedProposalHash: strings.Repeat("0", 64), AllowOps: []string{"label_add"}})
	if !errors.Is(runErr, domain.ErrCheckFailed) || result.ProposalHash != "" || len(store.writes) != 0 || result.Rows[0].Status != "skipped" || result.Rows[1].Reason != "qualification_failed" {
		t.Fatalf("result=%+v err=%v writes=%d", result, runErr, len(store.writes))
	}
}

func TestJiraPlanExpiredLaterRowAndParentExhaustionNeverDispatch(t *testing.T) {
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	execution, err := newJiraGuardedExecution(ctx, nil, jiraGuardedLabelMaxRequests, jiraGuardedLabelMaxResponseBytes, jiraGuardedLabelDeadline)
	if err != nil {
		t.Fatal(err)
	}
	defer execution.Close()
	store := &guardedLabelStore{}
	prepared := &jiraPlanPreparedRow{execution: execution, label: &jiraGuardedLabelPrepared{result: &JiraGuardedLabelResult{}}, labelOpts: JiraGuardedLabelOpts{Apply: true}}
	row := &JiraPlanResultRow{Row: 3, Family: "label"}
	if err := (&JiraService{tr: store}).executeJiraPlanRow(row, prepared); !errors.Is(err, domain.ErrCheckFailed) || row.Reason != "deadline_expired" || len(store.writes) != 0 {
		t.Fatalf("row=%+v err=%v writes=%d", row, err, len(store.writes))
	}

	port := &jiraPlanLabelPort{updated: "2026-08-23T12:00:00Z", charge: true, chargesPerRead: 2}
	result, runErr := runJiraPlanLabel(t, "preview", "", port)
	if !errors.Is(runErr, domain.ErrCheckFailed) || result.Status != "blocked" || port.writes != 0 || result.Usage.Requests != 1 {
		t.Fatalf("result=%+v err=%v writes=%d", result, runErr, port.writes)
	}
}
