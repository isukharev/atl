package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

// recordingTracker embeds Tracker so only the methods a test needs are
// implemented; it records call-through args and returns canned values/errors.
type recordingTracker struct {
	domain.Tracker

	// recorded args
	issueKey     string
	issueFields  []string
	searchJQL    string
	searchFields []string
	searchLimit  int
	createProj   string
	createType   string
	createSumm   string
	createBody   []byte
	createFields map[string]string
	updateKey    string
	updateSumm   string
	updateBody   []byte
	updateFields map[string]string
	transKey     string
	transTo      string
	transComment string
	commentKey   string
	commentBody  []byte
	linkFrom     string
	linkTo       string
	linkType     string
	epicIssue    string
	epicEpic     string
	foProject    string
	foType       string
	foField      string
	transitsKey  string

	// canned returns
	issue       *domain.Issue
	issues      []domain.Issue
	comment     *domain.Comment
	fieldDefs   []domain.FieldDef
	fieldOpts   []string
	transitions []domain.TransitionDef
	linkTypes   []string
	err         error
}

func (t *recordingTracker) GetIssue(_ context.Context, key string, fields []string) (*domain.Issue, error) {
	t.issueKey, t.issueFields = key, fields
	return t.issue, t.err
}

func (t *recordingTracker) Search(_ context.Context, jql string, fields []string, limit int, _ string) ([]domain.Issue, string, error) {
	t.searchJQL, t.searchFields, t.searchLimit = jql, fields, limit
	return t.issues, "", t.err
}

func (t *recordingTracker) Create(_ context.Context, project, issueType, summary string, body []byte, fields map[string]string) (*domain.Issue, error) {
	t.createProj, t.createType, t.createSumm, t.createBody, t.createFields = project, issueType, summary, body, fields
	return t.issue, t.err
}

func (t *recordingTracker) Update(_ context.Context, key, summary string, body []byte, fields map[string]string) error {
	t.updateKey, t.updateSumm, t.updateBody, t.updateFields = key, summary, body, fields
	return t.err
}

func (t *recordingTracker) Transition(_ context.Context, key, to, comment string, _ map[string]string) error {
	t.transKey, t.transTo, t.transComment = key, to, comment
	return t.err
}

func (t *recordingTracker) AddComment(_ context.Context, key string, body []byte) (*domain.Comment, error) {
	t.commentKey, t.commentBody = key, body
	return t.comment, t.err
}

func (t *recordingTracker) Link(_ context.Context, from, to, linkType string) error {
	t.linkFrom, t.linkTo, t.linkType = from, to, linkType
	return t.err
}

func (t *recordingTracker) LinkEpic(_ context.Context, issue, epic string) error {
	t.epicIssue, t.epicEpic = issue, epic
	return t.err
}

func (t *recordingTracker) Fields(context.Context) ([]domain.FieldDef, error) {
	return t.fieldDefs, t.err
}

func (t *recordingTracker) FieldOptions(_ context.Context, project, issueType, field string) ([]string, error) {
	t.foProject, t.foType, t.foField = project, issueType, field
	return t.fieldOpts, t.err
}

func (t *recordingTracker) Transitions(_ context.Context, key string) ([]domain.TransitionDef, error) {
	t.transitsKey = key
	return t.transitions, t.err
}

func (t *recordingTracker) LinkTypes(context.Context) ([]string, error) {
	return t.linkTypes, t.err
}

