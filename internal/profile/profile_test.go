package profile

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
)

func validProfile() Profile {
	return Profile{
		SchemaVersion: 1,
		Schema: SchemaFacts{JiraFields: []JiraFieldFact{{
			ID: "customfield_10001", Name: "Risk", Type: "string", Source: "atl jira fields",
			VerifiedAt: time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC),
		}}},
		Preferences: Preferences{Confirmed: true, Services: []string{"jira"}, MirrorRoot: "~/.atl/work"},
		TeamPolicy:  &TeamPolicy{Source: "team onboarding v1", Rules: []string{"review before push"}},
		Selectors: Selectors{Jira: []JiraSelector{{
			Name: "my-work", JQL: "assignee = currentUser()", Fields: []string{"status", "summary"},
		}}},
	}
}

func TestPreviewApplyRequiresExactHashes(t *testing.T) {
	dir := t.TempDir()
	p := validProfile()
	preview, err := BuildPreview(dir, p)
	if err != nil {
		t.Fatal(err)
	}
	if preview.CurrentExists || !preview.Changed || preview.CurrentHash != MissingHash() {
		t.Fatalf("preview = %+v", preview)
	}
	if _, err := Apply(dir, p, "wrong", preview.CurrentHash); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("candidate mismatch error = %v", err)
	}
	result, err := Apply(dir, p, preview.CandidateHash, preview.CurrentHash)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.ProfileHash != preview.CandidateHash {
		t.Fatalf("result = %+v", result)
	}
	info, err := os.Stat(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
	got, exists, hash, err := Read(dir)
	if err != nil || !exists || hash != preview.CandidateHash || got.SchemaVersion != 1 {
		t.Fatalf("read = %+v, %v, %q, %v", got, exists, hash, err)
	}
}

func TestApplyRejectsConcurrentProfileChange(t *testing.T) {
	dir := t.TempDir()
	p := validProfile()
	preview, err := BuildPreview(dir, p)
	if err != nil {
		t.Fatal(err)
	}
	other := p
	other.Preferences.MirrorRoot = "~/.atl/other"
	otherPreview, err := BuildPreview(dir, other)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(dir, other, otherPreview.CandidateHash, otherPreview.CurrentHash); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(dir, p, preview.CandidateHash, preview.CurrentHash); !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("error = %v, want version conflict", err)
	}
}

func TestProfileRejectsControlCharactersInMirrorRoot(t *testing.T) {
	for _, root := range []string{"safe\nother", "safe\tother", "safe\x00other"} {
		p := validProfile()
		p.Preferences.MirrorRoot = root
		if _, _, err := Canonical(p); !errors.Is(err, domain.ErrUsage) {
			t.Fatalf("Canonical mirror_root %q error = %v", root, err)
		}
	}
}

func TestProfileValidatesAndCanonicalizesConfluencePageFields(t *testing.T) {
	p := validProfile()
	p.RenderDefaults = &config.RenderConfig{Confluence: &config.RenderService{
		PageFields: []config.ConfluenceFieldView{{ID: " updated ", Placement: "section"}},
	}}
	_, canonical, err := Canonical(p)
	if err != nil {
		t.Fatal(err)
	}
	var normalized Profile
	if err := json.Unmarshal(canonical, &normalized); err != nil {
		t.Fatal(err)
	}
	got := normalized.RenderDefaults.Confluence.PageFields[0]
	if got.ID != "updated" || got.Label != "Updated" || got.Format != "datetime" {
		t.Fatalf("canonical field = %+v", got)
	}

	bad := validProfile()
	bad.RenderDefaults = &config.RenderConfig{Jira: &config.RenderService{
		PageFields: []config.ConfluenceFieldView{{ID: "title"}},
	}}
	if _, _, err := Canonical(bad); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("Jira accepted Confluence page_fields: %v", err)
	}
}

