package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

type mutatingGetPullStore struct {
	*pullStore
	mutate func()
}

type staticConfluenceAssetResolver struct{}

func (staticConfluenceAssetResolver) Resolve(context.Context, *domain.Resource, domain.Ref) ([]byte, string, error) {
	return []byte("image bytes"), "image.png", nil
}

func (s *mutatingGetPullStore) GetPage(ctx context.Context, id string, opts domain.PullOpts) (*domain.Resource, error) {
	if s.mutate != nil {
		s.mutate()
		s.mutate = nil
	}
	return s.pullStore.GetPage(ctx, id, opts)
}

func seedConfluenceSafetyPages(t *testing.T, root string, ids ...string) (*pullStore, *PullResult) {
	t.Helper()
	store := &pullStore{pages: map[string]*domain.Resource{}}
	for _, id := range ids {
		store.pages[id] = &domain.Resource{ID: id, Title: "Page " + id, SpaceKey: "DOC", Version: 1, Body: []byte("<p>old " + id + "</p>")}
		store.refs = append(store.refs, domain.PageRef{ID: id})
	}
	result, err := (&ConfluenceService{baseURL: confluenceTestBackendURL, store: store}).Pull(context.Background(), PullOpts{Space: "DOC", Into: root})
	if err != nil {
		t.Fatal(err)
	}
	return store, result
}

func pulledPathForID(t *testing.T, root string, result *PullResult, id string) string {
	t.Helper()
	for _, page := range result.Pages {
		if page.ID == id {
			return filepath.Join(root, filepath.FromSlash(page.Path))
		}
	}
	t.Fatalf("page %s not found in %+v", id, result.Pages)
	return ""
}

func TestConfluencePullPreservesDirtyPageAndContinuesCleanSibling(t *testing.T) {
	root := t.TempDir()
	store, seeded := seedConfluenceSafetyPages(t, root, "10", "20")
	dirtyPath := pulledPathForID(t, root, seeded, "10")
	dirty := []byte("<p>local edit</p>")
	if err := os.WriteFile(dirtyPath, dirty, 0o644); err != nil {
		t.Fatal(err)
	}
	store.pages["10"].Version = 2
	store.pages["10"].Body = []byte("<p>remote 10</p>")
	store.pages["20"].Version = 2
	store.pages["20"].Body = []byte("<p>remote 20</p>")
	store.getPageCalls = 0

	result, err := (&ConfluenceService{baseURL: confluenceTestBackendURL, store: store}).Pull(context.Background(), PullOpts{Space: "DOC", Into: root})
	if !errors.Is(err, domain.ErrCheckFailed) || result == nil || len(result.Pages) != 1 || result.Pages[0].ID != "20" || store.getPageCalls != 1 {
		t.Fatalf("result=%+v err=%v body calls=%d", result, err, store.getPageCalls)
	}
	if result.LocalSafety == nil || result.LocalSafety.Blocked != 1 || result.LocalSafety.Actions[0].Reason != "local_native_modified" {
		t.Fatalf("local safety=%+v", result.LocalSafety)
	}
	if got, readErr := os.ReadFile(dirtyPath); readErr != nil || string(got) != string(dirty) {
		t.Fatalf("dirty body=%q err=%v", got, readErr)
	}
}

