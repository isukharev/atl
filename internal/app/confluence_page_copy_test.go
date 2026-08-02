package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

const confluenceCopyTestBackend = "https://confluence.example.test/wiki"

type confluenceCopyRead struct {
	page *domain.Resource
	err  error
}

type confluenceCopyStore struct {
	domain.DocStore
	reads       map[string][]confluenceCopyRead
	readIndexes map[string]int
	created     *domain.Resource
	createErr   error
	createCalls int
	space       string
	parent      string
	title       string
	body        []byte
}

func (s *confluenceCopyStore) GetPageByStatus(_ context.Context, id, status string, _ domain.PullOpts) (*domain.Resource, error) {
	if status != "current" {
		return nil, errors.New("unexpected status")
	}
	if s.readIndexes == nil {
		s.readIndexes = map[string]int{}
	}
	index := s.readIndexes[id]
	queue := s.reads[id]
	if index >= len(queue) {
		return nil, errors.New("unexpected exact read")
	}
	s.readIndexes[id] = index + 1
	read := queue[index]
	if read.page == nil || read.err != nil {
		return read.page, read.err
	}
	copy := *read.page
	copy.Body = append([]byte(nil), read.page.Body...)
	copy.Ancestors = append([]string(nil), read.page.Ancestors...)
	copy.AncestorIDs = append([]string(nil), read.page.AncestorIDs...)
	return &copy, nil
}

func (s *confluenceCopyStore) CreatePage(_ context.Context, space, parent, title string, body []byte) (*domain.Resource, error) {
	s.createCalls++
	s.space, s.parent, s.title = space, parent, title
	s.body = append([]byte(nil), body...)
	return s.created, s.createErr
}

func confluenceCopyPage(id, title string, version int, body string) *domain.Resource {
	return &domain.Resource{
		ID: id, Type: "page", Status: "current", Title: title, SpaceKey: "DOC",
		Version: version, Body: []byte(body), BodyPresent: true, AncestorsPresent: true,
	}
}

func previewConfluenceCopy(t *testing.T, source *domain.Resource, opts ConfluencePageCopyOpts) *ConfluencePageCopyResult {
	t.Helper()
	store := &confluenceCopyStore{reads: map[string][]confluenceCopyRead{source.ID: {{page: source}}}}
	result, err := (&ConfluenceService{store: store, baseURL: confluenceCopyTestBackend}).CopyPageGuarded(context.Background(), source.ID, opts)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.Status != "would_apply" || result.ProposalHash == "" || store.createCalls != 0 {
		t.Fatalf("result=%+v err=%v creates=%d", result, err, store.createCalls)
	}
	return result
}

