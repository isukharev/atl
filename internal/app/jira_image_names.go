package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
)

// jiraImageOutputNames binds output names to immutable attachment IDs before
// any stream or file is opened. Inventory filenames remain authoritative even
// when a compatible port resolves the download through the ID fallback.
func jiraImageOutputNames(attachments []domain.Attachment) ([]string, error) {
	names := make([]string, len(attachments))
	seen := make(map[string]bool)
	for i, attachment := range attachments {
		if !strings.HasPrefix(attachment.MediaType, "image/") || (attachment.ID == "" && attachment.DownPath == "") {
			continue
		}
		base, ok := safepath.Base(attachment.Title)
		if !ok {
			continue
		}
		if len(attachment.ID) > 64 || !canonicalPositiveNumericString(attachment.ID) || seen[attachment.ID] {
			return nil, fmt.Errorf("%w: image attachment inventory requires distinct canonical numeric identities", domain.ErrCheckFailed)
		}
		seen[attachment.ID] = true
		// Keep the component within 255 bytes even for maximum-size source
		// filenames, without splitting a UTF-8 encoding at the cut.
		limit := 255 - len(attachment.ID) - 1
		if len(base) > limit {
			base = base[:limit]
			for !utf8.ValidString(base) {
				base = base[:len(base)-1]
			}
		}
		names[i] = attachment.ID + "-" + base
	}
	return names, nil
}

func preflightJiraImageTargets(directory string, names []string) error {
	for _, name := range names {
		if name == "" {
			continue
		}
		_, err := safepath.StatWithin(directory, filepath.Join(directory, name))
		if !os.IsNotExist(err) {
			return fmt.Errorf("%w: image output is occupied or unavailable; choose a fresh output directory", domain.ErrCheckFailed)
		}
	}
	return nil
}
