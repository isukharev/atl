package mirror

import (
	"bytes"
	"encoding/json"
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

func completePullPublicationFixture(t *testing.T) (*Mirror, CompletePullCheckpoint, CompletePullJournalEntry, []CompletePullArtifact) {
	t.Helper()
	root := t.TempDir()
	m := New(root)
	if err := m.EnsureScaffold(); err != nil {
		t.Fatal(err)
	}
	body := []byte("<p>private staged body</p>")
	state := SyncState{ID: "10", Version: 3, Hash: Hash(body), Path: "DOC/page/page.csf"}
	view := ViewState{Sections: []string{"content"}}
	checkpoint := CompletePullCheckpoint{
		Service: "confluence", SelectorSHA256: completePullTestHash,
		OptionsSHA256: strings.Repeat("b", 64), SelectionSHA256: strings.Repeat("c", 64), IDs: []string{"10"},
	}
	if err := m.SaveCompletePullCheckpoint(checkpoint); err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(Meta{ID: state.ID, Version: state.Version, Hash: state.Hash})
	artifacts := []CompletePullArtifact{
		{Path: mustPublicArtifactPath(t, state.Path), Data: body, Mode: 0o644},
		{Path: mustPublicArtifactPath(t, "DOC/page/page.md"), Data: []byte("# derived\n"), Mode: 0o644, BestEffort: true},
		{Path: mustPublicArtifactPath(t, "DOC/page/page.comments.json"), Data: []byte("{}\n"), Mode: 0o644},
		{Path: mustPublicArtifactPath(t, "DOC/page/page.comments.md"), Data: []byte("# Comments\n"), Mode: 0o644, BestEffort: true},
		{Path: mustPublicArtifactPath(t, "DOC/page/page.meta.json"), Data: append(meta, '\n'), Mode: 0o644},
		{Path: mustPrivateArtifactPath(t, ".atl/base/10.csf"), Data: body, Mode: 0o600},
		{Path: mustPublicArtifactPath(t, "DOC/page/page.assets/file.bin"), Data: []byte{0, 1, 2}, Mode: 0o644},
		{Path: mustPublicArtifactPath(t, "DOC/page/page.jira-macros.json"), Data: []byte("{}\n"), Mode: 0o600},
		{Path: mustPublicArtifactPath(t, "DOC/page/obsolete.jira-macros.json"), Remove: true},
	}
	obsolete := filepath.Join(root, "DOC", "page", "obsolete.jira-macros.json")
	if err := os.MkdirAll(filepath.Dir(obsolete), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(obsolete, []byte("obsolete\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return m, checkpoint, CompletePullJournalEntry{State: state, View: view}, artifacts
}

func assertPublicationRecovered(t *testing.T, m *Mirror, checkpoint CompletePullCheckpoint, entry CompletePullJournalEntry, artifacts []CompletePullArtifact) {
	t.Helper()
	if err := m.RecoverCompletePullPublication(checkpoint.SelectorSHA256, checkpoint, true); err != nil {
		t.Fatalf("recover publication: %v", err)
	}
	for _, artifact := range artifacts {
		artifactRel := artifactPathStringForTest(t, artifact.Path)
		path := filepath.Join(m.Root, filepath.FromSlash(artifactRel))
		got, err := os.ReadFile(path)
		if artifact.Remove {
			if !os.IsNotExist(err) {
				t.Fatalf("removed artifact %s survived: %v", artifactRel, err)
			}
			continue
		}
		if err != nil || !bytes.Equal(got, artifact.Data) {
			t.Fatalf("artifact %s=%q err=%v", artifactRel, got, err)
		}
	}
	journal, found, err := m.loadCompletePullJournal(checkpoint.SelectorSHA256)
	if err != nil || !found || len(journal.Entries) != 1 || !reflect.DeepEqual(journal.Entries[0], entry) {
		t.Fatalf("journal=%+v found=%t err=%v", journal, found, err)
	}
	dir, _ := m.completePullPublicationDir(checkpoint.SelectorSHA256)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("publication stage survived: %v", err)
	}
}

func TestCompletePullPublicationRecoversEveryArtifactAndAcceptanceBoundary(t *testing.T) {
	steps := []string{"intent", "artifact:0", "artifact:1", "artifact:2", "artifact:3", "artifact:4", "artifact:5", "artifact:6", "artifact:7", "artifact:8", "fully_published", "accepted", "committed", "retired"}
	for _, step := range steps {
		t.Run(step, func(t *testing.T) {
			m, checkpoint, entry, artifacts := completePullPublicationFixture(t)
			if step == "intent" {
				ops := defaultCompletePullPublicationOps()
				ops.after = func(got string) error {
					if got == step {
						return errors.New("injected crash")
					}
					return nil
				}
				if err := m.prepareCompletePullPublicationWith(checkpoint, 0, entry, true, artifacts, nil, ops); err == nil {
					t.Fatal("injected prepare crash returned nil")
				}
			} else {
				if err := m.PrepareCompletePullPublication(checkpoint, 0, entry, true, artifacts, nil); err != nil {
					t.Fatal(err)
				}
				ops := defaultCompletePullPublicationOps()
				ops.after = func(got string) error {
					if got == step {
						return errors.New("injected crash")
					}
					return nil
				}
				if err := m.recoverCompletePullPublicationWith(checkpoint.SelectorSHA256, checkpoint, true, ops); err == nil {
					t.Fatal("injected publication crash returned nil")
				}
			}
			if step == "committed" {
				dir, _ := m.completePullPublicationDir(checkpoint.SelectorSHA256)
				if err := os.WriteFile(filepath.Join(dir, ".tmp-0123456789abcdef"), []byte("torn atomic residue"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			assertPublicationRecovered(t, m, checkpoint, entry, artifacts)
		})
	}
}

func TestCompletePullPublicationBestEffortViewHasExactAbsentPostcondition(t *testing.T) {
	m, checkpoint, entry, artifacts := completePullPublicationFixture(t)
	if err := m.PrepareCompletePullPublication(checkpoint, 0, entry, true, artifacts, nil); err != nil {
		t.Fatal(err)
	}
	ops := defaultCompletePullPublicationOps()
	baseWrite := ops.writeOwned
	ops.writeOwned = func(m *Mirror, target, temp string, data []byte, mode os.FileMode) error {
		if strings.HasSuffix(target, "page.md") {
			return errors.New("injected derived-view write failure")
		}
		return baseWrite(m, target, temp, data, mode)
	}
	if err := m.recoverCompletePullPublicationWith(checkpoint.SelectorSHA256, checkpoint, true, ops); err != nil {
		t.Fatal(err)
	}
	mdPath := filepath.Join(m.Root, "DOC", "page", "page.md")
	if _, err := os.Stat(mdPath); !os.IsNotExist(err) {
		t.Fatalf("best-effort failed view was not absent: %v", err)
	}
	journal, found, err := m.loadCompletePullJournal(checkpoint.SelectorSHA256)
	if err != nil || !found || len(journal.Entries) != 1 {
		t.Fatalf("journal=%+v found=%t err=%v", journal, found, err)
	}
}

func TestCompletePullPublicationStagedPayloadCrashCleansPrivateResidueWithoutCanonicalWrites(t *testing.T) {
	m, checkpoint, entry, artifacts := completePullPublicationFixture(t)
	ops := defaultCompletePullPublicationOps()
	ops.after = func(step string) error {
		if step == "staged_payloads" {
			return errors.New("injected crash")
		}
		return nil
	}
	if err := m.prepareCompletePullPublicationWith(checkpoint, 0, entry, true, artifacts, nil, ops); err == nil {
		t.Fatal("injected stage crash returned nil")
	}
	if _, err := os.Stat(filepath.Join(m.Root, filepath.FromSlash(entry.State.Path))); !os.IsNotExist(err) {
		t.Fatalf("canonical artifact was published before intent: %v", err)
	}
	dir, _ := m.completePullPublicationDir(checkpoint.SelectorSHA256)
	if err := os.WriteFile(filepath.Join(dir, ".tmp-0123456789abcdef"), []byte("partial atomic stage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.RecoverCompletePullPublication(checkpoint.SelectorSHA256, checkpoint, true); err != nil {
		t.Fatalf("recover pre-intent residue: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("pre-intent residue survived: %v", err)
	}
	if err := m.PrepareCompletePullPublication(checkpoint, 0, entry, true, artifacts, nil); err != nil {
		t.Fatalf("restart after pre-intent crash: %v", err)
	}
}

func TestCompletePullPublicationPreservesUnexpectedPreIntentEvidence(t *testing.T) {
	m, checkpoint, _, _ := completePullPublicationFixture(t)
	dir, _ := m.completePullPublicationDir(checkpoint.SelectorSHA256)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	unexpected := filepath.Join(dir, "manual-note")
	if err := os.WriteFile(unexpected, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.RecoverCompletePullPublication(checkpoint.SelectorSHA256, checkpoint, true); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("unexpected residue error=%v", err)
	}
	if got, err := os.ReadFile(unexpected); err != nil || string(got) != "preserve" {
		t.Fatalf("unexpected evidence changed: %q err=%v", got, err)
	}
}

func TestCompletePullPublicationRejectsMissingStageAndUnexpectedUserEdit(t *testing.T) {
	t.Run("missing payload", func(t *testing.T) {
		m, checkpoint, entry, artifacts := completePullPublicationFixture(t)
		if err := m.PrepareCompletePullPublication(checkpoint, 0, entry, true, artifacts, nil); err != nil {
			t.Fatal(err)
		}
		dir, _ := m.completePullPublicationDir(checkpoint.SelectorSHA256)
		if err := os.Remove(filepath.Join(dir, "payload-0000")); err != nil {
			t.Fatal(err)
		}
		if err := m.RecoverCompletePullPublication(checkpoint.SelectorSHA256, checkpoint, true); !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("error=%v", err)
		}
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("evidence was not preserved: %v", err)
		}
	})

	t.Run("corrupt intent", func(t *testing.T) {
		m, checkpoint, entry, artifacts := completePullPublicationFixture(t)
		if err := m.PrepareCompletePullPublication(checkpoint, 0, entry, true, artifacts, nil); err != nil {
			t.Fatal(err)
		}
		dir, _ := m.completePullPublicationDir(checkpoint.SelectorSHA256)
		intentPath := filepath.Join(dir, "intent.json")
		if err := os.WriteFile(intentPath, []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := m.RecoverCompletePullPublication(checkpoint.SelectorSHA256, checkpoint, true); !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("error=%v", err)
		}
		if _, err := os.Stat(intentPath); err != nil {
			t.Fatalf("corrupt intent evidence was not preserved: %v", err)
		}
	})

	t.Run("third hash", func(t *testing.T) {
		m, checkpoint, entry, artifacts := completePullPublicationFixture(t)
		target := filepath.Join(m.Root, filepath.FromSlash(artifactPathStringForTest(t, artifacts[0].Path)))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte("reviewed old bytes"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := m.PrepareCompletePullPublication(checkpoint, 0, entry, true, artifacts, nil); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte("external edit"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := m.RecoverCompletePullPublication(checkpoint.SelectorSHA256, checkpoint, true); !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("error=%v", err)
		}
		got, _ := os.ReadFile(target)
		if string(got) != "external edit" {
			t.Fatalf("external edit changed to %q", got)
		}
	})
}

func TestCompletePullPublicationIncompletePageDoesNotAdvanceJournal(t *testing.T) {
	m, checkpoint, entry, artifacts := completePullPublicationFixture(t)
	if err := m.PrepareCompletePullPublication(checkpoint, 0, entry, false, artifacts, nil); err != nil {
		t.Fatal(err)
	}
	if err := m.RecoverCompletePullPublication(checkpoint.SelectorSHA256, checkpoint, true); err != nil {
		t.Fatal(err)
	}
	if _, found, err := m.loadCompletePullJournal(checkpoint.SelectorSHA256); err != nil || found {
		t.Fatalf("ineligible journal found=%t err=%v", found, err)
	}
	got, found, err := m.SyncStateOf(entry.State.ID)
	if err != nil || !found || got != entry.State {
		t.Fatalf("state=%+v found=%t err=%v", got, found, err)
	}
	loaded, found, err := m.CompletePullCheckpoint(checkpoint.SelectorSHA256)
	if err != nil || !found || loaded.NextIndex != 0 {
		t.Fatalf("checkpoint=%+v found=%t err=%v", loaded, found, err)
	}
}

func TestCompletePullPublicationRecoversExistingTrackedPageUpdate(t *testing.T) {
	root := t.TempDir()
	m := New(root)
	old := &domain.Resource{ID: "10", Title: "Page", SpaceKey: "DOC", Version: 1, Body: []byte("<p>old</p>")}
	dir, slug := m.PageDir(old.SpaceKey, nil, old.Title)
	if err := m.WriteView(dir, slug, old, nil, MDViewOpts{}); err != nil {
		t.Fatal(err)
	}
	updated := *old
	updated.Version = 2
	updated.Body = []byte("<p>new</p>")
	state, artifacts, err := m.PrepareCompletePullView(dir, slug, &updated, nil, MDViewOpts{})
	if err != nil {
		t.Fatal(err)
	}
	entry := CompletePullJournalEntry{State: state, View: ViewState{Sections: []string{"content"}}}
	checkpoint := CompletePullCheckpoint{Service: "confluence", SelectorSHA256: completePullTestHash, OptionsSHA256: strings.Repeat("b", 64), SelectionSHA256: strings.Repeat("c", 64), IDs: []string{"10"}}
	if err := m.SaveCompletePullCheckpoint(checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := m.PrepareCompletePullPublication(checkpoint, 0, entry, true, artifacts, nil); err != nil {
		t.Fatal(err)
	}
	ops := defaultCompletePullPublicationOps()
	ops.after = func(step string) error {
		if step == "artifact:0" {
			return errors.New("injected crash after tracked native replacement")
		}
		return nil
	}
	if err := m.recoverCompletePullPublicationWith(checkpoint.SelectorSHA256, checkpoint, true, ops); err == nil {
		t.Fatal("injected tracked-page crash returned nil")
	}
	if err := m.RecoverCompletePullPublication(checkpoint.SelectorSHA256, checkpoint, true); err != nil {
		t.Fatal(err)
	}
	recovered, err := m.RecoverCompletePullJournal(checkpoint.SelectorSHA256, checkpoint, true)
	if err != nil || recovered.NextIndex != 1 {
		t.Fatalf("checkpoint=%+v err=%v", recovered, err)
	}
	got, ok, err := m.SyncStateOf(updated.ID)
	if err != nil || !ok || got != state {
		t.Fatalf("state=%+v ok=%t err=%v", got, ok, err)
	}
}

func TestCompletePullPublicationRelocationRecoversStateThenExactRetirement(t *testing.T) {
	for _, step := range []string{"state", "relocation:0", "relocation:1", "relocation:2", "relocation:3"} {
		t.Run(step, func(t *testing.T) {
			root := t.TempDir()
			m := New(root)
			old := &domain.Resource{ID: "10", Title: "Old", SpaceKey: "DOC", Version: 1, Body: []byte("<p>old</p>")}
			oldDir, oldSlug := m.PageDir(old.SpaceKey, nil, old.Title)
			if err := m.WriteView(oldDir, oldSlug, old, nil, MDViewOpts{}); err != nil {
				t.Fatal(err)
			}
			oldMD, err := os.ReadFile(filepath.Join(oldDir, oldSlug+".md"))
			if err != nil {
				t.Fatal(err)
			}
			_, newSlug := m.PageDir("DOC", nil, "New")
			newRel := filepath.ToSlash(filepath.Join("DOC", newSlug, newSlug+".csf"))
			plan, err := m.PlanPageRelocation(old.ID, newRel, oldMD)
			if err != nil || plan == nil {
				t.Fatalf("plan=%+v err=%v", plan, err)
			}
			body := []byte("<p>new</p>")
			state := SyncState{ID: old.ID, Version: 2, Hash: Hash(body), Path: newRel}
			entry := CompletePullJournalEntry{State: state, View: ViewState{Sections: []string{"content"}}}
			meta, _ := json.Marshal(Meta{ID: state.ID, Version: state.Version, Hash: state.Hash})
			artifacts := []CompletePullArtifact{
				{Path: mustPublicArtifactPath(t, state.Path), Data: body, Mode: 0o644},
				{Path: mustPublicArtifactPath(t, filepath.ToSlash(filepath.Join("DOC", newSlug, newSlug+".md"))), Data: []byte("new view"), Mode: 0o644, BestEffort: true},
				{Path: mustPublicArtifactPath(t, filepath.ToSlash(filepath.Join("DOC", newSlug, newSlug+".meta.json"))), Data: append(meta, '\n'), Mode: 0o644},
				{Path: mustPrivateArtifactPath(t, ".atl/base/10.csf"), Data: body, Mode: 0o600},
			}
			checkpoint := CompletePullCheckpoint{Service: "confluence", SelectorSHA256: completePullTestHash, OptionsSHA256: strings.Repeat("b", 64), SelectionSHA256: strings.Repeat("c", 64), IDs: []string{"10"}}
			if err := m.SaveCompletePullCheckpoint(checkpoint); err != nil {
				t.Fatal(err)
			}
			if err := m.PrepareCompletePullPublication(checkpoint, 0, entry, true, artifacts, plan); err != nil {
				t.Fatal(err)
			}
			var sidecarResidue string
			if step == "state" {
				intent, _, _, err := m.readPublicationIntent(checkpoint.SelectorSHA256, checkpoint, true)
				if err != nil {
					t.Fatal(err)
				}
				sidecarResidue = filepath.Join(filepath.Dir(m.sidecarPath()), completePullSidecarTemp(intent.WriteToken))
				if err := os.WriteFile(sidecarResidue, []byte("partial"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			ops := defaultCompletePullPublicationOps()
			ops.after = func(got string) error {
				if got == step {
					return errors.New("injected crash")
				}
				return nil
			}
			if err := m.recoverCompletePullPublicationWith(checkpoint.SelectorSHA256, checkpoint, true, ops); err == nil {
				t.Fatal("injected relocation crash returned nil")
			}
			if err := m.RecoverCompletePullPublication(checkpoint.SelectorSHA256, checkpoint, true); err != nil {
				t.Fatal(err)
			}
			if sidecarResidue != "" {
				if _, err := os.Stat(sidecarResidue); !os.IsNotExist(err) {
					t.Fatalf("relocation sidecar residue survived: %v", err)
				}
			}
			for _, path := range []string{filepath.Join(oldDir, oldSlug+".csf"), filepath.Join(oldDir, oldSlug+".md"), filepath.Join(oldDir, oldSlug+".meta.json")} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("old artifact survived: %s (%v)", path, err)
				}
			}
			got, ok, err := m.SyncStateOf(old.ID)
			if err != nil || !ok || got != state {
				t.Fatalf("state=%+v ok=%t err=%v", got, ok, err)
			}
		})
	}
}

func TestCompletePullPublicationPrivateBoundedAndContentMinimized(t *testing.T) {
	m, checkpoint, entry, artifacts := completePullPublicationFixture(t)
	if err := m.PrepareCompletePullPublication(checkpoint, 0, entry, true, artifacts, nil); err != nil {
		t.Fatal(err)
	}
	dir, _ := m.completePullPublicationDir(checkpoint.SelectorSHA256)
	dirInfo, err := os.Stat(dir)
	if err != nil || dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("stage mode=%v err=%v", dirInfo, err)
	}
	intentPath := filepath.Join(dir, "intent.json")
	intentBytes, err := os.ReadFile(intentPath)
	if err != nil {
		t.Fatal(err)
	}
	intentInfo, _ := os.Stat(intentPath)
	if intentInfo.Mode().Perm() != 0o600 {
		t.Fatalf("intent mode=%o", intentInfo.Mode().Perm())
	}
	for _, forbidden := range []string{"private staged body", "backend_url", "access_token", "Page Title"} {
		if bytes.Contains(intentBytes, []byte(forbidden)) {
			t.Fatalf("intent contains %q", forbidden)
		}
	}
	entries, _ := os.ReadDir(dir)
	for _, item := range entries {
		if strings.HasPrefix(item.Name(), "payload-") {
			info, _ := item.Info()
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("payload %s mode=%o", item.Name(), info.Mode().Perm())
			}
		}
	}

	m2, checkpoint2, entry2, _ := completePullPublicationFixture(t)
	tooMany := make([]CompletePullArtifact, maxCompletePullPublicationArtifacts+1)
	for i := range tooMany {
		tooMany[i] = CompletePullArtifact{Path: mustPublicArtifactPath(t, fmt.Sprintf("DOC/remove-%04d", i)), Remove: true}
	}
	if err := m2.PrepareCompletePullPublication(checkpoint2, 0, entry2, true, tooMany, nil); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("artifact bound error=%v", err)
	}
}

func TestStagePublicationArtifactRejectsInvalidPrivateOptionsBeforeStaging(t *testing.T) {
	for _, tc := range []struct {
		name     string
		artifact CompletePullArtifact
	}{
		{name: "remove", artifact: CompletePullArtifact{Path: mustPrivateArtifactPath(t, ".atl/base/10.csf"), Remove: true}},
		{name: "best effort", artifact: CompletePullArtifact{Path: mustPrivateArtifactPath(t, ".atl/base/10.csf"), Data: []byte("private"), Mode: 0o600, BestEffort: true}},
		{name: "non owner mode", artifact: CompletePullArtifact{Path: mustPrivateArtifactPath(t, ".atl/base/10.csf"), Data: []byte("private"), Mode: 0o644}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := New(t.TempDir())
			wrote := false
			ops := defaultCompletePullPublicationOps()
			ops.write = func(string, string, []byte, os.FileMode) error {
				wrote = true
				return nil
			}
			stageDir := filepath.Join(m.Root, ".atl", "pull-publications", "pending")
			if _, err := m.stagePublicationArtifact(stageDir, tc.artifact, 0, "0123456789abcdef", ops); !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("stage private artifact error=%v", err)
			}
			if wrote {
				t.Fatal("invalid private artifact wrote a stage payload")
			}
			if _, err := os.Stat(stageDir); !os.IsNotExist(err) {
				t.Fatalf("invalid private artifact left stage residue: %v", err)
			}
		})
	}
}

func TestCompletePullPublicationDurableSchema2BytesRemainExact(t *testing.T) {
	entry := CompletePullJournalEntry{
		State: SyncState{ID: "10", Version: 3, Hash: "h", Path: "DOC/page.csf"},
		View:  ViewState{Sections: []string{"content"}},
	}
	intent := completePullPublicationIntent{
		SchemaVersion: 2, Service: "confluence", SelectorSHA256: "s",
		OptionsSHA256: "o", SelectionSHA256: "q", Index: 1, Entry: entry,
		Eligible: true,
		Artifacts: []completePullPublicationArtifact{{
			Path: "DOC/page.csf", Pre: completePullPublicationPreState{},
			Payload: "payload-0000", SHA256: "digest", Size: 4, Mode: 0o644, Temp: ".tmp-token",
		}},
		WriteToken: "token",
	}
	gotIntent, err := json.MarshalIndent(intent, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	wantIntent := "{\n" +
		"  \"schema_version\": 2,\n" +
		"  \"service\": \"confluence\",\n" +
		"  \"selector_sha256\": \"s\",\n" +
		"  \"options_sha256\": \"o\",\n" +
		"  \"selection_sha256\": \"q\",\n" +
		"  \"index\": 1,\n" +
		"  \"entry\": {\n" +
		"    \"state\": {\n" +
		"      \"id\": \"10\",\n" +
		"      \"version\": 3,\n" +
		"      \"hash\": \"h\",\n" +
		"      \"path\": \"DOC/page.csf\"\n" +
		"    },\n" +
		"    \"view\": {\n" +
		"      \"sections\": [\n" +
		"        \"content\"\n" +
		"      ]\n" +
		"    }\n" +
		"  },\n" +
		"  \"checkpoint_eligible\": true,\n" +
		"  \"artifacts\": [\n" +
		"    {\n" +
		"      \"path\": \"DOC/page.csf\",\n" +
		"      \"pre\": {\n" +
		"        \"present\": false\n" +
		"      },\n" +
		"      \"payload\": \"payload-0000\",\n" +
		"      \"sha256\": \"digest\",\n" +
		"      \"size\": 4,\n" +
		"      \"mode\": 420,\n" +
		"      \"temp\": \".tmp-token\"\n" +
		"    }\n" +
		"  ],\n" +
		"  \"next\": 0,\n" +
		"  \"write_token\": \"token\"\n" +
		"}"
	if string(gotIntent) != wantIntent {
		t.Fatalf("schema-2 intent bytes changed:\n got %s\nwant %s", gotIntent, wantIntent)
	}

	journal := completePullJournal{
		SchemaVersion: 1, Service: "confluence", SelectorSHA256: "s",
		OptionsSHA256: "o", SelectionSHA256: "q", StartIndex: 1,
		Entries: []CompletePullJournalEntry{entry}, WriteToken: "token",
	}
	gotJournal, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	wantJournal := "{\n" +
		"  \"schema_version\": 1,\n" +
		"  \"service\": \"confluence\",\n" +
		"  \"selector_sha256\": \"s\",\n" +
		"  \"options_sha256\": \"o\",\n" +
		"  \"selection_sha256\": \"q\",\n" +
		"  \"start_index\": 1,\n" +
		"  \"entries\": [\n" +
		"    {\n" +
		"      \"state\": {\n" +
		"        \"id\": \"10\",\n" +
		"        \"version\": 3,\n" +
		"        \"hash\": \"h\",\n" +
		"        \"path\": \"DOC/page.csf\"\n" +
		"      },\n" +
		"      \"view\": {\n" +
		"        \"sections\": [\n" +
		"          \"content\"\n" +
		"        ]\n" +
		"      }\n" +
		"    }\n" +
		"  ],\n" +
		"  \"write_token\": \"token\"\n" +
		"}"
	if string(gotJournal) != wantJournal {
		t.Fatalf("journal bytes changed:\n got %s\nwant %s", gotJournal, wantJournal)
	}
}

func TestCompletePullPublicationReparsesDurableClassesAndBindsPrivateBase(t *testing.T) {
	m, checkpoint, _, artifacts := completePullPublicationFixture(t)
	entry := CompletePullJournalEntry{
		State: SyncState{ID: "10", Version: 3, Hash: Hash(artifacts[0].Data), Path: "DOC/page/page.csf"},
		View:  ViewState{Sections: []string{"content"}},
	}
	if err := m.PrepareCompletePullPublication(checkpoint, 0, entry, true, artifacts, nil); err != nil {
		t.Fatal(err)
	}
	baseIntent, dir, found, err := m.readPublicationIntent(checkpoint.SelectorSHA256, checkpoint, true)
	if err != nil || !found {
		t.Fatalf("intent found=%t err=%v", found, err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*completePullPublicationIntent)
	}{
		{name: "exact private root", mutate: func(v *completePullPublicationIntent) { v.Artifacts[0].Path = ".atl" }},
		{name: "private case alias", mutate: func(v *completePullPublicationIntent) { v.Artifacts[0].Path = ".ATL/base/10.csf" }},
		{name: "other private subtree", mutate: func(v *completePullPublicationIntent) { v.Artifacts[0].Path = ".atl/cache/10.csf" }},
		{name: "base identity mismatch", mutate: func(v *completePullPublicationIntent) { v.Artifacts[5].Path = ".atl/base/20.csf" }},
		{name: "base extension mismatch", mutate: func(v *completePullPublicationIntent) { v.Artifacts[5].Path = ".atl/base/10.wiki" }},
		{name: "base removal", mutate: func(v *completePullPublicationIntent) {
			v.Artifacts[5] = completePullPublicationArtifact{Path: ".atl/base/10.csf", Remove: true}
		}},
		{name: "base best effort", mutate: func(v *completePullPublicationIntent) { v.Artifacts[5].BestEffort = true }},
		{name: "base permissive mode", mutate: func(v *completePullPublicationIntent) { v.Artifacts[5].Mode = 0o644 }},
		{name: "private relocation", mutate: func(v *completePullPublicationIntent) {
			v.Relocation = &completePullPublicationRelocation{Artifacts: []completePullPublicationArtifact{{Path: ".atl/base/10.csf", Remove: true}}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			encoded, _ := json.Marshal(baseIntent)
			var intent completePullPublicationIntent
			if err := json.Unmarshal(encoded, &intent); err != nil {
				t.Fatal(err)
			}
			tc.mutate(&intent)
			if err := validateCompletePullPublication(intent, checkpoint, ""); !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("error=%v", err)
			}
			if _, err := os.Stat(filepath.Join(dir, "intent.json")); err != nil {
				t.Fatalf("durable intent evidence changed: %v", err)
			}
		})
	}
}

func TestCompletePullPublicationRecoversExactOwnedDestinationResidue(t *testing.T) {
	tests := []struct {
		name     string
		eligible bool
		residue  func(*Mirror, CompletePullCheckpoint, completePullPublicationIntent) (string, error)
	}{
		{
			name: "canonical artifact", eligible: true,
			residue: func(m *Mirror, _ CompletePullCheckpoint, intent completePullPublicationIntent) (string, error) {
				artifact := intent.Artifacts[0]
				target := filepath.Join(m.Root, filepath.FromSlash(artifact.Path))
				if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
					return "", err
				}
				return filepath.Join(filepath.Dir(target), artifact.Temp), nil
			},
		},
		{
			name: "first journal", eligible: true,
			residue: func(m *Mirror, checkpoint CompletePullCheckpoint, intent completePullPublicationIntent) (string, error) {
				path, err := m.completePullJournalPath(checkpoint.SelectorSHA256)
				return filepath.Join(filepath.Dir(path), completePullJournalTemp(intent.WriteToken)), err
			},
		},
		{
			name: "ineligible sidecar", eligible: false,
			residue: func(m *Mirror, _ CompletePullCheckpoint, intent completePullPublicationIntent) (string, error) {
				return filepath.Join(filepath.Dir(m.sidecarPath()), completePullSidecarTemp(intent.WriteToken)), nil
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, checkpoint, entry, artifacts := completePullPublicationFixture(t)
			if err := m.PrepareCompletePullPublication(checkpoint, 0, entry, tc.eligible, artifacts, nil); err != nil {
				t.Fatal(err)
			}
			intent, _, found, err := m.readPublicationIntent(checkpoint.SelectorSHA256, checkpoint, true)
			if err != nil || !found {
				t.Fatalf("intent found=%t err=%v", found, err)
			}
			residue, err := tc.residue(m, checkpoint, intent)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(residue), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(residue, []byte("partial"), 0o600); err != nil {
				t.Fatal(err)
			}
			foreign := filepath.Join(filepath.Dir(residue), ".atl-cp-unrelated.tmp")
			if err := os.WriteFile(foreign, []byte("foreign"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := m.RecoverCompletePullPublication(checkpoint.SelectorSHA256, checkpoint, true); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(residue); !os.IsNotExist(err) {
				t.Fatalf("owned residue survived: %v", err)
			}
			if got, err := os.ReadFile(foreign); err != nil || string(got) != "foreign" {
				t.Fatalf("unowned sibling changed: %q err=%v", got, err)
			}
		})
	}
}

func TestCompletePullPublicationPreservesUnsafeOwnedDestinationResidue(t *testing.T) {
	m, checkpoint, entry, artifacts := completePullPublicationFixture(t)
	if err := m.PrepareCompletePullPublication(checkpoint, 0, entry, true, artifacts, nil); err != nil {
		t.Fatal(err)
	}
	intent, _, _, err := m.readPublicationIntent(checkpoint.SelectorSHA256, checkpoint, true)
	if err != nil {
		t.Fatal(err)
	}
	artifact := intent.Artifacts[0]
	target := filepath.Join(m.Root, filepath.FromSlash(artifact.Path))
	residue := filepath.Join(filepath.Dir(target), artifact.Temp)
	if err := os.MkdirAll(residue, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := m.RecoverCompletePullPublication(checkpoint.SelectorSHA256, checkpoint, true); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("unsafe residue error=%v", err)
	}
	if info, err := os.Stat(residue); err != nil || !info.IsDir() {
		t.Fatalf("unsafe residue was not preserved: info=%v err=%v", info, err)
	}
}

func TestCompletePullPublicationRevalidatesSymlinksAtStagingAndRecovery(t *testing.T) {
	t.Run("final component at staging", func(t *testing.T) {
		m, checkpoint, entry, artifacts := completePullPublicationFixture(t)
		target := filepath.Join(m.Root, filepath.FromSlash(entry.State.Path))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), "outside.csf")
		if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, target); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if err := m.PrepareCompletePullPublication(checkpoint, 0, entry, true, artifacts, nil); !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("staging symlink error=%v", err)
		}
		if got, err := os.ReadFile(outside); err != nil || string(got) != "outside" {
			t.Fatalf("staging followed final symlink: got=%q err=%v", got, err)
		}
	})

	t.Run("parent after durable intent", func(t *testing.T) {
		m, checkpoint, entry, artifacts := completePullPublicationFixture(t)
		if err := m.PrepareCompletePullPublication(checkpoint, 0, entry, true, artifacts, nil); err != nil {
			t.Fatal(err)
		}
		doc := filepath.Join(m.Root, "DOC")
		preserved := filepath.Join(m.Root, "DOC-preserved")
		if err := os.Rename(doc, preserved); err != nil {
			t.Fatal(err)
		}
		outside := t.TempDir()
		outsideTarget := filepath.Join(outside, "page", "page.csf")
		if err := os.MkdirAll(filepath.Dir(outsideTarget), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(outsideTarget, []byte("outside"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, doc); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if err := m.RecoverCompletePullPublication(checkpoint.SelectorSHA256, checkpoint, true); !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("recovery symlink error=%v", err)
		}
		if got, err := os.ReadFile(outsideTarget); err != nil || string(got) != "outside" {
			t.Fatalf("recovery followed parent symlink: got=%q err=%v", got, err)
		}
		dir, _ := m.completePullPublicationDir(checkpoint.SelectorSHA256)
		if _, err := os.Stat(filepath.Join(dir, "intent.json")); err != nil {
			t.Fatalf("recovery discarded durable evidence: %v", err)
		}
	})

	t.Run("final component after durable intent", func(t *testing.T) {
		m, checkpoint, entry, artifacts := completePullPublicationFixture(t)
		if err := m.PrepareCompletePullPublication(checkpoint, 0, entry, true, artifacts, nil); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(m.Root, filepath.FromSlash(entry.State.Path))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), "outside.csf")
		if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, target); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if err := m.RecoverCompletePullPublication(checkpoint.SelectorSHA256, checkpoint, true); !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("recovery final symlink error=%v", err)
		}
		if got, err := os.ReadFile(outside); err != nil || string(got) != "outside" {
			t.Fatalf("recovery followed final symlink: got=%q err=%v", got, err)
		}
		dir, _ := m.completePullPublicationDir(checkpoint.SelectorSHA256)
		if _, err := os.Stat(filepath.Join(dir, "intent.json")); err != nil {
			t.Fatalf("recovery discarded durable evidence: %v", err)
		}
	})
}

