package capability

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestEffectProfilesAreClosedSortedAndUnique(t *testing.T) {
	profiles := EffectProfiles()
	if len(profiles) == 0 {
		t.Fatal("effect profiles are empty")
	}
	remote := map[string]bool{"none": true, "read": true, "write": true}
	local := map[string]bool{"none": true, "read": true, "write": true, "download": true}
	credentials := map[string]bool{"none": true, "possible": true, "required": true}
	bound := map[string]bool{"none": true, "fixed": true, "caller": true, "required_internal_cap": true, "unknown": true}
	process := map[string]bool{"none": true, "launch": true}
	replay := map[string]bool{"replay_safe": true, "non_replay_safe": true, "mixed": true}
	output := map[string]bool{"data": true, "generator": true, "prose": true, "protocol": true}
	artifact := map[string]bool{"none": true, "possible": true, "required": true}
	configuration := map[string]bool{"none": true, "read": true, "write": true}
	selfUpdate := map[string]bool{"disabled": true, "possible": true}
	for i, profile := range profiles {
		if profile.ID == "" || profile.Summary == "" || i > 0 && profiles[i-1].ID >= profile.ID {
			t.Fatalf("profiles are empty, duplicated, or unsorted at %+v", profile)
		}
		if !remote[profile.RemoteEffect] || !local[profile.LocalEffect] || !credentials[profile.CredentialAccess] ||
			!bound[profile.NetworkBound] || !process[profile.ProcessEffect] || !replay[profile.ReplayClass] ||
			!output[profile.OutputKind] || !artifact[profile.LocalArtifact] || !configuration[profile.Configuration] || !selfUpdate[profile.SelfUpdate] {
			t.Fatalf("profile has open vocabulary: %+v", profile)
		}
		if got, ok := EffectProfileByID(profile.ID); !ok || !reflect.DeepEqual(got, profile) {
			t.Fatalf("lookup(%q)=%+v/%t want=%+v", profile.ID, got, ok, profile)
		}
	}
	profiles[0].ID = "mutated"
	if again := EffectProfiles(); again[0].ID == "mutated" {
		t.Fatal("EffectProfiles exposes package-owned storage")
	}
	if _, ok := EffectProfileByID("unknown"); ok {
		t.Fatal("unknown effect profile resolved")
	}
}

func TestEffectProfileWireUsesExactProcessEffectField(t *testing.T) {
	profile, ok := EffectProfileByID(EffectStdioServer)
	if !ok {
		t.Fatal("stdio profile is missing")
	}
	body, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["process_effect"] != "none" {
		t.Fatalf("process_effect=%v", fields["process_effect"])
	}
	if _, exists := fields["process_launch"]; exists {
		t.Fatalf("deprecated process_launch field is present: %s", body)
	}
}

func TestOptionalRemoteReadIsOneCanonicalUnionProfile(t *testing.T) {
	want := EffectProfile{
		ID:               EffectOptionalRemoteRead,
		Summary:          "inspect local mirror state and optionally read remote drift",
		RemoteEffect:     "read",
		LocalEffect:      "read",
		CredentialAccess: "possible",
		NetworkBound:     "unknown",
		ProcessEffect:    "none",
		ReplayClass:      "replay_safe",
		OutputKind:       "data",
		LocalArtifact:    "none",
		Configuration:    "read",
		SelfUpdate:       "possible",
	}
	got, ok := EffectProfileByID(EffectOptionalRemoteRead)
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("optional remote profile=%+v/%t want=%+v", got, ok, want)
	}

	matchingDimensions := 0
	matchingID := 0
	for _, profile := range EffectProfiles() {
		if profile.ID == want.ID {
			matchingID++
		}
		profile.ID, profile.Summary = want.ID, want.Summary
		if reflect.DeepEqual(profile, want) {
			matchingDimensions++
		}
	}
	if matchingID != 1 || matchingDimensions != 1 {
		t.Fatalf("optional remote profile id count=%d dimension count=%d, want 1/1", matchingID, matchingDimensions)
	}
}

func TestReviewedMutationProfilesClassifyConfigurationAndArtifacts(t *testing.T) {
	tests := []struct {
		id            string
		configuration string
		artifact      string
	}{
		{id: EffectCredentialWrite, configuration: "read", artifact: "none"},
		{id: EffectLocalArtifact, configuration: "none", artifact: "required"},
		{id: EffectLocalArtifactConfig, configuration: "read", artifact: "required"},
		{id: EffectLocalOptionalWrite, configuration: "none", artifact: "possible"},
		{id: EffectRemoteWriteLocal, configuration: "read", artifact: "possible"},
	}
	for _, test := range tests {
		t.Run(test.id, func(t *testing.T) {
			profile, ok := EffectProfileByID(test.id)
			if !ok || profile.Configuration != test.configuration || profile.LocalArtifact != test.artifact {
				t.Fatalf("profile=%+v/%t want configuration=%q local_artifact=%q", profile, ok, test.configuration, test.artifact)
			}
		})
	}
}