func TestConfluencePageCopyPreviewAndApply(t *testing.T) {
	source := confluenceCopyPage("10", "Source", 7, "<p>native</p>")
	preview := previewConfluenceCopy(t, source, ConfluencePageCopyOpts{Title: "  Copied  "})
	if preview.Mode != "dry-run" || preview.TargetSpace != "DOC" || preview.TargetParent != "" || preview.CurrentVersion != 7 || !preview.Complete {
		t.Fatalf("preview=%+v", preview)
	}
	readback := confluenceCopyPage("42", "Copied", 1, "<p>native</p>")
	store := &confluenceCopyStore{
		reads: map[string][]confluenceCopyRead{
			"10": {{page: source}, {page: source}},
			"42": {{page: readback}},
		},
		created: &domain.Resource{ID: "42"},
	}
	result, err := (&ConfluenceService{store: store, baseURL: confluenceCopyTestBackend}).CopyPageGuarded(context.Background(), "10", ConfluencePageCopyOpts{
		Title: "Copied", Apply: true, ExpectedVersion: 7, ExpectedProposalHash: preview.ProposalHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "applied" || result.ID != "42" || result.Version != 1 || !result.WriteAttempted || !result.Reconciled || store.createCalls != 1 {
		t.Fatalf("result=%+v creates=%d", result, store.createCalls)
	}
	if store.space != "DOC" || store.parent != "" || store.title != "Copied" || string(store.body) != "<p>native</p>" {
		t.Fatalf("create target=%q/%q title=%q body=%q", store.space, store.parent, store.title, store.body)
	}
}

func TestConfluencePageCopyProposalBindsInputs(t *testing.T) {
	source := confluenceCopyPage("10", "Source", 7, "body")
	base := confluencePageCopySnapshot{
		source: source, sourceTitleSHA256: "source-title", sourceHierarchyHash: "source-hierarchy",
		backendSHA256: "backend", title: "Copy", titleSHA256: "title",
		space: "DOC", parent: "20", parentVersion: 2, parentBodySHA256: "parent",
		parentHierarchyHash: "hierarchy", targetHierarchyHash: "target-hierarchy",
		register: true, rootSHA256: "root",
	}
	mutations := map[string]func(*confluencePageCopySnapshot){
		"backend":          func(v *confluencePageCopySnapshot) { v.backendSHA256 = "other" },
		"source id":        func(v *confluencePageCopySnapshot) { v.source = confluenceCopyPage("11", "Source", 7, "body") },
		"source version":   func(v *confluencePageCopySnapshot) { copy := *v.source; copy.Version++; v.source = &copy },
		"source body":      func(v *confluencePageCopySnapshot) { copy := *v.source; copy.Body = []byte("other"); v.source = &copy },
		"source title":     func(v *confluencePageCopySnapshot) { v.sourceTitleSHA256 = "other" },
		"source hierarchy": func(v *confluencePageCopySnapshot) { v.sourceHierarchyHash = "other" },
		"target title":     func(v *confluencePageCopySnapshot) { v.titleSHA256 = "other" },
		"target space":     func(v *confluencePageCopySnapshot) { v.space = "OTHER" },
		"target parent":    func(v *confluencePageCopySnapshot) { v.parent = "21" },
		"parent version":   func(v *confluencePageCopySnapshot) { v.parentVersion++ },
		"parent body":      func(v *confluencePageCopySnapshot) { v.parentBodySHA256 = "other" },
		"parent hierarchy": func(v *confluencePageCopySnapshot) { v.parentHierarchyHash = "other" },
		"target hierarchy": func(v *confluencePageCopySnapshot) { v.targetHierarchyHash = "other" },
		"registration":     func(v *confluencePageCopySnapshot) { v.register = false },
		"root":             func(v *confluencePageCopySnapshot) { v.rootSHA256 = "other" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := base
			mutate(&changed)
			if confluencePageCopyProposalHash(changed) == confluencePageCopyProposalHash(base) {
				t.Fatalf("proposal hash did not bind %s", name)
			}
		})
	}
}

func TestConfluencePageCopyBlocksPrewriteDrift(t *testing.T) {
	source := confluenceCopyPage("10", "Source", 7, "body")
	preview := previewConfluenceCopy(t, source, ConfluencePageCopyOpts{Title: "Copied"})
	changed := confluenceCopyPage("10", "Source", 8, "changed")
	store := &confluenceCopyStore{reads: map[string][]confluenceCopyRead{"10": {{page: source}, {page: changed}}}}
	result, err := (&ConfluenceService{store: store, baseURL: confluenceCopyTestBackend}).CopyPageGuarded(context.Background(), "10", ConfluencePageCopyOpts{
		Title: "Copied", Apply: true, ExpectedVersion: 7, ExpectedProposalHash: preview.ProposalHash,
	})
	if result == nil || result.Status != "blocked" || result.WriteAttempted || store.createCalls != 0 || !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("result=%+v err=%v creates=%d", result, err, store.createCalls)
	}
}

func TestConfluencePageCopyOutcomes(t *testing.T) {
	source := confluenceCopyPage("10", "Source", 7, "body")
	preview := previewConfluenceCopy(t, source, ConfluencePageCopyOpts{Title: "Copied"})
	tests := []struct {
		name       string
		created    *domain.Resource
		createErr  error
		readback   confluenceCopyRead
		wantStatus string
		wantKnown  bool
		wantError  bool
	}{
		{name: "definitive rejection", createErr: confluenceTrashHTTPError{status: 403, sentinel: domain.ErrForbidden}, wantStatus: "not_applied", wantError: true},
		{name: "missing id", created: &domain.Resource{}, wantStatus: "outcome_unknown", wantError: true},
		{name: "source id reused", created: &domain.Resource{ID: "10"}, wantStatus: "outcome_unknown", wantError: true},
		{name: "ambiguous with id recovered", created: &domain.Resource{ID: "42"}, createErr: errors.New("connection closed"), readback: confluenceCopyRead{page: confluenceCopyPage("42", "Copied", 1, "body")}, wantStatus: "recovered", wantKnown: true},
		{name: "normalized body mismatch", created: &domain.Resource{ID: "42"}, readback: confluenceCopyRead{page: confluenceCopyPage("42", "Copied", 1, "normalized")}, wantStatus: "outcome_unknown", wantKnown: true, wantError: true},
		{name: "intervening version", created: &domain.Resource{ID: "42"}, readback: confluenceCopyRead{page: confluenceCopyPage("42", "Copied", 2, "body")}, wantStatus: "outcome_unknown", wantKnown: true, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reads := map[string][]confluenceCopyRead{"10": {{page: source}, {page: source}}}
			if test.created != nil && test.created.ID != "" && test.created.ID != "10" {
				reads[test.created.ID] = []confluenceCopyRead{test.readback}
			}
			store := &confluenceCopyStore{reads: reads, created: test.created, createErr: test.createErr}
			result, err := (&ConfluenceService{store: store, baseURL: confluenceCopyTestBackend}).CopyPageGuarded(context.Background(), "10", ConfluencePageCopyOpts{
				Title: "Copied", Apply: true, ExpectedVersion: 7, ExpectedProposalHash: preview.ProposalHash,
			})
			if result == nil || result.Status != test.wantStatus || result.WriteAttempted != true || store.createCalls != 1 || (result.ID != "") != test.wantKnown || (err != nil) != test.wantError {
				t.Fatalf("result=%+v err=%v creates=%d", result, err, store.createCalls)
			}
		})
	}
}

