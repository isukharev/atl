package mirror

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
)

func TestSidecarRejectsDuplicateOrMalformedStableJiraIdentity(t *testing.T) {
	for _, tc := range []struct {
		name  string
		pages map[string]SyncState
	}{
		{name: "duplicate", pages: map[string]SyncState{
			"PROJ-1": {ID: "PROJ-1", Identity: "10001", Path: "PROJ/PROJ-1.wiki"},
			"PROJ-2": {ID: "PROJ-2", Identity: "10001", Path: "PROJ/PROJ-2.wiki"},
		}},
		{name: "non canonical", pages: map[string]SyncState{
			"PROJ-1": {ID: "PROJ-1", Identity: "010001", Path: "PROJ/PROJ-1.wiki"},
		}},
		{name: "confluence", pages: map[string]SyncState{
			"10": {ID: "10", Identity: "10001", Version: 1, Path: "DOC/page.csf"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := New(t.TempDir())
			if err := safepath.MkdirAllWithin(m.Root, filepath.Join(m.Root, ".atl"), 0o700); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(sidecarFile{Pages: tc.pages})
			if err != nil {
				t.Fatal(err)
			}
			if err := safepath.WriteFileWithin(m.Root, filepath.Join(m.Root, ".atl", "state.json"), encoded, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := m.SyncStates(); !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}
