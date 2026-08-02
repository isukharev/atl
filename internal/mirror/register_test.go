package mirror

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
)

func registrationFixture(_ string) (SyncState, ViewState, []byte, []RegistrationArtifact) {
	native := []byte{'n', 'a', 't', 'i', 'v', 'e', '\r', '\n', 0xff}
	state := SyncState{ID: "NEW-1", Version: 3, Hash: Hash(native), Path: "SPACE/new.wiki"}
	view := ViewState{Sections: []string{"metadata"}, DisplayTimeZone: "UTC"}
	artifacts := []RegistrationArtifact{
		{Path: state.Path, Data: native, Mode: 0o644},
		{Path: "SPACE/new.md", Data: []byte("# New\n"), Mode: 0o644},
		{Path: "SPACE/new.json", Data: []byte("{\"key\":\"NEW-1\"}\n"), Mode: 0o644},
	}
	return state, view, native, artifacts
}

func TestRegisterNewPublishesExactCompleteState(t *testing.T) {
	root := t.TempDir()
	m := New(root)
	state, view, base, artifacts := registrationFixture(root)
	if err := m.RegisterNew(state, view, ".wiki", base, artifacts); err != nil {
		t.Fatal(err)
	}
	for _, artifact := range artifacts {
		got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(artifact.Path)))
		if err != nil || !bytes.Equal(got, artifact.Data) {
			t.Fatalf("artifact %s = %x, err=%v", artifact.Path, got, err)
		}
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(artifact.Path)))
		if err != nil || info.Mode().Perm() != artifact.Mode {
			t.Fatalf("artifact %s mode=%v err=%v", artifact.Path, info, err)
		}
	}
	gotBase, present, err := m.ReadBaseBodyExt(state.ID, ".wiki")
	if err != nil || !present || !bytes.Equal(gotBase, base) {
		t.Fatalf("base=%x present=%t err=%v", gotBase, present, err)
	}
	gotState, present, err := m.SyncStateOf(state.ID)
	if err != nil || !present || gotState != state {
		t.Fatalf("state=%+v present=%t err=%v", gotState, present, err)
	}
	gotView, present, err := m.ViewStateOf(state.ID)
	if err != nil || !present || !reflect.DeepEqual(gotView, view) {
		t.Fatalf("view=%+v present=%t err=%v", gotView, present, err)
	}
	if _, present, err := m.StagedStateOf(state.ID); err != nil || present {
		t.Fatalf("staged present=%t err=%v", present, err)
	}

	before, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(state.Path)))
	if err := m.RegisterNew(state, view, ".wiki", base, artifacts); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("second registration error=%v, want ErrCheckFailed", err)
	}
	after, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(state.Path)))
	if !bytes.Equal(before, after) {
		t.Fatal("idempotence-negative retry changed the registered native file")
	}
}

func TestRegisterNewAcceptsExactEmptyNativeBody(t *testing.T) {
	root := t.TempDir()
	m := New(root)
	state := SyncState{ID: "EMPTY-1", Hash: Hash(nil), Path: "EMPTY/EMPTY-1.wiki"}
	artifacts := []RegistrationArtifact{
		{Path: state.Path, Data: nil, Mode: 0o644},
		{Path: "EMPTY/EMPTY-1.md", Data: []byte("# Empty\n"), Mode: 0o644},
	}
	if err := m.RegisterNew(state, ViewState{}, ".wiki", nil, artifacts); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(state.Path)))
	if err != nil || len(got) != 0 {
		t.Fatalf("empty native=%q err=%v", got, err)
	}
}

