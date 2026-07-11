package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
)

type panicOnPageReadStore struct{ domain.DocStore }

func (panicOnPageReadStore) GetPage(context.Context, string, domain.PullOpts) (*domain.Resource, error) {
	panic("backend page read reached while mutation lock was contended")
}

func TestConfluenceMutationLockIsExclusiveAndPersistent(t *testing.T) {
	root := t.TempDir()
	first, err := lockConfluenceMutations(root, true)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".atl", confluenceMutationLockName)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := lockConfluenceMutations(root, false); !errors.Is(err, domain.ErrCheckFailed) || second != nil {
		t.Fatalf("second lock = %v, err=%v", second, err)
	}
	if err := first.Unlock(); err != nil {
		t.Fatal(err)
	}
	third, err := lockConfluenceMutations(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = third.Unlock() }()
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("mutation lock inode was replaced")
	}
}

func TestConfluenceMutatorsFailBeforeWorkOnLockContention(t *testing.T) {
	root, mdPath := scaffoldPage(t, applyPage)
	lock, err := lockConfluenceMutations(root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Unlock() }()
	csfPath := filepath.Join(filepath.Dir(mdPath), "page.csf")
	csfBefore := mustReadFile(t, csfPath)
	mdBefore := mustReadFile(t, mdPath)

	svc := &ConfluenceService{store: panicOnPageReadStore{}, cfg: &config.Config{}}
	if _, err := svc.Status(context.Background(), root, false); err != nil {
		t.Fatalf("read-only status was blocked by mutation lock: %v", err)
	}
	tests := []struct {
		name string
		run  func() error
	}{
		{name: "pull", run: func() error {
			_, err := svc.Pull(context.Background(), PullOpts{ID: "100", Into: root})
			return err
		}},
		{name: "render", run: func() error {
			_, err := svc.Render(root, config.RenderService{})
			return err
		}},
		{name: "apply", run: func() error {
			_, err := Apply(mdPath, ApplyOpts{Into: root})
			return err
		}},
		{name: "push dry run", run: func() error {
			_, err := svc.Push(context.Background(), csfPath, PushOpts{Into: root, DryRun: true})
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.run(); !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("contention error = %v", err)
			}
		})
	}
	if got := mustReadFile(t, csfPath); got != csfBefore {
		t.Fatal("CSF changed during contention")
	}
	if got := mustReadFile(t, mdPath); got != mdBefore {
		t.Fatal("Markdown view changed during contention")
	}
}

func TestConfluenceMutationLockReleasesOnErrors(t *testing.T) {
	root, mdPath := scaffoldPage(t, applyPage)
	dir := filepath.Dir(mdPath)
	badRenderTarget := filepath.Join(dir, "page.txt")
	if err := os.WriteFile(badRenderTarget, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := &ConfluenceService{
		store: &pullStore{getErr: errors.New("backend failed")},
		cfg:   &config.Config{},
	}
	checks := []struct {
		name string
		run  func() error
	}{
		{name: "pull", run: func() error {
			_, err := svc.Pull(context.Background(), PullOpts{ID: "100", Into: root})
			return err
		}},
		{name: "render", run: func() error {
			_, err := svc.Render(badRenderTarget, config.RenderService{})
			return err
		}},
		{name: "apply", run: func() error {
			_, err := Apply(filepath.Join(dir, "missing.md"), ApplyOpts{Into: root})
			return err
		}},
		{name: "push", run: func() error {
			_, err := svc.Push(context.Background(), filepath.Join(dir, "missing.csf"), PushOpts{Into: root})
			return err
		}},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.run(); err == nil {
				t.Fatal("operation unexpectedly succeeded")
			}
			lock, err := lockConfluenceMutations(root, false)
			if err != nil {
				t.Fatalf("lock stayed held after error: %v", err)
			}
			if err := lock.Unlock(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestConfluenceInvalidTargetsDoNotCreateMirrorState(t *testing.T) {
	base := t.TempDir()
	t.Chdir(base)
	svc := &ConfluenceService{cfg: &config.Config{}}
	stray := filepath.Join(base, "stray.md")
	if err := os.WriteFile(stray, []byte("plain"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(stray, ApplyOpts{Into: base}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("stray apply error = %v", err)
	}
	if _, err := svc.Render("missing.md", config.RenderService{}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing render error = %v", err)
	}
	if _, err := svc.Push(context.Background(), "missing.csf", PushOpts{}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing push error = %v", err)
	}
	for _, unexpected := range []string{filepath.Join(base, ".atl"), filepath.Join(base, "mirror"), filepath.Join(base, "missing.md")} {
		if _, err := os.Stat(unexpected); !os.IsNotExist(err) {
			t.Fatalf("invalid target created %s: %v", unexpected, err)
		}
	}
}
