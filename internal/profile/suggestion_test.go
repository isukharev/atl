package profile

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
)

func observationsFor(t *testing.T, dir string) Observations {
	t.Helper()
	_, _, hash, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	services := []string{"jira", "confluence"}
	mirrorRoot := "~/.atl/team"
	return Observations{
		SchemaVersion:   1,
		BaseProfileHash: hash,
		Schema: SchemaFacts{JiraFields: []JiraFieldFact{{
			ID: "customfield_10002", Name: "Confidence", Type: "number", Source: "approved field metadata",
			VerifiedAt: time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC),
		}}},
		Preferences: &PreferenceProposal{Services: &services, MirrorRoot: &mirrorRoot},
		Selectors: Selectors{Jira: []JiraSelector{{
			Name: "active-work", JQL: "resolution is EMPTY", Fields: []string{"summary", "status"},
		}}},
		Evidence: []Evidence{{
			Source: "approved onboarding session", ObservedAt: time.Date(2026, 7, 10, 10, 5, 0, 0, time.UTC),
			Reason: "user confirmed recurring active-work reads",
		}},
	}
}

func TestSuggestionIsDeterministicAndDoesNotMutateProfile(t *testing.T) {
	dir := t.TempDir()
	p := validProfile()
	preview, _ := BuildPreview(dir, p)
	if _, err := Apply(dir, p, preview.CandidateHash, preview.CurrentHash); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	observations := observationsFor(t, dir)
	a, rejected, err := BuildSuggestion(dir, observations)
	if err != nil || rejected {
		t.Fatalf("suggest A rejected=%t err=%v", rejected, err)
	}
	observations.Evidence = append([]Evidence(nil), observations.Evidence...)
	services := []string{"confluence", "jira", "jira"}
	observations.Preferences.Services = &services
	b, _, err := BuildSuggestion(dir, observations)
	if err != nil {
		t.Fatal(err)
	}
	if a.SuggestionHash != b.SuggestionHash {
		t.Fatalf("hashes differ: %s != %s", a.SuggestionHash, b.SuggestionHash)
	}
	if !a.Candidate.Preferences.Confirmed || a.Candidate.TeamPolicy == nil || a.Candidate.TeamPolicy.Source != p.TeamPolicy.Source {
		t.Fatalf("candidate boundaries = %+v", a.Candidate)
	}
	after, err := os.ReadFile(Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("BuildSuggestion mutated profile.json")
	}
}

func TestSuggestionReviewApplyAndRejectAreHashGuarded(t *testing.T) {
	dir := t.TempDir()
	observations := observationsFor(t, dir)
	suggestion, _, err := BuildSuggestion(dir, observations)
	if err != nil {
		t.Fatal(err)
	}
	review, err := ReviewSuggestion(dir, suggestion)
	if err != nil {
		t.Fatal(err)
	}
	if review.PreviouslyRejected || review.Preview.CurrentHash != observations.BaseProfileHash {
		t.Fatalf("review = %+v", review)
	}
	if _, err := ApplySuggestion(dir, suggestion, strings.Repeat("0", 64), review.Preview.CandidateHash, review.Preview.CurrentHash); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("suggestion mismatch = %v", err)
	}

	rejected, err := RejectSuggestion(dir, suggestion, suggestion.SuggestionHash)
	if err != nil || !rejected.Changed {
		t.Fatalf("reject = %+v err=%v", rejected, err)
	}
	decisionBytes, err := os.ReadFile(decisionsPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(decisionBytes), "active-work") || strings.Contains(string(decisionBytes), "onboarding") {
		t.Fatalf("decision log retained private evidence: %s", decisionBytes)
	}
	review, err = ReviewSuggestion(dir, suggestion)
	if err != nil || !review.PreviouslyRejected {
		t.Fatalf("review after reject = %+v err=%v", review, err)
	}

	result, err := ApplySuggestion(dir, suggestion, suggestion.SuggestionHash, review.Preview.CandidateHash, review.Preview.CurrentHash)
	if err != nil || !result.Profile.Changed {
		t.Fatalf("apply = %+v err=%v", result, err)
	}
	got, exists, _, err := Read(dir)
	if err != nil || !exists || !got.Preferences.Confirmed || len(got.Schema.JiraFields) != 1 {
		t.Fatalf("profile = %+v exists=%t err=%v", got, exists, err)
	}
}

func TestSuggestionRefusesStaleBaseAndTeamPolicyObservation(t *testing.T) {
	dir := t.TempDir()
	observations := observationsFor(t, dir)
	other := Profile{SchemaVersion: 1, Preferences: Preferences{Confirmed: true, Services: []string{"jira"}}}
	preview, _ := BuildPreview(dir, other)
	if _, err := Apply(dir, other, preview.CandidateHash, preview.CurrentHash); err != nil {
		t.Fatal(err)
	}
	if _, _, err := BuildSuggestion(dir, observations); !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("stale base error = %v", err)
	}
	bad := `{"schema_version":1,"base_profile_hash":"` + strings.Repeat("0", 64) + `","team_policy":{"source":"inferred"}}`
	if _, err := DecodeObservationsStrict([]byte(bad)); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("team policy observation error = %v", err)
	}
}