func TestRegisterNewRejectsInvalidPlansBeforeWriting(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*SyncState, *string, *[]byte, *[]RegistrationArtifact)
	}{
		{name: "empty id", mutate: func(s *SyncState, _ *string, _ *[]byte, _ *[]RegistrationArtifact) { s.ID = "" }},
		{name: "noncanonical state path", mutate: func(s *SyncState, _ *string, _ *[]byte, _ *[]RegistrationArtifact) { s.Path = "SPACE/../new.wiki" }},
		{name: "reserved artifact", mutate: func(_ *SyncState, _ *string, _ *[]byte, a *[]RegistrationArtifact) { (*a)[1].Path = ".atl/other" }},
		{name: "extension mismatch", mutate: func(_ *SyncState, e *string, _ *[]byte, _ *[]RegistrationArtifact) { *e = ".csf" }},
		{name: "base mismatch", mutate: func(_ *SyncState, _ *string, b *[]byte, _ *[]RegistrationArtifact) { *b = []byte("different") }},
		{name: "native missing", mutate: func(_ *SyncState, _ *string, _ *[]byte, a *[]RegistrationArtifact) { *a = (*a)[1:] }},
		{name: "native differs from base", mutate: func(_ *SyncState, _ *string, _ *[]byte, a *[]RegistrationArtifact) {
			(*a)[0].Data = []byte("different")
		}},
		{name: "duplicate path", mutate: func(_ *SyncState, _ *string, _ *[]byte, a *[]RegistrationArtifact) { (*a)[2].Path = (*a)[1].Path }},
		{name: "invalid mode", mutate: func(_ *SyncState, _ *string, _ *[]byte, a *[]RegistrationArtifact) {
			(*a)[1].Mode = os.ModeSymlink | 0o644
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			state, view, base, artifacts := registrationFixture(root)
			ext := ".wiki"
			tc.mutate(&state, &ext, &base, &artifacts)
			if err := New(root).RegisterNew(state, view, ext, base, artifacts); !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("error=%v, want ErrCheckFailed", err)
			}
			if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
				t.Fatalf("invalid plan wrote entries=%v err=%v", entries, err)
			}
		})
	}
}

func TestRegisterNewRefusesPreexistingTargetsAndSymlinks(t *testing.T) {
	for _, tc := range []struct {
		name string
		path func(string, SyncState) string
		make func(string) ([]byte, error)
	}{
		{name: "exact artifact", path: func(root string, s SyncState) string { return filepath.Join(root, filepath.FromSlash(s.Path)) }, make: func(path string) ([]byte, error) {
			b := []byte("native\r\n\xff")
			return b, os.WriteFile(path, b, 0o644)
		}},
		{name: "different artifact", path: func(root string, s SyncState) string { return filepath.Join(root, filepath.FromSlash(s.Path)) }, make: func(path string) ([]byte, error) { b := []byte("user bytes"); return b, os.WriteFile(path, b, 0o644) }},
		{name: "base", path: func(root string, s SyncState) string {
			return filepath.Join(root, ".atl", "base", safepath.Segment(s.ID)+".wiki")
		}, make: func(path string) ([]byte, error) { b := []byte("old base"); return b, os.WriteFile(path, b, 0o600) }},
		{name: "directory", path: func(root string, s SyncState) string { return filepath.Join(root, filepath.FromSlash(s.Path)) }, make: func(path string) ([]byte, error) { return nil, os.Mkdir(path, 0o755) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			state, view, base, artifacts := registrationFixture(root)
			target := tc.path(root, state)
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				t.Fatal(err)
			}
			original, err := tc.make(target)
			if err != nil {
				t.Fatal(err)
			}
			if err := New(root).RegisterNew(state, view, ".wiki", base, artifacts); !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("error=%v, want ErrCheckFailed", err)
			}
			if original != nil {
				got, err := os.ReadFile(target)
				if err != nil || !bytes.Equal(got, original) {
					t.Fatalf("preexisting target changed to %q, err=%v", got, err)
				}
			}
			if _, ok, err := New(root).SyncStateOf(state.ID); err != nil || ok {
				t.Fatalf("collision recorded state: ok=%t err=%v", ok, err)
			}
		})
	}

	t.Run("symlink parent", func(t *testing.T) {
		root := t.TempDir()
		state, view, base, artifacts := registrationFixture(root)
		outside := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(root, "SPACE")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if err := New(root).RegisterNew(state, view, ".wiki", base, artifacts); !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("error=%v, want ErrCheckFailed", err)
		}
		entries, _ := os.ReadDir(outside)
		if len(entries) != 0 {
			t.Fatalf("registration escaped through symlink: %v", entries)
		}
	})
}