func TestJiraWrappersPassThrough(t *testing.T) {
	ctx := context.Background()

	t.Run("Issue", func(t *testing.T) {
		tr := &recordingTracker{issue: &domain.Issue{Key: "PROJ-1"}}
		svc := &JiraService{tr: tr}
		got, err := svc.Issue(ctx, "PROJ-1", []string{"summary"})
		if err != nil {
			t.Fatal(err)
		}
		if tr.issueKey != "PROJ-1" || len(tr.issueFields) != 1 || got.Key != "PROJ-1" {
			t.Errorf("Issue args/return: key=%q fields=%v ret=%+v", tr.issueKey, tr.issueFields, got)
		}
	})

	t.Run("Search", func(t *testing.T) {
		tr := &recordingTracker{issues: []domain.Issue{{Key: "A-1"}}}
		svc := &JiraService{tr: tr}
		got, _, err := svc.Search(ctx, "project = A", []string{"status"}, 50, "cur")
		if err != nil {
			t.Fatal(err)
		}
		if tr.searchJQL != "project = A" || tr.searchLimit != 50 || len(got) != 1 {
			t.Errorf("Search args/return: jql=%q limit=%d ret=%+v", tr.searchJQL, tr.searchLimit, got)
		}
	})

	t.Run("Create", func(t *testing.T) {
		tr := &recordingTracker{issue: &domain.Issue{Key: "NEW-1"}}
		svc := &JiraService{tr: tr}
		got, err := svc.Create(ctx, "PRJ", "Bug", "Boom", []byte("desc"), map[string]string{"prio": "High"})
		if err != nil {
			t.Fatal(err)
		}
		if tr.createProj != "PRJ" || tr.createType != "Bug" || tr.createSumm != "Boom" ||
			string(tr.createBody) != "desc" || tr.createFields["prio"] != "High" || got.Key != "NEW-1" {
			t.Errorf("Create args/return not forwarded: %+v ret=%+v", tr, got)
		}
	})

	t.Run("Update", func(t *testing.T) {
		tr := &recordingTracker{}
		svc := &JiraService{tr: tr}
		if err := svc.Update(ctx, "K-1", "newsum", []byte("nb"), map[string]string{"a": "b"}); err != nil {
			t.Fatal(err)
		}
		if tr.updateKey != "K-1" || tr.updateSumm != "newsum" || string(tr.updateBody) != "nb" || tr.updateFields["a"] != "b" {
			t.Errorf("Update args not forwarded: %+v", tr)
		}
	})

	t.Run("Transition", func(t *testing.T) {
		tr := &recordingTracker{}
		svc := &JiraService{tr: tr}
		if err := svc.Transition(ctx, "K-2", "Done", "lgtm", nil); err != nil {
			t.Fatal(err)
		}
		if tr.transKey != "K-2" || tr.transTo != "Done" || tr.transComment != "lgtm" {
			t.Errorf("Transition args not forwarded: %+v", tr)
		}
	})

	t.Run("Comment", func(t *testing.T) {
		tr := &recordingTracker{comment: &domain.Comment{ID: "c9"}}
		svc := &JiraService{tr: tr}
		got, err := svc.Comment(ctx, "K-3", []byte("body"))
		if err != nil {
			t.Fatal(err)
		}
		if tr.commentKey != "K-3" || string(tr.commentBody) != "body" || got.ID != "c9" {
			t.Errorf("Comment args/return not forwarded: %+v ret=%+v", tr, got)
		}
	})

	t.Run("Link", func(t *testing.T) {
		tr := &recordingTracker{}
		svc := &JiraService{tr: tr}
		if err := svc.Link(ctx, "A-1", "B-2", "blocks"); err != nil {
			t.Fatal(err)
		}
		if tr.linkFrom != "A-1" || tr.linkTo != "B-2" || tr.linkType != "blocks" {
			t.Errorf("Link args not forwarded: %+v", tr)
		}
	})

	t.Run("LinkEpic", func(t *testing.T) {
		tr := &recordingTracker{}
		svc := &JiraService{tr: tr}
		if err := svc.LinkEpic(ctx, "S-1", "EPIC-9"); err != nil {
			t.Fatal(err)
		}
		if tr.epicIssue != "S-1" || tr.epicEpic != "EPIC-9" {
			t.Errorf("LinkEpic args not forwarded: %+v", tr)
		}
	})

	t.Run("Fields", func(t *testing.T) {
		tr := &recordingTracker{fieldDefs: []domain.FieldDef{{ID: "f1"}}}
		svc := &JiraService{tr: tr}
		got, err := svc.Fields(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].ID != "f1" {
			t.Errorf("Fields return not propagated: %+v", got)
		}
	})

	t.Run("FieldOptions", func(t *testing.T) {
		tr := &recordingTracker{fieldOpts: []string{"o1", "o2"}}
		svc := &JiraService{tr: tr}
		got, err := svc.FieldOptions(ctx, "PRJ", "Story", "prio")
		if err != nil {
			t.Fatal(err)
		}
		if tr.foProject != "PRJ" || tr.foType != "Story" || tr.foField != "prio" || len(got) != 2 {
			t.Errorf("FieldOptions args/return not forwarded: %+v ret=%v", tr, got)
		}
	})

	t.Run("Transitions", func(t *testing.T) {
		tr := &recordingTracker{transitions: []domain.TransitionDef{{ID: "t1"}}}
		svc := &JiraService{tr: tr}
		got, err := svc.Transitions(ctx, "K-7")
		if err != nil {
			t.Fatal(err)
		}
		if tr.transitsKey != "K-7" || len(got) != 1 {
			t.Errorf("Transitions args/return not forwarded: key=%q ret=%+v", tr.transitsKey, got)
		}
	})

	t.Run("LinkTypes", func(t *testing.T) {
		tr := &recordingTracker{linkTypes: []string{"blocks", "relates"}}
		svc := &JiraService{tr: tr}
		got, err := svc.LinkTypes(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 {
			t.Errorf("LinkTypes return not propagated: %v", got)
		}
	})
}

// A sentinel error from the Tracker must propagate unchanged through the wrappers.
func TestJiraWrappersPropagateSentinel(t *testing.T) {
	ctx := context.Background()
	tr := &recordingTracker{err: domain.ErrNotFound}
	svc := &JiraService{tr: tr}

	if _, err := svc.Issue(ctx, "x", nil); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Issue did not propagate sentinel: %v", err)
	}
	if _, _, err := svc.Search(ctx, "x", nil, 1, ""); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Search did not propagate sentinel: %v", err)
	}
	if _, err := svc.Create(ctx, "p", "Bug", "s", nil, nil); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Create did not propagate sentinel: %v", err)
	}
	if err := svc.Update(ctx, "x", "s", nil, nil); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Update did not propagate sentinel: %v", err)
	}
	if err := svc.Transition(ctx, "x", "Done", "", nil); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Transition did not propagate sentinel: %v", err)
	}
	if _, err := svc.Comment(ctx, "x", nil); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Comment did not propagate sentinel: %v", err)
	}
	if err := svc.Link(ctx, "a", "b", "blocks"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Link did not propagate sentinel: %v", err)
	}
	if err := svc.LinkEpic(ctx, "a", "e"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("LinkEpic did not propagate sentinel: %v", err)
	}
	if _, err := svc.Fields(ctx); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Fields did not propagate sentinel: %v", err)
	}
	if _, err := svc.FieldOptions(ctx, "p", "t", "f"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("FieldOptions did not propagate sentinel: %v", err)
	}
	if _, err := svc.Transitions(ctx, "x"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Transitions did not propagate sentinel: %v", err)
	}
	if _, err := svc.LinkTypes(ctx); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("LinkTypes did not propagate sentinel: %v", err)
	}
}

