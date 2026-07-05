package cli

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

// TestJiraCommentAdd_FromMD: comments convert too; the wiki body lands in
// the comment POST payload.
func TestJiraCommentAdd_FromMD(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodPost, "/rest/api/2/issue/ENG-7/comment", http.StatusCreated,
		`{"id":"1","body":"x"}`)

	_, code := runCLI(t, jiraEnv(js.srv),
		"jira", "issue", "comment", "add", "ENG-7", "--from-md", writeTempMD(t, jiraMD))
	if code != exitOK {
		t.Fatalf("jira comment add --from-md: exit %d", code)
	}
	writes := js.writeReqsTo("/rest/api/2/issue/ENG-7/comment")
	if len(writes) != 1 {
		t.Fatalf("expected 1 write, got %d", len(writes))
	}
	if !strings.Contains(writes[0].body, `"h2. Контекст`) {
		t.Fatalf("comment body = %q, want converted wiki", writes[0].body)
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
