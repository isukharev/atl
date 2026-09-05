package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/diagnostic"
)

func TestConfPushUnconfirmedAcknowledgementRecoveryContract(t *testing.T) {
	for _, response := range []string{"{}", "{"} {
		for _, matches := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/matches=%v", response, matches), func(t *testing.T) {
				server := newConfServer(t)
				root, path := dirtyMirror(t, server, 7)
				server.writes = []cannedResp{{status: 200, body: response}}
				if matches {
					server.page = pageJSON("12345", "Design Doc", 8, editedCSF)
				}
				stdout, _, err := executeCLIRaw(t, confEnv(server.srv), "conf", "push", path, "--into", root)
				var result app.PushResult
				if decodeErr := json.Unmarshal([]byte(stdout), &result); decodeErr != nil {
					t.Fatal(decodeErr)
				}
				if len(result.Items) != 1 || len(server.writeReqs()) != 1 {
					t.Fatalf("result=%+v writes=%v", result, server.writeReqs())
				}
				if matches {
					if err != nil || !result.Items[0].Pushed || result.Items[0].NewVersion != 8 {
						t.Fatalf("result=%+v err=%v", result, err)
					}
				} else {
					recovery := diagnostic.Recover(err, diagnostic.OperationWrite)
					if codeFor(err) != exitCheckFailed || result.Items[0].Pushed || recovery.Action != diagnostic.RecoveryReconcileWriteOutcome || recovery.RetrySafe {
						t.Fatalf("result=%+v err=%v recovery=%+v", result, err, recovery)
					}
				}
				body, readErr := os.ReadFile(path)
				if readErr != nil || string(body) != editedCSF {
					t.Fatalf("candidate=%q err=%v", body, readErr)
				}
			})
		}
	}
}

func TestJiraImagesCLIEmitsDistinctIdentityQualifiedPaths(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rest/api/2/issue/PROJ-1" {
			_, _ = fmt.Fprintf(w, `{"fields":{"attachment":[{"id":"1","filename":"shot.png","mimeType":"image/png","content":%q},{"id":"2","filename":"shot.png","mimeType":"image/png","content":%q}]}}`, server.URL+"/first", server.URL+"/second")
			return
		}
		_, _ = io.WriteString(w, r.URL.Path)
	}))
	defer server.Close()
	dir := t.TempDir()
	out, code := runCLI(t, jiraEnv(server), "jira", "issue", "images", "PROJ-1", "--into", dir)
	var result struct {
		Images []string `json:"images"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if code != exitOK || len(result.Images) != 2 {
		t.Fatalf("code=%d result=%s", code, out)
	}
	for i, path := range result.Images {
		if filepath.Base(path) != fmt.Sprintf("%d-shot.png", i+1) {
			t.Fatalf("unexpected path %s", path)
		}
		body, err := os.ReadFile(path)
		want := []string{"/first", "/second"}[i]
		if err != nil || string(body) != want {
			t.Fatalf("body=%q err=%v", body, err)
		}
	}
}
