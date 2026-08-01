package compatibility

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestLoadMissingReturnsDefault(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")
	settings, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if settings != (Settings{SchemaVersion: SettingsSchemaVersion}) {
		t.Fatalf("settings = %+v", settings)
	}
	if Path(dir) != filepath.Join(dir, SettingsFileName) {
		t.Fatalf("Path = %q", Path(dir))
	}
}

func TestSettingsSaveLoadRoundTripAndModes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "config")
	want := Settings{SchemaVersion: 1, Confluence: &Activation{ProviderID: ConfluenceInlineCommentsDCProfileID, Version: "9.5.2", BuildNumber: "12345"}}
	if err := Save(dir, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Errorf("directory mode = %o, want 0700", got)
	}
	fileInfo, err := os.Stat(Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Errorf("file mode = %o, want 0600", got)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.SchemaVersion != want.SchemaVersion || got.Confluence == nil || *got.Confluence != *want.Confluence {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}

	if err := os.Chmod(Path(dir), 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); !errors.Is(err, domain.ErrConfig) {
		t.Fatalf("Load loose file error = %v, want ErrConfig", err)
	}
}

func TestLoadRejectsUnknownFutureMalformedAndTrailingData(t *testing.T) {
	cases := map[string]string{
		"unknown top-level": `{"schema_version":1,"unknown":true}`,
		"unknown pin":       `{"schema_version":1,"confluence":{"provider_id":"confluence-inline-comments-dc-profile-1","version":"9.5.2","build_number":"12345","unknown":true}}`,
		"future schema":     `{"schema_version":2}`,
		"zero schema":       `{"confluence":{"provider_id":"confluence-inline-comments-dc-profile-1","version":"9.5.2","build_number":"12345"}}`,
		"unknown provider":  `{"schema_version":1,"confluence":{"provider_id":"unknown","version":"9.5.2","build_number":"12345"}}`,
		"malformed version": `{"schema_version":1,"confluence":{"provider_id":"confluence-inline-comments-dc-profile-1","version":"9.5","build_number":"12345"}}`,
		"malformed build":   `{"schema_version":1,"confluence":{"provider_id":"confluence-inline-comments-dc-profile-1","version":"9.5.2","build_number":"build-12345"}}`,
		"wrong field type":  `{"schema_version":1,"confluence":{"provider_id":"confluence-inline-comments-dc-profile-1","version":952,"build_number":"12345"}}`,
		"trailing object":   `{"schema_version":1} {"schema_version":1}`,
		"empty":             ``,
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(Path(dir), []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(dir)
			if !errors.Is(err, domain.ErrConfig) {
				t.Fatalf("Load error = %v, want ErrConfig", err)
			}
			if (data != "" && strings.Contains(err.Error(), data)) || strings.Contains(err.Error(), "build-12345") {
				t.Fatalf("error includes file contents: %v", err)
			}
		})
	}
}

func TestLoadRejectsOversize(t *testing.T) {
	dir := t.TempDir()
	data := []byte(`{"schema_version":1,"unknown":"` + strings.Repeat("x", MaxSettingsBytes) + `"}`)
	if err := os.WriteFile(Path(dir), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); !errors.Is(err, domain.ErrConfig) || !strings.Contains(err.Error(), "exceeds 16 KiB") {
		t.Fatalf("Load oversized error = %v", err)
	}
}

func TestSaveRejectsInvalidSettingsWithoutWriting(t *testing.T) {
	for name, settings := range map[string]Settings{
		"future":   {SchemaVersion: 2},
		"provider": {SchemaVersion: 1, Confluence: &Activation{ProviderID: "unknown", Version: "9.5.2", BuildNumber: "12345"}},
		"version":  {SchemaVersion: 1, Confluence: &Activation{ProviderID: ConfluenceInlineCommentsDCProfileID, Version: "9.5", BuildNumber: "12345"}},
		"build":    {SchemaVersion: 1, Confluence: &Activation{ProviderID: ConfluenceInlineCommentsDCProfileID, Version: "9.5.2", BuildNumber: ""}},
	} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "not-created")
			if err := Save(dir, settings); !errors.Is(err, domain.ErrConfig) {
				t.Fatalf("Save error = %v, want ErrConfig", err)
			}
			if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid settings created directory: %v", err)
			}
		})
	}
}