func TestCompletePullPublicationRejectsPersistedUntrustedArtifactPathAndPreservesIntent(t *testing.T) {
	for _, invalid := range []string{".atl", ".ATL/base/10.csf", ".atl/cache/10.csf", "../escape.csf"} {
		t.Run(invalid, func(t *testing.T) {
			m, checkpoint, entry, artifacts := completePullPublicationFixture(t)
			if err := m.PrepareCompletePullPublication(checkpoint, 0, entry, true, artifacts, nil); err != nil {
				t.Fatal(err)
			}
			dir, _ := m.completePullPublicationDir(checkpoint.SelectorSHA256)
			intentPath := filepath.Join(dir, "intent.json")
			body, err := os.ReadFile(intentPath)
			if err != nil {
				t.Fatal(err)
			}
			var intent completePullPublicationIntent
			if err := json.Unmarshal(body, &intent); err != nil {
				t.Fatal(err)
			}
			intent.Artifacts[0].Path = invalid
			body, err = json.MarshalIndent(intent, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			body = append(body, '\n')
			if err := os.WriteFile(intentPath, body, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := m.RecoverCompletePullPublication(checkpoint.SelectorSHA256, checkpoint, true); !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("persisted path %q error=%v", invalid, err)
			}
			got, err := os.ReadFile(intentPath)
			if err != nil || !bytes.Equal(got, body) {
				t.Fatalf("invalid durable intent evidence changed: got=%q err=%v", got, err)
			}
		})
	}
}

func TestCompletePullJournalFirstWriteRequiresSurvivingIntent(t *testing.T) {
	m, checkpoint, entries := completePullJournalFixture(t, "10")
	if err := m.appendCompletePullJournalOwned(checkpoint, 0, entries[0], completePullTestWriteToken); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("ownerless first journal error=%v", err)
	}
	path, _ := m.completePullJournalPath(checkpoint.SelectorSHA256)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("ownerless journal appeared: %v", err)
	}
}

