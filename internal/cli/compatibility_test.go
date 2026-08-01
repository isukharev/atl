package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/compatibility"
)

func TestCompatibilityStatusAndClearUseSeparateStrictSettings(t *testing.T) {
	configDir := t.TempDir()
	settings := compatibility.Settings{SchemaVersion: compatibility.SettingsSchemaVersion, Confluence: &compatibility.Activation{
		ProviderID: compatibility.ConfluenceInlineCommentsDCProfileID, Version: "9.5.2", BuildNumber: "12345",
	}}
	if err := compatibility.Save(configDir, settings); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"ATL_CONFIG_DIR": configDir}
	out, code := runCLI(t, env, "compatibility", "status")
	if code != exitOK {
		t.Fatalf("status exit=%d output=%s", code, out)
	}
	var status app.CompatibilityStatusResult
	if err := json.Unmarshal([]byte(out), &status); err != nil {
		t.Fatal(err)
	}
	if status.Status != app.CompatibilityStatusConfigured || status.Reason != "remote_not_requested" || status.Qualified {
		t.Fatalf("status result=%+v", status)
	}
	if _, err := os.Stat(filepath.Join(configDir, "config.json")); !os.IsNotExist(err) {
		t.Fatalf("compatibility status touched ordinary config: %v", err)
	}

	out, code = runCLI(t, env, "compatibility", "clear", "confluence")
	if code != exitOK || !strings.Contains(out, `"status": "disabled"`) {
		t.Fatalf("clear exit=%d output=%s", code, out)
	}
	loaded, err := compatibility.Load(configDir)
	if err != nil || loaded.Confluence != nil {
		t.Fatalf("cleared settings=%+v err=%v", loaded, err)
	}
}

func TestCompatibilityPinBindsCompiledProfileAndReadOnlyRefusesBeforeWrite(t *testing.T) {
	for name, testCase := range map[string]struct {
		env      map[string]string
		args     []string
		wantCode int
	}{
		"success":   {map[string]string{}, []string{"compatibility", "pin", "confluence", "--version", "9.5.2", "--build-number", "12345"}, exitOK},
		"read only": {map[string]string{"ATL_READ_ONLY": "1"}, []string{"compatibility", "pin", "confluence", "--version", "9.5.2", "--build-number", "12345"}, exitCheckFailed},
	} {
		t.Run(name, func(t *testing.T) {
			configDir := t.TempDir()
			testCase.env["ATL_CONFIG_DIR"] = configDir
			_, code := runCLI(t, testCase.env, testCase.args...)
			if code != testCase.wantCode {
				t.Fatalf("exit=%d want %d", code, testCase.wantCode)
			}
			_, err := os.Stat(filepath.Join(configDir, "compatibility.json"))
			if testCase.wantCode == exitOK && err != nil {
				t.Fatalf("settings file missing after pin: %v", err)
			}
			if testCase.wantCode != exitOK && !os.IsNotExist(err) {
				t.Fatalf("settings file exists after refused pin: %v", err)
			}
		})
	}
}

func TestCompatibilityStatusTextIsContentFree(t *testing.T) {
	out, code := runCLI(t, map[string]string{}, "compatibility", "status", "-o", "text")
	if code != exitOK || out != "Confluence compatibility provider: disabled (not_configured).\n" {
		t.Fatalf("exit=%d output=%q", code, out)
	}
}
