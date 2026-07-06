package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/app"
)

// TestJiraPull_AssetsMirrorsImages drives `jira pull --assets`: the search
// projection carries attachment metadata (an image + a non-image), the image is
// streamed by its content URL into <KEY>.assets/, the .md links it, and the
// emitted JSON reports the per-issue asset count. The output shape is locked
// with a golden (a fixed relative --into keeps it host-independent).
func TestJiraPull_AssetsMirrorsImages(t *testing.T) {
	js := newJiraServer(t)
	searchBody, _ := json.Marshal(map[string]any{
		"issues": []map[string]any{{
			"id":  "1042",
			"key": "ENG-42",
			"fields": map[string]any{
				"summary":     "Pulled issue",
				"description": "h1. Heading",
				"status":      map[string]any{"name": "Open"},
				"issuetype":   map[string]any{"name": "Story"},
				"project":     map[string]any{"key": "ENG"},
				"attachment": []map[string]any{
					{"id": "500", "filename": "pic.png", "mimeType": "image/png", "size": 10, "content": "/secure/attachment/500/pic.png"},
					{"id": "501", "filename": "spec.pdf", "mimeType": "application/pdf", "size": 4, "content": "/secure/attachment/501/spec.pdf"},
				},
			},
		}},
		"startAt": 0, "maxResults": 50, "total": 1,
	})
	js.route(http.MethodGet, "/rest/api/2/search", http.StatusOK, string(searchBody))
	js.route(http.MethodGet, "/secure/attachment/500/pic.png", http.StatusOK, "IMAGEBYTES!")

	into := t.TempDir()
	out, stderr, code := runCLIFull(t, jiraEnv(js.srv), "jira", "pull", "--jql", "project=ENG", "--into", into, "--assets")
	if code != exitOK {
		t.Fatalf("jira pull --assets: exit %d, want 0 (stdout=%q stderr=%q)", code, out, stderr)
	}
	if stderr != "" {
		t.Errorf("no assets should be skipped, but got stderr warning: %q", stderr)
	}

	var res struct {
		Into          string           `json:"into"`
		Issues        []app.JiraPulled `json:"issues"`
		AssetsSkipped int              `json:"assets_skipped"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode pull result: %v\n%s", err, out)
	}
	if len(res.Issues) != 1 || res.Issues[0].Key != "ENG-42" || res.Issues[0].Assets != 1 {
		t.Fatalf("pull result = %+v, want one issue ENG-42 with 1 asset", res.Issues)
	}

	// The image landed under <into>/ENG/ENG-42.assets/ with its id-prefixed name.
	img := filepath.Join(into, "ENG", "ENG-42.assets", "500-pic.png")
	b, err := os.ReadFile(img)
	if err != nil {
		t.Fatalf("image asset not written at %s: %v", img, err)
	}
	if string(b) != "IMAGEBYTES!" {
		t.Errorf("image bytes = %q, want IMAGEBYTES!", b)
	}
	// The non-image PDF was not fetched.
	for _, r := range js.requests() {
		if r.path == "/secure/attachment/501/spec.pdf" {
			t.Errorf("non-image attachment was fetched: %s", r.path)
		}
	}
	// The .md links the mirrored image.
	mdb, err := os.ReadFile(filepath.Join(into, "ENG", "ENG-42.md"))
	if err != nil {
		t.Fatalf("read .md: %v", err)
	}
	if want := "![pic.png](ENG-42.assets/500-pic.png)"; !strings.Contains(string(mdb), want) {
		t.Errorf(".md missing image link %q\n%s", want, mdb)
	}

	// Lock the emitted JSON shape (assets count present; assets_skipped omitted).
	// The only volatile field is `into` (a temp dir); normalize it so the golden
	// is host-independent, exactly as the issue paths (relative) already are.
	normalized := strings.ReplaceAll(out, into, "<INTO>")
	assertGolden(t, "jira_pull_assets.json", []byte(normalized))
}
