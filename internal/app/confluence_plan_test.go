package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

type confluencePlanStore struct {
	domain.DocStore
	pages              map[string]*domain.Resource
	candidates         map[string][]byte
	getCalls           []string
	updateCalls        []string
	updateErr          error
	failGetAfterUpdate bool
}

func (s *confluencePlanStore) GetPage(_ context.Context, id string, _ domain.PullOpts) (*domain.Resource, error) {
	s.getCalls = append(s.getCalls, id)
	if s.failGetAfterUpdate && len(s.updateCalls) > 0 {
		return nil, errors.New("transport failed")
	}
	page, ok := s.pages[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	copy := *page
	copy.Body = append([]byte(nil), page.Body...)
	copy.BodyPresent = true
	return &copy, nil
}

func (s *confluencePlanStore) UpdatePage(_ context.Context, id string, expected int, title string, body []byte, force bool) (int, error) {
	s.updateCalls = append(s.updateCalls, id)
	page := s.pages[id]
	if force || page == nil || page.Version != expected || page.Title != title {
		return 0, domain.ErrVersionConflict
	}
	page.Version++
	page.Body = append([]byte(nil), body...)
	if s.updateErr != nil {
		return 0, s.updateErr
	}
	return page.Version, nil
}

func createPlanFixture(t *testing.T, count int) (root, planPath string, plan *ConfluencePlan, oldBodies, newBodies map[string][]byte) {
	t.Helper()
	root = t.TempDir()
	m := mirror.New(root)
	if err := m.EnsureScaffold(); err != nil {
		t.Fatal(err)
	}
	oldBodies, newBodies = map[string][]byte{}, map[string][]byte{}
	for i := 1; i <= count; i++ {
		id := string(rune('0' + i))
		title := "Page " + id
		old := []byte("<p>old " + id + "</p>")
		updated := []byte("<p>new " + id + "</p>")
		page := &domain.Resource{ID: id, Title: title, SpaceKey: "DOC", Type: "page", Version: 3, Body: old}
		dir, slug := m.PageDir(page.SpaceKey, nil, page.Title)
		if err := m.Write(dir, slug, page, nil); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, slug+".csf"), updated, 0o644); err != nil {
			t.Fatal(err)
		}
		oldBodies[id], newBodies[id] = old, updated
	}
	planPath = filepath.Join(t.TempDir(), "plan.json")
	if _, err := CreateConfluencePlan(root, root, planPath); err != nil {
		t.Fatalf("CreateConfluencePlan: %v", err)
	}
	var err error
	plan, err = loadConfluencePlan(planPath)
	if err != nil {
		t.Fatal(err)
	}
	return
}

func planRemotePages(plan *ConfluencePlan, bodies map[string][]byte, version int) map[string]*domain.Resource {
	out := map[string]*domain.Resource{}
	for _, entry := range plan.Entries {
		out[entry.ID] = &domain.Resource{ID: entry.ID, Title: entry.Title, SpaceKey: entry.Space, Type: "page", Version: version, Body: append([]byte(nil), bodies[entry.ID]...), BodyPresent: true}
	}
	return out
}

func TestCreateConfluencePlanIsDeterministicPrivateAndBound(t *testing.T) {
	root, first, plan, _, _ := createPlanFixture(t, 2)
	second := filepath.Join(t.TempDir(), "second.json")
	result, err := CreateConfluencePlan(root, root, second)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := os.ReadFile(first)
	b, _ := os.ReadFile(second)
	if !bytes.Equal(a, b) || result.ProposalHash != plan.ProposalHash || len(plan.Entries) != 2 {
		t.Fatalf("non-deterministic plan: result=%+v plan=%+v", result, plan)
	}
	info, err := os.Stat(first)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("plan mode=%o", info.Mode().Perm())
	}
	for _, entry := range plan.Entries {
		if filepath.IsAbs(entry.Path) || entry.Operation != "update" || entry.Space != "DOC" || entry.BaselineSHA256 == entry.CandidateSHA256 {
			t.Fatalf("bad entry: %+v", entry)
		}
	}
}

func TestCreateConfluencePlanRejectsUnsupportedMirrorState(t *testing.T) {
	root := t.TempDir()
	m := mirror.New(root)
	_ = m.EnsureScaffold()
	page := &domain.Resource{ID: "1", Title: "Broken", SpaceKey: "DOC", Version: 1, Body: []byte("<p>x</p>")}
	dir, slug := m.PageDir("DOC", nil, "Broken")
	if err := m.Write(dir, slug, page, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, slug+".csf"), []byte("<p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "plan.json")
	if result, err := CreateConfluencePlan(root, root, out); err == nil || result != nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("invalid plan was written: %v", err)
	}
}

