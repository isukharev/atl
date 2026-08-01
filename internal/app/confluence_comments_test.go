package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

type qualifiedPullStore struct {
	*pullStore
	inventory domain.ConfluenceCommentInventory
	readCalls int
	readOpts  domain.ConfluenceCommentReadOptions
}

func (s *qualifiedPullStore) ListConfluenceComments(_ context.Context, _ string, opts domain.ConfluenceCommentReadOptions) (domain.ConfluenceCommentInventory, error) {
	s.readCalls++
	s.readOpts = opts
	return s.inventory, nil
}

// pageDirFrom derives the on-disk page directory from a PulledPage.Path (which
// is relative to the mirror root).
func pageDirFrom(root, rel string) (dir, slug string) {
	full := filepath.Join(root, rel)
	return filepath.Dir(full), strings.TrimSuffix(filepath.Base(full), ".csf")
}

// Without --comments the pull must never call ListComments and must not write
// any comment sidecar — byte-for-byte the same traffic and files as today.
func TestPullCommentsFlagOffNoCallNoFiles(t *testing.T) {
	into := t.TempDir()
	st := &pullStore{pages: map[string]*domain.Resource{
		"100": {ID: "100", Title: "Alpha", SpaceKey: "SP", Version: 2, Body: []byte("<p>alpha</p>")},
	}, comments: map[string][]domain.Comment{
		"100": {{ID: "c1", Author: "Alice", Created: "2026-01-01", Body: "hi"}},
	}}
	svc := &ConfluenceService{store: st}
	res, err := svc.Pull(context.Background(), PullOpts{ID: "100", Into: into})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if st.listCommentsCalls != 0 {
		t.Errorf("flag off must not fetch comments, got %d ListComments calls", st.listCommentsCalls)
	}
	dir, slug := pageDirFrom(into, res.Pages[0].Path)
	for _, suffix := range []string{".comments.json", ".comments.md"} {
		if _, err := os.Stat(filepath.Join(dir, slug+suffix)); !os.IsNotExist(err) {
			t.Errorf("flag off wrote %s%s (err=%v), want absent", slug, suffix, err)
		}
	}
	// The meta carries no comment fields (omitempty) with the flag off.
	mb, _ := os.ReadFile(filepath.Join(dir, slug+".meta.json"))
	if strings.Contains(string(mb), "comment_count") || strings.Contains(string(mb), "comments_truncated") {
		t.Errorf("flag off leaked comment fields into meta: %s", mb)
	}
}

