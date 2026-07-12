package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

// clearConfigEnv removes every environment variable that influences Dir/Load so
// each test starts from a known, hermetic baseline and never reads the real
// ~/.config or developer env.
func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"ATL_CONFIG_DIR",
		"XDG_CONFIG_HOME",
		"ATL_CONFLUENCE_URL", "CONFLUENCE_URL",
		"ATL_JIRA_URL", "JIRA_URL",
		"ATL_UPDATE_URL",
	} {
		t.Setenv(k, "")
	}
}

func TestDirPrecedence(t *testing.T) {
	t.Run("ATL_CONFIG_DIR wins", func(t *testing.T) {
		clearConfigEnv(t)
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "xdg"))
		t.Setenv("HOME", t.TempDir())
		want := filepath.Join(t.TempDir(), "explicit")
		t.Setenv("ATL_CONFIG_DIR", want)
		if got := Dir(); got != want {
			t.Errorf("Dir() = %q, want %q", got, want)
		}
	})

	t.Run("XDG_CONFIG_HOME/atl when ATL_CONFIG_DIR unset", func(t *testing.T) {
		clearConfigEnv(t)
		t.Setenv("HOME", t.TempDir())
		xdg := filepath.Join(t.TempDir(), "xdg")
		t.Setenv("XDG_CONFIG_HOME", xdg)
		want := filepath.Join(xdg, "atl")
		if got := Dir(); got != want {
			t.Errorf("Dir() = %q, want %q", got, want)
		}
	})

	t.Run("~/.config/atl fallback when no env set", func(t *testing.T) {
		clearConfigEnv(t)
		home := t.TempDir()
		t.Setenv("HOME", home)
		want := filepath.Join(home, ".config", "atl")
		got := Dir()
		if runtime.GOOS == "windows" {
			// os.UserHomeDir keys off USERPROFILE on Windows; skip the
			// HOME-based assertion there rather than make a brittle claim.
			t.Skipf("home resolution is OS-specific; got %q", got)
		}
		if got != want {
			t.Errorf("Dir() = %q, want %q", got, want)
		}
	})
}

func TestPathJoinsDirAndFilename(t *testing.T) {
	clearConfigEnv(t)
	dir := t.TempDir()
	t.Setenv("ATL_CONFIG_DIR", dir)
	want := filepath.Join(dir, "config.json")
	if got := path(); got != want {
		t.Errorf("path() = %q, want %q", got, want)
	}
}

func TestLoadNoFileReturnsEmptyDefaults(t *testing.T) {
	clearConfigEnv(t)
	// Point at an empty temp dir: no config.json exists.
	t.Setenv("ATL_CONFIG_DIR", t.TempDir())
	c, err := Load()
	if err != nil {
		t.Fatalf("Load() with no file: unexpected error %v", err)
	}
	if c == nil {
		t.Fatal("Load() returned nil config")
	}
	if c.ConfluenceURL != "" || c.JiraURL != "" || c.UpdateBaseURL != "" {
		t.Errorf("Load() with no file = %+v, want zero-valued config", *c)
	}
}

func TestLoadReadsFileFromDisk(t *testing.T) {
	clearConfigEnv(t)
	dir := t.TempDir()
	t.Setenv("ATL_CONFIG_DIR", dir)
	want := &Config{
		ConfluenceURL: "https://confluence.example.com",
		JiraURL:       "https://jira.example.com",
		UpdateBaseURL: "https://dist.example.com",
	}
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if c.ConfluenceURL != want.ConfluenceURL || c.JiraURL != want.JiraURL || c.UpdateBaseURL != want.UpdateBaseURL {
		t.Errorf("Load() = %+v, want %+v", *c, *want)
	}
}

