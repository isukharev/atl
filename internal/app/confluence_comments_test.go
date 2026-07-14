package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

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

	// .comments.json is the domain.Comment array, pretty-printed with trailing NL.
	wantJSON, _ := json.MarshalIndent(comments, "", "  ")
	wantJSON = append(wantJSON, '\n')
	gotJSON, err := os.ReadFile(filepath.Join(dir, slug+".comments.json"))
	if err != nil {
		t.Fatalf("read comments.json: %v", err)
	}
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("comments.json mismatch:\n got %q\nwant %q", gotJSON, wantJSON)
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
	if meta.CommentCount != 2 || meta.CommentsTruncated {
		t.Errorf("meta comment fields = {count:%d truncated:%v}, want {2 false}", meta.CommentCount, meta.CommentsTruncated)
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