// With --comments the pull writes both sidecars, stamps the meta count, keeps the
// .csf byte-identical to a no-flag pull, leaves the page Clean, and refreshes the
// sidecars on re-pull.
func TestPullCommentsMirrorsSidecars(t *testing.T) {
	comments := []domain.Comment{
		{ID: "c1", Author: "Alice", Created: "2026-01-01T00:00:00.000Z", Body: "first",
			BodyStorage: "<p><strong>first</strong></p><ul><li>nested item</li></ul>"},
		{ID: "c2", Author: "Bob", Created: "2026-01-02T00:00:00.000Z", Body: "second"},
	}
	newStore := func() *pullStore {
		return &pullStore{pages: map[string]*domain.Resource{
			"100": {ID: "100", Title: "Alpha", SpaceKey: "SP", Version: 2, Body: []byte("<p>alpha</p>")},
		}, comments: map[string][]domain.Comment{"100": comments}}
	}

	into := t.TempDir()
	svc := &ConfluenceService{store: newStore()}
	res, err := svc.Pull(context.Background(), PullOpts{ID: "100", Into: into, Comments: true})
	if err != nil {
		t.Fatalf("pull --comments: %v", err)
	}
	if res.Pages[0].Comments == nil || *res.Pages[0].Comments != 2 {
		t.Errorf("PulledPage.Comments = %v, want 2", res.Pages[0].Comments)
	}
	if res.CommentsTruncated {
		t.Errorf("a complete listing must not report CommentsTruncated")
	}
	dir, slug := pageDirFrom(into, res.Pages[0].Path)

	// .comments.json is now an explicit schema-v2 envelope. This legacy-only
	// test store is preserved honestly as an unqualified migration source.
	gotJSON, err := os.ReadFile(filepath.Join(dir, slug+".comments.json"))
	if err != nil {
		t.Fatalf("read comments.json: %v", err)
	}
	decoded, err := mirror.DecodeConfluenceCommentsSidecar(gotJSON)
	if err != nil {
		t.Fatalf("decode comments.json: %v", err)
	}
	if decoded.Format != mirror.ConfluenceCommentsSidecarFormatV2 || decoded.V2 == nil || decoded.V2.SchemaVersion != 2 ||
		decoded.V2.PageID != "100" || decoded.V2.Count != 2 || decoded.V2.CommentsComplete || decoded.V2.ThreadsComplete ||
		!containsAppString(decoded.V2.PartialReasons, domain.ConfluenceCommentPartialLegacyUnqualified) ||
		decoded.V2.Comments[0].BodyStorage != comments[0].BodyStorage {
		t.Fatalf("comments.json qualification = %+v", decoded)
	}

	// .comments.md is the derived read view.
	wantMD := "# Comments\n\n## Comment by Alice (2026-01-01 00:00 UTC)\n\n**first**\n\n- nested item\n\n## Comment by Bob (2026-01-02 00:00 UTC)\n\nsecond\n\n"
	gotMD, err := os.ReadFile(filepath.Join(dir, slug+".comments.md"))
	if err != nil {
		t.Fatalf("read comments.md: %v", err)
	}
	if string(gotMD) != wantMD {
		t.Errorf("comments.md mismatch:\n got %q\nwant %q", gotMD, wantMD)
	}

	// .meta.json carries the count (and no truncation flag).
	var meta mirror.Meta
	mb, _ := os.ReadFile(filepath.Join(dir, slug+".meta.json"))
	if err := json.Unmarshal(mb, &meta); err != nil {
		t.Fatalf("decode meta: %v", err)
	}
	if meta.CommentCount != 2 || meta.CommentsTruncated || meta.CommentSidecarVersion != 2 ||
		meta.CommentsComplete == nil || *meta.CommentsComplete || meta.CommentThreadsComplete == nil || *meta.CommentThreadsComplete {
		t.Errorf("meta comment fields = %+v", meta)
	}

	// .csf is byte-identical to a pull without --comments (comments never touch it).
	plainInto := t.TempDir()
	plainSvc := &ConfluenceService{store: newStore()}
	plainRes, err := plainSvc.Pull(context.Background(), PullOpts{ID: "100", Into: plainInto})
	if err != nil {
		t.Fatalf("plain pull: %v", err)
	}
	withCSF, _ := os.ReadFile(filepath.Join(into, res.Pages[0].Path))
	plainCSF, _ := os.ReadFile(filepath.Join(plainInto, plainRes.Pages[0].Path))
	if string(withCSF) != string(plainCSF) {
		t.Errorf(".csf differs with --comments: %q vs %q", withCSF, plainCSF)
	}

	// The page with comment sidecars is still Clean (comments are out of the gate).
	entries, err := svc.Status(context.Background(), into, false)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if len(entries) != 1 || entries[0].LocallyEdited {
		t.Errorf("page with comments sidecars must read Clean, got %+v", entries)
	}

	// Re-pull with fresh comments refreshes the sidecars.
	svc.store.(*pullStore).comments["100"] = []domain.Comment{{ID: "c9", Author: "Carol", Created: "2026-03", Body: "later"}}
	if _, err := svc.Pull(context.Background(), PullOpts{ID: "100", Into: into, Comments: true}); err != nil {
		t.Fatalf("re-pull: %v", err)
	}
	gotMD2, _ := os.ReadFile(filepath.Join(dir, slug+".comments.md"))
	if !strings.Contains(string(gotMD2), "Carol") || strings.Contains(string(gotMD2), "Alice") {
		t.Errorf("re-pull did not refresh comments.md: %q", gotMD2)
	}
}

