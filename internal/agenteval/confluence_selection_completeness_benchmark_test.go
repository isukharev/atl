package agenteval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	confluenceSelectionPrimaryDirectory = "confluence-selection-completeness"
	confluenceSelectionHoldoutDirectory = "confluence-selection-completeness-holdout"
	confluenceSelectionPrimaryQuery     = `space = "DEMO" AND type = page ORDER BY title ASC`
	confluenceSelectionHoldoutQuery     = `space = "ARCHIVE" AND type = page`
	confluenceSelectionCapReason        = "selection truncated at 1000 pages by the safety cap"
	confluenceSelectionHoldoutReason    = "backend reported 5 total matches but only 2 were reachable"
	confluenceSelectionCapWarning       = "warning: selection truncated at 1000 pages (safety cap) — the rest was NOT mirrored; narrow the query or pull subsets\n"
)

func TestRepositoryConfluenceSelectionCompletenessFixturesDriveProviderOracles(t *testing.T) {
	t.Run("safety capped pull", func(t *testing.T) {
		root := confluenceSelectionBenchmarkRoot(confluenceSelectionPrimaryDirectory)
		fixture := loadRepositoryMockFixture(t, filepath.Join(root, "fixture.json"))
		assertConfluenceSelectionPrimaryTopology(t, fixture)

		result, summary, mirrorFiles := runConfluenceSelectionPullCLI(t, root, fixture, confluenceSelectionCapWarning)
		if len(result.Pages) != 1000 || !result.Truncated || result.TruncatedAt != 1000 {
			t.Fatalf("pull result did not preserve the cap: pages=%d truncated=%t truncated_at=%d", len(result.Pages), result.Truncated, result.TruncatedAt)
		}
		warningObserved := result.Truncated && result.TruncatedAt == 1000
		if !warningObserved {
			t.Fatalf("pull result does not require the CLI truncation warning: %+v", result)
		}
		if mirrorFiles.native != 1000 || mirrorFiles.metadata != 1000 || mirrorFiles.derived != 1000 {
			t.Fatalf("contained selected-binary mirror files drifted: %+v", mirrorFiles)
		}
		final := confluenceSelectionBenchmarkFinal(t, confluenceSelectionFinalInput{
			operation: "pull", query: confluenceSelectionPrimaryQuery, observedCount: len(result.Pages),
			complete: !result.Truncated, truncated: result.Truncated, truncatedAt: result.TruncatedAt,
			partialReason: confluenceSelectionCapReason, warningObserved: warningObserved,
			recommendedAction: "narrow-or-partition", localMirrorWritten: len(result.Pages) > 0,
		})
		assertConfluenceSelectionProviderOracles(t, root, final, summary.HTTPMethods, summary.UnexpectedRequests)
	})

	t.Run("unreachable backend total", func(t *testing.T) {
		root := confluenceSelectionBenchmarkRoot(confluenceSelectionHoldoutDirectory)
		fixtureBytes, err := os.ReadFile(filepath.Join(root, "fixture.json"))
		if err != nil {
			t.Fatal(err)
		}
		if !json.Valid(fixtureBytes) {
			t.Fatal("holdout fixture is not syntactically valid JSON")
		}
		fixture := loadRepositoryMockFixture(t, filepath.Join(root, "fixture.json"))
		if len(fixture.Routes) != 1 {
			t.Fatalf("holdout routes=%d want=1", len(fixture.Routes))
		}
		result, summary := runConfluenceSelectionSearchCLI(t, root, fixture)
		if result.Query != confluenceSelectionHoldoutQuery || result.Count != 2 || result.Complete ||
			!result.Truncated || result.NextCursor != nil || result.PartialReason != confluenceSelectionHoldoutReason {
			t.Fatalf("qualified holdout result drifted: %+v", result)
		}
		final := confluenceSelectionBenchmarkFinal(t, confluenceSelectionFinalInput{
			operation: "search", query: result.Query, observedCount: result.Count,
			complete: result.Complete, truncated: result.Truncated, partialReason: result.PartialReason,
			recommendedAction: "refine-or-investigate",
		})
		assertConfluenceSelectionProviderOracles(t, root, final, summary.HTTPMethods, summary.UnexpectedRequests)
	})
}

