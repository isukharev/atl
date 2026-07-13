package version

import "testing"

func TestResolveBuildInfo(t *testing.T) {
	tests := []struct {
		name           string
		version        string
		commit         string
		state          string
		settings       map[string]string
		wantVersion    string
		wantCommit     string
		wantBuildState string
	}{
		{
			name:    "stamps take precedence",
			version: "1.2.3", commit: "0123456789abcdef", state: "clean",
			settings:    map[string]string{"vcs.revision": "ffffffff", "vcs.modified": "true"},
			wantVersion: "1.2.3", wantCommit: "0123456789abcdef", wantBuildState: "clean",
		},
		{
			name:    "compiler vcs fallback",
			version: "dev", commit: "unknown", state: "unknown",
			settings:    map[string]string{"vcs.revision": "abcdef0123456789", "vcs.modified": "true"},
			wantVersion: "dev", wantCommit: "abcdef0123456789", wantBuildState: "dirty",
		},
		{
			name:        "clean compiler fallback",
			settings:    map[string]string{"vcs.revision": "abcdef", "vcs.modified": "false"},
			wantVersion: "dev", wantCommit: "abcdef", wantBuildState: "clean",
		},
		{
			name:    "unknown values remain explicit",
			version: " ", commit: "(devel)", state: "unexpected",
			settings:    map[string]string{"vcs.modified": "not-a-bool"},
			wantVersion: "dev", wantCommit: "unknown", wantBuildState: "unknown",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveBuildInfo(tt.version, tt.commit, tt.state, tt.settings)
			if got.Version != tt.wantVersion || got.Commit != tt.wantCommit || got.BuildState != tt.wantBuildState {
				t.Fatalf("resolveBuildInfo() = %+v, want version=%q commit=%q build_state=%q", got, tt.wantVersion, tt.wantCommit, tt.wantBuildState)
			}
		})
	}
}