func TestPullCommentsPersistsQualifiedV2WithoutSecondPageRead(t *testing.T) {
	rootID, otherRootID := "20", "10"
	base := &pullStore{pages: map[string]*domain.Resource{
		"100": {
			ID: "100", Title: "Alpha", SpaceKey: "SP", Version: 2,
			Body: []byte(`<p><ac:inline-comment-marker ac:ref="ref-10">selected</ac:inline-comment-marker></p>`),
		},
	}}
	store := &qualifiedPullStore{
		pullStore: base,
		inventory: completeQualifiedComments(domain.ConfluenceCommentRecord{
			ID: rootID, PageID: "100", RootID: &rootID,
			Relation: domain.ConfluenceCommentRelationRoot, Location: domain.ConfluenceCommentLocationInline,
			Resolution: domain.ConfluenceCommentResolutionOpen, Version: 1,
			MarkerRef: "ref-10", OriginalSelection: "selected", BodyStorage: "<p>comment</p>", CreatedAt: "2026-01-01",
		}, domain.ConfluenceCommentRecord{
			ID: otherRootID, PageID: "100", RootID: &otherRootID,
			Relation: domain.ConfluenceCommentRelationRoot, Location: domain.ConfluenceCommentLocationFooter,
			Resolution: domain.ConfluenceCommentResolutionUnknown, Version: 1,
			BodyStorage: "<p>footer</p>", CreatedAt: "2026-01-02",
		}),
	}
	svc := &ConfluenceService{store: store}
	result, err := svc.Pull(context.Background(), PullOpts{
		ID: "100", Into: t.TempDir(), Comments: true, Render: config.RenderService{Profile: "full"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if base.getPageCalls != 1 || base.listCommentsCalls != 0 || store.readCalls != 1 || !store.readOpts.DepthAll || len(store.readOpts.Locations) != 0 {
		t.Fatalf("reads page=%d legacy=%d qualified=%d opts=%+v", base.getPageCalls, base.listCommentsCalls, store.readCalls, store.readOpts)
	}
	dir, slug := pageDirFrom(result.Root, result.Pages[0].Path)
	data, err := os.ReadFile(filepath.Join(dir, slug+".comments.json"))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := mirror.DecodeConfluenceCommentsSidecar(data)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.V2 == nil || !decoded.V2.Complete || decoded.V2.RootCount != 2 || len(decoded.V2.Comments) != 2 {
		t.Fatalf("qualified sidecar = %+v", decoded.V2)
	}
	var inline *mirror.ConfluenceCommentsSidecarComment
	for i := range decoded.V2.Comments {
		if decoded.V2.Comments[i].ID == rootID {
			inline = &decoded.V2.Comments[i]
		}
	}
	if inline == nil || inline.Anchor == nil || inline.Anchor.Status != domain.ConfluenceAnchorMatched || inline.Anchor.ObservedSelection != "selected" {
		t.Fatalf("qualified inline sidecar = %+v", inline)
	}
	mdPath := filepath.Join(dir, slug+".md")
	if _, err := Apply(mdPath, ApplyOpts{Into: result.Root}); err != nil {
		t.Fatalf("untouched qualified view could not reproduce sidecar order: %v", err)
	}
}

func TestPullCommentsMigratesLegacySidecarToV2(t *testing.T) {
	into := t.TempDir()
	rootID := "10"
	base := &pullStore{pages: map[string]*domain.Resource{
		"100": {ID: "100", Title: "Alpha", SpaceKey: "SP", Version: 2, Body: []byte("<p>alpha</p>")},
	}}
	store := &qualifiedPullStore{pullStore: base, inventory: completeQualifiedComments(domain.ConfluenceCommentRecord{
		ID: rootID, PageID: "100", RootID: &rootID, Relation: domain.ConfluenceCommentRelationRoot,
		Location: domain.ConfluenceCommentLocationFooter, Resolution: domain.ConfluenceCommentResolutionUnknown, Version: 1,
	})}
	first, err := (&ConfluenceService{store: store}).Pull(context.Background(), PullOpts{ID: "100", Into: into})
	if err != nil {
		t.Fatal(err)
	}
	dir, slug := pageDirFrom(into, first.Pages[0].Path)
	legacy, _ := json.Marshal([]domain.Comment{{ID: "old", Author: "Legacy", Body: "old"}})
	if err := os.WriteFile(filepath.Join(dir, slug+".comments.json"), legacy, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := (&ConfluenceService{store: store}).Pull(context.Background(), PullOpts{ID: "100", Into: into, Comments: true}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, slug+".comments.json"))
	decoded, err := mirror.DecodeConfluenceCommentsSidecar(data)
	if err != nil || decoded.Format != mirror.ConfluenceCommentsSidecarFormatV2 || decoded.V2 == nil || decoded.V2.Comments[0].ID != rootID {
		t.Fatalf("migrated sidecar=%+v error=%v", decoded, err)
	}
}

func TestLegacyConfluenceCommentViewKeepsSourceTimestamp(t *testing.T) {
	comments := []domain.Comment{{Created: "2026-01-01T00:00:00.000+0300"}}
	got := confluenceCommentsForDisplay(comments, "")
	if len(got) != 1 || got[0].Created != comments[0].Created {
		t.Fatalf("legacy comments=%+v", got)
	}
	if comments[0].Created != "2026-01-01T00:00:00.000+0300" {
		t.Fatal("source comments were mutated")
	}
}

// A truncated comment listing surfaces both in the meta and in the pull result.
func TestPullCommentsTruncationSurfaced(t *testing.T) {
	into := t.TempDir()
	st := &pullStore{pages: map[string]*domain.Resource{
		"100": {ID: "100", Title: "Alpha", SpaceKey: "SP", Version: 2, Body: []byte("<p>alpha</p>")},
	}, comments: map[string][]domain.Comment{
		"100": {{ID: "c1", Author: "Alice", Created: "t", Body: "hi"}},
	}, commentsTruncated: map[string]bool{"100": true}}
	svc := &ConfluenceService{store: st}
	res, err := svc.Pull(context.Background(), PullOpts{ID: "100", Into: into, Comments: true})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if !res.CommentsTruncated {
		t.Errorf("PullResult.CommentsTruncated = false, want true")
	}
	dir, slug := pageDirFrom(into, res.Pages[0].Path)
	var meta mirror.Meta
	mb, _ := os.ReadFile(filepath.Join(dir, slug+".meta.json"))
	_ = json.Unmarshal(mb, &meta)
	if !meta.CommentsTruncated {
		t.Errorf("meta.CommentsTruncated = false, want true")
	}
}

// A ListComments failure aborts the pull (the user explicitly asked for comments).
func TestPullCommentsFetchErrorAborts(t *testing.T) {
	into := t.TempDir()
	st := &pullStore{pages: map[string]*domain.Resource{
		"100": {ID: "100", Title: "Alpha", SpaceKey: "SP", Version: 2, Body: []byte("<p>alpha</p>")},
	}, commentsErr: domain.ErrForbidden}
	svc := &ConfluenceService{store: st}
	if _, err := svc.Pull(context.Background(), PullOpts{ID: "100", Into: into, Comments: true}); err == nil {
		t.Fatalf("expected the pull to fail when comment fetch fails")
	}
}

// A --comments pull that finds ZERO comments must still be distinguishable from
// a pull that never fetched them: meta carries comments_pulled=true (count
// omitted at 0), the result carries an explicit "comments": 0, and the empty
// sidecar files exist.
func TestPullCommentsZeroCommentsStillMarked(t *testing.T) {
	into := t.TempDir()
	st := &pullStore{pages: map[string]*domain.Resource{
		"100": {ID: "100", Title: "Alpha", SpaceKey: "SP", Version: 2, Body: []byte("<p>alpha</p>")},
	}}
	svc := &ConfluenceService{store: st}
	res, err := svc.Pull(context.Background(), PullOpts{ID: "100", Into: into, Comments: true})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if res.Pages[0].Comments == nil || *res.Pages[0].Comments != 0 {
		t.Fatalf("PulledPage.Comments = %v, want explicit 0", res.Pages[0].Comments)
	}
	if b, _ := json.Marshal(res.Pages[0]); !strings.Contains(string(b), `"comments": 0`) && !strings.Contains(string(b), `"comments":0`) {
		t.Errorf("result JSON must carry an explicit comments:0, got %s", b)
	}
	dir, slug := pageDirFrom(into, res.Pages[0].Path)
	mb, _ := os.ReadFile(filepath.Join(dir, slug+".meta.json"))
	var meta mirror.Meta
	if err := json.Unmarshal(mb, &meta); err != nil {
		t.Fatalf("meta: %v", err)
	}
	if !meta.CommentsPulled || meta.CommentCount != 0 {
		t.Errorf("meta = pulled:%v count:%d, want pulled:true count:0", meta.CommentsPulled, meta.CommentCount)
	}
	if _, err := os.Stat(filepath.Join(dir, slug+".comments.json")); err != nil {
		t.Errorf("empty comments sidecar must still exist: %v", err)
	}
}
