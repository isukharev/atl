package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

// assetPullTracker serves a search projection and streams attachment bytes keyed
// by their content URL. It fails the test if the pull ever falls back to the
// per-issue ListAttachments/DownloadAttachment path (the N×M re-list the plan
// forbids).
type assetPullTracker struct {
	domain.Tracker
	t           *testing.T
	issues      []domain.Issue
	blobs       map[string][]byte
	failURL     map[string]bool
	streamCalls []string
}

func (tr *assetPullTracker) Search(context.Context, string, []string, int, string) ([]domain.Issue, string, error) {
	return tr.issues, "", nil
}

func (tr *assetPullTracker) StreamAttachment(_ context.Context, contentURL string) (io.ReadCloser, error) {
	tr.streamCalls = append(tr.streamCalls, contentURL)
	if tr.failURL[contentURL] {
		return nil, errors.New("simulated stream failure")
	}
	b, ok := tr.blobs[contentURL]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (tr *assetPullTracker) ListAttachments(context.Context, string) ([]domain.Attachment, error) {
	tr.t.Fatalf("pull must not call ListAttachments (N×M re-list); it should stream by content URL")
	return nil, nil
}

func (tr *assetPullTracker) DownloadAttachment(context.Context, string, string) (io.ReadCloser, string, error) {
	tr.t.Fatalf("pull must not call DownloadAttachment; it should stream by content URL")
	return nil, "", nil
}

// att builds one raw Jira attachment metadata map as it appears in
// fields["attachment"] (size arrives as a JSON float64).
func att(id, filename, mime, content string) map[string]any {
	return map[string]any{
		"id":       id,
		"filename": filename,
		"mimeType": mime,
		"size":     float64(len(content)),
		"content":  content,
	}
}

func issueWithAttachments(key, project string, atts ...map[string]any) domain.Issue {
	raw := make([]any, len(atts))
	for i, a := range atts {
		raw[i] = a
	}
	return domain.Issue{
		Key:     key,
		Project: project,
		Summary: "S",
		Status:  "Open",
		Type:    "Task",
		Fields:  map[string]any{"attachment": raw},
	}
}

// ---- decode helper robustness ----

func TestDecodeIssueAssets(t *testing.T) {
	t.Run("nil and non-array yield nil", func(t *testing.T) {
		if got := decodeIssueAssets(nil); got != nil {
			t.Errorf("nil raw = %+v, want nil", got)
		}
		if got := decodeIssueAssets("not-an-array"); got != nil {
			t.Errorf("string raw = %+v, want nil", got)
		}
	})

	t.Run("skips non-map elements, tolerates missing fields", func(t *testing.T) {
		raw := []any{
			"garbage",
			42,
			map[string]any{"id": "1", "filename": "a.png", "mimeType": "image/png", "size": float64(7), "content": "/c/1"},
			map[string]any{}, // fully empty is tolerated
		}
		got := decodeIssueAssets(raw)
		if len(got) != 2 {
			t.Fatalf("decoded %d assets, want 2 (maps only): %+v", len(got), got)
		}
		a := got[0]
		if a.ID != "1" || a.Title != "a.png" || a.MediaType != "image/png" || a.FileSize != 7 || a.ContentURL != "/c/1" {
			t.Errorf("decoded[0] = %+v, want fully populated", a)
		}
		if b := got[1]; b.ID != "" || b.Title != "" || b.FileSize != 0 {
			t.Errorf("decoded[1] = %+v, want zero-valued", b)
		}
	})

	t.Run("oddly-typed fields do not panic", func(t *testing.T) {
		raw := []any{map[string]any{"id": 99, "filename": true, "size": "big", "content": nil}}
		got := decodeIssueAssets(raw)
		if len(got) != 1 {
			t.Fatalf("decoded %d, want 1", len(got))
		}
		if got[0].ID != "" || got[0].Title != "" || got[0].FileSize != 0 {
			t.Errorf("odd types = %+v, want zeroed", got[0])
		}
	})
}

// ---- pull --assets behavior ----

func TestJiraPullAssetsDownloadsOnlyImages(t *testing.T) {
	into := t.TempDir()
	iss := issueWithAttachments("PROJ-1", "PROJ",
		att("10001", "shot.png", "image/png", "/secure/att/10001"),
		att("10002", "spec.pdf", "application/pdf", "/secure/att/10002"),
		att("10003", "photo.jpg", "image/jpeg", "/secure/att/10003"),
	)
	tr := &assetPullTracker{
		t:      t,
		issues: []domain.Issue{iss},
		blobs: map[string][]byte{
			"/secure/att/10001": []byte("PNGBYTES"),
			"/secure/att/10002": []byte("PDFBYTES"),
			"/secure/att/10003": []byte("JPGBYTES"),
		},
	}
	svc := &JiraService{tr: tr}
	res, err := svc.Pull(context.Background(), JiraPullOpts{JQL: "project=PROJ", Into: into, Limit: 1, Assets: true})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(res.Issues) != 1 || res.Issues[0].Assets != 2 {
		t.Fatalf("pull result = %+v, want 1 issue with 2 assets", res.Issues)
	}
	if res.AssetsSkipped != 0 {
		t.Errorf("AssetsSkipped = %d, want 0", res.AssetsSkipped)
	}
	// Only the two images landed on disk; the PDF was never streamed.
	assetsDir := filepath.Join(into, "PROJ", "PROJ-1.assets")
	wantFiles := []string{"10001-shot.png", "10003-photo.jpg"}
	for _, f := range wantFiles {
		if _, err := os.Stat(filepath.Join(assetsDir, f)); err != nil {
			t.Errorf("missing asset %s: %v", f, err)
		}
	}
	if _, err := os.Stat(filepath.Join(assetsDir, "10002-spec.pdf")); err == nil {
		t.Errorf("non-image spec.pdf was written, want skipped")
	}
	// The non-image was never streamed at all.
	for _, u := range tr.streamCalls {
		if u == "/secure/att/10002" {
			t.Errorf("non-image content URL %q was streamed", u)
		}
	}
	// The .md links only the images.
	md := readMD(t, into, "PROJ", "PROJ-1")
	mustContain(t, md, "## Image Attachments")
	mustContain(t, md, "![shot.png](PROJ-1.assets/10001-shot.png)")
	mustContain(t, md, "![photo.jpg](PROJ-1.assets/10003-photo.jpg)")
	mustNotContain(t, md, "spec.pdf")
}

func TestJiraPullAssetsSkipsEmptyAndOctetStream(t *testing.T) {
	into := t.TempDir()
	iss := issueWithAttachments("PROJ-2", "PROJ",
		att("1", "nomime.png", "", "/c/1"),
		att("2", "blob.png", "application/octet-stream", "/c/2"),
	)
	tr := &assetPullTracker{t: t, issues: []domain.Issue{iss}, blobs: map[string][]byte{"/c/1": []byte("x"), "/c/2": []byte("y")}}
	svc := &JiraService{tr: tr}
	res, err := svc.Pull(context.Background(), JiraPullOpts{JQL: "project=PROJ", Into: into, Limit: 1, Assets: true})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	// Neither is an image/* type: not downloaded, and not counted as "skipped"
	// (a skip is a failed image, not a filtered-out non-image).
	if res.Issues[0].Assets != 0 || res.AssetsSkipped != 0 {
		t.Fatalf("assets=%d skipped=%d, want 0/0", res.Issues[0].Assets, res.AssetsSkipped)
	}
	if len(tr.streamCalls) != 0 {
		t.Errorf("streamed %v, want nothing (mime filtered)", tr.streamCalls)
	}
	if _, err := os.Stat(filepath.Join(into, "PROJ", "PROJ-2.assets")); !os.IsNotExist(err) {
		t.Errorf("assets dir should not exist, stat err = %v", err)
	}
	mustNotContain(t, readMD(t, into, "PROJ", "PROJ-2"), "## Image Attachments")
}

func TestJiraPullAssetsDuplicateFilenames(t *testing.T) {
	into := t.TempDir()
	iss := issueWithAttachments("PROJ-3", "PROJ",
		att("100", "screen.png", "image/png", "/c/100"),
		att("200", "screen.png", "image/png", "/c/200"),
	)
	tr := &assetPullTracker{t: t, issues: []domain.Issue{iss}, blobs: map[string][]byte{"/c/100": []byte("first"), "/c/200": []byte("second")}}
	svc := &JiraService{tr: tr}
	res, err := svc.Pull(context.Background(), JiraPullOpts{JQL: "project=PROJ", Into: into, Limit: 1, Assets: true})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if res.Issues[0].Assets != 2 {
		t.Fatalf("assets = %d, want 2 (distinct id-prefixed files)", res.Issues[0].Assets)
	}
	assetsDir := filepath.Join(into, "PROJ", "PROJ-3.assets")
	first, err := os.ReadFile(filepath.Join(assetsDir, "100-screen.png"))
	if err != nil {
		t.Fatalf("read 100-screen.png: %v", err)
	}
	second, err := os.ReadFile(filepath.Join(assetsDir, "200-screen.png"))
	if err != nil {
		t.Fatalf("read 200-screen.png: %v", err)
	}
	if string(first) != "first" || string(second) != "second" {
		t.Errorf("duplicate filenames overwrote each other: %q / %q", first, second)
	}
}

func TestJiraPullAssetsUnsafeNamesCannotEscape(t *testing.T) {
	root := t.TempDir()
	into := filepath.Join(root, "mirror")
	iss := issueWithAttachments("PROJ-4", "PROJ",
		att("10", "../../../../tmp/atl-evil.png", "image/png", "/c/10"),
		att("../../evil-id", "ok.png", "image/png", "/c/11"),
	)
	tr := &assetPullTracker{t: t, issues: []domain.Issue{iss}, blobs: map[string][]byte{"/c/10": []byte("evil"), "/c/11": []byte("ok")}}
	svc := &JiraService{tr: tr}
	res, err := svc.Pull(context.Background(), JiraPullOpts{JQL: "project=PROJ", Into: into, Limit: 1, Assets: true})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	// Nothing was written outside the mirror tree.
	assertNothingOutside(t, root, into)
	escaped := filepath.Clean(filepath.Join(root, "..", "..", "..", "..", "tmp", "atl-evil.png"))
	if _, err := os.Stat(escaped); err == nil {
		t.Fatalf("asset escaped to %s", escaped)
	}
	// Whatever landed stayed confined to the issue's assets dir.
	assetsDir := filepath.Join(into, "PROJ", "PROJ-4.assets")
	if res.Issues[0].Assets > 0 {
		entries, _ := os.ReadDir(assetsDir)
		for _, e := range entries {
			p := filepath.Join(assetsDir, e.Name())
			if filepath.Dir(p) != filepath.Clean(assetsDir) {
				t.Errorf("asset %s escaped assets dir", p)
			}
		}
	}
}

func TestJiraPullAssetsBestEffortOnStreamError(t *testing.T) {
	into := t.TempDir()
	iss := issueWithAttachments("PROJ-5", "PROJ",
		att("1", "good.png", "image/png", "/c/1"),
		att("2", "bad.png", "image/png", "/c/2"),
	)
	tr := &assetPullTracker{
		t:       t,
		issues:  []domain.Issue{iss},
		blobs:   map[string][]byte{"/c/1": []byte("good"), "/c/2": []byte("bad")},
		failURL: map[string]bool{"/c/2": true},
	}
	svc := &JiraService{tr: tr}
	res, err := svc.Pull(context.Background(), JiraPullOpts{JQL: "project=PROJ", Into: into, Limit: 1, Assets: true})
	if err != nil {
		t.Fatalf("a failing image must not abort the pull, got err=%v", err)
	}
	// The issue is still fully written.
	if _, err := os.Stat(filepath.Join(into, "PROJ", "PROJ-5.md")); err != nil {
		t.Fatalf("issue .md missing after a failed image: %v", err)
	}
	if res.Issues[0].Assets != 1 || res.AssetsSkipped != 1 {
		t.Fatalf("assets=%d skipped=%d, want 1 landed / 1 skipped", res.Issues[0].Assets, res.AssetsSkipped)
	}
	// Only the image that landed on disk is linked; the failed one is not, and
	// its partial temp file is not left behind.
	md := readMD(t, into, "PROJ", "PROJ-5")
	mustContain(t, md, "![good.png](PROJ-5.assets/1-good.png)")
	mustNotContain(t, md, "bad.png")
	if _, err := os.Stat(filepath.Join(into, "PROJ", "PROJ-5.assets", "2-bad.png")); err == nil {
		t.Errorf("failed image left a file on disk")
	}
}

func TestJiraPullAssetsMarkdownPlacementBetweenDescriptionAndLinks(t *testing.T) {
	into := t.TempDir()
	iss := issueWithAttachments("PROJ-6", "PROJ", att("7", "d.png", "image/png", "/c/7"))
	iss.Body = "wiki body"
	iss.Links = []domain.IssueLink{{Type: "blocks", Key: "PROJ-9"}}
	iss.Comments = []domain.Comment{{Author: "bob", Created: "2026-01-01", Body: "hi"}}
	tr := &assetPullTracker{t: t, issues: []domain.Issue{iss}, blobs: map[string][]byte{"/c/7": []byte("img")}}
	svc := &JiraService{tr: tr}
	if _, err := svc.Pull(context.Background(), JiraPullOpts{JQL: "project=PROJ", Into: into, Limit: 1, Assets: true}); err != nil {
		t.Fatalf("pull: %v", err)
	}
	md := readMD(t, into, "PROJ", "PROJ-6")
	desc := strings.Index(md, "## Description")
	img := strings.Index(md, "## Image Attachments")
	links := strings.Index(md, "## Links")
	comments := strings.Index(md, "## Comments")
	if desc < 0 || img < 0 || links < 0 || comments < 0 {
		t.Fatalf("missing a section: desc=%d img=%d links=%d comments=%d\n%s", desc, img, links, comments, md)
	}
	if desc >= img || img >= links || links >= comments {
		t.Errorf("section order desc=%d img=%d links=%d comments=%d, want desc<img<links<comments", desc, img, links, comments)
	}
}

// The raw JSON snapshot must be untouched: it mirrors Jira's fields verbatim and
// must never gain local asset paths.
func TestJiraPullAssetsJSONSnapshotUnchanged(t *testing.T) {
	into := t.TempDir()
	iss := issueWithAttachments("PROJ-7", "PROJ",
		att("1", "a.png", "image/png", "/c/1"),
		att("2", "b.pdf", "application/pdf", "/c/2"),
	)
	tr := &assetPullTracker{t: t, issues: []domain.Issue{iss}, blobs: map[string][]byte{"/c/1": []byte("a"), "/c/2": []byte("b")}}
	svc := &JiraService{tr: tr}
	if _, err := svc.Pull(context.Background(), JiraPullOpts{JQL: "project=PROJ", Into: into, Limit: 1, Assets: true}); err != nil {
		t.Fatalf("pull: %v", err)
	}
	jb, err := os.ReadFile(filepath.Join(into, "PROJ", "PROJ-7.json"))
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	var snap JiraIssueSnapshot
	if err := json.Unmarshal(jb, &snap); err != nil {
		t.Fatalf("decode snapshot: %v\n%s", err, jb)
	}
	arr, ok := snap.Fields["attachment"].([]any)
	if !ok || len(arr) != 2 {
		t.Fatalf("snapshot attachment = %v, want the raw 2-element array", snap.Fields["attachment"])
	}
	// No local path leaked into the raw metadata.
	if strings.Contains(string(jb), ".assets/") || strings.Contains(string(jb), "\"path\"") {
		t.Errorf("snapshot leaked a local asset path:\n%s", jb)
	}
}

// A default pull (no --assets) downloads nothing and never touches the stream
// path.
func TestJiraPullWithoutAssetsSkipsImages(t *testing.T) {
	into := t.TempDir()
	iss := issueWithAttachments("PROJ-8", "PROJ", att("1", "a.png", "image/png", "/c/1"))
	tr := &assetPullTracker{t: t, issues: []domain.Issue{iss}, blobs: map[string][]byte{"/c/1": []byte("a")}}
	svc := &JiraService{tr: tr}
	res, err := svc.Pull(context.Background(), JiraPullOpts{JQL: "project=PROJ", Into: into, Limit: 1})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if res.Issues[0].Assets != 0 || len(tr.streamCalls) != 0 {
		t.Errorf("default pull downloaded assets: assets=%d streamCalls=%v", res.Issues[0].Assets, tr.streamCalls)
	}
	if _, err := os.Stat(filepath.Join(into, "PROJ", "PROJ-8.assets")); !os.IsNotExist(err) {
		t.Errorf("assets dir created without --assets")
	}
	mustNotContain(t, readMD(t, into, "PROJ", "PROJ-8"), "## Image Attachments")
}

// A filename with markdown-significant characters (spaces, parens, brackets)
// must not corrupt the generated image link: alt text is escaped and the
// destination is percent-encoded, while the on-disk name keeps the safe base.
func TestJiraPullAssetsMarkdownSignificantFilename(t *testing.T) {
	into := t.TempDir()
	iss := issueWithAttachments("PROJ-9", "PROJ",
		att("42", "shot (v1) [final].png", "image/png", "/c/42"),
	)
	tr := &assetPullTracker{t: t, issues: []domain.Issue{iss}, blobs: map[string][]byte{"/c/42": []byte("img")}}
	svc := &JiraService{tr: tr}
	res, err := svc.Pull(context.Background(), JiraPullOpts{JQL: "project=PROJ", Into: into, Limit: 1, Assets: true})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if res.Issues[0].Assets != 1 {
		t.Fatalf("assets = %d, want 1", res.Issues[0].Assets)
	}
	if _, err := os.Stat(filepath.Join(into, "PROJ", "PROJ-9.assets", "42-shot (v1) [final].png")); err != nil {
		t.Fatalf("asset file missing: %v", err)
	}
	md := readMD(t, into, "PROJ", "PROJ-9")
	mustContain(t, md, `![shot (v1) \[final\].png](PROJ-9.assets/42-shot%20%28v1%29%20[final].png)`)
	// The raw unescaped link must not appear.
	mustNotContain(t, md, "](PROJ-9.assets/42-shot (v1)")
}

func readMD(t *testing.T, into, project, key string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(into, project, key+".md"))
	if err != nil {
		t.Fatalf("read %s.md: %v", key, err)
	}
	return string(b)
}