func TestRegisterNewRejectsSidecarIdentityAndPathCollisions(t *testing.T) {
	for _, tc := range []struct {
		name  string
		patch func(*sidecarFile, SyncState)
	}{
		{name: "page identity", patch: func(sc *sidecarFile, s SyncState) { sc.Pages[s.ID] = s }},
		{name: "view identity", patch: func(sc *sidecarFile, s SyncState) { sc.Views[s.ID] = ViewState{} }},
		{name: "staged identity", patch: func(sc *sidecarFile, s SyncState) {
			sc.Staged[s.ID] = StagedState{ID: s.ID, Hash: s.Hash, BaseHash: s.Hash, Path: s.Path}
		}},
		{name: "tracked path", patch: func(sc *sidecarFile, s SyncState) { sc.Pages["OTHER"] = SyncState{ID: "OTHER", Path: s.Path} }},
		{name: "staged path", patch: func(sc *sidecarFile, s SyncState) {
			sc.Staged["OTHER"] = StagedState{ID: "OTHER", Hash: s.Hash, BaseHash: s.Hash, Path: s.Path}
		}},
		{name: "sanitized base path", patch: func(sc *sidecarFile, _ SyncState) { sc.Pages["NEW/1"] = SyncState{ID: "NEW/1", Path: "other.wiki"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			m := New(root)
			state, view, base, artifacts := registrationFixture(root)
			sc, _ := m.loadSidecar()
			tc.patch(&sc, state)
			if err := m.saveSidecar(sc); err != nil {
				t.Fatal(err)
			}
			if err := m.RegisterNew(state, view, ".wiki", base, artifacts); !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("error=%v, want ErrCheckFailed", err)
			}
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(state.Path))); !os.IsNotExist(err) {
				t.Fatalf("collision wrote native: %v", err)
			}
		})
	}
}

func TestRegisterNewRollsBackDefinitePublicationFailures(t *testing.T) {
	root := t.TempDir()
	m := New(root)
	state, view, base, artifacts := registrationFixture(root)
	writes := 0
	ops := registrationOps{
		writeExclusive: func(root, target string, data []byte, mode os.FileMode) error {
			writes++
			if writes == 3 {
				return errors.New("injected artifact failure")
			}
			return safepath.WriteFileExclusiveWithin(root, target, data, mode)
		},
		saveSidecar: func(sidecarFile) error { return errors.New("must not reach sidecar") },
	}
	if err := m.registerNewWith(state, view, ".wiki", base, artifacts, ops); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("error=%v, want ErrCheckFailed", err)
	}
	for _, artifact := range artifacts {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(artifact.Path))); !os.IsNotExist(err) {
			t.Fatalf("artifact survived definite rollback: %s (%v)", artifact.Path, err)
		}
	}
	if _, ok, err := m.SyncStateOf(state.ID); err != nil || ok {
		t.Fatalf("failed publication recorded state: ok=%t err=%v", ok, err)
	}
}

func TestRegisterNewRollbackPreservesChangedCreatedFile(t *testing.T) {
	root := t.TempDir()
	m := New(root)
	state, view, base, artifacts := registrationFixture(root)
	writes := 0
	nativePath := filepath.Join(root, filepath.FromSlash(state.Path))
	ops := registrationOps{
		writeExclusive: func(root, target string, data []byte, mode os.FileMode) error {
			writes++
			if writes == 2 {
				if err := os.WriteFile(nativePath, []byte("external edit"), 0o644); err != nil {
					t.Fatal(err)
				}
				return errors.New("injected failure after edit")
			}
			return safepath.WriteFileExclusiveWithin(root, target, data, mode)
		},
		saveSidecar: func(sidecarFile) error { return nil },
	}
	err := m.registerNewWith(state, view, ".wiki", base, artifacts, ops)
	if !errors.Is(err, domain.ErrCheckFailed) || !strings.Contains(err.Error(), "rollback is incomplete") {
		t.Fatalf("error=%v, want incomplete rollback check failure", err)
	}
	got, err := os.ReadFile(nativePath)
	if err != nil || string(got) != "external edit" {
		t.Fatalf("changed created file was removed/changed: %q err=%v", got, err)
	}
}