func TestConfluencePullRecoveryDoesNotOverrideUnappliedMarkdown(t *testing.T) {
	root := t.TempDir()
	store, seeded := seedConfluenceSafetyPages(t, root, "10")
	csfPath := pulledPathForID(t, root, seeded, "10")
	mdPath := strings.TrimSuffix(csfPath, ".csf") + ".md"
	md, err := os.ReadFile(mdPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mdPath, append(md, []byte("\nlocal Markdown\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	store.getPageCalls = 0
	result, err := (&ConfluenceService{baseURL: confluenceTestBackendURL, store: store}).Pull(context.Background(), PullOpts{ID: "10", Into: root, OverwriteLocal: true})
	if !errors.Is(err, domain.ErrCheckFailed) || !strings.Contains(err.Error(), "unapplied Markdown") || store.getPageCalls != 0 || result.LocalSafety.Blocked != 1 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, store.getPageCalls)
	}
}

func TestConfluencePullOverwriteAndStashDirtyNative(t *testing.T) {
	for _, recovery := range []string{"overwrite", "stash"} {
		t.Run(recovery, func(t *testing.T) {
			root := t.TempDir()
			store, seeded := seedConfluenceSafetyPages(t, root, "10")
			csfPath := pulledPathForID(t, root, seeded, "10")
			local := []byte("<p>local recovery bytes</p>")
			if err := os.WriteFile(csfPath, local, 0o644); err != nil {
				t.Fatal(err)
			}
			store.pages["10"].Version = 2
			store.pages["10"].Body = []byte("<p>remote replacement</p>")
			opts := PullOpts{ID: "10", Into: root, OverwriteLocal: recovery == "overwrite", StashLocal: recovery == "stash"}
			previewOpts := opts
			previewOpts.DryRun = true
			preview, previewErr := (&ConfluenceService{baseURL: confluenceTestBackendURL, store: store}).Pull(context.Background(), previewOpts)
			wantPreview := pullLocalWouldOverwrite
			if recovery == "stash" {
				wantPreview = pullLocalWouldStash
			}
			if previewErr != nil || preview.Pages[0].Status != wantPreview || preview.LocalSafety.Actions[0].Status != wantPreview {
				t.Fatalf("preview=%+v err=%v", preview, previewErr)
			}
			if got, readErr := os.ReadFile(csfPath); readErr != nil || string(got) != string(local) {
				t.Fatalf("preview changed native body=%q err=%v", got, readErr)
			}
			if _, statErr := os.Stat(filepath.Join(root, ".atl", "stash")); !os.IsNotExist(statErr) {
				t.Fatalf("preview created stash: %v", statErr)
			}
			result, err := (&ConfluenceService{baseURL: confluenceTestBackendURL, store: store}).Pull(context.Background(), opts)
			if err != nil || len(result.Pages) != 1 || result.LocalSafety == nil || len(result.LocalSafety.Actions) != 1 {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			wantStatus := pullLocalOverwritten
			if recovery == "stash" {
				wantStatus = pullLocalStashed
				stashPath := result.LocalSafety.Actions[0].StashPath
				stashed, readErr := os.ReadFile(filepath.Join(root, filepath.FromSlash(stashPath)))
				if readErr != nil || string(stashed) != string(local) {
					t.Fatalf("stash=%q bytes=%q err=%v", stashPath, stashed, readErr)
				}
			}
			if result.Pages[0].Status != wantStatus || result.LocalSafety.Actions[0].Status != wantStatus {
				t.Fatalf("page=%+v action=%+v", result.Pages[0], result.LocalSafety.Actions[0])
			}
		})
	}
}

func TestConfluencePullDryRunDoesNotCreateAbsentRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "absent")
	store := &pullStore{pages: map[string]*domain.Resource{"10": {ID: "10", Title: "Page 10", SpaceKey: "DOC", Version: 1, Body: []byte("<p>body</p>")}}}
	result, err := (&ConfluenceService{baseURL: confluenceTestBackendURL, store: store}).Pull(context.Background(), PullOpts{ID: "10", Into: root, DryRun: true})
	if err != nil || len(result.Pages) != 1 || result.Pages[0].Status != "would_pull" || result.LocalSafety == nil || !result.LocalSafety.DryRun {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if _, statErr := os.Stat(root); !os.IsNotExist(statErr) {
		t.Fatalf("dry-run root exists: %v", statErr)
	}
}

func TestConfluencePullMissingArtifactsDoNotShortCircuitLaterSafety(t *testing.T) {
	root := t.TempDir()
	store, seeded := seedConfluenceSafetyPages(t, root, "10", "20")
	first := pulledPathForID(t, root, seeded, "10")
	for _, path := range []string{first, strings.TrimSuffix(first, ".csf") + ".md", strings.TrimSuffix(first, ".csf") + ".meta.json"} {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	second := pulledPathForID(t, root, seeded, "20")
	if err := os.WriteFile(second, []byte("<p>later local edit</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	store.getPageCalls = 0
	result, err := (&ConfluenceService{baseURL: confluenceTestBackendURL, store: store}).Pull(context.Background(), PullOpts{Space: "DOC", Into: root})
	if !errors.Is(err, domain.ErrCheckFailed) || len(result.Pages) != 1 || result.Pages[0].ID != "10" || store.getPageCalls != 1 || result.LocalSafety.Blocked != 1 || result.LocalSafety.Actions[0].ID != "20" {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, store.getPageCalls)
	}
}

func TestConfluencePullRejectsConflictingRecoveryFlags(t *testing.T) {
	_, err := (&ConfluenceService{baseURL: confluenceTestBackendURL, store: &pullStore{}}).Pull(context.Background(), PullOpts{ID: "10", Into: t.TempDir(), OverwriteLocal: true, StashLocal: true})
	if !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("error=%v", err)
	}
}

func TestConfluencePullPreservesUntrackedClaimedMarkdown(t *testing.T) {
	for _, recovery := range []string{"default", "overwrite", "stash"} {
		t.Run(recovery, func(t *testing.T) {
			root := t.TempDir()
			targetDir := filepath.Join(root, "DOC", "page-10")
			if err := os.MkdirAll(targetDir, 0o755); err != nil {
				t.Fatal(err)
			}
			mdPath := filepath.Join(targetDir, "page-10.md")
			if err := os.WriteFile(mdPath, []byte("local untracked view"), 0o644); err != nil {
				t.Fatal(err)
			}
			store := &pullStore{pages: map[string]*domain.Resource{"10": {ID: "10", Title: "Page 10", SpaceKey: "DOC", Version: 1, Body: []byte("<p>remote</p>")}}}
			result, err := (&ConfluenceService{baseURL: confluenceTestBackendURL, store: store}).Pull(context.Background(), PullOpts{
				ID: "10", Into: root, OverwriteLocal: recovery == "overwrite", StashLocal: recovery == "stash",
			})
			if !errors.Is(err, domain.ErrCheckFailed) || result.LocalSafety == nil || result.LocalSafety.Actions[0].Reason != "target_artifacts_unqualified" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if got := string(mustReadBytes(t, mdPath)); got != "local untracked view" {
				t.Fatalf("untracked Markdown overwritten: %q", got)
			}
		})
	}
}

func TestConfluencePullAllowsChildScaffoldInClaimedDirectory(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		t.Run(fmt.Sprintf("dry_run_%t", dryRun), func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, "DOC", "page-10", "child-page"), 0o755); err != nil {
				t.Fatal(err)
			}
			store := &pullStore{pages: map[string]*domain.Resource{"10": {ID: "10", Title: "Page 10", SpaceKey: "DOC", Version: 1, Body: []byte("<p>remote</p>")}}}
			result, err := (&ConfluenceService{baseURL: confluenceTestBackendURL, store: store}).Pull(context.Background(), PullOpts{ID: "10", Into: root, DryRun: dryRun})
			if err != nil || len(result.Pages) != 1 {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestConfluencePullRevalidatesArtifactsAfterRemoteRead(t *testing.T) {
	root := t.TempDir()
	base, seeded := seedConfluenceSafetyPages(t, root, "10")
	csfPath := pulledPathForID(t, root, seeded, "10")
	mdPath := strings.TrimSuffix(csfPath, ".csf") + ".md"
	beforeCSF := mustReadBytes(t, csfPath)
	store := &mutatingGetPullStore{pullStore: base, mutate: func() {
		if err := os.WriteFile(mdPath, []byte("concurrent Markdown edit"), 0o644); err != nil {
			t.Fatal(err)
		}
	}}
	result, err := (&ConfluenceService{baseURL: confluenceTestBackendURL, store: store}).Pull(context.Background(), PullOpts{ID: "10", Into: root})
	if !errors.Is(err, domain.ErrCheckFailed) || result == nil || result.LocalSafety == nil || result.LocalSafety.Blocked != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if got := string(mustReadBytes(t, mdPath)); got != "concurrent Markdown edit" {
		t.Fatalf("concurrent edit overwritten: %q", got)
	}
	if got := mustReadBytes(t, csfPath); string(got) != string(beforeCSF) {
		t.Fatalf("native body changed before concurrent-view refusal: %q", got)
	}
}

func TestConfluencePullStagesNewPageAssetsBeforeTargetWrite(t *testing.T) {
	root := t.TempDir()
	body := []byte(`<p>image</p><ac:image><ri:attachment ri:filename="image.png"/></ac:image>`)
	store := &pullStore{pages: map[string]*domain.Resource{"10": {ID: "10", Title: "Page 10", SpaceKey: "DOC", Version: 1, Body: body}}}
	result, err := (&ConfluenceService{baseURL: confluenceTestBackendURL, store: store, assets: staticConfluenceAssetResolver{}}).Pull(context.Background(), PullOpts{ID: "10", Into: root, Assets: true})
	if err != nil || len(result.Pages) != 1 || result.Pages[0].Assets != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	assetPath := filepath.Join(root, "DOC", "page-10", "page-10.assets", "image.png")
	if got := string(mustReadBytes(t, assetPath)); got != "image bytes" {
		t.Fatalf("asset=%q", got)
	}

	blockedRoot := t.TempDir()
	blockedDir := filepath.Join(blockedRoot, "DOC", "page-10")
	if err := os.MkdirAll(blockedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blockedDir, "page-10.md"), []byte("local"), 0o644); err != nil {
		t.Fatal(err)
	}
	blocked, blockErr := (&ConfluenceService{baseURL: confluenceTestBackendURL, store: store, assets: staticConfluenceAssetResolver{}}).Pull(context.Background(), PullOpts{ID: "10", Into: blockedRoot, Assets: true})
	if !errors.Is(blockErr, domain.ErrCheckFailed) || blocked.LocalSafety == nil {
		t.Fatalf("result=%+v err=%v", blocked, blockErr)
	}
	if _, statErr := os.Stat(filepath.Join(blockedDir, "page-10.assets")); !os.IsNotExist(statErr) {
		t.Fatalf("blocked pull wrote partial assets: %v", statErr)
	}
}

func TestStagedConfluenceAssetSinkBoundsAggregateBytes(t *testing.T) {
	sink := &stagedConfluenceAssetSink{slug: "page", bytes: confluenceStagedAssetBytesMax - 1}
	if _, err := sink.Put("one.png", []byte{1}); err != nil {
		t.Fatal(err)
	}
	if _, err := sink.Put("two.png", []byte{1}); err == nil || sink.err == nil {
		t.Fatal("aggregate staging limit was not enforced")
	}
	if sink.bytes != confluenceStagedAssetBytesMax || len(sink.assets) != 1 {
		t.Fatalf("bytes=%d assets=%d", sink.bytes, len(sink.assets))
	}
}

func TestConfluenceIncrementalBlockKeepsWatermarkUnchanged(t *testing.T) {
	root := t.TempDir()
	_, seeded := seedConfluenceSafetyPages(t, root, "10")
	csfPath := pulledPathForID(t, root, seeded, "10")
	if err := os.WriteFile(csfPath, []byte("<p>local</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	page, hit := incrementalPage("10", 2, "2026-07-13T12:00:00Z")
	store := &incrementalPullStore{pullStore: &pullStore{pages: map[string]*domain.Resource{"10": page}}, searchPages: map[string]domain.PageSearchPage{"": {Results: []domain.PageRef{hit}, Complete: true}}}
	result, err := (&ConfluenceService{baseURL: confluenceTestBackendURL, store: store}).Pull(context.Background(), PullOpts{CQL: "type=page", Into: root, Incremental: true, Since: "2026-07-13T11:00:00Z"})
	if !errors.Is(err, domain.ErrCheckFailed) || result.Incremental.WatermarkAdvanced || store.getCalls != 0 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, store.getCalls)
	}
	if _, ok, loadErr := mirror.New(root).IncrementalWatermark(confluenceIncrementalService, selectorHash("type=page")); loadErr != nil || ok {
		t.Fatalf("watermark exists=%t err=%v", ok, loadErr)
	}
}
