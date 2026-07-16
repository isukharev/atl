package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/agenteval"
)

func startAgentEvalFixture(t *testing.T, scenario string) *agenteval.MockBackend {
	t.Helper()
	path := filepath.Join("..", "..", "benchmarks", "agent-eval", scenario, "fixture.json")
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