// ---- renderIssueMarkdown branches ----

func TestRenderIssueMarkdownFull(t *testing.T) {
	is := &domain.Issue{
		Key:      "PROJ-42",
		Summary:  "Fix the thing",
		Status:   "In Progress",
		Type:     "Bug",
		Project:  "PROJ",
		Assignee: "alice",
		Labels:   []string{"backend", "urgent"},
		Body:     "h1. Heading\n\nNative *wiki* body with [a link|http://x].",
		Links: []domain.IssueLink{
			{Type: "blocks", Key: "PROJ-7"},
			{Type: "relates to", Key: "PROJ-8"},
		},
		Comments: []domain.Comment{
			{Author: "bob", Created: "2026-01-01", Body: "first comment"},
			{Author: "carol", Created: "2026-01-02", Body: "second comment"},
		},
	}
	got := string(renderIssueMarkdown(is, nil))

	mustContain(t, got, "key: PROJ-42")
	mustContain(t, got, "status: In Progress")
	mustContain(t, got, "type: Bug")
	mustContain(t, got, "project: PROJ")
	mustContain(t, got, "assignee: alice")
	mustContain(t, got, "labels: [backend, urgent]")
	mustContain(t, got, "# PROJ-42 — Fix the thing")
	mustContain(t, got, "## Description (Jira wiki)")
	// the native wiki body must appear verbatim, not converted
	mustContain(t, got, "h1. Heading\n\nNative *wiki* body with [a link|http://x].")
	mustContain(t, got, "## Links")
	mustContain(t, got, "- blocks PROJ-7")
	mustContain(t, got, "- relates to PROJ-8")
	mustContain(t, got, "## Comments")
	mustContain(t, got, "**bob** (2026-01-01):")
	mustContain(t, got, "first comment")
	mustContain(t, got, "**carol** (2026-01-02):")
}

