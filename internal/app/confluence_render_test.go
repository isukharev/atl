package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

// seedConfMirror writes one page (.csf + .md + .meta.json + base + sidecar) via
// the mirror engine and returns the mirror root and page dir/slug.
func seedConfMirror(t *testing.T, comments []domain.Comment) (root, dir, slug string) {
	t.Helper()
	root = t.TempDir()
	m := mirror.New(root)
	if err := m.EnsureScaffold(); err != nil {
		t.Fatal(err)
	}
	page := &domain.Resource{
		ID: "1001", Title: "My Page", SpaceKey: "DOCS", Version: 3,
		Labels: []string{"a", "b"},
		Body:   []byte("<p>Body text.</p>"),
	}
	pdir, pslug, err := m.ClaimPageDir(page.SpaceKey, page.Ancestors, page.Title, page.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.BeginSync()
	if err != nil {
		t.Fatal(err)
	}
	if comments != nil {
		if err := b.WriteComments(pdir, pslug, page, nil, comments, false, mirror.MDViewOpts{}); err != nil {
			t.Fatal(err)
		}
	} else if err := b.Write(pdir, pslug, page, nil); err != nil {
		t.Fatal(err)
	}
	if err := b.Flush(); err != nil {
		t.Fatal(err)
	}
	return root, pdir, pslug
}

// Default profile render leaves the .md byte-identical to the body-only view.
func TestConfRenderDefaultByteIdentical(t *testing.T) {
	root, dir, slug := seedConfMirror(t, nil)
	before := mustReadFile(t, filepath.Join(dir, slug+".md"))
	svc := NewConfluenceRenderer(&config.Config{})
	if _, err := svc.Render(root, config.RenderService{}); err != nil {
		t.Fatal(err)
	}
	after := mustReadFile(t, filepath.Join(dir, slug+".md"))
	if before != after {
		t.Errorf("default render changed the .md:\n before=%q\n after=%q", before, after)
	}
	if strings.Contains(after, "---\ntitle:") {
		t.Error("default profile must not add frontmatter")
	}
}

func TestReadCommentsSidecarDistinguishesLegacyFutureAndStaleV2(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "DOCS", "page")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "page.comments.json")
	if err := os.WriteFile(path, []byte(`[{"id":"1","author":"Legacy"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	comments, err := readCommentsSidecar(root, dir, "page", "100", 3)
	if err != nil || len(comments.flat) != 1 || comments.flat[0].ID != "1" || comments.qualified != nil {
		t.Fatalf("legacy comments=%+v error=%v", comments, err)
	}
	if err := os.WriteFile(path, []byte(`{"schema_version":99}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readCommentsSidecar(root, dir, "page", "100", 3); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("future sidecar error=%v, want ErrCheckFailed", err)
	}
	v2 := mirror.ConfluenceCommentsSidecarV2{
		SchemaVersion: 2, PageID: "100", PageVersion: 2,
		Complete: true, CommentsComplete: true, ThreadsComplete: true, AnchorsComplete: true,
		PartialReasons: []string{}, Capabilities: completeCommentCapabilities(),
		Comments: []mirror.ConfluenceCommentsSidecarComment{}, Diagnostics: []mirror.ConfluenceCommentsSidecarDiagnostic{},
	}
	encoded, err := mirror.EncodeConfluenceCommentsSidecarV2(v2)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readCommentsSidecar(root, dir, "page", "100", 3); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("stale sidecar error=%v, want ErrCheckFailed", err)
	}
}

