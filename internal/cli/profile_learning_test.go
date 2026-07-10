package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	profilepkg "github.com/isukharev/atl/internal/profile"
)

func TestProfileSuggestionCLIReviewRejectAndApply(t *testing.T) {
	cfgDir := t.TempDir()
	privateDir := t.TempDir()
	if err := os.Chmod(privateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	observations := filepath.Join(privateDir, "observations.json")
	observationJSON := strings.Replace(`{
  "schema_version": 1,
  "base_profile_hash": "BASE_HASH",
  "schema": {"jira_fields": [{
    "id": "customfield_10001", "name": "Risk", "type": "string",
    "source": "approved field lookup", "verified_at": "2026-07-10T12:00:00Z"
  }]},
  "preferences": {"services": ["jira"], "mirror_root": "~/.atl/work"},
  "evidence": [{
    "source": "approved session", "observed_at": "2026-07-10T12:05:00Z",
    "reason": "user confirmed Jira workflow"
  }]
}`, "BASE_HASH", profilepkg.MissingHash(), 1)
	if err := os.WriteFile(observations, []byte(observationJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	suggestionPath := filepath.Join(privateDir, "learning.atl-suggestion.json")
	env := map[string]string{"ATL_CONFIG_DIR": cfgDir}

	out, code := runCLI(t, env, "profile", "suggest", "--from-file", observations, "--out", suggestionPath)
	if code != exitOK {
		t.Fatalf("suggest exit %d: %s", code, out)
	}
	var suggested struct {
		SuggestionHash string `json:"suggestion_hash"`
	}
	if err := json.Unmarshal([]byte(out), &suggested); err != nil {
		t.Fatal(err)
	}
	assertGolden(t, "profile_suggest.json", normalizeLearningGolden(out, cfgDir, privateDir))

	out, code = runCLI(t, env, "profile", "suggestion", "review", "--from-file", suggestionPath)
	if code != exitOK {
		t.Fatalf("review exit %d: %s", code, out)
	}
	var review struct {
		SuggestionHash string `json:"suggestion_hash"`
		Preview        struct {
			CandidateHash string `json:"candidate_hash"`
			CurrentHash   string `json:"current_hash"`
		} `json:"preview"`
	}
	if err := json.Unmarshal([]byte(out), &review); err != nil {
		t.Fatal(err)
	}
	assertGolden(t, "profile_suggestion_review.json", normalizeLearningGolden(out, cfgDir, privateDir))
	if review.SuggestionHash != suggested.SuggestionHash {
		t.Fatalf("review hash %q != suggest hash %q", review.SuggestionHash, suggested.SuggestionHash)
	}

	out, code = runCLI(t, env, "profile", "suggestion", "reject", "--from-file", suggestionPath, "--suggestion-hash", review.SuggestionHash)
	if code != exitOK || !strings.Contains(out, `"changed": true`) {
		t.Fatalf("reject exit %d: %s", code, out)
	}
	assertGolden(t, "profile_suggestion_reject.json", normalizeLearningGolden(out, cfgDir, privateDir))

	out, code = runCLI(t, env, "profile", "suggest", "--from-file", observations, "--out", suggestionPath)
	if code != exitOK || !strings.Contains(out, `"previously_rejected": true`) {
		t.Fatalf("repeat suggest exit %d: %s", code, out)
	}

	out, code = runCLI(t, env, "profile", "suggestion", "apply", "--from-file", suggestionPath,
		"--suggestion-hash", review.SuggestionHash, "--candidate-hash", review.Preview.CandidateHash,
		"--expected-current-hash", review.Preview.CurrentHash)
	if code != exitOK || !strings.Contains(out, `"changed": true`) {
		t.Fatalf("apply exit %d: %s", code, out)
	}
	assertGolden(t, "profile_suggestion_apply.json", normalizeLearningGolden(out, cfgDir, privateDir))
	out, code = runCLI(t, env, "profile", "show", "--section", "schema", "--service", "jira")
	if code != exitOK || !strings.Contains(out, "customfield_10001") {
		t.Fatalf("show exit %d: %s", code, out)
	}
}

func TestProfileRevalidationCLI(t *testing.T) {
	cfgDir := t.TempDir()
	privateDir := t.TempDir()
	if err := os.Chmod(privateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	batch := filepath.Join(privateDir, "checks.json")
	batchJSON := strings.Replace(`{
  "schema_version": 1,
  "base_profile_hash": "BASE_HASH",
  "checked_at": "2026-07-10T12:00:00Z",
  "jira_fields": [
    {"id":"customfield_10001","status":"verified","name":"Risk","type":"string","source":"approved lookup"},
    {"id":"customfield_10002","status":"failed","source":"approved lookup","error":"forbidden"}
  ]
}`, "BASE_HASH", profilepkg.MissingHash(), 1)
	if err := os.WriteFile(batch, []byte(batchJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(privateDir, "verified.atl-observations.json")
	env := map[string]string{"ATL_CONFIG_DIR": cfgDir}
	out, code := runCLI(t, env, "profile", "revalidate", "--from-file", batch, "--out", outPath)
	if code != exitOK || !strings.Contains(out, `"status": "failed"`) || !strings.Contains(out, `"status": "verified"`) {
		t.Fatalf("revalidate exit %d: %s", code, out)
	}
	assertGolden(t, "profile_revalidate.json", normalizeLearningGolden(out, cfgDir, privateDir))
	out, code = runCLI(t, env, "profile", "revalidation", "status", "--stale-before", "2026-07-01T00:00:00Z", "--service", "jira")
	if code != exitOK || !strings.Contains(out, `"verified_pending"`) || !strings.Contains(out, `"failed"`) {
		t.Fatalf("status exit %d: %s", code, out)
	}
	assertGolden(t, "profile_revalidation_status.json", normalizeLearningGolden(out, cfgDir, privateDir))
}

func normalizeLearningGolden(out, configDir, privateDir string) []byte {
	out = strings.ReplaceAll(out, filepath.ToSlash(privateDir), "<private-dir>")
	out = strings.ReplaceAll(out, privateDir, "<private-dir>")
	return normalizeProfileGolden(out, configDir)
}