func TestRenderIssueMarkdownMinimal(t *testing.T) {
	// No description, no assignee, no labels, no links, no comments: the optional
	// sections must be omitted entirely.
	is := &domain.Issue{
		Key:     "MIN-1",
		Summary: "Bare issue",
		Status:  "Open",
		Type:    "Task",
		Project: "MIN",
	}
	got := string(renderIssueMarkdown(is, nil))

	mustContain(t, got, "key: MIN-1")
	mustContain(t, got, "# MIN-1 — Bare issue")
	mustNotContain(t, got, "assignee:")
	mustNotContain(t, got, "labels:")
	mustNotContain(t, got, "## Description")
	mustNotContain(t, got, "## Links")
	mustNotContain(t, got, "## Comments")
}

// A summary containing YAML-significant characters must be quoted/escaped in the
// frontmatter so the file stays valid YAML.
func TestRenderIssueMarkdownYAMLEscape(t *testing.T) {
	is := &domain.Issue{
		Key:      "Q-1",
		Summary:  `Title: with "quotes" and # hash`,
		Status:   "Open",
		Type:     "Task",
		Project:  "Q",
		Assignee: "name: with colon",
	}
	got := string(renderIssueMarkdown(is, nil))
	mustContain(t, got, `summary: "Title: with \"quotes\" and # hash"`)
	mustContain(t, got, `assignee: "name: with colon"`)
	// but the H1 heading uses the raw summary (not the escaped form)
	mustContain(t, got, `# Q-1 — Title: with "quotes" and # hash`)
}

