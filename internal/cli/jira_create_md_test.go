package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/isukharev/atl/internal/app"
)

// --- jira --from-md: markdown-authored issue bodies -------------------------

const jiraMD = "## Контекст\n\nIntro with **bold**.\n\n- one\n- two\n\n```bash\necho hi\n```\n"

// jiraWiki is what mdwiki.ConvertDocument produces for jiraMD — pinned so a
// converter drift that changes what goes over the wire is caught.
const jiraWiki = "h2. Контекст\n\nIntro with *bold*.\n\n* one\n* two\n\n{code:bash}\necho hi\n{code}"

func writeTempMD(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write md: %v", err)
	}
	return p
}

// TestJiraCreate_FromMD: the converted wiki markup is what reaches the wire
// as fields.description.
func TestJiraCreate_FromMD(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodPost, "/rest/api/2/issue", http.StatusCreated, `{"key":"ENG-7"}`)

	out, code := runCLI(t, jiraEnv(js.srv),
		"jira", "issue", "create", "--project", "ENG", "--type", "Task",
		"--summary", "MD", "--from-md", writeTempMD(t, jiraMD))
	if code != exitOK {
		t.Fatalf("jira create --from-md: exit %d (stdout=%q)", code, out)
	}
	writes := js.writeReqsTo("/rest/api/2/issue")
	if len(writes) != 1 {
		t.Fatalf("expected 1 write, got %d", len(writes))
	}
	if got := jiraFields(t, writes[0].body)["description"]; got != jiraWiki {
		t.Fatalf("description = %q, want converted wiki %q", got, jiraWiki)
	}
}

// TestJiraUpdate_FromMD: same conversion path on update.
func TestJiraUpdate_FromMD(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodPut, "/rest/api/2/issue/ENG-7", http.StatusNoContent, ``)

	_, code := runCLI(t, jiraEnv(js.srv),
		"jira", "issue", "update", "ENG-7", "--from-md", writeTempMD(t, jiraMD))
	if code != exitOK {
		t.Fatalf("jira update --from-md: exit %d", code)
	}
	writes := js.writeReqsTo("/rest/api/2/issue/ENG-7")
	if len(writes) != 1 {
		t.Fatalf("expected 1 write, got %d", len(writes))
	}
	if got := jiraFields(t, writes[0].body)["description"]; got != jiraWiki {
		t.Fatalf("description = %q, want %q", got, jiraWiki)
	}
}

// TestJiraCommentPreview_FromMD: comments convert too; the reviewed proposal
// contains the exact native wiki bytes without issuing a write.
func TestJiraCommentPreview_FromMD(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/api/2/myself", http.StatusOK,
		`{"name":"alice","key":"user-1","displayName":"Alice"}`)
	js.route(http.MethodGet, "/rest/api/2/issue/ENG-7/comment", http.StatusOK,
		`{"startAt":0,"total":0,"comments":[]}`)

	out, code := runCLI(t, jiraEnv(js.srv),
		"jira", "issue", "comment", "preview", "ENG-7", "--from-md", writeTempMD(t, jiraMD))
	if code != exitOK {
		t.Fatalf("jira comment preview --from-md: exit %d", code)
	}
	var result app.JiraCommentAddResult
	if err := json.Unmarshal([]byte(out), &result); err != nil || result.Body != jiraWiki {
		t.Fatalf("result=%+v err=%v, want converted wiki %q", result, err, jiraWiki)
	}
	if writes := js.writeReqsTo("/rest/api/2/issue/ENG-7/comment"); len(writes) != 0 {
		t.Fatalf("preview wrote %d comments", len(writes))
	}
}

// TestJiraFromMD_FailClosed: an unconvertible block refuses with exit 8 and
// sends nothing; the empty --from-md value and the flag conflict are usage
// errors — for all three commands via the shared wikiBody helper.
func TestJiraFromMD_FailClosed(t *testing.T) {
	js := newJiraServer(t)
	bad := writeTempMD(t, "# ok\n\n- [ ] task lists have no wiki equivalent\n")
	wiki := writeTempMD(t, "h2. raw wiki")

	cases := []struct {
		name string
		args []string
		want int
	}{
		{"create unsupported", []string{"jira", "issue", "create", "--project", "ENG",
			"--type", "Task", "--summary", "S", "--from-md", bad}, exitCheckFailed},
		{"update unsupported", []string{"jira", "issue", "update", "ENG-7", "--from-md", bad}, exitCheckFailed},
		{"comment unsupported", []string{"jira", "issue", "comment", "add", "ENG-7", "--from-md", bad}, exitCheckFailed},
		{"create both flags", []string{"jira", "issue", "create", "--project", "ENG",
			"--type", "Task", "--summary", "S", "--from-file", wiki, "--from-md", bad}, exitUsage},
		{"comment both flags", []string{"jira", "issue", "comment", "add", "ENG-7",
			"--from-file", wiki, "--from-md", bad}, exitUsage},
		{"create empty value", []string{"jira", "issue", "create", "--project", "ENG",
			"--type", "Task", "--summary", "S", "--from-md", ""}, exitUsage},
	}
	for _, c := range cases {
		out, code := runCLI(t, jiraEnv(js.srv), c.args...)
		if code != c.want {
			t.Errorf("%s: exit %d, want %d (stdout=%q)", c.name, code, c.want, out)
		}
	}
	if reqs := js.requests(); len(reqs) != 0 {
		t.Fatalf("fail-closed breached: %d request(s) sent: %+v", len(reqs), reqs)
	}
}