func TestRepositoryConfluenceSelectionCompletenessFixtureMutationsFailClosed(t *testing.T) {
	t.Run("empty cap probe", func(t *testing.T) {
		root := confluenceSelectionBenchmarkRoot(confluenceSelectionPrimaryDirectory)
		fixture := loadRepositoryMockFixture(t, filepath.Join(root, "fixture.json"))
		probeFound := false
		for index := range fixture.Routes {
			route := &fixture.Routes[index]
			if route.Path == "/wiki/rest/api/search" && route.QueryEquals["start"] == "1000" && route.QueryEquals["limit"] == "1" {
				route.Body = json.RawMessage(`{"results":[],"size":0,"_links":{}}`)
				probeFound = true
			}
		}
		if !probeFound {
			t.Fatal("primary fixture probe route not found")
		}
		result, summary, _ := runConfluenceSelectionPullCLI(t, root, fixture, "")
		if len(result.Pages) != 1000 || result.Truncated || result.TruncatedAt != 0 {
			t.Fatalf("empty terminal probe did not clear cap/warning: pages=%d result=%+v", len(result.Pages), result)
		}
		methods, unexpected := summary.HTTPMethods, summary.UnexpectedRequests
		final := confluenceSelectionBenchmarkFinal(t, confluenceSelectionFinalInput{
			operation: "pull", query: confluenceSelectionPrimaryQuery, observedCount: len(result.Pages),
			complete: true, recommendedAction: "none", localMirrorWritten: true,
		})
		for _, provider := range []string{"codex", "claude"} {
			spec := loadRepositoryRunSpec(t, filepath.Join(root, "run.cli."+provider+".json"))
			checks := confluenceSelectionEvaluate(t, spec, final, methods, unexpected, 1)
			for _, name := range []string{
				"complete_correct", "truncated_correct", "truncated_at_correct", "partial_reason_correct",
				"warning_observed", "absence_claim_rejected", "unobserved_possible", "remediation_correct",
			} {
				if checks[name] {
					t.Fatalf("%s empty-probe mutation passed %q", provider, name)
				}
			}
		}
	})

	t.Run("reported total becomes reachable", func(t *testing.T) {
		root := confluenceSelectionBenchmarkRoot(confluenceSelectionHoldoutDirectory)
		fixture := loadRepositoryMockFixture(t, filepath.Join(root, "fixture.json"))
		var body map[string]any
		if err := decodeJSONDocument(fixture.Routes[0].Body, &body); err != nil {
			t.Fatal(err)
		}
		body["totalCount"] = json.Number("2")
		mutated, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		fixture.Routes[0].Body = mutated
		result, summary := runConfluenceSelectionSearchCLI(t, root, fixture)
		if !result.Complete || result.Truncated || result.PartialReason != "" || result.NextCursor != nil {
			t.Fatalf("reachable-total mutation did not become complete: %+v", result)
		}
		methods, unexpected := summary.HTTPMethods, summary.UnexpectedRequests
		final := confluenceSelectionBenchmarkFinal(t, confluenceSelectionFinalInput{
			operation: "search", query: result.Query, observedCount: result.Count,
			complete: true, recommendedAction: "none",
		})
		for _, provider := range []string{"codex", "claude"} {
			spec := loadRepositoryRunSpec(t, filepath.Join(root, "run.cli."+provider+".json"))
			checks := confluenceSelectionEvaluate(t, spec, final, methods, unexpected, 1)
			for _, name := range []string{"complete_correct", "truncated_correct", "partial_reason_correct", "absence_claim_rejected", "unobserved_possible", "remediation_correct"} {
				if checks[name] {
					t.Fatalf("%s reachable-total mutation passed %q", provider, name)
				}
			}
		}
	})
}