func TestYamlEscape(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain", "plain"},
		{"has: colon", `"has: colon"`},
		{"has # hash", `"has # hash"`},
		{"has \"quote\"", `"has \"quote\""`},
		{"has 'apostrophe'", `"has 'apostrophe'"`},
		{"line\nbreak", "\"line\nbreak\""},
	}
	for _, c := range cases {
		if got := yamlEscape(c.in); got != c.want {
			t.Errorf("yamlEscape(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Pull writes the rendered markdown verbatim to disk and a sibling identity
// snapshot with Jira fields.
func TestJiraPullWritesMarkdownAndJSON(t *testing.T) {
	into := t.TempDir()
	tr := partialTracker{issues: []domain.Issue{
		{ID: "10001", Key: "PROJ-1", Project: "PROJ", Summary: "S", Status: "Open", Type: "Task", Body: "wiki body here", Fields: map[string]any{"customfield_1": "x"}},
	}}
	svc := &JiraService{tr: tr}
	res, err := svc.Pull(context.Background(), JiraPullOpts{JQL: "project = PROJ", Into: into, Limit: 1, Fields: []string{"customfield_1"}})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	out := res.Issues
	if len(out) != 1 || out[0].Key != "PROJ-1" {
		t.Fatalf("unexpected pull result: %+v", out)
	}
	md, err := os.ReadFile(filepath.Join(into, out[0].Path))
	if err != nil {
		t.Fatalf("md not written: %v", err)
	}
	mustContain(t, string(md), "wiki body here")
	jsonPath := strings.TrimSuffix(filepath.Join(into, out[0].Path), ".md") + ".json"
	jb, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatalf("expected sibling json at %s: %v", jsonPath, err)
	}
	var snap JiraIssueSnapshot
	if err := json.Unmarshal(jb, &snap); err != nil {
		t.Fatalf("decode snapshot: %v\n%s", err, jb)
	}
	if snap.Key != "PROJ-1" || snap.ID != "10001" || snap.Fields["customfield_1"] != "x" {
		t.Errorf("snapshot = %+v, want key/id/custom field", snap)
	}
}

// countingPullTracker serves a search projection and counts per-issue
// re-fetches, which Pull must never make (#65: the projection already carries
// the same fields through the same adapter mapping).
type countingPullTracker struct {
	domain.Tracker
	issues   []domain.Issue
	getCalls int
}

func (t *countingPullTracker) Search(context.Context, string, []string, int, string) ([]domain.Issue, string, error) {
	return t.issues, "", nil
}

func (t *countingPullTracker) GetIssue(context.Context, string, []string) (*domain.Issue, error) {
	t.getCalls++
	return nil, errors.New("unexpected per-issue GetIssue during pull")
}

// Pull consumes the search projection directly — one HTTP request per search
// page, zero per-issue re-fetches.
func TestJiraPullDoesNotRefetchPerIssue(t *testing.T) {
	into := t.TempDir()
	tr := &countingPullTracker{issues: []domain.Issue{
		{ID: "1", Key: "PROJ-1", Project: "PROJ", Summary: "a", Body: "body one"},
		{ID: "2", Key: "PROJ-2", Project: "PROJ", Summary: "b", Body: "body two"},
	}}
	svc := &JiraService{tr: tr}
	res, err := svc.Pull(context.Background(), JiraPullOpts{JQL: "project = PROJ", Into: into, Limit: 0})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	out := res.Issues
	if tr.getCalls != 0 {
		t.Fatalf("pull made %d per-issue GetIssue calls, want 0 (search projection suffices)", tr.getCalls)
	}
	if len(out) != 2 {
		t.Fatalf("pulled %d issues, want 2: %+v", len(out), out)
	}
	for _, p := range out {
		if _, err := os.Stat(filepath.Join(into, p.Path)); err != nil {
			t.Errorf("missing %s: %v", p.Path, err)
		}
	}
}

// A snapshot (.json) write failure fails the pull loudly — a disk-full run
// must not report issues as pulled with missing/stale snapshots.
func TestJiraPullSnapshotWriteFailureAborts(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission-based failure injection is a no-op as root")
	}
	into := t.TempDir()
	dir := filepath.Join(into, "PROJ")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Pre-plant a read-only snapshot so the refresh write fails.
	if err := os.WriteFile(filepath.Join(dir, "PROJ-1.json"), []byte("{}"), 0o444); err != nil {
		t.Fatal(err)
	}
	tr := &countingPullTracker{issues: []domain.Issue{
		{ID: "1", Key: "PROJ-1", Project: "PROJ", Summary: "a", Body: "b"},
	}}
	svc := &JiraService{tr: tr}
	_, err := svc.Pull(context.Background(), JiraPullOpts{JQL: "project = PROJ", Into: into, Limit: 0})
	if err == nil || !strings.Contains(err.Error(), "snapshot PROJ-1") {
		t.Fatalf("snapshot write failure must abort the pull, got err=%v", err)
	}
}

func mustContain(t *testing.T, hay, needle string) {
	t.Helper()
	if !strings.Contains(hay, needle) {
		t.Errorf("output missing %q\n--- full ---\n%s", needle, hay)
	}
}

func mustNotContain(t *testing.T, hay, needle string) {
	t.Helper()
	if strings.Contains(hay, needle) {
		t.Errorf("output unexpectedly contains %q\n--- full ---\n%s", needle, hay)
	}
}
