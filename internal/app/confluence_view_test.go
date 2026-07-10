package app

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

func TestConfluencePageViewIsReadOnlyAndWritesNothing(t *testing.T) {
	root := t.TempDir()
	store := &recordingStore{page: &domain.Resource{
		ID: "42", Title: "Design", SpaceKey: "DOC", Version: 7,
		Body: []byte(`<h1>Plan</h1><p>Hello <ri:user ri:userkey="u1"/></p>`),
	}}
	svc := &ConfluenceService{
		store: store,
		users: func(context.Context, string) (string, error) {
			panic("transient view must not fetch auxiliary user data")
		},
		cfg: &config.Config{},
	}
	res, err := svc.ViewPage(context.Background(), "42", ConfluencePageViewOpts{
		Root: root, Render: config.RenderService{Profile: "minimal"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if store.getID != "42" || store.getFormat != "csf" {
		t.Fatalf("get = id %q format %q", store.getID, store.getFormat)
	}
	if store.commentsID != "" {
		t.Fatalf("minimal view fetched comments for %q", store.commentsID)
	}
	if res.ID != "42" || res.Title != "Design" || res.Space != "DOC" || res.Version != 7 {
		t.Fatalf("identity = %+v", res)
	}
	if !strings.HasPrefix(res.Markdown, mirror.ConfluenceDocumentMarker+"\n"+mirror.ConfluenceBodyReadOnlyMarker+"\n") {
		t.Fatalf("transient body is not explicitly read-only:\n%s", res.Markdown)
	}
	if strings.Contains(res.Markdown, mirror.ConfluenceBodyMarker) {
		t.Fatalf("transient view contains editable marker:\n%s", res.Markdown)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("transient view wrote under render root: %v", entries)
	}
}

func TestConfluencePageViewFetchesConfiguredCommentsOnly(t *testing.T) {
	store := &recordingStore{
		page:              &domain.Resource{ID: "42", Title: "Design", SpaceKey: "DOC", Version: 7, Body: []byte(`<p>Hello</p>`)},
		comments:          []domain.Comment{{ID: "9", Author: "Ada", Body: "Review"}},
		commentsTruncated: true,
	}
	svc := &ConfluenceService{store: store, cfg: &config.Config{}}
	res, err := svc.ViewPage(context.Background(), "42", ConfluencePageViewOpts{
		Root: t.TempDir(), Render: config.RenderService{Profile: "full"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if store.commentsID != "42" {
		t.Fatalf("comments fetched for %q", store.commentsID)
	}
	for _, marker := range []string{mirror.ConfluenceMetadataMarker, mirror.ConfluenceCommentsMarker, "Ada", "Review"} {
		if !strings.Contains(res.Markdown, marker) {
			t.Fatalf("full view missing %q:\n%s", marker, res.Markdown)
		}
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "truncated") {
		t.Fatalf("warnings = %v", res.Warnings)
	}
}

func TestConfluencePageViewRejectsUnrenderableCSF(t *testing.T) {
	store := &recordingStore{page: &domain.Resource{ID: "42", Body: []byte(`<p>`)}}
	svc := &ConfluenceService{store: store, cfg: &config.Config{}}
	_, err := svc.ViewPage(context.Background(), "42", ConfluencePageViewOpts{Root: t.TempDir()})
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("error = %v", err)
	}
}

func TestConfluencePageViewCannotBeAppliedAsMirrorEdit(t *testing.T) {
	root, mdPath := scaffoldPage(t, applyPage)
	csfPath := strings.TrimSuffix(mdPath, ".md") + ".csf"
	before := mustReadFile(t, csfPath)
	md := mustReadFile(t, mdPath)
	md = strings.Replace(md, mirror.ConfluenceBodyMarker, mirror.ConfluenceBodyReadOnlyMarker, 1)
	if err := os.WriteFile(mdPath, []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Apply(mdPath, ApplyOpts{Into: root, DryRun: true})
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("apply error = %v", err)
	}
	if got := mustReadFile(t, csfPath); got != before {
		t.Fatal("apply changed CSF for a transient read-only document")
	}
}