func TestConfRenderWarnsAndOmitsInvalidCommentSidecar(t *testing.T) {
	root, dir, slug := seedConfMirror(t, nil)
	csfPath := filepath.Join(dir, slug+".csf")
	metaPath := filepath.Join(dir, slug+".meta.json")
	beforeCSF := mustReadFile(t, csfPath)
	beforeMeta := mustReadFile(t, metaPath)
	if err := os.WriteFile(filepath.Join(dir, slug+".comments.json"), []byte(`{"schema_version":99}`), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := NewConfluenceRenderer(&config.Config{}).Render(root, config.RenderService{Profile: "full"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Warnings) == 0 || !strings.Contains(result.Warnings[0], "comments") {
		t.Fatalf("warnings = %v", result.Warnings)
	}
	md := mustReadFile(t, filepath.Join(dir, slug+".md"))
	if strings.Contains(md, mirror.ConfluenceCommentsMarker) {
		t.Fatalf("invalid sidecar rendered comments: %s", md)
	}
	if got := mustReadFile(t, csfPath); got != beforeCSF {
		t.Fatal("offline render changed native CSF")
	}
	if got := mustReadFile(t, metaPath); got != beforeMeta {
		t.Fatal("offline render changed metadata")
	}
}

func TestConfRenderRefusesUnsupportedExistingViewVersion(t *testing.T) {
	root, dir, slug := seedConfMirror(t, nil)
	mdPath := filepath.Join(dir, slug+".md")
	future := strings.Replace(mustReadFile(t, mdPath), mirror.ConfluenceDocumentMarker, "<!-- atl:document confluence-page v99 -->", 1)
	future = strings.Replace(future, "\n", "\r\n", 1)
	if err := os.WriteFile(mdPath, []byte(future), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := NewConfluenceRenderer(&config.Config{})
	if _, err := svc.Render(root, config.RenderService{}); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("future view render error = %v", err)
	}
	if got := mustReadFile(t, mdPath); got != future {
		t.Fatalf("future view was overwritten:\n%s", got)
	}
}

func TestConfRenderAcceptsCurrentCRLFMarkerLine(t *testing.T) {
	root, dir, slug := seedConfMirror(t, nil)
	mdPath := filepath.Join(dir, slug+".md")
	crlfMarker := strings.Replace(mustReadFile(t, mdPath), "\n", "\r\n", 1)
	mustWriteFile(t, mdPath, crlfMarker)
	if _, err := NewConfluenceRenderer(&config.Config{}).Render(root, config.RenderService{}); err != nil {
		t.Fatalf("current CRLF marker render: %v", err)
	}
	if got := mustReadFile(t, mdPath); !strings.HasPrefix(got, mirror.ConfluenceDocumentMarker+"\n") {
		t.Fatalf("marker was not regenerated canonically: %q", got)
	}
}

func TestConfRenderPreflightsWholeBatchBeforeSiblingRewrite(t *testing.T) {
	root, dir, slug := seedConfMirror(t, nil)
	firstPath := filepath.Join(dir, slug+".md")
	firstBefore := mustReadFile(t, firstPath)
	m := mirror.New(root)
	page := &domain.Resource{ID: "1002", Title: "Future", SpaceKey: "DOCS", Version: 1, Body: []byte("<p>Future.</p>")}
	secondDir, secondSlug, err := m.ClaimPageDir(page.SpaceKey, nil, page.Title, page.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.BeginSync()
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Write(secondDir, secondSlug, page, nil); err != nil {
		t.Fatal(err)
	}
	if err := b.Flush(); err != nil {
		t.Fatal(err)
	}
	secondPath := filepath.Join(secondDir, secondSlug+".md")
	future := strings.Replace(mustReadFile(t, secondPath), mirror.ConfluenceDocumentMarker, "<!-- atl:document confluence-page v99 -->", 1)
	mustWriteFile(t, secondPath, future)

	if _, err := NewConfluenceRenderer(&config.Config{}).Render(root, config.RenderService{Profile: "full"}); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("render error=%v, want check failed", err)
	}
	if got := mustReadFile(t, firstPath); got != firstBefore {
		t.Fatal("sibling view changed before future-version refusal")
	}
}

func TestConfRenderMigratesKnownLegacyViewMarkers(t *testing.T) {
	for _, marker := range []string{mirror.ConfluenceDocumentMarkerV5, mirror.ConfluenceDocumentMarkerV4} {
		t.Run(marker, func(t *testing.T) {
			root, dir, slug := seedConfMirror(t, nil)
			mdPath := filepath.Join(dir, slug+".md")
			legacy := strings.Replace(mustReadFile(t, mdPath), mirror.ConfluenceDocumentMarker, marker, 1)
			if err := os.WriteFile(mdPath, []byte(legacy), 0o644); err != nil {
				t.Fatal(err)
			}
			svc := NewConfluenceRenderer(&config.Config{})
			if _, err := svc.Render(root, config.RenderService{}); err != nil {
				t.Fatalf("legacy render migration: %v", err)
			}
			if got := mustReadFile(t, mdPath); !strings.HasPrefix(got, mirror.ConfluenceDocumentMarker+"\n") {
				t.Fatalf("legacy marker was not upgraded: %q", got)
			}
		})
	}
}

func TestConfRenderPreservesUnsupportedHistoricalViewMarkers(t *testing.T) {
	for _, marker := range []string{"<!-- atl:document confluence-page v3 -->", "<!-- atl:document confluence-page v2 -->", "<!-- atl:document confluence-page v1 -->"} {
		t.Run(marker, func(t *testing.T) {
			root, dir, slug := seedConfMirror(t, nil)
			mdPath := filepath.Join(dir, slug+".md")
			old := strings.Replace(mustReadFile(t, mdPath), mirror.ConfluenceDocumentMarker, marker, 1)
			mustWriteFile(t, mdPath, old)
			if _, err := NewConfluenceRenderer(&config.Config{}).Render(root, config.RenderService{}); !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("historical render error=%v", err)
			}
			if got := mustReadFile(t, mdPath); got != old {
				t.Fatal("unsupported historical view was overwritten")
			}
		})
	}
}

func TestConfRenderPreservesUnversionedView(t *testing.T) {
	root, dir, slug := seedConfMirror(t, nil)
	mdPath := filepath.Join(dir, slug+".md")
	unversioned := strings.Replace(mustReadFile(t, mdPath), mirror.ConfluenceDocumentMarker, "<!-- atl:document confluence-page -->", 1)
	mustWriteFile(t, mdPath, unversioned)
	if _, err := NewConfluenceRenderer(&config.Config{}).Render(root, config.RenderService{}); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("unversioned render error=%v", err)
	}
	if got := mustReadFile(t, mdPath); got != unversioned {
		t.Fatal("unversioned view was overwritten")
	}
}

