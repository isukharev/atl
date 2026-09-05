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
	attachments []domain.Attachment
	streams     int
}

func (s *namedImageTracker) ListAttachments(context.Context, string) ([]domain.Attachment, error) {
	return s.attachments, nil
}
func (s *namedImageTracker) StreamAttachment(_ context.Context, path string) (io.ReadCloser, error) {
	s.streams++
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
