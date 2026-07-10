package profile

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/domain"
)

func installProfile(t *testing.T, dir string, p Profile) string {
	t.Helper()
	preview, err := BuildPreview(dir, p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(dir, p, preview.CandidateHash, preview.CurrentHash); err != nil {
		t.Fatal(err)
	}
	_, _, hash, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func privateOutDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRevalidationRemembersFailureWithoutReplacingVerifiedFact(t *testing.T) {
	dir := t.TempDir()
	p := Profile{SchemaVersion: 1, Schema: SchemaFacts{JiraFields: []JiraFieldFact{{
		ID: "customfield_10001", Name: "Risk", Source: "initial metadata",
		VerifiedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}}}}
	hash := installProfile(t, dir, p)
	batch := RevalidationBatch{
		SchemaVersion: 1, BaseProfileHash: hash, CheckedAt: time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC),
		JiraFields: []JiraFieldCheck{{ID: "customfield_10001", Status: "failed", Source: "approved field lookup", Error: "backend unavailable"}},
	}
	out := filepath.Join(privateOutDir(t), "x.atl-observations.json")
	result, err := ApplyRevalidation(dir, out, batch)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 1 || result.Entries[0].Status != "failed" {
		t.Fatalf("result = %+v", result)
	}
	got, _, _, err := Read(dir)
	if err != nil || len(got.Schema.JiraFields) != 1 || got.Schema.JiraFields[0].Source != "initial metadata" {
		t.Fatalf("verified profile fact changed: %+v err=%v", got, err)
	}
	status, err := RevalidationStatusFor(dir, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), "jira")
	if err != nil || len(status.Entries) != 1 || status.Entries[0].Status != "failed" {
		t.Fatalf("status = %+v err=%v", status, err)
	}
	info, err := os.Stat(filepath.Join(dir, revalidationFileName))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("state mode = %v err=%v", info, err)
	}
	older := batch
	older.CheckedAt = batch.CheckedAt.Add(-time.Hour)
	olderOut := filepath.Join(privateOutDir(t), "older.atl-observations.json")
	if _, err := ApplyRevalidation(dir, olderOut, older); !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("older check error = %v", err)
	}
	if _, err := os.Stat(olderOut); !os.IsNotExist(err) {
		t.Fatalf("older check unexpectedly wrote observations: %v", err)
	}
}

func TestVerifiedRevalidationFlowsThroughSuggestionBeforeProfile(t *testing.T) {
	dir := t.TempDir()
	hash := installProfile(t, dir, Profile{SchemaVersion: 1})
	checkedAt := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	batch := RevalidationBatch{
		SchemaVersion: 1, BaseProfileHash: hash, CheckedAt: checkedAt,
		JiraFields: []JiraFieldCheck{{ID: "customfield_10001", Status: "verified", Name: "Risk", Type: "string", Source: "approved field lookup"}},
	}
	out := filepath.Join(privateOutDir(t), "x.atl-observations.json")
	if _, err := ApplyRevalidation(dir, out, batch); err != nil {
		t.Fatal(err)
	}
	profileBefore, _, _, _ := Read(dir)
	if len(profileBefore.Schema.JiraFields) != 0 {
		t.Fatal("revalidation directly mutated profile")
	}
	status, err := RevalidationStatusFor(dir, checkedAt.Add(-time.Hour), "jira")
	if err != nil || len(status.Entries) != 1 || status.Entries[0].Status != "verified_pending" {
		t.Fatalf("pending status = %+v err=%v", status, err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	observations, err := DecodeObservationsStrict(data)
	if err != nil {
		t.Fatal(err)
	}
	suggestion, _, err := BuildSuggestion(dir, observations)
	if err != nil {
		t.Fatal(err)
	}
	review, err := ReviewSuggestion(dir, suggestion)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplySuggestion(dir, suggestion, suggestion.SuggestionHash, review.Preview.CandidateHash, review.Preview.CurrentHash); err != nil {
		t.Fatal(err)
	}
	status, err = RevalidationStatusFor(dir, checkedAt.Add(-time.Hour), "jira")
	if err != nil || len(status.Entries) != 1 || status.Entries[0].Status != "fresh" {
		t.Fatalf("final status = %+v err=%v", status, err)
	}
}

func TestRevalidationIsBaseGuardedAndStrict(t *testing.T) {
	dir := t.TempDir()
	batch := RevalidationBatch{
		SchemaVersion: 1, BaseProfileHash: MissingHash(), CheckedAt: time.Now().UTC(),
		ConfluenceSpaces: []ConfluenceSpaceCheck{{Key: "DOC", Status: "missing", Source: "approved lookup"}},
	}
	installProfile(t, dir, Profile{SchemaVersion: 1, Preferences: Preferences{Confirmed: true, Services: []string{"confluence"}}})
	if _, err := ApplyRevalidation(dir, filepath.Join(privateOutDir(t), "x.atl-observations.json"), batch); !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("stale base error = %v", err)
	}
	bad := `{"schema_version":1,"base_profile_hash":"` + MissingHash() + `","checked_at":"2026-07-10T12:00:00Z","jira_fields":[{"id":"x","status":"failed","source":"lookup"}],"team_policy":{}}`
	if _, err := DecodeRevalidationBatchStrict([]byte(bad)); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("strict error = %v", err)
	}
}

func TestRevalidationStatusUsesExplicitCutoff(t *testing.T) {
	dir := t.TempDir()
	verifiedAt := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	installProfile(t, dir, Profile{SchemaVersion: 1, Schema: SchemaFacts{ConfluenceSpaces: []ConfluenceSpaceFact{{
		Key: "DOC", Name: "Docs", Source: "space lookup", VerifiedAt: verifiedAt,
	}}}})
	fresh, err := RevalidationStatusFor(dir, verifiedAt.Add(-time.Second), "confluence")
	if err != nil || fresh.Entries[0].Status != "fresh" {
		t.Fatalf("fresh = %+v err=%v", fresh, err)
	}
	stale, err := RevalidationStatusFor(dir, verifiedAt.Add(time.Second), "confluence")
	if err != nil || stale.Entries[0].Status != "stale" {
		t.Fatalf("stale = %+v err=%v", stale, err)
	}
}

func TestRevalidationBatchAndStateWritersAreBounded(t *testing.T) {
	batch := RevalidationBatch{
		SchemaVersion: 1, BaseProfileHash: MissingHash(), CheckedAt: time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC),
		JiraFields: make([]JiraFieldCheck, maxItems+1),
	}
	encoded, err := json.Marshal(batch)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeRevalidationBatchStrict(encoded); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("item limit error = %v", err)
	}
	state := revalidationState{
		SchemaVersion: 1,
		JiraFields: []jiraCheckState{{
			JiraFieldCheck: JiraFieldCheck{ID: "x", Status: "failed", Source: "lookup", Error: strings.Repeat("x", MaxBytes)},
			CheckedAt:      time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC),
		}},
	}
	if err := writeRevalidationState(t.TempDir(), state); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("state size error = %v", err)
	}
}
