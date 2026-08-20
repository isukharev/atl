package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
)

// recordingTracker embeds Tracker so only the methods a test needs are
// implemented; it records call-through args and returns canned values/errors.
type recordingTracker struct {
	domain.Tracker

	// recorded args
	issueKey       string
	issueFields    []string
	searchJQL      string
	searchFields   []string
	searchLimit    int
	createProj     string
	createType     string
	createSumm     string
	createBody     []byte
	createFields   map[string]domain.JiraFieldInput
	createSingle   bool
	createRedacted bool
	updateKey      string
	updateSumm     string
	updateBody     []byte
	updateFields   map[string]domain.JiraFieldInput
	transKey       string
	transTo        string
	transComment   string
	commentKey     string
	commentBody    []byte
	linkFrom       string
	linkTo         string
	linkType       string
	epicIssue      string
	epicEpic       string
	foProject      string
	foType         string
	foField        string
	transitsKey    string

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

type configuredEpicTracker struct {
	*recordingTracker
	resolvedField string
}

func (t *configuredEpicTracker) LinkEpicWithField(_ context.Context, issue, epic, fieldID string) error {
	t.epicIssue, t.epicEpic, t.resolvedField = issue, epic, fieldID
	return t.err
}

func (t *recordingTracker) GetIssue(_ context.Context, key string, fields []string) (*domain.Issue, error) {
	t.issueKey, t.issueFields = key, fields
	return t.issue, t.err
}

func (t *recordingTracker) Search(_ context.Context, jql string, fields []string, limit int, _ string) ([]domain.Issue, string, error) {
	t.searchJQL, t.searchFields, t.searchLimit = jql, fields, limit
	return t.issues, "", t.err
}

func (t *recordingTracker) Create(ctx context.Context, project, issueType, summary string, body []byte, fields map[string]domain.JiraFieldInput) (*domain.Issue, error) {
	t.createProj, t.createType, t.createSumm, t.createBody, t.createFields = project, issueType, summary, body, fields
	t.createSingle = domain.SingleAttempt(ctx)
	t.createRedacted = domain.RedactedHTTPTrace(ctx)
	return t.issue, t.err
}

type createMetadataRecordingTracker struct {
	*recordingTracker
	issueTypes    []domain.JiraIssueType
	metadataErr   error
	metadataCalls int
}

func (t *createMetadataRecordingTracker) ReadCreateIssueTypes(context.Context, string) ([]domain.JiraIssueType, error) {
	t.metadataCalls++
	return t.issueTypes, t.metadataErr
}

func (t *recordingTracker) Update(_ context.Context, key, summary string, body []byte, fields map[string]domain.JiraFieldInput) error {
	t.updateKey, t.updateSumm, t.updateBody, t.updateFields = key, summary, body, fields
	return t.err
}

func (t *recordingTracker) Transition(_ context.Context, key, to, comment string, _ map[string]domain.JiraFieldInput) error {
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
		tr := &createMetadataRecordingTracker{
			recordingTracker: &recordingTracker{issue: &domain.Issue{Key: "NEW-1"}},
			issueTypes:       []domain.JiraIssueType{{ID: "17", Name: "Bug"}},
		}
		svc := &JiraService{tr: tr}
		got, err := svc.Create(ctx, "PRJ", "Bug", "Boom", []byte("desc"), map[string]domain.JiraFieldInput{"prio": {Value: "High"}})
		if err != nil {
			t.Fatal(err)
		}
		if tr.createProj != "PRJ" || tr.createType != "17" || tr.createSumm != "Boom" ||
			string(tr.createBody) != "desc" || tr.createFields["prio"].Value != "High" || got.Key != "NEW-1" || got.Type != "Bug" ||
			!tr.createSingle || !tr.createRedacted {
			t.Errorf("Create args/return not forwarded: %+v ret=%+v", tr, got)
		}
	})

	t.Run("Update", func(t *testing.T) {
		tr := &recordingTracker{}
		svc := &JiraService{tr: tr}
		if err := svc.Update(ctx, "K-1", "newsum", []byte("nb"), map[string]domain.JiraFieldInput{"a": {Value: "b"}}); err != nil {
			t.Fatal(err)
		}
		if tr.updateKey != "K-1" || tr.updateSumm != "newsum" || string(tr.updateBody) != "nb" || tr.updateFields["a"].Value != "b" {
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

	t.Run("LinkEpic configured field", func(t *testing.T) {
		tr := &configuredEpicTracker{recordingTracker: &recordingTracker{}}
		cfg := &config.Config{Render: &config.RenderConfig{Jira: &config.RenderService{EpicField: "customfield_42"}}}
		svc := &JiraService{tr: tr, cfg: cfg}
		if err := svc.LinkEpic(ctx, "S-1", "EPIC-9"); err != nil {
			t.Fatal(err)
		}
		if tr.resolvedField != "customfield_42" || tr.epicIssue != "S-1" || tr.epicEpic != "EPIC-9" {
			t.Fatalf("configured Epic Link args not forwarded: %+v", tr)
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
	createTR := &createMetadataRecordingTracker{recordingTracker: tr, issueTypes: []domain.JiraIssueType{{ID: "17", Name: "Bug"}}}
	createSvc := &JiraService{tr: createTR}
	if _, err := createSvc.Create(ctx, "p", "Bug", "s", nil, nil); !errors.Is(err, domain.ErrCheckFailed) {
		t.Errorf("Create did not classify an unqualified write error: %v", err)
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

type createHTTPStatusError struct{ status int }

func (e createHTTPStatusError) Error() string   { return "create request failed" }
func (e createHTTPStatusError) HTTPStatus() int { return e.status }

func TestJiraCreateResolvesTypeAndClassifiesOutcome(t *testing.T) {
	types := []domain.JiraIssueType{
		{ID: "10", Name: "Task"},
		{ID: "11", Name: "Story"},
		{ID: "12", Name: "Story"},
		{ID: "13", Name: "Задача"},
	}
	newService := func() (*JiraService, *createMetadataRecordingTracker) {
		tracker := &createMetadataRecordingTracker{
			recordingTracker: &recordingTracker{issue: &domain.Issue{Key: "PROJ-1"}},
			issueTypes:       types,
		}
		return &JiraService{tr: tracker}, tracker
	}

	for _, test := range []struct {
		selector, wantID, wantName string
	}{
		{selector: "10", wantID: "10", wantName: "Task"},
		{selector: "Task", wantID: "10", wantName: "Task"},
		{selector: "13", wantID: "13", wantName: "Задача"},
		{selector: "Задача", wantID: "13", wantName: "Задача"},
	} {
		t.Run("resolved "+test.selector, func(t *testing.T) {
			svc, tracker := newService()
			issue, err := svc.Create(context.Background(), "PROJ", test.selector, "Summary", nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if tracker.createType != test.wantID || issue.Type != test.wantName || !tracker.createSingle || !tracker.createRedacted {
				t.Fatalf("create type/result/request policy = %q/%q/%t/%t, want %s/%s/true/true", tracker.createType, issue.Type, tracker.createSingle, tracker.createRedacted, test.wantID, test.wantName)
			}
		})
	}

	for _, selector := range []string{"Missing", "Story"} {
		t.Run("preflight "+selector, func(t *testing.T) {
			svc, tracker := newService()
			_, err := svc.Create(context.Background(), "PROJ", selector, "Summary", nil, nil)
			if !errors.Is(err, domain.ErrNotFound) && !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("create error = %v, want selector rejection", err)
			}
			if tracker.createType != "" {
				t.Fatalf("create request ran with type %q after invalid selector", tracker.createType)
			}
		})
	}

	t.Run("definitive rejection", func(t *testing.T) {
		svc, tracker := newService()
		cause := createHTTPStatusError{status: 400}
		tracker.err = cause
		_, err := svc.Create(context.Background(), "PROJ", "Task", "Summary", nil, nil)
		if !errors.Is(err, cause) || errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("create error = %v, want definitive rejection without ambiguous outcome", err)
		}
		if tracker.createType != "10" {
			t.Fatalf("create attempts = %q, want one resolved request", tracker.createType)
		}
	})

	t.Run("ambiguous outcome", func(t *testing.T) {
		svc, tracker := newService()
		tracker.err = errors.New("transport unavailable")
		_, err := svc.Create(context.Background(), "PROJ", "Task", "Summary", nil, nil)
		if !errors.Is(err, domain.ErrCheckFailed) || !strings.Contains(err.Error(), "outcome is unknown") {
			t.Fatalf("create error = %v, want ambiguous outcome classification", err)
		}
		if tracker.createType != "10" {
			t.Fatalf("create attempts = %q, want one resolved request", tracker.createType)
		}
	})

	t.Run("type override is rejected before metadata or create", func(t *testing.T) {
		svc, tracker := newService()
		_, err := svc.Create(context.Background(), "PROJ", "Task", "Summary", nil, map[string]domain.JiraFieldInput{
			"issuetype": {Value: `{"id":"10"}`},
		})
		if !errors.Is(err, domain.ErrUsage) || tracker.metadataCalls != 0 || tracker.createType != "" {
			t.Fatalf("create error/metadata/create = %v/%d/%q, want usage error and no request", err, tracker.metadataCalls, tracker.createType)
		}
	})
}

// ---- renderIssueMarkdown branches ----

// jiraRS builds the resolved render settings for a profile (no include/exclude),
// the way the pull/render paths do, for renderIssueMarkdown unit tests.
func jiraRS(profile string) RenderSettings {
	rs, _ := computeSettings("jira", config.RenderService{Profile: profile})
	return rs
}

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
	got := string(renderIssueMarkdown(is, nil, jiraRS("default")))

	mustContain(t, got, "| Key | PROJ-42 |")
	mustContain(t, got, "| Status | In Progress |")
	mustContain(t, got, "| Type | Bug |")
	mustContain(t, got, "| Project | PROJ |")
	mustContain(t, got, "| Assignee | alice |")
	mustContain(t, got, "| Labels | backend, urgent |")
	mustContain(t, got, "# PROJ-42 — Fix the thing")
	// The .md is now a rendered read view: the section header is a plain
	// "# Description" and the wiki body is converted to markdown (the verbatim
	// wiki lives in the sibling <KEY>.wiki file, not here).
	mustContain(t, got, "# Description\n")
	mustNotContain(t, got, "# Description (Jira wiki)")
	mustContain(t, got, "\n## Heading\n")                                // Jira h1 nests below generated Description
	mustContain(t, got, "Native **wiki** body with [a link](http://x).") // *wiki*/[a|b] converted
	mustNotContain(t, got, "h1. Heading")                                // raw wiki heading gone
	mustNotContain(t, got, "[a link|http://x]")                          // raw wiki link gone
	mustContain(t, got, "# Links")
	mustContain(t, got, "- blocks PROJ-7")
	mustContain(t, got, "- relates to PROJ-8")
	mustContain(t, got, "# Comments")
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
	got := string(renderIssueMarkdown(is, nil, jiraRS("default")))

	mustContain(t, got, "| Key | MIN-1 |")
	mustContain(t, got, "# MIN-1 — Bare issue")
	mustNotContain(t, got, "| Assignee |")
	mustNotContain(t, got, "| Labels |")
	mustContain(t, got, "# Description")
	mustNotContain(t, got, "# Links")
	mustNotContain(t, got, "# Comments")
}

// Metadata is a valid Markdown table even when values contain table delimiters
// or physical line breaks.
func TestRenderIssueMarkdownMetadataEscaping(t *testing.T) {
	is := &domain.Issue{
		Key:      "Q-1",
		Summary:  "Title | with quotes\nand hash",
		Status:   "Open",
		Type:     "Task",
		Project:  "Q",
		Assignee: "name | with pipe",
	}
	got := string(renderIssueMarkdown(is, nil, jiraRS("default")))
	mustContain(t, got, `| Summary | Title &#124; with quotes and hash |`)
	mustContain(t, got, `| Assignee | name &#124; with pipe |`)
	mustContain(t, got, "# Q-1 — Title | with quotes and hash")
}

// Pull writes the rendered markdown verbatim to disk and a sibling identity
// snapshot with Jira fields.
func TestJiraPullWritesMarkdownAndJSON(t *testing.T) {
	into := t.TempDir()
	tr := partialTracker{issues: []domain.Issue{
		{ID: "10001", Key: "PROJ-1", Project: "PROJ", Summary: "S", Status: "Open", Type: "Task", Body: "wiki body here", Fields: map[string]any{"customfield_1": "x"}},
	}}
	svc := &JiraService{tr: tr, baseURL: jiraMirrorTestBackendURL}
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
	svc := &JiraService{tr: tr, baseURL: jiraMirrorTestBackendURL}
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
	into := t.TempDir()
	// This fixture intentionally pre-plants a Jira-looking artifact before the
	// pull. Bind it explicitly so the test continues to isolate the snapshot
	// write failure rather than the legacy-mirror migration gate.
	bindJiraTestMirror(t, into)
	dir := filepath.Join(into, "PROJ")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Pre-plant a non-empty directory at the snapshot path so atomic replacement
	// fails deterministically without relying on user/umask permissions.
	snapshotPath := filepath.Join(dir, "PROJ-1.json")
	if err := os.Mkdir(snapshotPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapshotPath, "blocker"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tr := &countingPullTracker{issues: []domain.Issue{
		{ID: "1", Key: "PROJ-1", Project: "PROJ", Summary: "a", Body: "b"},
	}}
	svc := &JiraService{tr: tr, baseURL: jiraMirrorTestBackendURL}
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