func TestLoadTrimsTrailingSlashesFromFile(t *testing.T) {
	clearConfigEnv(t)
	dir := t.TempDir()
	t.Setenv("ATL_CONFIG_DIR", dir)
	on := `{"confluence_url":"https://c.example.com/","jira_url":"https://j.example.com//","update_base_url":"https://u.example.com/"}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(on), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.ConfluenceURL != "https://c.example.com" {
		t.Errorf("ConfluenceURL = %q, want trailing slash trimmed", c.ConfluenceURL)
	}
	if c.JiraURL != "https://j.example.com" {
		t.Errorf("JiraURL = %q, want all trailing slashes trimmed", c.JiraURL)
	}
	if c.UpdateBaseURL != "https://u.example.com" {
		t.Errorf("UpdateBaseURL = %q, want trailing slash trimmed", c.UpdateBaseURL)
	}
}

func TestLoadEnvOverlaysFile(t *testing.T) {
	clearConfigEnv(t)
	dir := t.TempDir()
	t.Setenv("ATL_CONFIG_DIR", dir)
	onDisk := `{"confluence_url":"https://file-conf.example.com","jira_url":"https://file-jira.example.com","update_base_url":"https://file-upd.example.com"}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(onDisk), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ATL_CONFLUENCE_URL", "https://env-conf.example.com")
	t.Setenv("ATL_JIRA_URL", "https://env-jira.example.com")
	t.Setenv("ATL_UPDATE_URL", "https://env-upd.example.com")

	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.ConfluenceURL != "https://env-conf.example.com" {
		t.Errorf("ConfluenceURL = %q, want env override", c.ConfluenceURL)
	}
	if c.JiraURL != "https://env-jira.example.com" {
		t.Errorf("JiraURL = %q, want env override", c.JiraURL)
	}
	if c.UpdateBaseURL != "https://env-upd.example.com" {
		t.Errorf("UpdateBaseURL = %q, want env override", c.UpdateBaseURL)
	}
}

// firstEnv prefers ATL_-prefixed vars, but the legacy unprefixed names still
// work as a fallback overlay.
func TestLoadLegacyEnvNamesOverlay(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("ATL_CONFIG_DIR", t.TempDir())
	t.Setenv("CONFLUENCE_URL", "https://legacy-conf.example.com")
	t.Setenv("JIRA_URL", "https://legacy-jira.example.com")

	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.ConfluenceURL != "https://legacy-conf.example.com" {
		t.Errorf("ConfluenceURL = %q, want legacy env value", c.ConfluenceURL)
	}
	if c.JiraURL != "https://legacy-jira.example.com" {
		t.Errorf("JiraURL = %q, want legacy env value", c.JiraURL)
	}
}