func TestDecodeStrictAndBoundaries(t *testing.T) {
	tests := []struct {
		name string
		json string
	}{
		{"unknown", `{"schema_version":1,"surprise":true}`},
		{"unconfirmed preference", `{"schema_version":1,"preferences":{"services":["jira"]}}`},
		{"policy without source", `{"schema_version":1,"team_policy":{"rules":["x"]}}`},
		{"fact without provenance", `{"schema_version":1,"schema":{"jira_fields":[{"id":"x","name":"X"}]}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := DecodeStrict([]byte(tt.json)); !errors.Is(err, domain.ErrUsage) {
				t.Fatalf("error = %v, want usage", err)
			}
		})
	}
}

func TestReadStoredCorruptionIsConfigError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(`{"schema_version":99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := Read(dir); !errors.Is(err, domain.ErrConfig) {
		t.Fatalf("error = %v, want config", err)
	}
}

func TestReadRejectsOversizedStoredProfile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, MaxBytes+1); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := Read(dir); !errors.Is(err, domain.ErrConfig) {
		t.Fatalf("error = %v, want config", err)
	}
}

func TestApplyRefusesBusyProfileLock(t *testing.T) {
	dir := t.TempDir()
	lock, acquired, err := safepath.TryLockFileWithin(dir, filepath.Join(dir, lockFileName), 0o600)
	if err != nil || !acquired {
		t.Fatalf("acquire lock: acquired=%t err=%v", acquired, err)
	}
	defer func() { _ = lock.Unlock() }()
	p := validProfile()
	preview, err := BuildPreview(dir, p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(dir, p, preview.CandidateHash, preview.CurrentHash); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("error = %v, want check failed", err)
	}
}

func TestPreviewCanReplaceUnsupportedSchemaWithoutInterpretingIt(t *testing.T) {
	dir := t.TempDir()
	legacy := []byte(`{"schema_version":2,"future_private_data":{"shape":"unknown"}}`)
	if err := os.WriteFile(filepath.Join(dir, FileName), legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := Read(dir); !errors.Is(err, domain.ErrConfig) {
		t.Fatalf("ordinary read error = %v, want config", err)
	}
	p := validProfile()
	preview, err := BuildPreview(dir, p)
	if err != nil {
		t.Fatalf("migration preview: %v", err)
	}
	if preview.MigrationFromSchemaVersion == nil || *preview.MigrationFromSchemaVersion != 2 {
		t.Fatalf("migration version = %v", preview.MigrationFromSchemaVersion)
	}
	if preview.CurrentHash != hashBytes(legacy) {
		t.Fatalf("current hash = %q, want raw legacy hash", preview.CurrentHash)
	}
	if _, err := Apply(dir, p, preview.CandidateHash, preview.CurrentHash); err != nil {
		t.Fatalf("migration apply: %v", err)
	}
	if got, exists, _, err := Read(dir); err != nil || !exists || got.SchemaVersion != SchemaVersion {
		t.Fatalf("read migrated = %+v exists=%t err=%v", got, exists, err)
	}
}

func TestNoopApplyRepairsOwnerOnlyMode(t *testing.T) {
	dir := t.TempDir()
	p := validProfile()
	preview, err := BuildPreview(dir, p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(dir, p, preview.CandidateHash, preview.CurrentHash); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, FileName)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	preview, err = BuildPreview(dir, p)
	if err != nil || preview.Changed {
		t.Fatalf("noop preview = %+v err=%v", preview, err)
	}
	result, err := Apply(dir, p, preview.CandidateHash, preview.CurrentHash)
	if err != nil || result.Changed {
		t.Fatalf("noop apply = %+v err=%v", result, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
}

func TestCanonicalNormalizesUnorderedLists(t *testing.T) {
	a := validProfile()
	a.Preferences.Services = []string{"jira", "confluence", "jira"}
	a.Selectors.Jira[0].Fields = []string{"summary", "status", "summary"}
	b := validProfile()
	b.Preferences.Services = []string{"confluence", "jira"}
	b.Selectors.Jira[0].Fields = []string{"status", "summary"}
	ha, _, err := Canonical(a)
	if err != nil {
		t.Fatal(err)
	}
	hb, _, err := Canonical(b)
	if err != nil {
		t.Fatal(err)
	}
	if ha != hb {
		t.Fatalf("normalized hashes differ: %s != %s", ha, hb)
	}
}

func TestCanonicalDoesNotMutateCallerSlices(t *testing.T) {
	p := validProfile()
	p.Preferences.Services = []string{"jira", "confluence"}
	p.Selectors.Jira[0].Fields = []string{"summary", "status"}
	if _, _, err := Canonical(p); err != nil {
		t.Fatal(err)
	}
	if got := p.Preferences.Services; got[0] != "jira" || got[1] != "confluence" {
		t.Fatalf("services mutated: %v", got)
	}
	if got := p.Selectors.Jira[0].Fields; got[0] != "summary" || got[1] != "status" {
		t.Fatalf("fields mutated: %v", got)
	}
}

func TestCanonicalProfileEnforcesStoredReadLimit(t *testing.T) {
	p := Profile{
		SchemaVersion: 1,
		TeamPolicy:    &TeamPolicy{Source: "declared policy", Rules: []string{strings.Repeat("x", MaxBytes)}},
	}
	if _, _, err := Canonical(p); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("error = %v, want check failed", err)
	}
}

func TestPreviewSectionSummaryDoesNotOmitRemovals(t *testing.T) {
	dir := t.TempDir()
	p := validProfile()
	initial, _ := BuildPreview(dir, p)
	if _, err := Apply(dir, p, initial.CandidateHash, initial.CurrentHash); err != nil {
		t.Fatal(err)
	}
	empty := Profile{SchemaVersion: 1}
	preview, err := BuildPreview(dir, empty)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(preview.Sections)
	if string(encoded) == "[]" {
		t.Fatal("section changes unexpectedly empty")
	}
	for _, change := range preview.Sections {
		if change.Section == "team_policy" && change.Status != "removed" {
			t.Fatalf("team policy status = %q, want removed", change.Status)
		}
	}
}
