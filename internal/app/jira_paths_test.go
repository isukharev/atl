package app

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

// partialTracker embeds the interface so only the methods a test needs are
// implemented; any unexpected call panics with a nil-method dispatch.
type partialTracker struct {
	domain.Tracker
	atts   []domain.Attachment
	data   []byte
	name   string
	issues []domain.Issue
}

func (t partialTracker) ListAttachments(context.Context, string) ([]domain.Attachment, error) {
	return t.atts, nil
}

func (t partialTracker) DownloadAttachment(context.Context, string, string) (io.ReadCloser, string, error) {
	return io.NopCloser(bytes.NewReader(t.data)), t.name, nil
}

func (t partialTracker) UploadAttachment(_ context.Context, _ string, filename string, data io.Reader, _ int64) (*domain.Attachment, error) {
	b, err := io.ReadAll(data)
	if err != nil {
		return nil, err
	}
	return &domain.Attachment{ID: "42", Title: filename, FileSize: int64(len(b))}, nil
}

func (t partialTracker) Search(context.Context, string, []string, int, string) ([]domain.Issue, string, error) {
	return t.issues, "", nil
}

func (t partialTracker) GetIssue(_ context.Context, key string, _ []string) (*domain.Issue, error) {
	for i := range t.issues {
		if t.issues[i].Key == key {
			is := t.issues[i]
			return &is, nil
		}
	}
	return nil, domain.ErrNotFound
}

// A hostile Jira attachment filename must not let `jira images` escape the
// output directory.
func TestJiraImagesRejectsTraversalFilename(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "mirror")
	s := &JiraService{tr: partialTracker{
		atts: []domain.Attachment{{ID: "1", MediaType: "image/png"}},
		data: []byte("evil"),
		name: "../../../../tmp/atl-evil.png",
	}}
	if _, err := s.Images(context.Background(), "PROJ-1", dir); err != nil {
		t.Logf("Images returned %v (acceptable: rejected)", err)
	}
	assertNothingOutside(t, root, dir)
	escaped := filepath.Clean(filepath.Join(root, "..", "..", "..", "..", "tmp", "atl-evil.png"))
	if _, err := os.Stat(escaped); err == nil {
		t.Fatalf("attachment escaped to %s", escaped)
	}
}

func TestJiraDownloadAttachmentWritesAnyAttachment(t *testing.T) {
	dir := t.TempDir()
	s := &JiraService{tr: partialTracker{
		data: []byte("xlsx bytes"),
		name: "report.xlsx",
	}}
	path, name, err := s.DownloadAttachment(context.Background(), "PROJ-1", "42", dir)
	if err != nil {
		t.Fatalf("DownloadAttachment: %v", err)
	}
	if name != "report.xlsx" {
		t.Fatalf("name = %q, want report.xlsx", name)
	}
	if filepath.Dir(path) != filepath.Clean(dir) {
		t.Fatalf("path = %q, want inside %q", path, dir)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if string(data) != "xlsx bytes" {
		t.Fatalf("downloaded data = %q, want xlsx bytes", data)
	}
}

func TestJiraDownloadAttachmentConfinesTraversalFilename(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "mirror")
	s := &JiraService{tr: partialTracker{
		data: []byte("evil"),
		name: "../../../../tmp/atl-evil.xlsx",
	}}
	path, _, err := s.DownloadAttachment(context.Background(), "PROJ-1", "42", dir)
	if err != nil {
		t.Fatalf("DownloadAttachment: %v", err)
	}
	if filepath.Base(path) != "atl-evil.xlsx" || filepath.Dir(path) != filepath.Clean(dir) {
		t.Fatalf("path = %q, want confined basename under %q", path, dir)
	}
	assertNothingOutside(t, root, dir)
	escaped := filepath.Clean(filepath.Join(root, "..", "..", "..", "..", "tmp", "atl-evil.xlsx"))
	if _, err := os.Stat(escaped); err == nil {
		t.Fatalf("attachment escaped to %s", escaped)
	}
}

func TestJiraUploadAttachmentReadsFileAndUsesBaseName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.xlsx")
	if err := os.WriteFile(path, []byte("xlsx bytes"), 0o644); err != nil {
		t.Fatalf("write upload file: %v", err)
	}
	tr := &recordingUploadTracker{}
	s := &JiraService{tr: tr}
	att, err := s.UploadAttachment(context.Background(), "PROJ-1", path)
	if err != nil {
		t.Fatalf("UploadAttachment: %v", err)
	}
	if tr.uploadedKey != "PROJ-1" || tr.uploadedName != "report.xlsx" || string(tr.uploadedData) != "xlsx bytes" || tr.uploadedSize != int64(len("xlsx bytes")) {
		t.Fatalf("uploaded key=%q name=%q size=%d data=%q", tr.uploadedKey, tr.uploadedName, tr.uploadedSize, tr.uploadedData)
	}
	if att.ID != "42" || att.Title != "report.xlsx" {
		t.Fatalf("attachment = %+v, want id/title", att)
	}
}

type recordingUploadTracker struct {
	domain.Tracker
	uploadedKey  string
	uploadedName string
	uploadedSize int64
	uploadedData []byte
}

func (t *recordingUploadTracker) UploadAttachment(_ context.Context, key, filename string, data io.Reader, size int64) (*domain.Attachment, error) {
	b, err := io.ReadAll(data)
	if err != nil {
		return nil, err
	}
	t.uploadedKey = key
	t.uploadedName = filename
	t.uploadedSize = size
	t.uploadedData = append([]byte(nil), b...)
	return &domain.Attachment{ID: "42", Title: filename, FileSize: int64(len(b))}, nil
}

// A hostile Jira issue key must not let `jira pull` escape the --into directory.
func TestJiraPullRejectsTraversalKey(t *testing.T) {
	root := t.TempDir()
	into := filepath.Join(root, "mirror")
	s := &JiraService{tr: partialTracker{
		issues: []domain.Issue{{Key: "../../../../tmp/atl-evil", Project: "PROJ"}},
	}}
	res, err := s.Pull(context.Background(), JiraPullOpts{JQL: "project = PROJ", Into: into, Limit: 1})
	if err != nil {
		t.Logf("Pull returned %v (acceptable: rejected)", err)
	}
	var out []JiraPulled
	if res != nil {
		out = res.Issues
	}
	for _, p := range out {
		// p.Path is relative to --into; it must not climb out of it. (A single
		// sanitized filename may contain ".." as literal characters — that is not
		// a traversal — so test path semantics, not a substring.)
		if rel := filepath.Clean(p.Path); rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Fatalf("returned escaping path %q", p.Path)
		}
	}
	assertNothingOutside(t, root, into)
}

// assertNothingOutside fails if any regular file under root lies outside allowed.
func assertNothingOutside(t *testing.T, root, allowed string) {
	t.Helper()
	allowed = filepath.Clean(allowed)
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		clean := filepath.Clean(p)
		rel, rerr := filepath.Rel(allowed, clean)
		if rerr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Errorf("file written outside the output dir: %s", clean)
		}
		return nil
	})
}
