package app

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/domain"
)

type namedImageTracker struct {
	domain.Tracker
	attachments  []domain.Attachment
	streams      int
	beforeStream func()
}

func (s *namedImageTracker) ListAttachments(context.Context, string) ([]domain.Attachment, error) {
	return s.attachments, nil
}
func (s *namedImageTracker) StreamAttachment(_ context.Context, path string) (io.ReadCloser, error) {
	s.streams++
	if s.beforeStream != nil {
		s.beforeStream()
	}
	return io.NopCloser(strings.NewReader(path)), nil
}

func TestJiraImagesKeepCollidingNamesDistinctAndOrderIndependent(t *testing.T) {
	for _, names := range [][]string{{"screenshot.png", "screenshot.png"}, {"dir/a:b.png", "a-b.png"}, {strings.Repeat("界", 100) + ".png", strings.Repeat("界", 100) + ".png"}} {
		attachments := []domain.Attachment{{ID: "1", Title: names[0], MediaType: "image/png", DownPath: "/first"}, {ID: "2", Title: names[1], MediaType: "image/png", DownPath: "/second"}}
		var original []string
		for _, reverse := range []bool{false, true} {
			inventory := slices.Clone(attachments)
			if reverse {
				slices.Reverse(inventory)
			}
			tracker := &namedImageTracker{attachments: inventory}
			paths, err := (&JiraService{tr: tracker}).Images(t.Context(), "PROJ-1", t.TempDir())
			if err != nil || len(paths) != 2 || paths[0] == paths[1] {
				t.Fatalf("paths=%v err=%v", paths, err)
			}
			var bases []string
			for i, path := range paths {
				data, err := os.ReadFile(path)
				if err != nil || string(data) != inventory[i].DownPath {
					t.Fatalf("data=%q err=%v", data, err)
				}
				base := filepath.Base(path)
				if len(base) > 255 || !utf8.ValidString(base) {
					t.Fatalf("invalid output filename")
				}
				bases = append(bases, base)
			}
			slices.Sort(bases)
			if !reverse {
				original = bases
			} else if !slices.Equal(original, bases) {
				t.Fatalf("filename binding changed with order")
			}
		}
	}
}

func TestJiraImagesRejectAmbiguousInventoryBeforeDownloads(t *testing.T) {
	for _, id := range []string{"1", "../1", "", "01"} {
		tracker := &namedImageTracker{attachments: []domain.Attachment{{ID: "1", Title: "a.png", MediaType: "image/png", DownPath: "/first"}, {ID: id, Title: "b.png", MediaType: "image/png", DownPath: "/second"}}}
		directory := filepath.Join(t.TempDir(), "absent")
		_, err := (&JiraService{tr: tracker}).Images(t.Context(), "PROJ-1", directory)
		if !errors.Is(err, domain.ErrCheckFailed) || tracker.streams != 0 {
			t.Fatalf("id=%q err=%v streams=%d", id, err, tracker.streams)
		}
		if _, err := os.Stat(directory); !os.IsNotExist(err) {
			t.Fatalf("preflight created output directory")
		}
	}
}

func TestJiraImagesPreserveExistingAndRacingLegacyTargets(t *testing.T) {
	for _, race := range []bool{false, true} {
		dir := t.TempDir()
		path := filepath.Join(dir, "1-shot.png")
		create := func() {
			if err := os.WriteFile(path, []byte("legacy"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		tracker := &namedImageTracker{attachments: []domain.Attachment{{ID: "1", Title: "shot.png", MediaType: "image/png", DownPath: "/new"}}}
		if race {
			tracker.beforeStream = create
		} else {
			create()
		}
		_, err := (&JiraService{tr: tracker}).Images(t.Context(), "PROJ-1", dir)
		if !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("race=%v err=%v", race, err)
		}
		if !race && tracker.streams != 0 {
			t.Fatal("pre-existing target reached download")
		}
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "legacy" {
			t.Fatalf("data=%q err=%v", data, err)
		}
	}
}

func FuzzJiraImageOutputNameContainment(f *testing.F) {
	for _, seed := range [][2]string{{"1", "shot.png"}, {"1", "../a:b.png"}, {"01", "same.png"}, {"../1", "x"}, {"2", strings.Repeat("界", 100)}} {
		f.Add(seed[0], seed[1])
	}
	f.Fuzz(func(t *testing.T, id, title string) {
		names, err := jiraImageOutputNames([]domain.Attachment{{ID: id, Title: title, MediaType: "image/png", DownPath: "/image"}})
		if err != nil || names[0] == "" {
			return
		}
		name := names[0]
		if filepath.Base(name) != name || strings.ContainsAny(name, `/\`) || len(name) > 255 || !utf8.ValidString(name) {
			t.Fatal("unsafe image output component")
		}
	})
}
