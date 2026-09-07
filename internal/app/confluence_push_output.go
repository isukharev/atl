package app

import (
	"encoding/json"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
)

// MarshalJSON keeps the dry-run review surface explicit without changing the
// established shape of apply results. Empty review slices must encode as [] so
// callers can distinguish a neutral result from output produced by an older or
// incomplete implementation.
func (item PushItem) MarshalJSON() ([]byte, error) {
	type wireItem PushItem
	if !item.DryRun {
		return json.Marshal(wireItem(item))
	}
	problems := item.Problems
	if problems == nil {
		problems = []csf.Problem{}
	}
	removed := item.Removed
	if removed == nil {
		removed = []domain.Ref{}
	}
	added := item.Added
	if added == nil {
		added = []domain.Ref{}
	}
	return json.Marshal(struct {
		Path       string        `json:"path"`
		ID         string        `json:"id"`
		Problems   []csf.Problem `json:"problems"`
		Removed    []domain.Ref  `json:"removed_fragments"`
		Added      []domain.Ref  `json:"added_fragments"`
		Pushed     bool          `json:"pushed"`
		DryRun     bool          `json:"dry_run"`
		NewVersion int           `json:"new_version,omitempty"`
		Skipped    string        `json:"skipped,omitempty"`
		Drifted    bool          `json:"remote_drifted"`
		Failed     string        `json:"failed,omitempty"`
		Warning    string        `json:"warning,omitempty"`
	}{
		Path: item.Path, ID: item.ID, Problems: problems, Removed: removed,
		Added: added, Pushed: item.Pushed, DryRun: item.DryRun,
		NewVersion: item.NewVersion, Skipped: item.Skipped, Drifted: item.Drifted,
		Failed: item.Failed, Warning: item.Warning,
	})
}