func TestRegisterNewDistinguishesSidecarSaveOutcomes(t *testing.T) {
	t.Run("definite failure rolls back", func(t *testing.T) {
		root := t.TempDir()
		m := New(root)
		state, view, base, artifacts := registrationFixture(root)
		err := m.registerNewWith(state, view, ".wiki", base, artifacts, registrationOps{
			writeExclusive: safepath.WriteFileExclusiveWithin,
			saveSidecar:    func(sidecarFile) error { return errors.New("injected definite save failure") },
		})
		if !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("error=%v, want ErrCheckFailed", err)
		}
		for _, artifact := range artifacts {
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(artifact.Path))); !os.IsNotExist(err) {
				t.Fatalf("artifact survived definite sidecar failure: %s", artifact.Path)
			}
		}
		if _, present, err := m.ReadBaseBodyExt(state.ID, ".wiki"); err != nil || present {
			t.Fatalf("base survived definite sidecar failure: present=%t err=%v", present, err)
		}
	})

	t.Run("committed error is success", func(t *testing.T) {
		root := t.TempDir()
		m := New(root)
		state, view, base, artifacts := registrationFixture(root)
		err := m.registerNewWith(state, view, ".wiki", base, artifacts, registrationOps{
			writeExclusive: safepath.WriteFileExclusiveWithin,
			saveSidecar: func(sc sidecarFile) error {
				if err := m.saveSidecar(sc); err != nil {
					return err
				}
				return errors.New("injected error after commit")
			},
		})
		if err != nil {
			t.Fatalf("committed save error returned failure: %v", err)
		}
		if got, ok, _ := m.SyncStateOf(state.ID); !ok || got != state {
			t.Fatalf("committed state=%+v ok=%t", got, ok)
		}
	})

	t.Run("ambiguous state preserves files", func(t *testing.T) {
		root := t.TempDir()
		m := New(root)
		state, view, base, artifacts := registrationFixture(root)
		err := m.registerNewWith(state, view, ".wiki", base, artifacts, registrationOps{
			writeExclusive: safepath.WriteFileExclusiveWithin,
			saveSidecar: func(sc sidecarFile) error {
				delete(sc.Views, state.ID)
				if err := m.saveSidecar(sc); err != nil {
					return err
				}
				return errors.New("injected ambiguous save failure")
			},
		})
		if !errors.Is(err, domain.ErrCheckFailed) || !strings.Contains(err.Error(), "ambiguous") {
			t.Fatalf("error=%v, want ambiguous check failure", err)
		}
		for _, artifact := range artifacts {
			got, readErr := os.ReadFile(filepath.Join(root, filepath.FromSlash(artifact.Path)))
			if readErr != nil || !bytes.Equal(got, artifact.Data) {
				t.Fatalf("ambiguous artifact %s was not preserved", artifact.Path)
			}
		}
	})
}

func TestRegisterNewMergesUnrelatedSidecarState(t *testing.T) {
	root := t.TempDir()
	m := New(root)
	sc, _ := m.loadSidecar()
	sc.Pages["OLD"] = SyncState{ID: "OLD", Hash: "old", Path: "old.csf"}
	sc.Views["OLD"] = ViewState{Sections: []string{"page_fields"}}
	sc.Staged["LOCAL"] = StagedState{ID: "LOCAL", Hash: strings.Repeat("1", 64), BaseHash: strings.Repeat("2", 64), Path: "local.csf"}
	if err := m.saveSidecar(sc); err != nil {
		t.Fatal(err)
	}
	state, view, base, artifacts := registrationFixture(root)
	if err := m.RegisterNew(state, view, ".wiki", base, artifacts); err != nil {
		t.Fatal(err)
	}
	got, err := m.loadSidecar()
	if err != nil {
		t.Fatal(err)
	}
	if got.Pages["OLD"].Path != "old.csf" || !reflect.DeepEqual(got.Views["OLD"].Sections, []string{"page_fields"}) {
		t.Fatalf("unrelated tracked state lost: %+v", got)
	}
	if _, ok := got.Staged["LOCAL"]; !ok {
		t.Fatalf("unrelated staged state lost: %+v", got.Staged)
	}
}

