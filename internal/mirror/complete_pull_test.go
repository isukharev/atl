package mirror

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
)

const completePullTestHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestCompletePullCheckpointRoundTripModeAndRetire(t *testing.T) {
	root := t.TempDir()
	m := New(root)
	if err := m.EnsureScaffold(); err != nil {
		t.Fatal(err)
	}
	want := CompletePullCheckpoint{
		Service: "confluence", SelectorSHA256: completePullTestHash,
		OptionsSHA256: strings.Repeat("b", 64), SelectionSHA256: strings.Repeat("c", 64),
		IDs: []string{"10", "20"}, NextIndex: 1,
	}
	if err := m.SaveCompletePullCheckpoint(want); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".atl", "complete-pulls", completePullTestHash+".json")
	manifestBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"backend_url", "token", "title", "body"} {
		if strings.Contains(string(manifestBefore), forbidden) {
			t.Fatalf("private checkpoint unexpectedly contains %q", forbidden)
		}
	}
	got, ok, err := m.CompletePullCheckpoint(completePullTestHash)
	if err != nil || !ok || got.SchemaVersion != 1 || got.NextIndex != 1 || len(got.IDs) != 2 {
		t.Fatalf("got=%+v ok=%v err=%v", got, ok, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("checkpoint mode=%v err=%v", info, err)
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil || dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("checkpoint dir mode=%v err=%v", dirInfo, err)
	}
	want.NextIndex = 2
	if err := m.SaveCompletePullCheckpoint(want); err != nil {
		t.Fatal(err)
	}
	manifestAfter, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(manifestAfter) != string(manifestBefore) {
		t.Fatal("progress update rewrote the immutable identity manifest")
	}
	progressPath := filepath.Join(root, ".atl", "complete-pulls", completePullTestHash+".progress.json")
	progressInfo, err := os.Stat(progressPath)
	if err != nil || progressInfo.Mode().Perm() != 0o600 {
		t.Fatalf("progress mode=%v err=%v", progressInfo, err)
	}
	got, ok, err = m.CompletePullCheckpoint(completePullTestHash)
	if err != nil || !ok || got.NextIndex != 2 {
		t.Fatalf("updated progress=%+v ok=%v err=%v", got, ok, err)
	}
	if err := m.RemoveCompletePullCheckpoint(completePullTestHash); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := m.CompletePullCheckpoint(completePullTestHash); err != nil || ok {
		t.Fatalf("retired checkpoint ok=%v err=%v", ok, err)
	}
}

func TestCompletePullCheckpointRejectsCorruptOrUnsafeState(t *testing.T) {
	root := t.TempDir()
	m := New(root)
	if err := m.EnsureScaffold(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.CompletePullCheckpoint("../escape"); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("unsafe hash error=%v", err)
	}
	bad := CompletePullCheckpoint{
		Service: "confluence", SelectorSHA256: completePullTestHash,
		OptionsSHA256: strings.Repeat("b", 64), SelectionSHA256: strings.Repeat("c", 64),
		IDs: []string{"10", "10"},
	}
	if err := m.SaveCompletePullCheckpoint(bad); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("duplicate checkpoint error=%v", err)
	}
	path, err := m.completePullCheckpointPath(completePullTestHash)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.CompletePullCheckpoint(completePullTestHash); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("corrupt checkpoint error=%v", err)
	}
}

func TestCompletePullCheckpointIgnoresStaleProgressAfterAtomicRestart(t *testing.T) {
	root := t.TempDir()
	m := New(root)
	if err := m.EnsureScaffold(); err != nil {
		t.Fatal(err)
	}
	old := CompletePullCheckpoint{
		Service: "confluence", SelectorSHA256: completePullTestHash,
		OptionsSHA256: strings.Repeat("b", 64), SelectionSHA256: strings.Repeat("c", 64),
		IDs: []string{"10", "20"}, NextIndex: 1,
	}
	if err := m.SaveCompletePullCheckpoint(old); err != nil {
		t.Fatal(err)
	}
	// Model the only cross-file restart crash window: the immutable selection
	// was atomically replaced, but the old tiny progress file still exists.
	restarted := old
	restarted.SchemaVersion = completePullCheckpointSchema
	restarted.OptionsSHA256 = strings.Repeat("d", 64)
	restarted.SelectionSHA256 = strings.Repeat("e", 64)
	restarted.IDs = []string{"30"}
	restarted.NextIndex = 0
	b, err := json.MarshalIndent(restarted, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path, err := m.completePullCheckpointPath(completePullTestHash)
	if err != nil {
		t.Fatal(err)
	}
	if err := safepath.WriteFileWithin(root, path, append(b, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	got, ok, err := m.CompletePullCheckpoint(completePullTestHash)
	if err != nil || !ok || got.NextIndex != 0 || !reflect.DeepEqual(got.IDs, []string{"30"}) {
		t.Fatalf("restarted checkpoint=%+v ok=%v err=%v", got, ok, err)
	}
}