func TestConfluencePageCopyRejectsParentIdentityAndHierarchyDrift(t *testing.T) {
	source := confluenceCopyPage("10", "Source", 7, "body")
	parent := confluenceCopyPage("20", "Parent", 4, "parent")
	parent.Parent = "5"
	parent.Ancestors = []string{"Home"}
	parent.AncestorIDs = []string{"5"}
	created := confluenceCopyPage("42", "Copied", 1, "body")
	created.Parent = "20"
	created.Ancestors = []string{"Renamed home", "Parent"}
	created.AncestorIDs = []string{"5", "20"}
	previewStore := &confluenceCopyStore{reads: map[string][]confluenceCopyRead{
		"10": {{page: source}}, "20": {{page: parent}},
	}}
	preview, err := (&ConfluenceService{store: previewStore, baseURL: confluenceCopyTestBackend}).CopyPageGuarded(context.Background(), "10", ConfluencePageCopyOpts{Title: "Copied", Parent: "20"})
	if err != nil {
		t.Fatal(err)
	}
	store := &confluenceCopyStore{
		reads: map[string][]confluenceCopyRead{
			"10": {{page: source}, {page: source}},
			"20": {{page: parent}, {page: parent}},
			"42": {{page: created}},
		},
		created: &domain.Resource{ID: "42"},
	}
	result, err := (&ConfluenceService{store: store, baseURL: confluenceCopyTestBackend}).CopyPageGuarded(context.Background(), "10", ConfluencePageCopyOpts{
		Title: "Copied", Parent: "20", Apply: true, ExpectedVersion: 7, ExpectedProposalHash: preview.ProposalHash,
	})
	if result == nil || result.Status != "outcome_unknown" || result.ID != "42" || !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("result=%+v err=%v", result, err)
	}

	parentPreview := previewConfluenceCopy(t, source, ConfluencePageCopyOpts{Title: "Child copy", Parent: "10"})
	parentIdentityStore := &confluenceCopyStore{
		reads:   map[string][]confluenceCopyRead{"10": {{page: source}, {page: source}}},
		created: &domain.Resource{ID: "10"},
	}
	result, err = (&ConfluenceService{store: parentIdentityStore, baseURL: confluenceCopyTestBackend}).CopyPageGuarded(context.Background(), "10", ConfluencePageCopyOpts{
		Title: "Child copy", Parent: "10", Apply: true, ExpectedVersion: 7, ExpectedProposalHash: parentPreview.ProposalHash,
	})
	if result == nil || result.Status != "outcome_unknown" || result.ID != "" || !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("parent identity result=%+v err=%v", result, err)
	}
}

func TestConfluencePageCopyRegistrationUsesExactReadback(t *testing.T) {
	root := filepath.Join(t.TempDir(), "mirror")
	source := confluenceCopyPage("10", "Source", 7, "body")
	preview := previewConfluenceCopy(t, source, ConfluencePageCopyOpts{Title: "Copied", Register: true, Root: root})
	readback := confluenceCopyPage("42", "Copied", 1, "body")
	store := &confluenceCopyStore{
		reads:   map[string][]confluenceCopyRead{"10": {{page: source}, {page: source}}, "42": {{page: readback}}},
		created: &domain.Resource{ID: "42"},
	}
	result, err := (&ConfluenceService{store: store, baseURL: confluenceCopyTestBackend}).CopyPageGuarded(context.Background(), "10", ConfluencePageCopyOpts{
		Title: "Copied", Register: true, Root: root, Apply: true,
		ExpectedVersion: 7, ExpectedProposalHash: preview.ProposalHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Registration == nil || result.Registration.Status != "registered" || !result.Registration.ReadbackReconciled {
		t.Fatalf("result=%+v", result)
	}
	local, body, err := mirror.New(root).LoadCSF(filepath.Join(root, filepath.FromSlash(result.Registration.Path)))
	if err != nil || local.Synced == nil || local.Dirty || string(body) != "body" {
		t.Fatalf("local=%+v body=%q err=%v", local, body, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".atl")); err != nil {
		t.Fatal(err)
	}
}
