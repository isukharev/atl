package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/agenteval"
	"github.com/isukharev/atl/internal/app"
)

func startAgentEvalFixture(t *testing.T, scenario string) *agenteval.MockBackend {
	t.Helper()
	path := filepath.Join("..", "..", "benchmarks", "agent-eval", scenario, "fixture.json")
	return startAgentEvalFixturePath(t, path)
}

func startAgentEvalFixturePath(t *testing.T, path string) *agenteval.MockBackend {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	fixture, decodeErr := agenteval.DecodeMockFixture(file)
	_ = file.Close()
	if decodeErr != nil {
		t.Fatal(decodeErr)
	}
	backend, err := agenteval.StartMockBackend(fixture)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(backend.Close)
	return backend
}

func TestSyntheticConfluencePlanMutationBoundaries(t *testing.T) {
	caseRoot := filepath.Join("..", "..", "benchmarks", "agent-eval", "confluence-plan-mutation")
	source := filepath.Join(caseRoot, "workspace", "mirror")
	for _, test := range []struct {
		name, fixture, status, entryStatus string
		applyExit                          int
		readOnly                           bool
		methods                            map[string]int
	}{
		{name: "preview", fixture: "fixture.preview.json", status: "would_apply", entryStatus: "would_apply", applyExit: -1, readOnly: true, methods: map[string]int{"GET": 1}},
		{name: "apply", fixture: "fixture.apply.json", status: "applied", entryStatus: "applied", applyExit: exitOK, methods: map[string]int{"GET": 3, "PUT": 1}},
		{name: "conflict", fixture: "fixture.conflict.json", status: "partial", entryStatus: "failed", applyExit: exitVersionConfl, methods: map[string]int{"GET": 3, "PUT": 1}},
		{name: "unknown", fixture: "fixture.unknown.json", status: "partial", entryStatus: "unknown", applyExit: exitCheckFailed, methods: map[string]int{"GET": 3, "PUT": 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := startAgentEvalFixturePath(t, filepath.Join(caseRoot, test.fixture))
			root := filepath.Join(t.TempDir(), "mirror")
			if err := os.CopyFS(root, os.DirFS(source)); err != nil {
				t.Fatal(err)
			}
			plan := filepath.Join(t.TempDir(), "plan.json")
			createArgs := []string{"conf", "plan", "create", root, "--out", plan}
			if test.readOnly {
				createArgs = append([]string{"--read-only"}, createArgs...)
			}
			created, code := runCLI(t, backend.Environment(), createArgs...)
			if code != exitOK {
				t.Fatalf("create exit=%d output=%s", code, created)
			}
			var createResult app.ConfluencePlanCreateResult
			if err := json.Unmarshal([]byte(created), &createResult); err != nil || createResult.ProposalHash == "" || createResult.OperationCount != 1 {
				t.Fatalf("create result=%+v err=%v", createResult, err)
			}
			previewArgs := []string{"conf", "plan", "preview", plan}
			if test.readOnly {
				previewArgs = append([]string{"--read-only"}, previewArgs...)
			}
			previewed, code := runCLI(t, backend.Environment(), previewArgs...)
			if code != exitOK {
				t.Fatalf("preview exit=%d output=%s", code, previewed)
			}
			out := previewed
			if test.applyExit >= 0 {
				out, code = runCLI(t, backend.Environment(), "conf", "plan", "apply", plan, "--expected-proposal-hash", createResult.ProposalHash, "--confirm", "APPLY")
				if code != test.applyExit {
					t.Fatalf("apply exit=%d want=%d output=%s", code, test.applyExit, out)
				}
			}
			var result app.ConfluencePlanApplyResult
			if err := json.Unmarshal([]byte(out), &result); err != nil || result.Status != test.status || len(result.Entries) != 1 || result.Entries[0].Status != test.entryStatus {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			methods, unexpected, _ := backend.Summary()
			if unexpected != 0 || !equalIntMap(methods, test.methods) {
				t.Fatalf("methods=%v want=%v unexpected=%d", methods, test.methods, unexpected)
			}
		})
	}
}

func equalIntMap(left, right map[string]int) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func TestSyntheticConfluenceMirrorReviewIsExactAndOffline(t *testing.T) {
	backend := startAgentEvalFixture(t, "confluence-mirror-review")
	source := filepath.Join("..", "..", "benchmarks", "agent-eval", "confluence-mirror-review", "workspace", "mirror")
	root := filepath.Join(t.TempDir(), "mirror")
	if err := os.CopyFS(root, os.DirFS(source)); err != nil {
		t.Fatal(err)
	}

	out, code := runCLI(t, backend.Environment(), "--read-only", "conf", "diff", root, "--into", root)
	if code != exitCheckFailed {
		t.Fatalf("exit=%d output=%s", code, out)
	}
	var result app.ConfluenceDiffResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if result.Complete || result.Summary.Total != 4 || result.Summary.Modified != 2 || result.Summary.Unchanged != 1 || result.Summary.BaselineMismatch != 1 {
		t.Fatalf("result=%+v", result)
	}
	want := map[string]struct {
		state    string
		semantic bool
		byteOnly bool
	}{
		"Baseline drift":    {state: "baseline_mismatch"},
		"Policy formatting": {state: "modified", byteOnly: true},
		"Rollout plan":      {state: "modified", semantic: true},
		"Stable runbook":    {state: "unchanged"},
	}
	for _, page := range result.Pages {
		expected, ok := want[page.Title]
		if !ok || page.State != expected.state || page.SemanticChanged != expected.semantic || page.ByteOnly != expected.byteOnly {
			t.Fatalf("unexpected page classification: %+v", page)
		}
		delete(want, page.Title)
	}
	if len(want) != 0 {
		t.Fatalf("missing page classifications: %+v", want)
	}
	textOut, textCode := runCLI(t, backend.Environment(), "--read-only", "conf", "diff", root, "--into", root, "-o", "text")
	if textCode != exitCheckFailed {
		t.Fatalf("text exit=%d output=%s", textCode, textOut)
	}
	for _, want := range []string{
		"| baseline_mismatch | Baseline drift | OPS/baseline-drift.csf | n/a | 0 |",
		"| modified | Policy formatting | OPS/policy-format.csf | byte-only | 0 |",
		"| modified | Rollout plan | OPS/rollout-plan.csf | semantic | 1 |",
		"| unchanged | Stable runbook | OPS/stable-runbook.csf | none | 0 |",
	} {
		if !strings.Contains(textOut, want) {
			t.Fatalf("text output=%s, want row=%s", textOut, want)
		}
	}
	if strings.Contains(textOut, root) {
		t.Fatalf("text output retained absolute mirror root: %s", textOut)
	}
	methods, unexpected, duplicates := backend.Summary()
	if len(methods) != 0 || unexpected != 0 || duplicates != 0 {
		t.Fatalf("offline diff made backend requests: methods=%v unexpected=%d duplicates=%d", methods, unexpected, duplicates)
	}
}

func TestSyntheticConfluencePageEvidenceRouteIsBoundedAndSelectsApprovedOccurrence(t *testing.T) {
	backend := startAgentEvalFixture(t, "confluence-page-evidence")
	env := backend.Environment()
	reference := "/wiki/pages/viewpage.action?pageId=7001"

	resolved, code := runCLI(t, env, "-o", "id", "conf", "page", "resolve", reference)
	if code != exitOK || strings.TrimSpace(resolved) != "7001" {
		t.Fatalf("resolve exit=%d output=%s", code, resolved)
	}
	outline, code := runCLI(t, env, "conf", "page", "outline", reference)
	if code != exitOK || strings.Count(outline, `"title": "Decision"`) != 2 {
		t.Fatalf("outline exit=%d output=%s", code, outline)
	}
	section, code := runCLI(t, env, "-o", "text", "conf", "page", "section", reference, "--heading", "Decision", "--occurrence", "2")
	if code != exitOK || !strings.Contains(section, "Reliability") || !strings.Contains(section, "95 percent") || strings.Contains(section, "Draft only") || strings.Contains(section, "Historical discussion") {
		t.Fatalf("section exit=%d output=%s", code, section)
	}
	methods, unexpected, duplicates := backend.Summary()
	if methods["GET"] != 2 || len(methods) != 1 || unexpected != 0 || duplicates != 1 {
		t.Fatalf("methods=%v unexpected=%d duplicates=%d", methods, unexpected, duplicates)
	}
}

func TestSyntheticConfluenceDecisionBriefRouteUsesOnlyBoundedSections(t *testing.T) {
	backend := startAgentEvalFixture(t, "confluence-decision-brief")
	env := backend.Environment()
	type source struct{ id, heading string }
	sources := []source{{"7101", "Objectives"}, {"7102", "Risks"}, {"7103", "Decision"}}
	var combined strings.Builder
	for _, source := range sources {
		reference := "/wiki/pages/viewpage.action?pageId=" + source.id
		resolved, code := runCLI(t, env, "-o", "id", "conf", "page", "resolve", reference)
		if code != exitOK || strings.TrimSpace(resolved) != source.id {
			t.Fatalf("resolve %s exit=%d output=%s", source.id, code, resolved)
		}
		outline, code := runCLI(t, env, "conf", "page", "outline", reference)
		if code != exitOK || !strings.Contains(outline, `"title": "`+source.heading+`"`) {
			t.Fatalf("outline %s exit=%d output=%s", source.id, code, outline)
		}
		section, code := runCLI(t, env, "-o", "text", "conf", "page", "section", reference, "--heading", source.heading)
		if code != exitOK {
			t.Fatalf("section %s exit=%d output=%s", source.id, code, section)
		}
		combined.WriteString(section)
	}
	for _, evidence := range []string{"40 percent", "Team Alpha", "Dependency contract unconfirmed", "Capacity test pending", "Team Delta", "supersedes"} {
		if !strings.Contains(combined.String(), evidence) {
			t.Errorf("bounded sections omit %q: %s", evidence, combined.String())
		}
	}
	for _, excluded := range []string{"Historical context", "Monitoring ownership resolved", "Full rollout was rejected"} {
		if strings.Contains(combined.String(), excluded) {
			t.Errorf("bounded sections included %q", excluded)
		}
	}
	methods, unexpected, duplicates := backend.Summary()
	if methods["GET"] != 6 || len(methods) != 1 || unexpected != 0 || duplicates != 3 {
		t.Fatalf("methods=%v unexpected=%d duplicates=%d", methods, unexpected, duplicates)
	}
}