// Malformed JSON fails as configuration before env overlays or backend access;
// silently treating it as empty could erase unrelated settings on the next set.
func TestLoadMalformedJSONFailsClosed(t *testing.T) {
	clearConfigEnv(t)
	dir := t.TempDir()
	t.Setenv("ATL_CONFIG_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{not valid json"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ATL_JIRA_URL", "https://env-jira.example.com")

	if _, err := Load(); !errors.Is(err, domain.ErrConfig) {
		t.Fatalf("Load() malformed error=%v", err)
	}
	if _, err := LoadForEdit(); !errors.Is(err, domain.ErrConfig) {
		t.Fatalf("LoadForEdit() malformed error=%v", err)
	}
}

func TestSaveRoundTripsThroughLoad(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("ATL_CONFIG_DIR", t.TempDir())
	want := &Config{
		ConfluenceURL: "https://conf.example.com",
		JiraURL:       "https://jira.example.com",
		UpdateBaseURL: "https://upd.example.com",
	}
	if err := Save(want); err != nil {
		t.Fatalf("Save() = %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if got.ConfluenceURL != want.ConfluenceURL || got.JiraURL != want.JiraURL || got.UpdateBaseURL != want.UpdateBaseURL {
		t.Errorf("round trip = %+v, want %+v", *got, *want)
	}
}

func TestSaveWritesToResolvedPath(t *testing.T) {
	clearConfigEnv(t)
	dir := t.TempDir()
	t.Setenv("ATL_CONFIG_DIR", dir)
	if err := Save(&Config{JiraURL: "https://jira.example.com"}); err != nil {
		t.Fatalf("Save() = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json")); err != nil {
		t.Errorf("config.json not written to resolved path: %v", err)
	}
}

func TestSaveCreatesMissingDir(t *testing.T) {
	clearConfigEnv(t)
	// Nested, not-yet-existing config dir: Save must MkdirAll it.
	dir := filepath.Join(t.TempDir(), "nested", "atl")
	t.Setenv("ATL_CONFIG_DIR", dir)
	if err := Save(&Config{ConfluenceURL: "https://conf.example.com"}); err != nil {
		t.Fatalf("Save() into missing dir = %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("config dir not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("expected %q to be a directory", dir)
	}
}

// The config file holds the self-update source URL and must stay owner-only
// (0600), consistent with credentials. A looser mode would be a security
// regression.
func TestSaveFileModeIs0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix file permissions are not modeled on windows")
	}
	clearConfigEnv(t)
	dir := t.TempDir()
	t.Setenv("ATL_CONFIG_DIR", dir)
	if err := Save(&Config{JiraURL: "https://jira.example.com"}); err != nil {
		t.Fatalf("Save() = %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("config.json mode = %o, want 0600", perm)
	}
}

// The dir is created 0700 (owner-only) per Save's MkdirAll.
func TestSaveDirModeIs0700(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix file permissions are not modeled on windows")
	}
	clearConfigEnv(t)
	dir := filepath.Join(t.TempDir(), "atl")
	t.Setenv("ATL_CONFIG_DIR", dir)
	if err := Save(&Config{}); err != nil {
		t.Fatalf("Save() = %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("config dir mode = %o, want 0700", perm)
	}
}

// WriteFileAtomic writes via a ".tmp-*" sibling then renames it; on success no
// temp artifact should be left behind in the config dir.
func TestSaveLeavesNoTempFile(t *testing.T) {
	clearConfigEnv(t)
	dir := t.TempDir()
	t.Setenv("ATL_CONFIG_DIR", dir)
	if err := Save(&Config{JiraURL: "https://jira.example.com"}); err != nil {
		t.Fatalf("Save() = %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("leftover temp file after Save: %q", e.Name())
		}
	}
	if len(names) != 1 || names[0] != "config.json" {
		t.Errorf("config dir entries = %v, want [config.json]", names)
	}
}

func TestFirstEnv(t *testing.T) {
	t.Run("first non-empty wins", func(t *testing.T) {
		t.Setenv("ATL_FE_A", "")
		t.Setenv("ATL_FE_B", "second")
		t.Setenv("ATL_FE_C", "third")
		if got := firstEnv("ATL_FE_A", "ATL_FE_B", "ATL_FE_C"); got != "second" {
			t.Errorf("firstEnv = %q, want %q", got, "second")
		}
	})

	t.Run("earliest set var preferred over later", func(t *testing.T) {
		t.Setenv("ATL_FE_A", "first")
		t.Setenv("ATL_FE_B", "second")
		if got := firstEnv("ATL_FE_A", "ATL_FE_B"); got != "first" {
			t.Errorf("firstEnv = %q, want %q", got, "first")
		}
	})

	t.Run("empty when none set", func(t *testing.T) {
		t.Setenv("ATL_FE_A", "")
		t.Setenv("ATL_FE_B", "")
		if got := firstEnv("ATL_FE_A", "ATL_FE_B"); got != "" {
			t.Errorf("firstEnv = %q, want empty", got)
		}
	})

	t.Run("no keys", func(t *testing.T) {
		if got := firstEnv(); got != "" {
			t.Errorf("firstEnv() = %q, want empty", got)
		}
	})
}
