package mirror

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/isukharev/atl/internal/domain"
)

// ValidateJiraCompletePullSelection verifies the immutable selection binding
// used by Jira resume and offline inspection. Options still require the caller
// to supply the original command; they cannot be reconstructed from a digest.
func ValidateJiraCompletePullSelection(value CompletePullCheckpoint) error {
	body, err := json.Marshal(value.IDs)
	ordered := sort.SliceIsSorted(value.IDs, func(a, b int) bool {
		left, right := value.IDs[a], value.IDs[b]
		if len(left) != len(right) {
			return len(left) < len(right)
		}
		return left < right
	})
	if err != nil || Hash(body) != value.SelectionSHA256 || !ordered {
		return fmt.Errorf("%w: complete Jira checkpoint selection identity is invalid", domain.ErrCheckFailed)
	}
	return nil
}

// applyCompletePullProgress is the shared read-only progress fold. Stale
// bindings retain the existing conservative zero-prefix restart semantics.
func applyCompletePullProgress(value CompletePullCheckpoint, progress completePullProgress, progressPath string) (CompletePullCheckpoint, bool, error) {
	if staleCompletePullProgressService(value.Service, progress) {
		if value.Service == CompletePullServiceConfluence {
			value.Includes.EvidenceComplete = true
		}
		return value, true, nil
	}
	legacyConfluence := value.Service == CompletePullServiceConfluence && progress.SchemaVersion == completePullProgressSchema && progress.Service == ""
	currentConfluenceProgress := value.Service == CompletePullServiceConfluence &&
		(progress.SchemaVersion == completePullConfluenceProgressSchema || progress.SchemaVersion == completePullConfluenceProgressSchema4) &&
		validCompletePullProgressService(value.Service, progress.Service)
	currentProgress := currentConfluenceProgress ||
		value.Service == CompletePullServiceJira && progress.SchemaVersion == completePullProgressSchemaFor(value.Service) && validCompletePullProgressService(value.Service, progress.Service)
	if !legacyConfluence && !currentProgress {
		return CompletePullCheckpoint{}, false, fmt.Errorf("%w: unsupported complete-pull progress schema %d in %s", domain.ErrCheckFailed, progress.SchemaVersion, progressPath)
	}
	if progress.SelectorSHA256 != value.SelectorSHA256 || progress.OptionsSHA256 != value.OptionsSHA256 || progress.SelectionSHA256 != value.SelectionSHA256 {
		// A crash after atomically replacing a restarted selection can leave the
		// previous tiny progress sidecar. Replaying from zero is conservative;
		// trusting or rejecting that stale prefix would make recovery worse.
		if value.Service == CompletePullServiceConfluence {
			value.Includes.EvidenceComplete = true
		}
		return value, true, nil
	}
	if progress.NextIndex < 0 || progress.NextIndex > len(value.IDs) {
		return CompletePullCheckpoint{}, false, fmt.Errorf("%w: complete-pull progress is outside its selection in %s", domain.ErrCheckFailed, progressPath)
	}
	value.NextIndex = progress.NextIndex
	if value.Service == CompletePullServiceConfluence {
		if legacyConfluence {
			value.Includes.EvidenceComplete = progress.NextIndex == 0
		} else if progress.Includes == nil {
			return CompletePullCheckpoint{}, false, fmt.Errorf("%w: current Confluence complete-pull progress omits include evidence in %s", domain.ErrCheckFailed, progressPath)
		} else {
			value.Includes = *progress.Includes
			if err := validateCompletePullIncludeProgressSchema(progress.SchemaVersion, value.Includes, value.NextIndex); err != nil {
				return CompletePullCheckpoint{}, false, fmt.Errorf("%w in %s", err, progressPath)
			}
			if value.NextIndex == 0 && value.Includes == (CompletePullIncludeProgress{}) {
				// There is no accepted prefix whose evidence could be unknown.
				// Normalize an explicit empty current object the same way as an
				// absent legacy progress file, without improving any durable page.
				value.Includes.EvidenceComplete = true
			}
		}
	} else if progress.Includes != nil {
		return CompletePullCheckpoint{}, false, fmt.Errorf("%w: Jira complete-pull progress contains Confluence include evidence in %s", domain.ErrCheckFailed, progressPath)
	}
	return value, true, nil
}
