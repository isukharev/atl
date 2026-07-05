package cli

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- jira issue edit: one-command targeted description edit -----------------

const editIssueJSON = `{"key":"ENG-7","fields":{"description":"h2. Params\n\n* timeout = 300\n\nh2. Check"}}`

func editServer(t *testing.T) *jiraServer {
	t.Helper()
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/api/2/issue/ENG-7", http.StatusOK, editIssueJSON)
	js.route(http.MethodPut, "/rest/api/2/issue/ENG-7", http.StatusNoContent, ``)
	return js
}

// TestJiraEdit_Replace: the spliced description — untouched bytes preserved —
// is what reaches the wire, and only the description field is sent.
func TestJiraEdit_Replace(t *testing.T) {
	js := editServer(t)

	out, code := runCLI(t, jiraEnv(js.srv),
		"jira", "issue", "edit", "ENG-7", "--old", "timeout = 300", "--new", "timeout = 600")
	if code != exitOK {
		t.Fatalf("edit: exit %d (stdout=%q)", code, out)
	}
	writes := js.writeReqsTo("/rest/api/2/issue/ENG-7")
	if len(writes) != 1 {
		t.Fatalf("expected 1 PUT, got %d", len(writes))
	}
	fl := jiraFields(t, writes[0].body)
	if got := fl["description"]; got != "h2. Params\n\n* timeout = 600\n\nh2. Check" {
		t.Fatalf("description = %q", got)
	}
	if _, ok := fl["summary"]; ok {
		t.Fatal("edit must not send summary")
	}
	for _, want := range []string{`"pass": "exact"`, `"count": 1`, `"region_before"`, `"region_after"`} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %s: %q", want, out)
		}
	}
}

// TestJiraEdit_DryRun: the match is reported but no PUT is sent.
func TestJiraEdit_DryRun(t *testing.T) {
	js := editServer(t)

	out, code := runCLI(t, jiraEnv(js.srv),
		"jira", "issue", "edit", "ENG-7", "--old", "timeout = 300", "--new", "timeout = 600", "--dry-run")
	if code != exitOK {
		t.Fatalf("dry-run: exit %d (stdout=%q)", code, out)
	}
	if writes := js.writeReqsTo("/rest/api/2/issue"); len(writes) != 0 {
		t.Fatalf("dry-run must not PUT, got %d writes", len(writes))
	}
	if !strings.Contains(out, `"dry_run": true`) {
		t.Errorf("output missing dry_run marker: %q", out)
	}
}

// TestJiraEdit_NoMatchExit4: a missed needle is a refusal (exit 4), not an
// overwrite, and nothing is written.
func TestJiraEdit_NoMatchExit4(t *testing.T) {
	js := editServer(t)

	_, code := runCLI(t, jiraEnv(js.srv),
		"jira", "issue", "edit", "ENG-7", "--old", "no such text", "--new", "x")
	if code != exitNotFound {
		t.Fatalf("no-match: exit %d, want %d", code, exitNotFound)
	}
	if writes := js.writeReqsTo("/rest/api/2/issue"); len(writes) != 0 {
		t.Fatalf("no-match must not PUT, got %d writes", len(writes))
	}
}

// TestJiraEdit_AmbiguousExit2: two matches without --all refuse with exit 2.
func TestJiraEdit_AmbiguousExit2(t *testing.T) {
	js := editServer(t)

	_, code := runCLI(t, jiraEnv(js.srv),
		"jira", "issue", "edit", "ENG-7", "--old", "h2. ", "--new", "h3. ")
	if code != exitUsage {
		t.Fatalf("ambiguous: exit %d, want %d", code, exitUsage)
	}
	if writes := js.writeReqsTo("/rest/api/2/issue"); len(writes) != 0 {
		t.Fatalf("ambiguous must not PUT, got %d writes", len(writes))
	}
}

// TestJiraEdit_AllReplacesEvery: --all lifts the uniqueness requirement.
func TestJiraEdit_All(t *testing.T) {
	js := editServer(t)

	_, code := runCLI(t, jiraEnv(js.srv),
		"jira", "issue", "edit", "ENG-7", "--old", "h2. ", "--new", "h3. ", "--all")
	if code != exitOK {
		t.Fatalf("--all: exit %d", code)
	}
	writes := js.writeReqsTo("/rest/api/2/issue/ENG-7")
	if len(writes) != 1 {
		t.Fatalf("expected 1 PUT, got %d", len(writes))
	}
	if got := jiraFields(t, writes[0].body)["description"]; got != "h3. Params\n\n* timeout = 300\n\nh3. Check" {
		t.Fatalf("description = %q", got)
	}
}

// TestJiraEdit_DeleteWithEmptyNew: --new ” deletes the matched text.
func TestJiraEdit_DeleteWithEmptyNew(t *testing.T) {
	js := editServer(t)

	_, code := runCLI(t, jiraEnv(js.srv),
		"jira", "issue", "edit", "ENG-7", "--old", "* timeout = 300\n\n", "--new", "")
	if code != exitOK {
		t.Fatalf("delete: exit %d", code)
	}
	writes := js.writeReqsTo("/rest/api/2/issue/ENG-7")
	if len(writes) != 1 {
		t.Fatalf("expected 1 PUT, got %d", len(writes))
	}
	if got := jiraFields(t, writes[0].body)["description"]; got != "h2. Params\n\nh2. Check" {
		t.Fatalf("description = %q", got)
	}
}