func TestRepositoryConfluenceSelectionCompletenessSamplingAndRouteIdentity(t *testing.T) {
	pair := loadRepositorySamplingPairContract(t, confluenceSelectionPrimaryDirectory)
	primaryRoot, holdoutRoot := pair.Primary.Root, pair.Holdout.Root
	if pair.Primary.Scenario.TaskClass != "confluence/selection-completeness" {
		t.Fatalf("primary scenario task class drifted: %+v", pair.Primary.Scenario)
	}
	primarySchema, err := os.ReadFile(filepath.Join(primaryRoot, "response-schema.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	holdoutSchema, err := os.ReadFile(filepath.Join(holdoutRoot, "response-schema.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(primarySchema, holdoutSchema) {
		t.Fatal("primary and holdout response schemas are not byte-identical")
	}
	primaryFixture, err := os.ReadFile(filepath.Join(primaryRoot, "fixture.json"))
	if err != nil {
		t.Fatal(err)
	}
	holdoutFixture, err := os.ReadFile(filepath.Join(holdoutRoot, "fixture.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(primaryFixture, holdoutFixture) {
		t.Fatal("holdout fixture is not distinct from the primary fixture")
	}

	for _, provider := range []string{"codex", "claude"} {
		runFile := "run.cli." + provider + ".json"
		primary, holdout := pair.Primary.Runs[runFile], pair.Holdout.Runs[runFile]
		wantProvider, wantModel := "codex", "gpt-5.6-luna"
		if provider == "claude" {
			wantProvider, wantModel = "claude-code", "claude-opus-4-8"
		}
		if primary.Provider != wantProvider || primary.Model != wantModel || primary.Reasoning != "high" ||
			holdout.Provider != wantProvider || holdout.Model != wantModel || holdout.Reasoning != "high" ||
			primary.Variant != "confluence-selection-completeness-v1" ||
			primary.EffectiveCategory() != BenchmarkCategorySurfaceNative ||
			primary.EffectiveSurface() != SurfaceCLISkill {
			t.Fatalf("%s exact paired cohort drifted: primary=%+v holdout=%+v", provider, primary, holdout)
		}
		primaryPrompt, err := os.ReadFile(filepath.Join(primaryRoot, primary.PromptFile))
		if err != nil {
			t.Fatal(err)
		}
		holdoutPrompt, err := os.ReadFile(filepath.Join(holdoutRoot, holdout.PromptFile))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(primaryPrompt, holdoutPrompt) {
			t.Fatalf("%s holdout prompt is not distinct", provider)
		}
		for name, prompt := range map[string][]byte{"primary": primaryPrompt, "holdout": holdoutPrompt} {
			if !bytes.Contains(prompt, []byte("`atl:confluence` skill")) {
				t.Fatalf("%s %s prompt does not bind the exact named skill", provider, name)
			}
		}
		assertConfluenceSelectionCommandPolicy(t, primaryRoot, primary, confluenceSelectionPrimaryQuery, "pull")
		assertConfluenceSelectionCommandPolicy(t, holdoutRoot, holdout, confluenceSelectionHoldoutQuery, "search")
		for _, test := range []struct {
			root string
			spec RunSpec
		}{{primaryRoot, primary}, {holdoutRoot, holdout}} {
			checks := confluenceSelectionEvaluate(t, test.spec, []byte(`{}`), map[string]int{}, 0, 2)
			if checks["used_atl_once"] {
				t.Fatalf("%s second CLI invocation passed used_atl_once", test.spec.Provider)
			}
		}
		if primary.Provider == "claude-code" {
			checks := confluenceSelectionEvaluateWithSkillMap(
				t, primary, []byte(`{}`), map[string]int{}, 0, 1,
				map[string]int{"atl:jira": 1},
			)
			if checks["used_skill"] {
				t.Fatal("Claude wrong named Skill event passed used_skill")
			}
		}
	}
}

type confluenceSelectionFinalInput struct {
	operation          string
	query              string
	observedCount      int
	complete           bool
	truncated          bool
	truncatedAt        int
	partialReason      string
	warningObserved    bool
	recommendedAction  string
	localMirrorWritten bool
	remoteWrites       bool
}

func confluenceSelectionBenchmarkFinal(t *testing.T, input confluenceSelectionFinalInput) []byte {
	t.Helper()
	var truncatedAt any
	if input.truncatedAt != 0 {
		truncatedAt = input.truncatedAt
	}
	final := map[string]any{
		"operation": input.operation, "query": input.query, "observed_count": input.observedCount,
		"complete": input.complete, "truncated": input.truncated, "truncated_at": truncatedAt,
		"continuation_cursor": nil, "partial_reason": input.partialReason,
		"warning_observed": input.warningObserved, "absence_claim_supported": input.complete,
		"unobserved_matches_possible": !input.complete, "recommended_action": input.recommendedAction,
		"local_mirror_written": input.localMirrorWritten, "remote_writes_performed": input.remoteWrites,
		"selection_only": true,
	}
	encoded, err := json.Marshal(final)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func confluenceSelectionBenchmarkRoot(directory string) string {
	return filepath.Join("..", "..", "benchmarks", "agent-eval", directory)
}

type confluenceSelectionPullWire struct {
	Root  string `json:"root"`
	Pages []struct {
		ID      string `json:"id"`
		Title   string `json:"title"`
		Path    string `json:"path"`
		Version int    `json:"version"`
		Assets  int    `json:"assets"`
	} `json:"pages"`
	Truncated   bool `json:"truncated,omitempty"`
	TruncatedAt int  `json:"truncated_at,omitempty"`
}

type confluenceSelectionSearchWire struct {
	SchemaVersion int    `json:"schema_version"`
	Query         string `json:"query"`
	Results       []struct {
		ID      string `json:"id"`
		Title   string `json:"title"`
		Space   string `json:"space"`
		Version int    `json:"version"`
		Updated string `json:"updated,omitempty"`
		Parent  string `json:"parent,omitempty"`
		Excerpt string `json:"excerpt,omitempty"`
		URL     string `json:"url,omitempty"`
	} `json:"results"`
	Count         int     `json:"count"`
	Complete      bool    `json:"complete"`
	Truncated     bool    `json:"truncated"`
	PartialReason string  `json:"partial_reason,omitempty"`
	NextCursor    *string `json:"next_cursor"`
}

func decodeConfluenceSelectionSearchWire(data []byte) (confluenceSelectionSearchWire, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return confluenceSelectionSearchWire{}, fmt.Errorf("decode Confluence search members: %w", err)
	}
	if root == nil {
		return confluenceSelectionSearchWire{}, fmt.Errorf("Confluence search wire must be an object")
	}
	if err := requireConfluenceSelectionMembers(root, "Confluence search", []string{
		"schema_version", "query", "results", "count", "complete", "truncated", "next_cursor",
	}, []string{"partial_reason"}); err != nil {
		return confluenceSelectionSearchWire{}, err
	}
	if err := rejectNullConfluenceSelectionMembers(root, "Confluence search", []string{
		"schema_version", "query", "results", "count", "complete", "truncated",
	}); err != nil {
		return confluenceSelectionSearchWire{}, err
	}
	if err := requireNonemptyOptionalConfluenceSelectionStrings(root, "Confluence search", []string{
		"partial_reason",
	}); err != nil {
		return confluenceSelectionSearchWire{}, err
	}
	var results []map[string]json.RawMessage
	if err := json.Unmarshal(root["results"], &results); err != nil {
		return confluenceSelectionSearchWire{}, fmt.Errorf("decode Confluence search results: %w", err)
	}
	if results == nil {
		return confluenceSelectionSearchWire{}, fmt.Errorf("Confluence search results must be an array")
	}
	for index, result := range results {
		if err := requireConfluenceSelectionMembers(result, fmt.Sprintf("Confluence search result[%d]", index),
			[]string{"id", "title", "space", "version"},
			[]string{"updated", "parent", "excerpt", "url"}); err != nil {
			return confluenceSelectionSearchWire{}, err
		}
		if err := rejectNullConfluenceSelectionMembers(result, fmt.Sprintf("Confluence search result[%d]", index),
			[]string{"id", "title", "space", "version"}); err != nil {
			return confluenceSelectionSearchWire{}, err
		}
		if err := requireNonemptyOptionalConfluenceSelectionStrings(
			result,
			fmt.Sprintf("Confluence search result[%d]", index),
			[]string{"updated", "parent", "excerpt", "url"},
		); err != nil {
			return confluenceSelectionSearchWire{}, err
		}
	}
	var wire confluenceSelectionSearchWire
	if err := decodeStrict(bytes.NewReader(data), &wire); err != nil {
		return confluenceSelectionSearchWire{}, err
	}
	if wire.SchemaVersion != 1 || wire.Results == nil || wire.Count != len(wire.Results) {
		return confluenceSelectionSearchWire{}, fmt.Errorf(
			"Confluence search wire is inconsistent: schema=%d count=%d results=%d",
			wire.SchemaVersion, wire.Count, len(wire.Results),
		)
	}
	for index, result := range wire.Results {
		if result.ID == "" || result.Title == "" || result.Space == "" || result.Version < 1 {
			return confluenceSelectionSearchWire{}, fmt.Errorf("Confluence search result[%d] is incomplete", index)
		}
	}
	return wire, nil
}

func requireConfluenceSelectionMembers(
	document map[string]json.RawMessage,
	owner string,
	required []string,
	optional []string,
) error {
	allowed := make(map[string]struct{}, len(required)+len(optional))
	for _, name := range required {
		allowed[name] = struct{}{}
		if _, ok := document[name]; !ok {
			return fmt.Errorf("%s is missing required member %q", owner, name)
		}
	}
	for _, name := range optional {
		allowed[name] = struct{}{}
	}
	for name := range document {
		if _, ok := allowed[name]; !ok {
			return fmt.Errorf("%s contains unknown member %q", owner, name)
		}
	}
	return nil
}

func rejectNullConfluenceSelectionMembers(
	document map[string]json.RawMessage,
	owner string,
	members []string,
) error {
	for _, name := range members {
		if bytes.Equal(bytes.TrimSpace(document[name]), []byte("null")) {
			return fmt.Errorf("%s member %q must not be null", owner, name)
		}
	}
	return nil
}

func requireNonemptyOptionalConfluenceSelectionStrings(
	document map[string]json.RawMessage,
	owner string,
	members []string,
) error {
	for _, name := range members {
		raw, ok := document[name]
		if !ok {
			continue
		}
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return fmt.Errorf("%s optional member %q must not be null", owner, name)
		}
		var value string
		if err := json.Unmarshal(raw, &value); err != nil || value == "" {
			return fmt.Errorf("%s optional member %q must be a non-empty string", owner, name)
		}
	}
	return nil
}

func TestConfluenceSelectionSearchWireRequiresReleasedMembers(t *testing.T) {
	valid := []byte(`{"schema_version":1,"query":"type = page","results":[],"count":0,"complete":true,"truncated":false,"next_cursor":null}`)
	if _, err := decodeConfluenceSelectionSearchWire(valid); err != nil {
		t.Fatalf("valid Confluence search wire: %v", err)
	}
	for _, name := range []string{"schema_version", "results", "next_cursor"} {
		t.Run("missing "+name, func(t *testing.T) {
			var document map[string]json.RawMessage
			if err := json.Unmarshal(valid, &document); err != nil {
				t.Fatal(err)
			}
			delete(document, name)
			mutated, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeConfluenceSelectionSearchWire(mutated); err == nil {
				t.Fatalf("missing required member %q passed", name)
			}
		})
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(valid, &document); err != nil {
		t.Fatal(err)
	}
	document["complete"] = json.RawMessage("null")
	mutated, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeConfluenceSelectionSearchWire(mutated); err == nil {
		t.Fatal("null required boolean passed")
	}
	for name, raw := range map[string]json.RawMessage{
		"partial_reason": json.RawMessage("null"),
		"results":        json.RawMessage(`[{"id":"1","title":"Page","space":"SPACE","version":1,"excerpt":null}]`),
	} {
		t.Run("null optional "+name, func(t *testing.T) {
			var candidate map[string]json.RawMessage
			if err := json.Unmarshal(valid, &candidate); err != nil {
				t.Fatal(err)
			}
			candidate[name] = raw
			if name == "results" {
				candidate["count"] = json.RawMessage("1")
			}
			mutated, err := json.Marshal(candidate)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeConfluenceSelectionSearchWire(mutated); err == nil {
				t.Fatalf("null optional member in %q passed", name)
			}
		})
	}
}

type confluenceSelectionMirrorFiles struct {
	native   int
	derived  int
	metadata int
}

func runConfluenceSelectionPullCLI(
	t *testing.T,
	root string,
	fixture MockFixture,
	wantStderr string,
) (confluenceSelectionPullWire, SyntheticATLProcessSummary, confluenceSelectionMirrorFiles) {
	t.Helper()
	process := startConfluenceSelectionCLIProcess(t, prepareConfluenceSelectionPullFixture(t, fixture), root)
	mirrorRoot := filepath.Join(process.runtimeRoot, "selection-mirror")
	if _, err := os.Lstat(mirrorRoot); !os.IsNotExist(err) {
		t.Fatalf("selection mirror existed before the selected CLI: %v", err)
	}
	result, err := process.RunCLIJSON(t.Context(),
		"conf", "pull", "--cql", confluenceSelectionPrimaryQuery, "--into", "selection-mirror")
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || !bytes.Equal(result.Stderr, []byte(wantStderr)) {
		t.Fatalf("selected pull exit=%d stderr=%q", result.ExitCode, result.Stderr)
	}
	var wire confluenceSelectionPullWire
	if err := decodeStrict(bytes.NewReader(result.JSON), &wire); err != nil {
		t.Fatalf("decode selected Confluence pull wire: %v", err)
	}
	if wire.Root != "selection-mirror" {
		t.Fatalf("selected pull root=%q", wire.Root)
	}
	summary := assertConfluenceSelectionProcessSummary(t, process, 1011, "confluence_selection_pull")
	return wire, summary, inspectConfluenceSelectionMirror(t, process, mirrorRoot, wire)
}

func runConfluenceSelectionSearchCLI(
	t *testing.T,
	root string,
	fixture MockFixture,
) (confluenceSelectionSearchWire, SyntheticATLProcessSummary) {
	t.Helper()
	process := startConfluenceSelectionCLIProcess(t, prepareConfluenceSelectionSearchFixture(t, fixture), root)
	before, err := os.ReadDir(filepath.Join(process.runtimeRoot, "mirror"))
	if err != nil || len(before) != 0 {
		t.Fatalf("search runtime mirror before selected CLI: entries=%d err=%v", len(before), err)
	}
	result, err := process.RunCLIJSON(t.Context(),
		"conf", "search", "--cql", confluenceSelectionHoldoutQuery, "--limit", "25")
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || len(result.Stderr) != 0 {
		t.Fatalf("selected search exit=%d stderr=%q", result.ExitCode, result.Stderr)
	}
	wire, err := decodeConfluenceSelectionSearchWire(result.JSON)
	if err != nil {
		t.Fatalf("decode selected Confluence search wire: %v", err)
	}
	after, err := os.ReadDir(filepath.Join(process.runtimeRoot, "mirror"))
	if err != nil || len(after) != 0 {
		t.Fatalf("selected search changed its process mirror: entries=%d err=%v", len(after), err)
	}
	if _, err := os.Lstat(filepath.Join(process.runtimeRoot, "selection-mirror")); !os.IsNotExist(err) {
		t.Fatalf("selected search created a pull mirror: %v", err)
	}
	return wire, assertConfluenceSelectionProcessSummary(t, process, 1, "confluence_selection_search")
}

func startConfluenceSelectionCLIProcess(
	t *testing.T,
	fixture MockFixture,
	root string,
) *SyntheticATLProcess {
	t.Helper()
	spec := loadRepositoryRunSpec(t, filepath.Join(root, "run.cli.codex.json"))
	process, err := StartSyntheticATLProcess(t.Context(), SyntheticATLProcessConfig{
		Binary: repositorySyntheticATLBinary(t), Fixture: fixture,
		ScratchRoot: privateSyntheticATLScratch(t),
		CLIPolicy:   CLICommandPolicy{SchemaVersion: CLICommandPolicySchemaVersion, Rules: spec.AllowedCLICommands},
		Timeout:     2 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := process.Close(); err != nil {
			t.Errorf("close synthetic Confluence selection process: %v", err)
		}
	})
	return process
}

func assertConfluenceSelectionProcessSummary(
	t *testing.T,
	process *SyntheticATLProcess,
	getCount int,
	ruleName string,
) SyntheticATLProcessSummary {
	t.Helper()
	summary := process.Summary()
	if !process.RequestSequenceComplete() || !equalHTTPMethods(summary.HTTPMethods, map[string]int{"GET": getCount}) ||
		summary.UnexpectedRequests != 0 || summary.DuplicateRequests != 0 ||
		!equalHTTPMethods(summary.CLIInvocations, map[string]int{ruleName: 1}) || len(summary.MCPInvocations) != 0 {
		t.Fatalf("selected Confluence selection process drifted: summary=%+v sequence_complete=%t",
			summary, process.RequestSequenceComplete())
	}
	return summary
}

func inspectConfluenceSelectionMirror(
	t *testing.T,
	process *SyntheticATLProcess,
	mirrorRoot string,
	wire confluenceSelectionPullWire,
) confluenceSelectionMirrorFiles {
	t.Helper()
	canonicalRoot, err := filepath.EvalSymlinks(mirrorRoot)
	if err != nil {
		t.Fatalf("resolve selected pull mirror: %v", err)
	}
	inside, err := pathWithin(process.runtimeRoot, canonicalRoot)
	if err != nil || !inside {
		t.Fatalf("selected pull mirror escaped its runtime: path=%q err=%v", canonicalRoot, err)
	}
	seen := make(map[string]struct{}, len(wire.Pages))
	counts := confluenceSelectionMirrorFiles{}
	for _, page := range wire.Pages {
		if page.ID == "" || page.Title == "" || page.Version < 1 || page.Path == "" {
			t.Fatalf("selected pull page wire is incomplete: %+v", page)
		}
		if _, duplicate := seen[page.Path]; duplicate {
			t.Fatalf("selected pull repeated mirror path %q", page.Path)
		}
		seen[page.Path] = struct{}{}
		csfPath := filepath.Join(canonicalRoot, filepath.FromSlash(page.Path))
		inside, err := pathWithin(canonicalRoot, csfPath)
		if err != nil || !inside || filepath.Ext(csfPath) != ".csf" {
			t.Fatalf("selected pull exposed an uncontained native path %q: %v", page.Path, err)
		}
		for _, artifact := range []struct {
			path  string
			count *int
		}{
			{csfPath, &counts.native},
			{strings.TrimSuffix(csfPath, ".csf") + ".md", &counts.derived},
			{strings.TrimSuffix(csfPath, ".csf") + ".meta.json", &counts.metadata},
		} {
			if err := validateConfluenceSelectionMirrorArtifact(canonicalRoot, artifact.path); err != nil {
				t.Fatalf("selected pull artifact %q is unsafe: %v", artifact.path, err)
			}
			*artifact.count++
		}
	}
	return counts
}

func validateConfluenceSelectionMirrorArtifact(canonicalRoot, path string) error {
	inside, err := pathWithin(canonicalRoot, path)
	if err != nil {
		return fmt.Errorf("artifact path is not lexically contained: %w", err)
	}
	if !inside {
		return fmt.Errorf("artifact path is not lexically contained")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("artifact is not a regular file")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	inside, err = pathWithin(canonicalRoot, resolved)
	if err != nil {
		return fmt.Errorf("artifact resolves outside the mirror: %w", err)
	}
	if !inside {
		return fmt.Errorf("artifact resolves outside the mirror")
	}
	return nil
}

func TestConfluenceSelectionMirrorArtifactRejectsDescendantSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "page.csf"), []byte("opaque"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "nested")
	if err := os.Symlink(outside, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("create test symlink: %v", err)
		}
		t.Fatal(err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateConfluenceSelectionMirrorArtifact(
		canonicalRoot,
		filepath.Join(canonicalRoot, filepath.Base(link), "page.csf"),
	); err == nil {
		t.Fatal("artifact below a descendant symlink passed mirror containment")
	}
}

type confluenceSelectionBackendSearchPage struct {
	Results []struct {
		Content struct {
			ID    string `json:"id"`
			Title string `json:"title"`
			Space struct {
				Key string `json:"key"`
			} `json:"space"`
			Version struct {
				Number int    `json:"number"`
				When   string `json:"when"`
			} `json:"version"`
		} `json:"content"`
		Title   string `json:"title"`
		Excerpt string `json:"excerpt"`
	} `json:"results"`
	Size       int  `json:"size"`
	TotalCount *int `json:"totalCount,omitempty"`
	Links      struct {
		Next string `json:"next,omitempty"`
	} `json:"_links"`
}

func prepareConfluenceSelectionPullFixture(t *testing.T, fixture MockFixture) MockFixture {
	t.Helper()
	prepared := fixture
	prepared.Routes = slices.Clone(fixture.Routes)
	searchNames := map[int]string{}
	searchIDs := map[int][]string{}
	contentNames := map[string]string{}
	contentOrdinal := 0
	for index := range prepared.Routes {
		route := &prepared.Routes[index]
		switch {
		case route.Path == fixture.ConfluenceContext+"/rest/api/search":
			start, err := strconv.Atoi(route.QueryEquals["start"])
			if err != nil || (start != 1000 && (start < 0 || start > 900 || start%100 != 0)) ||
				route.Method != "GET" || route.Status != 200 || route.Name != "" ||
				len(route.QueryEquals) != 4 || route.QueryEquals["cql"] != confluenceSelectionPrimaryQuery ||
				route.QueryEquals["expand"] != "content.version,content.space" ||
				(start == 1000 && route.QueryEquals["limit"] != "1") ||
				(start != 1000 && route.QueryEquals["limit"] != "100") ||
				len(route.QueryContains) != 0 || len(route.RequestBody) != 0 || len(route.Responses) != 0 {
				t.Fatalf("retained selection search route %d is not exact: %+v", index, *route)
			}
			var page confluenceSelectionBackendSearchPage
			if err := decodeStrict(bytes.NewReader(route.Body), &page); err != nil {
				t.Fatalf("decode retained selection search page start=%d: %v", start, err)
			}
			ids := make([]string, len(page.Results))
			for resultIndex, result := range page.Results {
				if result.Content.ID == "" {
					t.Fatalf("selection search start=%d result=%d has no id", start, resultIndex)
				}
				ids[resultIndex] = result.Content.ID
			}
			name := fmt.Sprintf("selection-search-%04d", start)
			route.Name, route.closedQuery = name, true
			searchNames[start], searchIDs[start] = name, ids
		case strings.HasPrefix(route.Path, fixture.ConfluenceContext+"/rest/api/content/"):
			id := strings.TrimPrefix(route.Path, fixture.ConfluenceContext+"/rest/api/content/")
			if id == "" || route.Method != "GET" || route.Status != 200 || route.Name != "" ||
				len(route.QueryEquals) != 1 || route.QueryEquals["expand"] != "body.storage,version,space,ancestors,metadata.labels" ||
				len(route.QueryContains) != 0 || len(route.RequestBody) != 0 || len(route.Responses) != 0 {
				t.Fatalf("retained selection content route %d is not exact: %+v", index, *route)
			}
			contentOrdinal++
			name := fmt.Sprintf("selection-content-%04d", contentOrdinal)
			route.Name, route.closedQuery = name, true
			if _, duplicate := contentNames[id]; duplicate {
				t.Fatalf("retained selection content repeats id %q", id)
			}
			contentNames[id] = name
		default:
			t.Fatalf("retained selection route %d has unsupported path %q", index, route.Path)
		}
	}
	prepared.RequestSequence = make([]string, 0, len(prepared.Routes))
	for start := 0; start <= 1000; start += 100 {
		name := searchNames[start]
		if name == "" {
			t.Fatalf("retained selection fixture has no search start=%d", start)
		}
		prepared.RequestSequence = append(prepared.RequestSequence, name)
	}
	for start := 0; start <= 900; start += 100 {
		for _, id := range searchIDs[start] {
			name := contentNames[id]
			if name == "" {
				t.Fatalf("retained selection id %q has no content route", id)
			}
			prepared.RequestSequence = append(prepared.RequestSequence, name)
		}
	}
	if len(prepared.RequestSequence) != 1011 || len(contentNames) != 1000 {
		t.Fatalf("prepared selection sequence=%d content=%d", len(prepared.RequestSequence), len(contentNames))
	}
	if err := prepared.Validate(); err != nil {
		t.Fatalf("prepare Confluence selection pull fixture: %v", err)
	}
	return prepared
}

func prepareConfluenceSelectionSearchFixture(t *testing.T, fixture MockFixture) MockFixture {
	t.Helper()
	if len(fixture.Routes) != 1 || len(fixture.RequestSequence) != 0 {
		t.Fatalf("retained selection search topology drifted: routes=%d sequence=%v", len(fixture.Routes), fixture.RequestSequence)
	}
	prepared := fixture
	prepared.Routes = slices.Clone(fixture.Routes)
	route := &prepared.Routes[0]
	wantQuery := map[string]string{
		"cql": confluenceSelectionHoldoutQuery, "limit": "25", "start": "0",
		"expand": "content.version,content.space",
	}
	if route.Name != "" || route.Method != "GET" || route.Path != fixture.ConfluenceContext+"/rest/api/search" ||
		route.Status != 200 || !maps.Equal(route.QueryEquals, wantQuery) || len(route.QueryContains) != 0 ||
		len(route.RequestBody) != 0 || len(route.Responses) != 0 {
		t.Fatalf("retained selection search route is not exact: %+v", *route)
	}
	var page confluenceSelectionBackendSearchPage
	if err := decodeStrict(bytes.NewReader(route.Body), &page); err != nil {
		t.Fatalf("decode retained selection search fixture: %v", err)
	}
	if page.Size != len(page.Results) {
		t.Fatalf("retained selection search size=%d results=%d", page.Size, len(page.Results))
	}
	route.Name, route.closedQuery = "selection-search", true
	prepared.RequestSequence = []string{route.Name}
	if err := prepared.Validate(); err != nil {
		t.Fatalf("prepare Confluence selection search fixture: %v", err)
	}
	return prepared
}

func assertConfluenceSelectionProviderOracles(t *testing.T, root string, final []byte, methods map[string]int, unexpected int) {
	t.Helper()
	for _, provider := range []string{"codex", "claude"} {
		spec := loadRepositoryRunSpec(t, filepath.Join(root, "run.cli."+provider+".json"))
		assertConfluenceSelectionSchemaMatchesFinal(t, root, spec, final)
		checks := confluenceSelectionEvaluate(t, spec, final, methods, unexpected, 1)
		for name, passed := range checks {
			if !passed {
				t.Fatalf("%s fixture-derived final failed %q", spec.Provider, name)
			}
		}
		assertConfluenceSelectionAnswerMutationsFail(t, spec, final, methods, unexpected)
	}
}

func confluenceSelectionEvaluate(t *testing.T, spec RunSpec, final []byte, methods map[string]int, unexpected, invocations int) map[string]bool {
	t.Helper()
	return confluenceSelectionEvaluateWithSkillMap(
		t, spec, final, methods, unexpected, invocations,
		map[string]int{"atl:confluence": 1},
	)
}

func confluenceSelectionEvaluateWithSkillMap(t *testing.T, spec RunSpec, final []byte, methods map[string]int, unexpected, invocations int, skillInvocationsByName map[string]int) map[string]bool {
	t.Helper()
	exitCodes := make([]int, invocations)
	checks, err := evaluateRunChecks(
		spec.Checks, final, "", invocations, 0, unexpected, 1,
		skillInvocationsByName, 0, 0, methods, true, exitCodes,
	)
	if err != nil {
		t.Fatal(err)
	}
	return checks
}

func assertConfluenceSelectionAnswerMutationsFail(t *testing.T, spec RunSpec, final []byte, methods map[string]int, unexpected int) {
	t.Helper()
	mutations := []struct {
		field string
		value any
		check string
	}{
		{"complete", true, "complete_correct"},
		{"truncated", false, "truncated_correct"},
		{"truncated_at", nil, "truncated_at_correct"},
		{"partial_reason", "different reason", "partial_reason_correct"},
		{"absence_claim_supported", true, "absence_claim_rejected"},
		{"unobserved_matches_possible", false, "unobserved_possible"},
		{"recommended_action", "none", "remediation_correct"},
		{"remote_writes_performed", true, "no_remote_writes"},
		{"selection_only", false, "selection_only"},
	}
	for _, mutation := range mutations {
		t.Run(spec.Provider+"/"+mutation.field, func(t *testing.T) {
			var document map[string]any
			if err := json.Unmarshal(final, &document); err != nil {
				t.Fatal(err)
			}
			current := document[mutation.field]
			value := mutation.value
			if mutation.field == "truncated_at" && current == nil {
				value = float64(999)
			} else if current == value {
				value = nil
			}
			document[mutation.field] = value
			mutated, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			checks := confluenceSelectionEvaluate(t, spec, mutated, methods, unexpected, 1)
			if checks[mutation.check] {
				t.Fatalf("independent answer mutation %q passed %q", mutation.field, mutation.check)
			}
		})
	}
}

func assertConfluenceSelectionSchemaMatchesFinal(t *testing.T, root string, spec RunSpec, final []byte) {
	t.Helper()
	schema, err := os.ReadFile(filepath.Join(root, spec.ResponseSchemaFile))
	if err != nil {
		t.Fatal(err)
	}
	provider, err := providerResponseSchema(spec, schema)
	if err != nil {
		t.Fatalf("%s provider schema conversion failed: %v", spec.Provider, err)
	}
	for name, candidate := range map[string][]byte{"retained": schema, "provider": provider} {
		if err := validateJSONSchemaSubsetInstance(candidate, final); err != nil {
			t.Fatalf("%s %s schema rejected fixture-derived final: %v", spec.Provider, name, err)
		}
	}
	var rootSchema struct {
		AdditionalProperties *bool                      `json:"additionalProperties"`
		Properties           map[string]json.RawMessage `json:"properties"`
		Required             []string                   `json:"required"`
	}
	if err := json.Unmarshal(schema, &rootSchema); err != nil {
		t.Fatal(err)
	}
	propertyNames := make([]string, 0, len(rootSchema.Properties))
	for name := range rootSchema.Properties {
		propertyNames = append(propertyNames, name)
	}
	required := slices.Clone(rootSchema.Required)
	slices.Sort(propertyNames)
	slices.Sort(required)
	if rootSchema.AdditionalProperties == nil || *rootSchema.AdditionalProperties || !slices.Equal(propertyNames, required) {
		t.Fatalf("response schema is not a closed all-required object: properties=%v required=%v", propertyNames, required)
	}
}

func assertConfluenceSelectionCommandPolicy(t *testing.T, root string, spec RunSpec, query, operation string) {
	t.Helper()
	prompt, err := os.ReadFile(filepath.Join(root, spec.PromptFile))
	if err != nil {
		t.Fatal(err)
	}
	normalizedPrompt := strings.Join(strings.Fields(string(prompt)), " ")
	if strings.Contains(normalizedPrompt, "atl capabilities") || !strings.Contains(normalizedPrompt, "`atl conf "+operation) {
		t.Fatalf("%s prompt does not retain the one-command route", spec.Provider)
	}
	if !strings.Contains(normalizedPrompt, "Do not run a second command") {
		t.Fatalf("%s prompt does not reject a second CLI invocation", spec.Provider)
	}
	var args []string
	switch operation {
	case "pull":
		args = []string{"conf", "pull", "--cql", query, "--into", "selection-mirror"}
	case "search":
		args = []string{"conf", "search", "--cql", query, "--limit", "25"}
	default:
		t.Fatalf("unsupported operation %q", operation)
	}
	switch spec.Provider {
	case "codex":
		policy := CLICommandPolicy{SchemaVersion: CLICommandPolicySchemaVersion, Rules: spec.AllowedCLICommands}
		if _, err := policy.Match(args); err != nil {
			t.Fatalf("exact %s command rejected: %v", operation, err)
		}
		wrongQuery := slices.Clone(args)
		wrongQuery[3] = query + " AND label = wrong"
		if _, err := policy.Match(wrongQuery); err == nil {
			t.Fatal("wrong query passed Codex policy")
		}
		if _, err := policy.Match(append(slices.Clone(args), "--verbose")); err == nil {
			t.Fatal("extra flag passed Codex policy")
		}
	case "claude-code":
		if len(spec.AllowedATLCommands) != 1 || !strings.HasSuffix(spec.AllowedATLCommands[0], " --") ||
			!strings.Contains(spec.AllowedATLCommands[0], query) {
			t.Fatalf("Claude exact terminated prefix drifted: %v", spec.AllowedATLCommands)
		}
		wrongQuery := strings.Replace(spec.AllowedATLCommands[0], query, query+" AND label = wrong", 1)
		extraFlag := strings.TrimSuffix(spec.AllowedATLCommands[0], " --") + " --verbose --"
		if slices.Contains(spec.AllowedATLCommands, wrongQuery) || slices.Contains(spec.AllowedATLCommands, extraFlag) {
			t.Fatalf("Claude policy admits a wrong query or extra flag: %v", spec.AllowedATLCommands)
		}
	default:
		t.Fatalf("unexpected provider %q", spec.Provider)
	}
}

func assertConfluenceSelectionPrimaryTopology(t *testing.T, fixture MockFixture) {
	t.Helper()
	if len(fixture.Routes) != 1011 {
		t.Fatalf("primary routes=%d want=1011", len(fixture.Routes))
	}
	wantSearchQuery := map[string]string{
		"cql": confluenceSelectionPrimaryQuery, "expand": "content.version,content.space",
	}
	pageIDs := map[string]struct{}{}
	contentIDs := map[string]struct{}{}
	responseIDs := map[string]struct{}{}
	searchStarts := map[int]struct{}{}
	probeID := ""
	searchRoutes, contentRoutes := 0, 0
	for _, route := range fixture.Routes {
		if route.Method != "GET" || route.Status != 200 || len(route.QueryContains) != 0 || len(route.RequestBody) != 0 || len(route.Responses) != 0 {
			t.Fatalf("primary route is not one exact stateless GET: %+v", route)
		}
		switch {
		case route.Path == "/wiki/rest/api/search":
			searchRoutes++
			if route.QueryEquals["cql"] != wantSearchQuery["cql"] || route.QueryEquals["expand"] != wantSearchQuery["expand"] || len(route.QueryEquals) != 4 {
				t.Fatalf("search selector drifted: %+v", route.QueryEquals)
			}
			start, err := strconv.Atoi(route.QueryEquals["start"])
			if err != nil {
				t.Fatal(err)
			}
			if _, duplicate := searchStarts[start]; duplicate {
				t.Fatalf("duplicate search start %d", start)
			}
			searchStarts[start] = struct{}{}
			var body struct {
				Results []struct {
					Content struct {
						ID string `json:"id"`
					} `json:"content"`
				} `json:"results"`
				Size  int `json:"size"`
				Links struct {
					Next string `json:"next"`
				} `json:"_links"`
			}
			if err := json.Unmarshal(route.Body, &body); err != nil {
				t.Fatal(err)
			}
			if start == 1000 {
				if route.QueryEquals["limit"] != "1" || len(body.Results) != 1 || body.Size != 1 {
					t.Fatalf("probe route drifted: selector=%v body=%s", route.QueryEquals, route.Body)
				}
				probeID = body.Results[0].Content.ID
				continue
			}
			if start < 0 || start > 900 || start%100 != 0 || route.QueryEquals["limit"] != "100" ||
				len(body.Results) != 100 || body.Size != 100 || body.Links.Next == "" {
				t.Fatalf("search page start=%d drifted: selector=%v size=%d results=%d next=%q", start, route.QueryEquals, body.Size, len(body.Results), body.Links.Next)
			}
			for _, result := range body.Results {
				if result.Content.ID == "" {
					t.Fatal("search page contains an empty id")
				}
				if _, duplicate := pageIDs[result.Content.ID]; duplicate {
					t.Fatalf("search pages repeat id %q", result.Content.ID)
				}
				pageIDs[result.Content.ID] = struct{}{}
			}
		case strings.HasPrefix(route.Path, "/wiki/rest/api/content/"):
			contentRoutes++
			if len(route.QueryEquals) != 1 || route.QueryEquals["expand"] != "body.storage,version,space,ancestors,metadata.labels" {
				t.Fatalf("content selector drifted: %+v", route)
			}
			id := strings.TrimPrefix(route.Path, "/wiki/rest/api/content/")
			if id == "" {
				t.Fatal("content route has empty id")
			}
			if _, duplicate := contentIDs[id]; duplicate {
				t.Fatalf("content routes repeat id %q", id)
			}
			contentIDs[id] = struct{}{}
			var body struct {
				ID    string `json:"id"`
				Title string `json:"title"`
				Space struct {
					Key string `json:"key"`
				} `json:"space"`
				Version struct {
					Number int `json:"number"`
				} `json:"version"`
				Body struct {
					Storage struct {
						Value *string `json:"value"`
					} `json:"storage"`
				} `json:"body"`
			}
			if err := json.Unmarshal(route.Body, &body); err != nil {
				t.Fatal(err)
			}
			if body.ID != id || body.Title == "" || body.Space.Key == "" || body.Version.Number < 1 ||
				body.Body.Storage.Value == nil || *body.Body.Storage.Value == "" {
				t.Fatalf("content response does not minimally and exactly project path id %q: %+v", id, body)
			}
			if _, duplicate := responseIDs[body.ID]; duplicate {
				t.Fatalf("content responses repeat id %q", body.ID)
			}
			responseIDs[body.ID] = struct{}{}
		default:
			t.Fatalf("unexpected primary route path %q", route.Path)
		}
	}
	if searchRoutes != 11 || contentRoutes != 1000 || len(pageIDs) != 1000 || len(contentIDs) != 1000 || len(responseIDs) != 1000 || len(searchStarts) != 11 || probeID == "" {
		t.Fatalf("topology search=%d content=%d page_ids=%d content_ids=%d response_ids=%d starts=%d probe=%q", searchRoutes, contentRoutes, len(pageIDs), len(contentIDs), len(responseIDs), len(searchStarts), probeID)
	}
	for start := 0; start <= 1000; start += 100 {
		if _, ok := searchStarts[start]; !ok {
			t.Fatalf("missing exact search start %d", start)
		}
	}
	for id := range pageIDs {
		if _, ok := contentIDs[id]; !ok {
			t.Fatalf("search id %q has no exact content route", id)
		}
		if _, ok := responseIDs[id]; !ok {
			t.Fatalf("search id %q has no matching content response identity", id)
		}
	}
	if _, repeated := pageIDs[probeID]; repeated {
		t.Fatalf("probe id %q repeats a selected id", probeID)
	}
	if _, fetched := contentIDs[probeID]; fetched {
		t.Fatalf("probe page %q has a content route and could be fetched", probeID)
	}
	if _, fetched := responseIDs[probeID]; fetched {
		t.Fatalf("probe page %q appears in a content response", probeID)
	}
}