func TestRegisterNewNeverReportsStateWithoutExactFiles(t *testing.T) {
	root := t.TempDir()
	m := New(root)
	state, view, base, artifacts := registrationFixture(root)
	removed := filepath.Join(root, filepath.FromSlash(artifacts[1].Path))
	err := m.registerNewWith(state, view, ".wiki", base, artifacts, registrationOps{
		writeExclusive: safepath.WriteFileExclusiveWithin,
		saveSidecar: func(sidecarFile) error {
			if err := os.Remove(removed); err != nil {
				return err
			}
			return errors.New("injected save failure with missing artifact")
		},
	})
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("error=%v, want ErrCheckFailed", err)
	}
	if _, ok, err := m.SyncStateOf(state.ID); err != nil || ok {
		t.Fatalf("missing artifact was recorded: ok=%t err=%v", ok, err)
	}
	for _, artifact := range artifacts {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(artifact.Path))); !os.IsNotExist(err) {
			t.Fatalf("definite failure left artifact %s: %v", artifact.Path, err)
		}
	}
}

func TestRegisterNewRaceNeverRemovesNewlyOccupiedTarget(t *testing.T) {
	root := t.TempDir()
	m := New(root)
	state, view, base, artifacts := registrationFixture(root)
	racedTarget := filepath.Join(root, filepath.FromSlash(artifacts[1].Path))
	err := m.registerNewWith(state, view, ".wiki", base, artifacts, registrationOps{
		writeExclusive: func(root, target string, data []byte, mode os.FileMode) error {
			if target == racedTarget {
				if err := os.WriteFile(target, []byte("raced user bytes"), 0o600); err != nil {
					t.Fatal(err)
				}
				return fmt.Errorf("stop: %w", os.ErrExist)
			}
			return safepath.WriteFileExclusiveWithin(root, target, data, mode)
		},
		saveSidecar: func(sidecarFile) error { return nil },
	})
	if !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("error=%v, want ErrCheckFailed", err)
	}
	got, readErr := os.ReadFile(racedTarget)
	if readErr != nil || string(got) != "raced user bytes" {
		t.Fatalf("newly occupied target changed: %q err=%v", got, readErr)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(state.Path))); !os.IsNotExist(err) {
		t.Fatalf("file created earlier in the call survived rollback: %v", err)
	}
}