// Full profile adds typed page fields and, when the sidecar is present, a Comments
// section.
func TestConfRenderFullAddsPageFieldsAndComments(t *testing.T) {
	root, dir, slug := seedConfMirror(t, []domain.Comment{{ID: "1", Author: "alice", Created: "2026-01-01", Body: "hi"}})
	svc := NewConfluenceRenderer(&config.Config{})
	if _, err := svc.Render(root, config.RenderService{Profile: "full"}); err != nil {
		t.Fatal(err)
	}
	md := mustReadFile(t, filepath.Join(dir, slug+".md"))
	if !strings.HasPrefix(md, mirror.ConfluenceDocumentMarker+"\n"+mirror.ConfluencePageFieldsMarker+"\n# Metadata\n") ||
		!strings.Contains(md, "| Title | My Page |") || !strings.Contains(md, "| Labels | a, b |") {
		t.Errorf("full render missing typed page fields:\n%s", md)
	}
	if !strings.Contains(md, "# Comments") || !strings.Contains(md, "## Comment by alice (2026-01-01)") {
		t.Errorf("full render missing comments section:\n%s", md)
	}
}

// Full profile with no comments sidecar renders page fields but silently skips
// the Comments section.
func TestConfRenderFullNoSidecarSkipsComments(t *testing.T) {
	root, dir, slug := seedConfMirror(t, nil)
	svc := NewConfluenceRenderer(&config.Config{})
	if _, err := svc.Render(root, config.RenderService{Profile: "full"}); err != nil {
		t.Fatal(err)
	}
	md := mustReadFile(t, filepath.Join(dir, slug+".md"))
	if !strings.Contains(md, "| Title | My Page |") {
		t.Errorf("expected page metadata table:\n%s", md)
	}
	if strings.Contains(md, "# Comments") {
		t.Errorf("no sidecar → no Comments section:\n%s", md)
	}
}

func TestConfRenderUnknownRestrictionIsExplicitAndWarned(t *testing.T) {
	root, dir, slug := seedConfMirror(t, nil)
	svc := NewConfluenceRenderer(&config.Config{})
	res, err := svc.Render(root, config.RenderService{
		Profile: "minimal", Include: []string{SecPageFields},
		PageFields: []config.ConfluenceFieldView{{ID: "restricted", ShowEmpty: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "re-pull") {
		t.Fatalf("warnings = %v", res.Warnings)
	}
	md := mustReadFile(t, filepath.Join(dir, slug+".md"))
	if !strings.Contains(md, "| Restricted | Unknown — re-pull required |") {
		t.Fatalf("unknown restriction was presented as a fact:\n%s", md)
	}
}

// Render must not touch the substrate: the .csf and .meta.json stay byte-stable
// so conf status remains clean.
func TestConfRenderLeavesSubstrateUntouched(t *testing.T) {
	root, dir, slug := seedConfMirror(t, nil)
	csfBefore := mustReadFile(t, filepath.Join(dir, slug+".csf"))
	metaBefore := mustReadFile(t, filepath.Join(dir, slug+".meta.json"))
	svc := NewConfluenceRenderer(&config.Config{})
	if _, err := svc.Render(root, config.RenderService{Profile: "full"}); err != nil {
		t.Fatal(err)
	}
	if mustReadFile(t, filepath.Join(dir, slug+".csf")) != csfBefore {
		t.Error(".csf changed")
	}
	if mustReadFile(t, filepath.Join(dir, slug+".meta.json")) != metaBefore {
		t.Error(".meta.json changed")
	}
	// Status stays clean (no local edit reported).
	entries, err := (&ConfluenceService{}).Status(context.Background(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.LocallyEdited {
			t.Errorf("page %s reported locally edited after render", e.ID)
		}
	}
}