// TestJiraEdit_ClearWholeDescription: matching the entire body with --new ”
// sends description:"" on the wire (clears it), not a no-op PUT.
func TestJiraEdit_ClearWholeDescription(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/api/2/issue/ENG-9", http.StatusOK, `{"key":"ENG-9","fields":{"description":"obsolete text"}}`)
	js.route(http.MethodPut, "/rest/api/2/issue/ENG-9", http.StatusNoContent, ``)

	_, code := runCLI(t, jiraEnv(js.srv),
		"jira", "issue", "edit", "ENG-9", "--old", "obsolete text", "--new", "")
	if code != exitOK {
		t.Fatalf("clear: exit %d", code)
	}
	writes := js.writeReqsTo("/rest/api/2/issue/ENG-9")
	if len(writes) != 1 {
		t.Fatalf("expected 1 PUT, got %d", len(writes))
	}
	fl := jiraFields(t, writes[0].body)
	got, ok := fl["description"]
	if !ok || got != "" {
		t.Fatalf("description = %v (present=%v), want empty string", got, ok)
	}
}

// TestJiraEdit_CrossLineWhitespaceExit8: a whitespace-pass match crossing a
// line break refuses with exit 8 and writes nothing.
func TestJiraEdit_CrossLineWhitespaceExit8(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/api/2/issue/ENG-9", http.StatusOK, `{"key":"ENG-9","fields":{"description":"h2. Verify\nsteps here"}}`)

	_, code := runCLI(t, jiraEnv(js.srv),
		"jira", "issue", "edit", "ENG-9", "--old", "Verify steps", "--new", "Checked")
	if code != exitCheckFailed {
		t.Fatalf("cross-line: exit %d, want %d", code, exitCheckFailed)
	}
	if writes := js.writeReqsTo("/rest/api/2/issue"); len(writes) != 0 {
		t.Fatalf("cross-line refusal must not PUT, got %d writes", len(writes))
	}
}

// TestJiraEdit_EmptyDescriptionExit4: nothing to edit is a not-found refusal
// with a pointer to issue update.
func TestJiraEdit_EmptyDescriptionExit4(t *testing.T) {
	js := newJiraServer(t)
	js.route(http.MethodGet, "/rest/api/2/issue/ENG-8", http.StatusOK, `{"key":"ENG-8","fields":{}}`)

	_, code := runCLI(t, jiraEnv(js.srv),
		"jira", "issue", "edit", "ENG-8", "--old", "x", "--new", "y")
	if code != exitNotFound {
		t.Fatalf("empty description: exit %d, want %d", code, exitNotFound)
	}
	if writes := js.writeReqsTo("/rest/api/2/issue"); len(writes) != 0 {
		t.Fatalf("empty description must not PUT, got %d writes", len(writes))
	}
}

// TestJiraEdit_FlagValidation: missing/conflicting flags exit 2 before any
// HTTP traffic.
func TestJiraEdit_FlagValidation(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"missing old", []string{"jira", "issue", "edit", "ENG-7", "--new", "x"}},
		{"empty old", []string{"jira", "issue", "edit", "ENG-7", "--old", "", "--new", "x"}},
		{"missing new", []string{"jira", "issue", "edit", "ENG-7", "--old", "x"}},
		{"old and old-file", []string{"jira", "issue", "edit", "ENG-7", "--old", "x", "--old-file", "f", "--new", "y"}},
		{"new and new-file", []string{"jira", "issue", "edit", "ENG-7", "--old", "x", "--new", "y", "--new-file", "f"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			js := newJiraServer(t)
			_, code := runCLI(t, jiraEnv(js.srv), tc.args...)
			if code != exitUsage {
				t.Fatalf("exit %d, want %d", code, exitUsage)
			}
			if n := len(js.requests()); n != 0 {
				t.Fatalf("flag validation must not hit the backend, got %d requests", n)
			}
		})
	}
}

// TestJiraEdit_NewFileStripsOneNewline: --new-file drops exactly one trailing
// newline (editor/Write-tool artifact), like conf edit.
func TestJiraEdit_NewFileStripsOneNewline(t *testing.T) {
	js := editServer(t)
	nf := filepath.Join(t.TempDir(), "new.txt")
	if err := os.WriteFile(nf, []byte("timeout = 600\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, code := runCLI(t, jiraEnv(js.srv),
		"jira", "issue", "edit", "ENG-7", "--old", "timeout = 300", "--new-file", nf)
	if code != exitOK {
		t.Fatalf("--new-file: exit %d", code)
	}
	writes := js.writeReqsTo("/rest/api/2/issue/ENG-7")
	if len(writes) != 1 {
		t.Fatalf("expected 1 PUT, got %d", len(writes))
	}
	if got := jiraFields(t, writes[0].body)["description"]; got != "h2. Params\n\n* timeout = 600\n\nh2. Check" {
		t.Fatalf("description = %q", got)
	}
}