func TestRegisterNewDurabilityBarriersAreChildToRootAndStateLast(t *testing.T) {
	root := t.TempDir()
	m := New(root)
	state, view, base, artifacts := registrationFixture(root)
	artifacts[1].Path = "SPACE/nested/views/new.md"
	var events []string
	err := m.registerNewWith(state, view, ".wiki", base, artifacts, registrationOps{
		writeExclusive: safepath.WriteFileExclusiveWithin,
		saveSidecar: func(sc sidecarFile) error {
			events = append(events, "save-state")
			return m.saveSidecar(sc)
		},
		syncDirectory: func(_ string, target string) error {
			rel, err := filepath.Rel(root, target)
			if err != nil {
				t.Fatal(err)
			}
			events = append(events, "sync:"+filepath.ToSlash(rel))
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"sync:SPACE/nested/views",
		"sync:.atl/base",
		"sync:SPACE/nested",
		"sync:.atl",
		"sync:SPACE",
		"sync:.",
		"save-state",
		"sync:.atl",
		"sync:.",
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("durability events:\n got %q\nwant %q", events, want)
	}
}

func TestRegisterNewPreStateDurabilityFailureRollsBack(t *testing.T) {
	root := t.TempDir()
	m := New(root)
	state, view, base, artifacts := registrationFixture(root)
	saves := 0
	syncs := 0
	preBarrierFailed := false
	var rollbackSynced []string
	err := m.registerNewWith(state, view, ".wiki", base, artifacts, registrationOps{
		writeExclusive: safepath.WriteFileExclusiveWithin,
		saveSidecar: func(sc sidecarFile) error {
			saves++
			return m.saveSidecar(sc)
		},
		syncDirectory: func(_ string, target string) error {
			syncs++
			if syncs == 2 {
				preBarrierFailed = true
				return errors.New("injected pre-state directory sync failure")
			}
			if preBarrierFailed {
				rel, err := filepath.Rel(root, target)
				if err != nil {
					t.Fatal(err)
				}
				rollbackSynced = append(rollbackSynced, filepath.ToSlash(rel))
			}
			return nil
		},
	})
	if !errors.Is(err, domain.ErrCheckFailed) || !strings.Contains(err.Error(), "durably publish") {
		t.Fatalf("error=%v, want pre-state durability check failure", err)
	}
	if saves != 0 {
		t.Fatalf("sidecar save called %d times after pre-state barrier failure", saves)
	}
	wantRollbackSync := []string{".atl/base", ".atl", "SPACE", "."}
	if !reflect.DeepEqual(rollbackSynced, wantRollbackSync) {
		t.Fatalf("artifact/base rollback sync order:\n got %q\nwant %q", rollbackSynced, wantRollbackSync)
	}
	if _, ok, err := m.SyncStateOf(state.ID); err != nil || ok {
		t.Fatalf("pre-state barrier failure recorded state: ok=%t err=%v", ok, err)
	}
	for _, artifact := range artifacts {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(artifact.Path))); !os.IsNotExist(err) {
			t.Fatalf("artifact survived pre-state barrier rollback: %s (%v)", artifact.Path, err)
		}
	}
	if _, present, err := m.ReadBaseBodyExt(state.ID, ".wiki"); err != nil || present {
		t.Fatalf("base survived pre-state barrier rollback: present=%t err=%v", present, err)
	}
}

func TestRegisterNewPostStateDurabilityFailurePreservesStateAndArtifacts(t *testing.T) {
	root := t.TempDir()
	m := New(root)
	state, view, base, artifacts := registrationFixture(root)
	preDirs, err := registrationTargetDirectories(root, appendPreparedRegistrationForTest(t, m, state, base, artifacts))
	if err != nil {
		t.Fatal(err)
	}
	syncs := 0
	err = m.registerNewWith(state, view, ".wiki", base, artifacts, registrationOps{
		writeExclusive: safepath.WriteFileExclusiveWithin,
		saveSidecar:    func(sc sidecarFile) error { return m.saveSidecar(sc) },
		syncDirectory: func(_, _ string) error {
			syncs++
			if syncs == len(preDirs)+1 {
				return errors.New("injected post-state directory sync failure")
			}
			return nil
		},
	})
	if !errors.Is(err, domain.ErrCheckFailed) || !strings.Contains(err.Error(), "durability is ambiguous") {
		t.Fatalf("error=%v, want post-state durability ambiguity", err)
	}
	if got, ok, readErr := m.SyncStateOf(state.ID); readErr != nil || !ok || got != state {
		t.Fatalf("post-state barrier failure lost state: state=%+v ok=%t err=%v", got, ok, readErr)
	}
	for _, artifact := range artifacts {
		got, readErr := os.ReadFile(filepath.Join(root, filepath.FromSlash(artifact.Path)))
		if readErr != nil || !bytes.Equal(got, artifact.Data) {
			t.Fatalf("post-state barrier failure changed artifact %s: %q err=%v", artifact.Path, got, readErr)
		}
	}
	if got, present, readErr := m.ReadBaseBodyExt(state.ID, ".wiki"); readErr != nil || !present || !bytes.Equal(got, base) {
		t.Fatalf("post-state barrier failure changed base: %q present=%t err=%v", got, present, readErr)
	}
}

