package mirror

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIncrementalWatermarkRoundTripAndMode(t *testing.T) {
	root := t.TempDir()
	m := New(root)
	if err := m.EnsureScaffold(); err != nil {
		t.Fatal(err)
	}
	want := IncrementalWatermark{Service: "confluence", SelectorSHA256: "abc", Selector: "type=page", Since: "2026-07-13 12:00", TimeZone: "UTC", BoundaryVersions: map[string]int{"10": 2}}
	if err := m.SaveIncrementalWatermark(want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := m.IncrementalWatermark("confluence", "abc")
	if err != nil || !ok || got.Since != want.Since || got.BoundaryVersions["10"] != 2 {
		t.Fatalf("got=%+v ok=%v err=%v", got, ok, err)
	}
	info, err := os.Stat(filepath.Join(root, ".atl", "incremental.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("state mode=%v err=%v", info, err)
	}
}
