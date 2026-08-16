package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/mirror"
)

func TestJiraAttachmentBodiesRequiresExplicitBoundsBeforeEffects(t *testing.T) {
	for _, args := range [][]string{
		{"jira", "attachment-bodies"},
		{"jira", "attachment-bodies", "--max-attachment-bytes", "3"},
		{"jira", "attachment-bodies", "--max-transactions", "1"},
	} {
		if out, code := runCLI(t, map[string]string{}, args...); code != exitUsage || out != "" {
			t.Fatalf("args=%v code=%d stdout=%q", args, code, out)
		}
	}
}

func TestJiraAttachmentBodiesResumesQualifiedInventory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/search":
			writeJSON(w, http.StatusOK, `{"issues":[{"id":"1042","key":"ENG-42","fields":{"project":{"key":"ENG"}}}],"startAt":0,"maxResults":100,"total":1}`)
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/issue/1042" && r.URL.Query().Get("fields") == "attachment":
			writeJSON(w, http.StatusOK, `{"id":"1042","key":"ENG-42","fields":{"attachment":[{"id":"7001","filename":"fixture.bin","mimeType":"application/octet-stream","size":3,"created":"2026-01-01","content":"/secure/attachment/7001/fixture.bin","author":{"name":"fixture","key":"stable","displayName":"Fixture"}}]}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/issue/1042" && r.URL.Query().Get("fields") == "updated":
			writeJSON(w, http.StatusOK, `{"id":"1042","key":"ENG-42","fields":{"updated":"2026-01-01"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/2/issue/1042":
			writeJSON(w, http.StatusOK, `{"id":"1042","key":"ENG-42","fields":{"summary":"Synthetic issue","description":"native body","status":{"name":"Open"},"issuetype":{"name":"Task"},"project":{"key":"ENG"},"updated":"2026-01-01","issuelinks":[]}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/secure/attachment/7001/fixture.bin":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("abc"))
		default:
			t.Errorf("unexpected request %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
			writeJSON(w, http.StatusNotFound, `{}`)
		}
	}))
	t.Cleanup(server.Close)

	root := t.TempDir()
	env := jiraEnv(server)
	env["ATL_READ_ONLY"] = "1"
	if out, code := runCLI(t, env,
		"jira", "pull", "--complete", "--project", "ENG", "--max-issues", "1", "--into", root,
		"--attachments", "--max-attachments-per-issue", "1",
	); code != exitOK || out == "" {
		t.Fatalf("inventory pull code=%d stdout=%q", code, out)
	}
	out, code := runCLI(t, env,
		"jira", "attachment-bodies", "--into", root,
		"--attachment-media-type", "application/octet-stream", "--max-attachment-bytes", "3", "--max-transactions", "1",
	)
	if code != exitOK {
		t.Fatalf("materializer code=%d stdout=%q", code, out)
	}
	var result app.JiraAttachmentBodyMaterializeResult
	if err := json.Unmarshal([]byte(out), &result); err != nil || result.Captured != 1 || result.Remaining != 0 || !result.Complete {
		t.Fatalf("decode=%v result=%+v", err, result)
	}
	stem := filepath.Join(root, "ENG", "ENG-42")
	body, err := os.ReadFile(filepath.Join(stem+".attachments", "7001.body"))
	if err != nil || string(body) != "abc" {
		t.Fatalf("body=%q err=%v", body, err)
	}
	sidecarData, err := os.ReadFile(stem + ".attachments.json")
	if err != nil {
		t.Fatal(err)
	}
	sidecar, err := mirror.DecodeAttachmentSidecarV1(sidecarData)
	if err != nil || !sidecar.Complete || sidecar.BodiesState != mirror.AttachmentBodiesComplete ||
		len(sidecar.Attachments) != 1 || sidecar.Attachments[0].Body.State != mirror.AttachmentBodyCaptured {
		t.Fatalf("sidecar=%+v err=%v", sidecar, err)
	}
}
