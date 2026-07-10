package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const profileFixture = `{
  "schema_version": 1,
  "schema": {
    "jira_fields": [{
      "id": "customfield_10001",
      "name": "Risk",
      "type": "string",
      "source": "atl jira fields",
      "verified_at": "2026-07-01T12:00:00Z"
    }]
  },
  "preferences": {
    "confirmed": true,
    "services": ["jira"],
    "mirror_root": "~/.atl/work"
  },
  "team_policy": {
    "source": "team onboarding v1",
    "rules": ["review before push"]
  },
  "selectors": {
    "jira": [{"name":"my-work","jql":"assignee = currentUser()","fields":["summary","status"]}]
  }
}`

func TestProfilePreviewApplyShowAndGuidance(t *testing.T) {
	cfgDir := t.TempDir()
	candidate := filepath.Join(t.TempDir(), "candidate.json")
	if err := os.WriteFile(candidate, []byte(profileFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"ATL_CONFIG_DIR": cfgDir, "ATL_NO_UPDATE": "1"}

	out, code := runCLI(t, env, "profile", "preview", "--from-file", candidate)
	if code != 0 {
		t.Fatalf("preview exit %d: %s", code, out)
	}
	var preview struct {
		CurrentHash   string `json:"current_hash"`
		CandidateHash string `json:"candidate_hash"`
		Changed       bool   `json:"changed"`
	}
	if err := json.Unmarshal([]byte(out), &preview); err != nil {
		t.Fatal(err)
	}
	if !preview.Changed || len(preview.CurrentHash) != 64 || len(preview.CandidateHash) != 64 {
		t.Fatalf("preview = %+v", preview)
	}

	out, code = runCLI(t, env, "profile", "apply", "--from-file", candidate,
		"--candidate-hash", preview.CandidateHash, "--expected-current-hash", preview.CurrentHash)
	if code != 0 || !strings.Contains(out, `"changed": true`) {
		t.Fatalf("apply exit %d: %s", code, out)
	}

	out, code = runCLI(t, env, "profile", "show", "--section", "schema", "--service", "jira")
	if code != 0 || !strings.Contains(out, `"jira_fields"`) || strings.Contains(out, `"team_policy"`) {
		t.Fatalf("show slice exit %d: %s", code, out)
	}
	out, code = runCLI(t, env, "profile", "show")
	if code != 0 || strings.Contains(out, `"data"`) || strings.Contains(out, "customfield") {
		t.Fatalf("metadata-only show exit %d: %s", code, out)
	}

	out, code = runCLI(t, env, "profile", "guidance", "-o", "text")
	if code != 0 {
		t.Fatalf("guidance exit %d: %s", code, out)
	}
	if strings.Contains(out, "customfield") || strings.Contains(out, "assignee =") || !strings.Contains(out, "Load only the needed slice") {
		t.Fatalf("guidance leaked profile data or omitted slice instruction: %s", out)
	}
}

func TestProfileApplyRejectsUnreviewedCandidate(t *testing.T) {
	cfgDir := t.TempDir()
	candidate := filepath.Join(t.TempDir(), "candidate.json")
	if err := os.WriteFile(candidate, []byte(profileFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"ATL_CONFIG_DIR": cfgDir, "ATL_NO_UPDATE": "1"}
	out, code := runCLI(t, env, "profile", "apply", "--from-file", candidate,
		"--candidate-hash", strings.Repeat("0", 64), "--expected-current-hash", strings.Repeat("0", 64))
	if code != exitCheckFailed {
		t.Fatalf("apply exit %d: %s", code, out)
	}
}

func TestProfileShowMissingAndFlagValidation(t *testing.T) {
	env := map[string]string{"ATL_CONFIG_DIR": t.TempDir(), "ATL_NO_UPDATE": "1"}
	out, code := runCLI(t, env, "profile", "show")
	if code != 0 || !strings.Contains(out, `"exists": false`) {
		t.Fatalf("missing show exit %d: %s", code, out)
	}
	_, code = runCLI(t, env, "profile", "show", "--section", "preferences", "--service", "jira")
	if code != exitUsage {
		t.Fatalf("invalid service/section exit = %d, want %d", code, exitUsage)
	}
}

func TestProfileOutputGolden(t *testing.T) {
	cfgDir := t.TempDir()
	candidate := filepath.Join(t.TempDir(), "candidate.json")
	if err := os.WriteFile(candidate, []byte(profileFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"ATL_CONFIG_DIR": cfgDir, "ATL_NO_UPDATE": "1"}

	previewOut, code := runCLI(t, env, "profile", "preview", "--from-file", candidate)
	if code != exitOK {
		t.Fatalf("preview exit %d", code)
	}
	assertGolden(t, "profile_preview.json", normalizeProfileGolden(previewOut, cfgDir))
	var preview struct {
		CurrentHash   string `json:"current_hash"`
		CandidateHash string `json:"candidate_hash"`
	}
	if err := json.Unmarshal([]byte(previewOut), &preview); err != nil {
		t.Fatal(err)
	}

	applyOut, code := runCLI(t, env, "profile", "apply", "--from-file", candidate,
		"--candidate-hash", preview.CandidateHash, "--expected-current-hash", preview.CurrentHash)
	if code != exitOK {
		t.Fatalf("apply exit %d", code)
	}
	assertGolden(t, "profile_apply.json", normalizeProfileGolden(applyOut, cfgDir))

	showOut, code := runCLI(t, env, "profile", "show")
	if code != exitOK {
		t.Fatalf("show exit %d", code)
	}
	assertGolden(t, "profile_show.json", normalizeProfileGolden(showOut, cfgDir))

	sectionOut, code := runCLI(t, env, "profile", "show", "--section", "preferences")
	if code != exitOK {
		t.Fatalf("section show exit %d", code)
	}
	assertGolden(t, "profile_show_preferences.json", normalizeProfileGolden(sectionOut, cfgDir))

	guidanceOut, code := runCLI(t, env, "profile", "guidance")
	if code != exitOK {
		t.Fatalf("guidance exit %d", code)
	}
	assertGolden(t, "profile_guidance.json", []byte(guidanceOut))
}

func TestProfileApplyCurrentHashConflictMapsToExitFive(t *testing.T) {
	cfgDir := t.TempDir()
	env := map[string]string{"ATL_CONFIG_DIR": cfgDir, "ATL_NO_UPDATE": "1"}
	writeCandidate := func(name, root string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), name+".json")
		body := strings.Replace(profileFixture, "~/.atl/work", root, 1)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	a := writeCandidate("a", "~/.atl/a")
	b := writeCandidate("b", "~/.atl/b")

	previewA := profilePreviewHashes(t, env, a)
	previewB := profilePreviewHashes(t, env, b)
	if _, code := runCLI(t, env, "profile", "apply", "--from-file", b,
		"--candidate-hash", previewB.CandidateHash, "--expected-current-hash", previewB.CurrentHash); code != exitOK {
		t.Fatalf("apply B exit %d", code)
	}
	if _, code := runCLI(t, env, "profile", "apply", "--from-file", a,
		"--candidate-hash", previewA.CandidateHash, "--expected-current-hash", previewA.CurrentHash); code != exitVersionConfl {
		t.Fatalf("stale apply exit %d, want %d", code, exitVersionConfl)
	}
}

type previewHashes struct {
	CurrentHash   string `json:"current_hash"`
	CandidateHash string `json:"candidate_hash"`
}

func profilePreviewHashes(t *testing.T, env map[string]string, path string) previewHashes {
	t.Helper()
	out, code := runCLI(t, env, "profile", "preview", "--from-file", path)
	if code != exitOK {
		t.Fatalf("preview exit %d", code)
	}
	var hashes previewHashes
	if err := json.Unmarshal([]byte(out), &hashes); err != nil {
		t.Fatal(err)
	}
	return hashes
}

var profileHashPattern = regexp.MustCompile(`[a-f0-9]{64}`)

func normalizeProfileGolden(out, configDir string) []byte {
	out = strings.ReplaceAll(out, filepath.ToSlash(configDir), "<config-dir>")
	out = strings.ReplaceAll(out, configDir, "<config-dir>")
	out = profileHashPattern.ReplaceAllString(out, "<sha256>")
	return []byte(out)
}