func TestCreateConfluencePlanRefusesExistingOutput(t *testing.T) {
	root, _, _, _, _ := createPlanFixture(t, 1)
	out := filepath.Join(t.TempDir(), "reviewed.json")
	if err := os.WriteFile(out, []byte("keep reviewed bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := CreateConfluencePlan(root, root, out)
	if !errors.Is(err, domain.ErrCheckFailed) || result != nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	data, readErr := os.ReadFile(out)
	if readErr != nil || string(data) != "keep reviewed bytes" {
		t.Fatalf("output=%q err=%v", data, readErr)
	}
}

func TestLoadConfluencePlanClassifiesMissingPlanAndRoot(t *testing.T) {
	if _, err := loadConfluencePlan(filepath.Join(t.TempDir(), "missing.json")); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing plan error = %v", err)
	}
	root, path, plan, _, _ := createPlanFixture(t, 1)
	missingRoot := filepath.Join(filepath.Dir(root), "missing-root")
	plan.Root, plan.Target = missingRoot, missingRoot
	plan.ProposalHash = confluencePlanHash(plan)
	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfluencePlan(path); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing root error = %v", err)
	}
}

func TestLoadConfluencePlanRejectsTampering(t *testing.T) {
	_, path, _, _, _ := createPlanFixture(t, 1)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.Replace(data, []byte(`"expected_version": 3`), []byte(`"expected_version": 4`), 1)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfluencePlan(path); err == nil {
		t.Fatal("tampered plan accepted")
	}
}

func TestLoadConfluencePlanRejectsReformattingWithSameHash(t *testing.T) {
	_, path, _, _, _ := createPlanFixture(t, 1)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfluencePlan(path); err == nil {
		t.Fatal("reformatted plan accepted")
	}
}

