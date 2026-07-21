package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/agenteval"
)

func TestCapabilityCatalogDefinitionsAreValidAndUnique(t *testing.T) {
	root := newRoot()
	catalog, err := buildCapabilityCatalog(root, capabilitySelection{})
	if err != nil {
		t.Fatal(err)
	}
	if catalog.SchemaVersion != capabilityCatalogSchemaVersion || catalog.Selection.Count != len(capabilityDefinitions) {
		t.Fatalf("catalog metadata=%+v definitions=%d", catalog, len(capabilityDefinitions))
	}
	if catalog.Routing.Match != "exact" || !strings.Contains(catalog.Routing.ReferenceLoad, "do not search") {
		t.Fatalf("routing contract=%+v", catalog.Routing)
	}
	seen := map[string]bool{}
	for _, item := range catalog.Capabilities {
		if seen[item.ID] {
			t.Fatalf("duplicate capability id %q", item.ID)
		}
		seen[item.ID] = true
		if item.Access != "read-only" && item.Access != "mutating" {
			t.Fatalf("%s access=%q", item.ID, item.Access)
		}
		if len(item.OutputModes) == 0 || item.OutputModes[0] != "json" {
			t.Fatalf("%s output modes=%v", item.ID, item.OutputModes)
		}
		if item.Skill == "" || item.Reference == "" || !strings.HasSuffix(item.Reference, ".md") {
			t.Fatalf("%s skill route=%q/%q", item.ID, item.Skill, item.Reference)
		}
	}
}

func TestCapabilityTaskRoutesStaySmallAndOrdered(t *testing.T) {
	tests := []struct {
		task string
		ids  []string
	}{
		{"jira/evidence", []string{"jira.issue.fields", "jira.epic.digest", "jira.issue.field.get", "jira.issue.refs", "jira.issue.history"}},
		{"jira/portfolio", []string{"jira.board.list", "jira.board.view", "jira.structure.folders", "jira.structure.view", "jira.portfolio.epic.digest", "jira.portfolio.confluence.section"}},
		{"jira/board-portfolio", []string{"jira.board-portfolio.fields", "jira.board-portfolio.view", "jira.board-portfolio.epic.digest"}},
		{"jira/batch-analysis", []string{"jira.batch.issue.export"}},
		{"jira/structure-planning", []string{"jira.structure.rows", "jira.structure.values", "jira.structure.issue.export"}},
		{"jira/edit", []string{"jira.issue.fields.edit", "jira.issue.field.preview", "jira.issue.field.set", "jira.issue.worklog.list", "jira.issue.worklog.add", "jira.issue.plan.apply"}},
		{"confluence/evidence", []string{"confluence.page.resolve", "confluence.page.outline", "confluence.page.section", "confluence.page.view"}},
		{"confluence/table-analytics", []string{"confluence.table.extract"}},
		{"confluence/edit", []string{"confluence.pull", "confluence.diff", "confluence.plan.create", "confluence.plan.preview", "confluence.plan.apply"}},
		{"knowledge/search", []string{"knowledge.jira.search", "knowledge.confluence.search", "knowledge.jira.field", "knowledge.confluence.outline", "knowledge.confluence.section"}},
	}
	root := newRoot()
	for _, tt := range tests {
		t.Run(tt.task, func(t *testing.T) {
			catalog, err := buildCapabilityCatalog(root, capabilitySelection{Task: tt.task})
			if err != nil {
				t.Fatal(err)
			}
			ids := make([]string, len(catalog.Capabilities))
			for i := range catalog.Capabilities {
				ids[i] = catalog.Capabilities[i].ID
			}
			if !reflect.DeepEqual(ids, tt.ids) {
				t.Fatalf("ids=%v want=%v", ids, tt.ids)
			}
			if len(ids) > 6 {
				t.Fatalf("route expanded beyond bounded catalog contract: %v", ids)
			}
		})
	}
}