func TestCompletePullPublicationCommittedIntentRequiresAcceptanceEvidence(t *testing.T) {
	m, checkpoint, entry, artifacts := completePullPublicationFixture(t)
	if err := m.PrepareCompletePullPublication(checkpoint, 0, entry, true, artifacts, nil); err != nil {
		t.Fatal(err)
	}
	dir, _ := m.completePullPublicationDir(checkpoint.SelectorSHA256)
	intentPath := filepath.Join(dir, "intent.json")
	b, err := os.ReadFile(intentPath)
	if err != nil {
		t.Fatal(err)
	}
	var intent completePullPublicationIntent
	if err := decodeCompletePullJSON(intentPath, b, &intent); err != nil {
		t.Fatal(err)
	}
	intent.Next = len(intent.Artifacts)
	intent.Committed = true
	for _, artifact := range artifacts {
		if artifact.Remove {
			continue
		}
		target := filepath.Join(m.Root, filepath.FromSlash(artifactPathStringForTest(t, artifact.Path)))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, artifact.Data, artifact.Mode); err != nil {
			t.Fatal(err)
		}
	}
	encoded, _ := json.MarshalIndent(intent, "", "  ")
	if err := safepath.WriteFileWithin(m.Root, intentPath, append(encoded, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	// A forged/torn committed bit must not be allowed to discard the only
	// staged evidence when no accepted journal entry exists.
	if err := m.RecoverCompletePullPublication(checkpoint.SelectorSHA256, checkpoint, true); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("error=%v", err)
	}
}