func TestConfluencePlanPreviewAndApplySuccess(t *testing.T) {
	root, path, plan, oldBodies, newBodies := createPlanFixture(t, 2)
	store := &confluencePlanStore{pages: planRemotePages(plan, oldBodies, 3), candidates: newBodies}
	svc := &ConfluenceService{store: store, cfg: &config.Config{}}
	preview, err := svc.PreviewConfluencePlan(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Mode != "preview" || preview.Status != "would_apply" || len(store.updateCalls) != 0 || len(preview.Entries) != 2 {
		t.Fatalf("preview=%+v calls=%v", preview, store.updateCalls)
	}
	result, err := svc.ApplyConfluencePlan(context.Background(), path, ConfluencePlanApplyOpts{Confirm: "APPLY", ExpectedProposalHash: plan.ProposalHash})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "applied" || !result.Complete || len(store.updateCalls) != 2 {
		t.Fatalf("result=%+v calls=%v", result, store.updateCalls)
	}
	m := mirror.New(root)
	for _, entry := range plan.Entries {
		state, ok, err := m.SyncStateOf(entry.ID)
		if err != nil || !ok || state.Version != 4 || state.Hash != entry.CandidateSHA256 {
			t.Fatalf("state=%+v ok=%t err=%v", state, ok, err)
		}
	}
}

func TestConfluencePlanStaleBatchDoesNoWrites(t *testing.T) {
	_, path, plan, oldBodies, newBodies := createPlanFixture(t, 2)
	pages := planRemotePages(plan, oldBodies, 3)
	pages[plan.Entries[1].ID].Version = 9
	store := &confluencePlanStore{pages: pages, candidates: newBodies}
	svc := &ConfluenceService{store: store, cfg: &config.Config{}}
	result, err := svc.ApplyConfluencePlan(context.Background(), path, ConfluencePlanApplyOpts{Confirm: "APPLY", ExpectedProposalHash: plan.ProposalHash})
	if err == nil || result == nil || result.Status != "blocked" || result.Entries[1].Failure != "remote-version-drift" || len(store.updateCalls) != 0 {
		t.Fatalf("result=%+v err=%v calls=%v", result, err, store.updateCalls)
	}
}

func TestConfluencePlanApplyRequiresExecutionGates(t *testing.T) {
	_, path, _, _, _ := createPlanFixture(t, 1)
	svc := &ConfluenceService{store: &confluencePlanStore{}}
	for _, opts := range []ConfluencePlanApplyOpts{{}, {Confirm: "YES"}, {Confirm: "APPLY"}, {ExpectedProposalHash: strings.Repeat("a", 64)}} {
		result, err := svc.ApplyConfluencePlan(context.Background(), path, opts)
		if !errors.Is(err, domain.ErrUsage) || result != nil {
			t.Fatalf("opts=%+v result=%+v err=%v", opts, result, err)
		}
	}
}

func TestConfluencePlanLockFailureIsBlockedAndIncomplete(t *testing.T) {
	root, path, plan, oldBodies, newBodies := createPlanFixture(t, 1)
	lock, err := lockConfluenceMutations(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Unlock() }()
	svc := &ConfluenceService{store: &confluencePlanStore{pages: planRemotePages(plan, oldBodies, 3), candidates: newBodies}}
	result, err := svc.PreviewConfluencePlan(context.Background(), path)
	if !errors.Is(err, domain.ErrCheckFailed) || result == nil || result.Status != "blocked" || result.Complete || len(result.Entries) != 1 || result.Entries[0].Status != "not_checked" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestConfluencePlanRejectsNonPageRemoteType(t *testing.T) {
	_, path, plan, oldBodies, newBodies := createPlanFixture(t, 1)
	pages := planRemotePages(plan, oldBodies, 3)
	pages[plan.Entries[0].ID].Type = "blogpost"
	store := &confluencePlanStore{pages: pages, candidates: newBodies}
	svc := &ConfluenceService{store: store, cfg: &config.Config{}}
	result, err := svc.ApplyConfluencePlan(context.Background(), path, ConfluencePlanApplyOpts{Confirm: "APPLY", ExpectedProposalHash: plan.ProposalHash})
	if err == nil || result == nil || result.Status != "blocked" || len(store.updateCalls) != 0 {
		t.Fatalf("result=%+v err=%v calls=%v", result, err, store.updateCalls)
	}
}

func TestConfluencePlanReconcilesAmbiguousSuccessAndResumes(t *testing.T) {
	_, path, plan, oldBodies, newBodies := createPlanFixture(t, 1)
	store := &confluencePlanStore{pages: planRemotePages(plan, oldBodies, 3), candidates: newBodies, updateErr: errors.New("ambiguous transport")}
	svc := &ConfluenceService{store: store, cfg: &config.Config{}}
	result, err := svc.ApplyConfluencePlan(context.Background(), path, ConfluencePlanApplyOpts{Confirm: "APPLY", ExpectedProposalHash: plan.ProposalHash})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Entries[0].Reconciled || result.Entries[0].Status != "applied" || len(store.updateCalls) != 1 {
		t.Fatalf("result=%+v", result)
	}
	store.updateErr = nil
	again, err := svc.ApplyConfluencePlan(context.Background(), path, ConfluencePlanApplyOpts{Confirm: "APPLY", ExpectedProposalHash: plan.ProposalHash})
	if err != nil {
		t.Fatal(err)
	}
	if len(store.updateCalls) != 1 || again.Entries[0].Status != "already_satisfied" {
		t.Fatalf("again=%+v calls=%v", again, store.updateCalls)
	}
}

func TestConfluencePlanUnknownStopsWithoutReplay(t *testing.T) {
	_, path, plan, oldBodies, newBodies := createPlanFixture(t, 2)
	store := &confluencePlanStore{pages: planRemotePages(plan, oldBodies, 3), candidates: newBodies, updateErr: errors.New("ambiguous"), failGetAfterUpdate: true}
	svc := &ConfluenceService{store: store, cfg: &config.Config{}}
	result, err := svc.ApplyConfluencePlan(context.Background(), path, ConfluencePlanApplyOpts{Confirm: "APPLY", ExpectedProposalHash: plan.ProposalHash})
	if err == nil || result == nil || result.Status != "partial" || result.Entries[0].Status != "unknown" || result.Entries[1].Status != "not_attempted" || len(store.updateCalls) != 1 {
		t.Fatalf("result=%+v err=%v calls=%v", result, err, store.updateCalls)
	}
}

func TestConfluencePlanHashGatePrecedesNetwork(t *testing.T) {
	_, path, _, oldBodies, newBodies := createPlanFixture(t, 1)
	store := &confluencePlanStore{pages: map[string]*domain.Resource{}, candidates: newBodies}
	_ = oldBodies
	svc := &ConfluenceService{store: store, cfg: &config.Config{}}
	if result, err := svc.ApplyConfluencePlan(context.Background(), path, ConfluencePlanApplyOpts{Confirm: "APPLY", ExpectedProposalHash: "wrong"}); err == nil || result != nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(store.updateCalls) != 0 || len(store.getCalls) != 0 {
		t.Fatalf("network call before hash gate: gets=%v updates=%v", store.getCalls, store.updateCalls)
	}
}

func TestConfluencePlanLocalDriftPrecedesNetwork(t *testing.T) {
	root, path, plan, oldBodies, newBodies := createPlanFixture(t, 1)
	entry := plan.Entries[0]
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(entry.Path)), []byte(`<p>changed again</p>`), 0o644); err != nil {
		t.Fatal(err)
	}
	store := &confluencePlanStore{pages: planRemotePages(plan, oldBodies, 3), candidates: newBodies}
	svc := &ConfluenceService{store: store, cfg: &config.Config{}}
	result, err := svc.PreviewConfluencePlan(context.Background(), path)
	if err == nil || result == nil || result.Status != "blocked" || len(store.getCalls) != 0 || len(store.updateCalls) != 0 {
		t.Fatalf("result=%+v err=%v gets=%v updates=%v", result, err, store.getCalls, store.updateCalls)
	}
}

func TestConfluencePlanMarkdownEscapesDynamicStructure(t *testing.T) {
	result := &ConfluencePlanApplyResult{Schema: confluencePlanSchema, ProposalHash: strings.Repeat("a", 64), Root: "/tmp/`root\nnext", Target: "/tmp/target", Mode: "preview", Status: "would_apply", Complete: true, Entries: []ConfluencePlanApplyEntry{{ID: "1", Title: "A | B", Space: "D", Path: "D/a.csf", Status: "would_apply", ExpectedVersion: 1}}}
	markdown := ConfluencePlanApplyMarkdown(result)
	if strings.Contains(markdown, "root\nnext") || !strings.Contains(markdown, "A \\| B") || !strings.Contains(markdown, "\\`root next") {
		t.Fatalf("unsafe markdown: %q", markdown)
	}
}