func TestCapabilityRoutesPointToTheirFocusedWorkflow(t *testing.T) {
	root := newRoot()
	structure, err := buildCapabilityCatalog(root, capabilitySelection{Task: "jira/structure-planning"})
	if err != nil {
		t.Fatal(err)
	}
	if len(structure.Capabilities) != 3 || structure.Capabilities[0].Command != "jira structure rows" || structure.Capabilities[1].Command != "jira structure values" || structure.Capabilities[2].Command != "jira export" {
		t.Fatalf("structure route=%+v", structure.Capabilities)
	}
	for _, item := range structure.Capabilities {
		if item.Skill != "jira" || item.Reference != "reference/structure-batch.md" {
			t.Fatalf("structure workflow route=%s/%s", item.Skill, item.Reference)
		}
	}

	knowledge, err := buildCapabilityCatalog(root, capabilitySelection{Task: "knowledge/search"})
	if err != nil {
		t.Fatal(err)
	}
	if len(knowledge.Capabilities) == 0 || knowledge.Capabilities[0].Skill != "search-knowledge" || knowledge.Capabilities[0].Reference != "SKILL.md" {
		t.Fatalf("knowledge discovery route=%+v", knowledge.Capabilities)
	}
}

func TestAgentEvalScenariosUseCatalogCapabilitiesForTheirTask(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "benchmarks", "agent-eval", "*", "scenario.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no public agent-eval scenarios found")
	}
	catalog, err := buildCapabilityCatalog(newRoot(), capabilitySelection{})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]capability{}
	for _, item := range catalog.Capabilities {
		byID[item.ID] = item
	}
	for _, path := range paths {
		file, openErr := os.Open(path)
		if openErr != nil {
			t.Fatal(openErr)
		}
		scenario, decodeErr := agenteval.DecodeScenario(file)
		_ = file.Close()
		if decodeErr != nil {
			t.Fatalf("%s: %v", path, decodeErr)
		}
		for _, id := range scenario.RequiredCapabilities {
			item, ok := byID[id]
			if !ok {
				t.Errorf("%s requires capability %q absent from catalog", path, id)
				continue
			}
			if item.TaskClass != scenario.TaskClass {
				t.Errorf("%s capability %q task=%q want=%q", path, id, item.TaskClass, scenario.TaskClass)
			}
		}
	}
}

func TestCapabilitiesCommandIsOfflineAndSupportsAllOutputModes(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer srv.Close()
	cfgDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(`{"read_only":`), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"ATL_CONFIG_DIR": cfgDir,
		"ATL_JIRA_URL":   srv.URL, "ATL_JIRA_PAT": "test-pat",
		"ATL_CONFLUENCE_URL": srv.URL, "ATL_CONFLUENCE_PAT": "test-pat",
	}

	out, code := runCLI(t, env, "capabilities", "--task", "jira/evidence")
	if code != exitOK {
		t.Fatalf("json exit=%d output=%s", code, out)
	}
	var catalog capabilityCatalog
	if err := json.Unmarshal([]byte(out), &catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.Selection.Task != "jira/evidence" || catalog.Selection.Count != 5 {
		t.Fatalf("selection=%+v", catalog.Selection)
	}

	out, code = runCLI(t, env, "capabilities", "--id", "confluence.page.section", "-o", "text")
	if code != exitOK || !strings.Contains(out, "`confluence.page.section`") || !strings.Contains(out, "`atl conf page section`") {
		t.Fatalf("text exit=%d output=%q", code, out)
	}

	out, code = runCLI(t, env, "capabilities", "--task", "jira/edit", "--access", "mutating", "-o", "id")
	if code != exitOK || out != "jira.issue.field.set\njira.issue.worklog.add\njira.issue.plan.apply\n" {
		t.Fatalf("id exit=%d output=%q", code, out)
	}
	if requests != 0 {
		t.Fatalf("offline catalog made %d backend requests", requests)
	}
}

func TestCapabilitiesExactSelectionFailsLoudly(t *testing.T) {
	if _, code := runCLI(t, nil, "capabilities", "--task", "jira/unknown"); code != exitNotFound {
		t.Fatalf("unknown task exit=%d", code)
	}
	if _, code := runCLI(t, nil, "capabilities", "--service", "other"); code != exitUsage {
		t.Fatalf("bad service exit=%d", code)
	}
}

func TestUnsupportedIDOutputFailsBeforeConfigAndNetwork(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer srv.Close()
	cfgDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(`{"read_only":`), 0o600); err != nil {
		t.Fatal(err)
	}
	env := jiraEnv(srv)
	env["ATL_CONFIG_DIR"] = cfgDir
	if _, code := runCLI(t, env, "jira", "issue", "get", "PROJ-1", "-o", "id"); code != exitUsage || requests != 0 {
		t.Fatalf("exit=%d requests=%d", code, requests)
	}
}