func TestWriteSuggestionRequiresPrivateParentAndWrites0600(t *testing.T) {
	dir := t.TempDir()
	suggestion, _, err := BuildSuggestion(dir, observationsFor(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	publicDir := t.TempDir()
	if err := os.Chmod(publicDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteSuggestion(filepath.Join(publicDir, "x.atl-suggestion.json"), suggestion); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("public parent error = %v", err)
	}
	privateDir := t.TempDir()
	if err := os.Chmod(privateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(privateDir, "x.atl-suggestion.json")
	if err := WriteSuggestion(path, suggestion); err != nil {
		t.Fatal(err)
	}
	if err := WriteSuggestion(filepath.Join(privateDir, "profile.json"), suggestion); !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("reserved suffix error = %v", err)
	}
	link := filepath.Join(t.TempDir(), "private-link")
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dir, link); err == nil {
		if err := WriteSuggestion(filepath.Join(link, "x.atl-suggestion.json"), suggestion); err != nil {
			t.Fatalf("private symlink parent error = %v", err)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeSuggestionStrict(data)
	if err != nil || decoded.SuggestionHash != suggestion.SuggestionHash {
		t.Fatalf("decoded = %+v err=%v", decoded, err)
	}
}

func TestSuggestionFileTamperingIsRejected(t *testing.T) {
	dir := t.TempDir()
	suggestion, _, err := BuildSuggestion(dir, observationsFor(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(suggestion)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), "active-work", "other-work", 1))
	if _, err := DecodeSuggestionStrict(data); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("tamper error = %v", err)
	}
}

func TestPartialPreferenceAndRenderProposalsPreserveSiblingValues(t *testing.T) {
	dir := t.TempDir()
	p := Profile{
		SchemaVersion: 1,
		Preferences:   Preferences{Confirmed: true, Services: []string{"confluence"}, MirrorRoot: "~/.atl/keep"},
		RenderDefaults: &config.RenderConfig{
			Jira:       &config.RenderService{Profile: "default"},
			Confluence: &config.RenderService{Profile: "minimal"},
		},
	}
	hash := installProfile(t, dir, p)
	services := []string{"jira"}
	observations := Observations{
		SchemaVersion: 1, BaseProfileHash: hash,
		Preferences:    &PreferenceProposal{Services: &services},
		RenderDefaults: &config.RenderConfig{Jira: &config.RenderService{Profile: "full"}},
		Evidence: []Evidence{{
			Source: "approved workflow review", ObservedAt: time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC),
			Reason: "user confirmed Jira and full Jira rendering",
		}},
	}
	suggestion, _, err := BuildSuggestion(dir, observations)
	if err != nil {
		t.Fatal(err)
	}
	if suggestion.Candidate.Preferences.MirrorRoot != "~/.atl/keep" {
		t.Fatalf("mirror root cleared: %+v", suggestion.Candidate.Preferences)
	}
	if suggestion.Candidate.RenderDefaults.Confluence == nil || suggestion.Candidate.RenderDefaults.Confluence.Profile != "minimal" {
		t.Fatalf("Confluence render defaults cleared: %+v", suggestion.Candidate.RenderDefaults)
	}
	if suggestion.Candidate.RenderDefaults.Jira == nil || suggestion.Candidate.RenderDefaults.Jira.Profile != "full" {
		t.Fatalf("Jira render proposal not applied: %+v", suggestion.Candidate.RenderDefaults)
	}
}

func TestSuggestionAndDecisionWritersEnforceReadLimit(t *testing.T) {
	dir := t.TempDir()
	suggestion := Suggestion{
		SchemaVersion: 1, BaseProfileHash: MissingHash(), Candidate: Profile{SchemaVersion: 1},
		Evidence: []Evidence{{
			Source: "approved review", ObservedAt: time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC),
			Reason: strings.Repeat("x", MaxBytes),
		}},
	}
	hash, _, err := canonicalSuggestion(suggestion)
	if err != nil {
		t.Fatal(err)
	}
	suggestion.SuggestionHash = hash
	if err := WriteSuggestion(filepath.Join(privateOutDir(t), "oversized.atl-suggestion.json"), suggestion); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("oversized suggestion error = %v", err)
	}

	state := decisions{SchemaVersion: 1, Rejected: make([]string, 70000)}
	for i := range state.Rejected {
		state.Rejected[i] = fmt.Sprintf("%064x", i)
	}
	if err := writeDecisions(dir, state); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("oversized decisions error = %v", err)
	}
}