func TestRegisterNewRollbackDurabilityIsChildToRoot(t *testing.T) {
	root := t.TempDir()
	m := New(root)
	state, view, base, artifacts := registrationFixture(root)
	artifacts[1].Path = "SPACE/nested/views/new.md"
	writes := 0
	var synced []string
	err := m.registerNewWith(state, view, ".wiki", base, artifacts, registrationOps{
		writeExclusive: func(root, target string, data []byte, mode os.FileMode) error {
			writes++
			if writes == 3 {
				return errors.New("injected publication failure")
			}
			return safepath.WriteFileExclusiveWithin(root, target, data, mode)
		},
		saveSidecar: func(sidecarFile) error { return errors.New("must not save state") },
		syncDirectory: func(_ string, target string) error {
			rel, err := filepath.Rel(root, target)
			if err != nil {
				t.Fatal(err)
			}
			synced = append(synced, filepath.ToSlash(rel))
			return nil
		},
	})
	if !errors.Is(err, domain.ErrCheckFailed) || strings.Contains(err.Error(), "durability is ambiguous") {
		t.Fatalf("error=%v, want durably completed rollback failure", err)
	}
	want := []string{"SPACE/nested/views", "SPACE/nested", "SPACE", "."}
	if !reflect.DeepEqual(synced, want) {
		t.Fatalf("rollback sync order:\n got %q\nwant %q", synced, want)
	}
	for _, artifact := range artifacts[:2] {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(artifact.Path))); !os.IsNotExist(err) {
			t.Fatalf("rolled-back artifact remains: %s (%v)", artifact.Path, err)
		}
	}
}

func TestRegisterNewRollbackSyncFailureIsAmbiguousAndPreservesRacedBytes(t *testing.T) {
	root := t.TempDir()
	m := New(root)
	state, view, base, artifacts := registrationFixture(root)
	racedTarget := filepath.Join(root, filepath.FromSlash(artifacts[1].Path))
	writes := 0
	err := m.registerNewWith(state, view, ".wiki", base, artifacts, registrationOps{
		writeExclusive: func(root, target string, data []byte, mode os.FileMode) error {
			writes++
			if writes == 2 {
				if err := os.WriteFile(racedTarget, []byte("raced bytes"), 0o600); err != nil {
					t.Fatal(err)
				}
				return fmt.Errorf("injected collision: %w", os.ErrExist)
			}
			return safepath.WriteFileExclusiveWithin(root, target, data, mode)
		},
		saveSidecar: func(sidecarFile) error { return errors.New("must not save state") },
		syncDirectory: func(_, _ string) error {
			return errors.New("injected rollback directory sync failure")
		},
	})
	if !errors.Is(err, domain.ErrCheckFailed) || !strings.Contains(err.Error(), "rollback is incomplete or its durability is ambiguous") {
		t.Fatalf("error=%v, want rollback durability ambiguity", err)
	}
	got, readErr := os.ReadFile(racedTarget)
	if readErr != nil || string(got) != "raced bytes" {
		t.Fatalf("raced target changed: %q err=%v", got, readErr)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(state.Path))); !os.IsNotExist(err) {
		t.Fatalf("attempt-owned native survived logical rollback: %v", err)
	}
	if _, ok, err := m.SyncStateOf(state.ID); err != nil || ok {
		t.Fatalf("rollback sync failure recorded state: ok=%t err=%v", ok, err)
	}
}

func appendPreparedRegistrationForTest(t *testing.T, m *Mirror, state SyncState, base []byte, artifacts []RegistrationArtifact) []preparedRegistrationArtifact {
	t.Helper()
	prepared, preparedBase, err := m.prepareRegistration(state, ".wiki", base, artifacts)
	if err != nil {
		t.Fatal(err)
	}
	return append(prepared, preparedBase)
}
