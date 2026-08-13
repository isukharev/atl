package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/app"
)

func TestReadOnlyProjectionIsConsistentAcrossInspectionCommands(t *testing.T) {
	tests := []struct {
		name                  string
		configured, flag, env bool
		wantSource            string
	}{
		{name: "none", wantSource: "none"},
		{name: "configuration", configured: true, wantSource: "configuration"},
		{name: "environment", env: true, wantSource: "environment"},
		{name: "environment over configuration", configured: true, env: true, wantSource: "environment"},
		{name: "flag", flag: true, wantSource: "flag"},
		{name: "flag over configuration", configured: true, flag: true, wantSource: "flag"},
		{name: "flag over environment", flag: true, env: true, wantSource: "flag"},
		{name: "flag over all", configured: true, flag: true, env: true, wantSource: "flag"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configDir := t.TempDir()
			body := []byte(`{"read_only":false}`)
			if test.configured {
				body = []byte(`{"read_only":true}`)
			}
			if err := os.WriteFile(filepath.Join(configDir, "config.json"), body, 0o600); err != nil {
				t.Fatal(err)
			}
			env := map[string]string{"ATL_CONFIG_DIR": configDir}
			if test.env {
				env["ATL_READ_ONLY"] = "1"
			}
			prefix := []string{}
			if test.flag {
				prefix = append(prefix, "--read-only")
			}
			effective := test.configured || test.flag || test.env

			configArgs := append(append([]string(nil), prefix...), "config", "show")
			out, code := runCLI(t, env, configArgs...)
			if code != exitOK {
				t.Fatalf("config show exit=%d output=%s", code, out)
			}
			var configResult configShowResult
			if err := json.Unmarshal([]byte(out), &configResult); err != nil {
				t.Fatal(err)
			}
			if configResult.ReadOnly != test.configured || configResult.ConfiguredReadOnly != test.configured ||
				configResult.EffectiveReadOnly != effective || configResult.ReadOnlySource != test.wantSource {
				t.Fatalf("config projection=%+v", configResult)
			}

			doctorArgs := append(append([]string(nil), prefix...), "doctor")
			out, code = runCLI(t, env, doctorArgs...)
			if code != exitCheckFailed {
				t.Fatalf("doctor exit=%d output=%s", code, out)
			}
			var doctorResult app.DoctorResult
			if err := json.Unmarshal([]byte(out), &doctorResult); err != nil {
				t.Fatal(err)
			}
			if doctorResult.Safety.ReadOnly != effective || doctorResult.Safety.ConfiguredReadOnly != test.configured ||
				doctorResult.Safety.EffectiveReadOnly != effective || doctorResult.Safety.ReadOnlySource != test.wantSource {
				t.Fatalf("doctor projection=%+v", doctorResult.Safety)
			}

			policyArgs := append(append([]string(nil), prefix...), "policy", "show")
			out, code = runCLI(t, env, policyArgs...)
			if code != exitOK {
				t.Fatalf("policy show exit=%d output=%s", code, out)
			}
			var policyResult policyShowResult
			if err := json.Unmarshal([]byte(out), &policyResult); err != nil {
				t.Fatal(err)
			}
			if policyResult.ReadOnly.Active != effective || policyResult.ReadOnly.ConfiguredReadOnly != test.configured ||
				policyResult.ReadOnly.EffectiveReadOnly != effective || policyResult.ReadOnly.ReadOnlySource != test.wantSource {
				t.Fatalf("policy projection=%+v", policyResult.ReadOnly)
			}
			if effective {
				if source, ok := policyResult.ReadOnly.Source.(string); !ok || source != test.wantSource {
					t.Fatalf("legacy policy read_only.source=%v want=%q", policyResult.ReadOnly.Source, test.wantSource)
				}
			} else if policyResult.ReadOnly.Source != nil {
				t.Fatalf("legacy inactive policy read_only.source=%v want null", policyResult.ReadOnly.Source)
			}
			textArgs := append(append([]string(nil), prefix...), "policy", "show", "-o", "text")
			textOut, textCode := runCLI(t, env, textArgs...)
			if textCode != exitOK || !strings.Contains(textOut, "configured_read_only: "+strconv.FormatBool(test.configured)) ||
				!strings.Contains(textOut, "effective_read_only: "+strconv.FormatBool(effective)) ||
				!strings.Contains(textOut, "read_only_source: "+test.wantSource) {
				t.Fatalf("policy text projection exit=%d output=%q", textCode, textOut)
			}
		})
	}
}
