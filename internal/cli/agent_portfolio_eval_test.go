package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/isukharev/atl/internal/agenteval"
	"github.com/isukharev/atl/internal/app"
)

func TestSyntheticQuarterPortfolioRouteUsesOneSnapshotAndNarrowExpansions(t *testing.T) {
	fixturePath := filepath.Join("..", "..", "benchmarks", "agent-eval", "jira-quarter-portfolio", "fixture.json")
	file, err := os.Open(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := agenteval.DecodeMockFixture(file)
	_ = file.Close()
	if err != nil {
		t.Fatal(err)
	}
	backend, err := agenteval.StartMockBackend(fixture)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	env := backend.Environment()

	fieldsOut, code := runCLI(t, env, "jira", "fields")
	if code != exitOK {
		t.Fatalf("field discovery exit=%d output=%s", code, fieldsOut)
	}
	var fields struct {
		Fields []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"fields"`
	}
	if err := json.Unmarshal([]byte(fieldsOut), &fields); err != nil || len(fields.Fields) != 3 {
		t.Fatalf("field discovery=%s err=%v", fieldsOut, err)
	}

	boardOut, code := runCLI(t, env,
		"jira", "board", "view", "5", "--scope", "board", "--limit", "50",
		"--columns", "key,summary,status,issuetype,updated,customfield_11001,customfield_11002,customfield_11003")
	if code != exitOK {
		t.Fatalf("board view exit=%d output=%s", code, boardOut)
	}
	var board app.BoardSnapshot
	if err := json.Unmarshal([]byte(boardOut), &board); err != nil {
		t.Fatal(err)
	}
	if !board.Complete || len(board.Rows) != 8 || board.Board.ID != 5 {
		t.Fatalf("board snapshot complete=%t rows=%d board=%+v", board.Complete, len(board.Rows), board.Board)
	}

	for _, key := range []string{"PROJ-10", "PROJ-20", "PROJ-30"} {
		out, digestCode := runCLI(t, env,
			"jira", "epic", "digest", key, "--quarter", "2026-Q2",
			"--include", "identity,status-field,history", "--status-field", "customfield_11002")
		if digestCode != exitOK {
			t.Fatalf("digest %s exit=%d output=%s", key, digestCode, out)
		}
		var digest app.JiraEpicDigestResult
		if err := json.Unmarshal([]byte(out), &digest); err != nil {
			t.Fatal(err)
		}
		for _, source := range []string{"identity", "status-field", "history"} {
			if !digest.Sources[source].Complete {
				t.Fatalf("digest %s source %s=%+v", key, source, digest.Sources[source])
			}
		}
		if digest.StatusField == nil || digest.StatusField.LastChange == nil {
			t.Fatalf("digest %s lacks dated status evidence", key)
		}
	}

	for _, pageID := range []string{"9001", "9002", "9003"} {
		out, sectionCode := runCLI(t, env,
			"conf", "page", "section", "/wiki/pages/viewpage.action?pageId="+pageID,
			"--heading", "Results", "--max-bytes", "32768")
		if sectionCode != exitOK {
			t.Fatalf("section %s exit=%d output=%s", pageID, sectionCode, out)
		}
		var section app.ConfluencePageSectionResult
		if err := json.Unmarshal([]byte(out), &section); err != nil || !section.Complete || section.ID != pageID {
			t.Fatalf("section %s result=%s err=%v", pageID, out, err)
		}
	}

	methods, unexpected, duplicates := backend.Summary()
	if methods["GET"] != 15 || len(methods) != 1 || unexpected != 0 || duplicates != 2 {
		t.Fatalf("requests=%v unexpected=%d duplicates=%d", methods, unexpected, duplicates)
	}
}
