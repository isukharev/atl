package capability

import "sort"

// EffectProfile is a static, informational upper-bound projection of the
// effects an executable command may have. It is catalog metadata, not an
// enforcement boundary; runtime flags and selected inputs can narrow effects.
type EffectProfile struct {
	ID               string `json:"id"`
	Summary          string `json:"summary"`
	RemoteEffect     string `json:"remote_effect"`
	LocalEffect      string `json:"local_effect"`
	CredentialAccess string `json:"credential_access"`
	NetworkBound     string `json:"network_bound"`
	ProcessEffect    string `json:"process_effect"`
	ReplayClass      string `json:"replay_class"`
	OutputKind       string `json:"output_kind"`
	LocalArtifact    string `json:"local_artifact"`
	Configuration    string `json:"configuration"`
	SelfUpdate       string `json:"self_update"`
}

// Effect profile identifiers are part of the reviewed executable-command
// registry contract. Keep the vocabulary closed and reusable rather than
// copying effect dimensions into curated capability definitions.
const (
	EffectPure                 = "pure"
	EffectGenerator            = "generator"
	EffectProse                = "prose"
	EffectConfigRead           = "config-read"
	EffectConfigWrite          = "config-write"
	EffectCredentialRead       = "credential-read"  //nolint:gosec // Public effect-profile identifier, not credential material.
	EffectCredentialWrite      = "credential-write" //nolint:gosec // Public effect-profile identifier, not credential material.
	EffectSetup                = "setup"
	EffectDiagnostic           = "diagnostic"
	EffectLocalRead            = "local-read"
	EffectLocalWrite           = "local-write"
	EffectLocalArtifact        = "local-artifact"
	EffectLocalArtifactConfig  = "local-artifact-config-read"
	EffectLocalProse           = "local-prose"
	EffectLocalOptionalWrite   = "local-read-optional-artifact"
	EffectLocalReadUpdatable   = "local-read-updatable"
	EffectLocalWriteUpdatable  = "local-write-updatable"
	EffectOptionalRemoteRead   = "optional-remote-read"
	EffectRemoteRead           = "remote-read"
	EffectRemoteReadCapped     = "remote-read-capped"
	EffectRemoteReadCaller     = "remote-read-caller-bounded"
	EffectRemoteReadFixed      = "remote-read-fixed"
	EffectRemoteReadWithLocal  = "remote-read-with-local"
	EffectGuardedCreatePreview = "guarded-create-preview"
	EffectGuardedFieldPreview  = "guarded-field-preview"
	EffectRemoteReadLocal      = "remote-read-local"
	EffectRemoteDownload       = "remote-download"
	EffectRemoteOpen           = "remote-open"
	EffectRemotePull           = "remote-pull"
	EffectRemoteWrite          = "remote-write"
	EffectRemoteWriteWithLocal = "remote-write-with-local"
	EffectRemoteWriteLocal     = "remote-write-local"
	EffectGuardedCreateApply   = "guarded-create-apply"
	EffectGuardedFieldApply    = "guarded-field-apply"
	EffectCorpusBuild          = "corpus-build"
	EffectStdioServer          = "stdio-server"
)

var effectProfiles = []EffectProfile{
	{ID: EffectConfigRead, Summary: "inspect effective configuration", RemoteEffect: "none", LocalEffect: "read", CredentialAccess: "none", NetworkBound: "none", ProcessEffect: "none", ReplayClass: "replay_safe", OutputKind: "data", LocalArtifact: "none", Configuration: "read", SelfUpdate: "disabled"},
	{ID: EffectConfigWrite, Summary: "update local configuration", RemoteEffect: "none", LocalEffect: "write", CredentialAccess: "none", NetworkBound: "none", ProcessEffect: "none", ReplayClass: "non_replay_safe", OutputKind: "data", LocalArtifact: "none", Configuration: "write", SelfUpdate: "disabled"},
	{ID: EffectCorpusBuild, Summary: "capture explicitly capped remote selections into local corpus state", RemoteEffect: "read", LocalEffect: "write", CredentialAccess: "required", NetworkBound: "caller", ProcessEffect: "none", ReplayClass: "non_replay_safe", OutputKind: "data", LocalArtifact: "required", Configuration: "read", SelfUpdate: "disabled"},
	{ID: EffectCredentialRead, Summary: "inspect credential resolution state without requiring material to exist", RemoteEffect: "none", LocalEffect: "read", CredentialAccess: "possible", NetworkBound: "none", ProcessEffect: "none", ReplayClass: "replay_safe", OutputKind: "data", LocalArtifact: "none", Configuration: "none", SelfUpdate: "disabled"},
	{ID: EffectCredentialWrite, Summary: "inspect configuration and remove credential-store state when present", RemoteEffect: "none", LocalEffect: "write", CredentialAccess: "possible", NetworkBound: "none", ProcessEffect: "none", ReplayClass: "non_replay_safe", OutputKind: "data", LocalArtifact: "none", Configuration: "read", SelfUpdate: "disabled"},
	{ID: EffectDiagnostic, Summary: "inspect common local state and optionally make fixed service probes", RemoteEffect: "read", LocalEffect: "read", CredentialAccess: "possible", NetworkBound: "fixed", ProcessEffect: "none", ReplayClass: "replay_safe", OutputKind: "data", LocalArtifact: "none", Configuration: "read", SelfUpdate: "disabled"},
	{ID: EffectGenerator, Summary: "generate static shell integration text", RemoteEffect: "none", LocalEffect: "none", CredentialAccess: "none", NetworkBound: "none", ProcessEffect: "none", ReplayClass: "replay_safe", OutputKind: "generator", LocalArtifact: "none", Configuration: "none", SelfUpdate: "disabled"},
	{ID: EffectGuardedCreateApply, Summary: "apply an exact guarded Jira create with optional local registration and no startup update", RemoteEffect: "write", LocalEffect: "write", CredentialAccess: "required", NetworkBound: "fixed", ProcessEffect: "none", ReplayClass: "non_replay_safe", OutputKind: "data", LocalArtifact: "possible", Configuration: "read", SelfUpdate: "disabled"},
	{ID: EffectGuardedCreatePreview, Summary: "preview an exact guarded Jira create with optional local qualification and no startup update", RemoteEffect: "read", LocalEffect: "read", CredentialAccess: "required", NetworkBound: "fixed", ProcessEffect: "none", ReplayClass: "replay_safe", OutputKind: "data", LocalArtifact: "none", Configuration: "read", SelfUpdate: "disabled"},
	{ID: EffectGuardedFieldApply, Summary: "apply one exact guarded Jira custom-field update from local inputs with no startup update", RemoteEffect: "write", LocalEffect: "read", CredentialAccess: "required", NetworkBound: "fixed", ProcessEffect: "none", ReplayClass: "non_replay_safe", OutputKind: "data", LocalArtifact: "none", Configuration: "read", SelfUpdate: "disabled"},
	{ID: EffectGuardedFieldPreview, Summary: "preview one exact guarded Jira custom-field update from local inputs with no startup update", RemoteEffect: "read", LocalEffect: "read", CredentialAccess: "required", NetworkBound: "fixed", ProcessEffect: "none", ReplayClass: "replay_safe", OutputKind: "data", LocalArtifact: "none", Configuration: "read", SelfUpdate: "disabled"},
	{ID: EffectLocalArtifact, Summary: "read and write a caller-selected local artifact", RemoteEffect: "none", LocalEffect: "write", CredentialAccess: "none", NetworkBound: "none", ProcessEffect: "none", ReplayClass: "non_replay_safe", OutputKind: "data", LocalArtifact: "required", Configuration: "none", SelfUpdate: "disabled"},
	{ID: EffectLocalArtifactConfig, Summary: "read and write a caller-selected local artifact after inspecting configuration", RemoteEffect: "none", LocalEffect: "write", CredentialAccess: "none", NetworkBound: "none", ProcessEffect: "none", ReplayClass: "non_replay_safe", OutputKind: "data", LocalArtifact: "required", Configuration: "read", SelfUpdate: "disabled"},
	{ID: EffectLocalProse, Summary: "read bounded local state and emit human-oriented guidance", RemoteEffect: "none", LocalEffect: "read", CredentialAccess: "none", NetworkBound: "none", ProcessEffect: "none", ReplayClass: "replay_safe", OutputKind: "prose", LocalArtifact: "none", Configuration: "none", SelfUpdate: "disabled"},
	{ID: EffectLocalRead, Summary: "inspect bounded local state", RemoteEffect: "none", LocalEffect: "read", CredentialAccess: "none", NetworkBound: "none", ProcessEffect: "none", ReplayClass: "replay_safe", OutputKind: "data", LocalArtifact: "none", Configuration: "none", SelfUpdate: "disabled"},
	{ID: EffectLocalOptionalWrite, Summary: "inspect sealed-corpus state and optionally write an owner-private identity artifact", RemoteEffect: "none", LocalEffect: "write", CredentialAccess: "none", NetworkBound: "none", ProcessEffect: "none", ReplayClass: "non_replay_safe", OutputKind: "data", LocalArtifact: "possible", Configuration: "none", SelfUpdate: "disabled"},
	{ID: EffectLocalReadUpdatable, Summary: "inspect bounded local state with startup update eligible", RemoteEffect: "none", LocalEffect: "read", CredentialAccess: "none", NetworkBound: "none", ProcessEffect: "none", ReplayClass: "replay_safe", OutputKind: "data", LocalArtifact: "none", Configuration: "read", SelfUpdate: "possible"},
	{ID: EffectLocalWrite, Summary: "update bounded local state", RemoteEffect: "none", LocalEffect: "write", CredentialAccess: "none", NetworkBound: "none", ProcessEffect: "none", ReplayClass: "non_replay_safe", OutputKind: "data", LocalArtifact: "none", Configuration: "read", SelfUpdate: "disabled"},
	{ID: EffectLocalWriteUpdatable, Summary: "update a local artifact with startup update eligible", RemoteEffect: "none", LocalEffect: "write", CredentialAccess: "none", NetworkBound: "none", ProcessEffect: "none", ReplayClass: "non_replay_safe", OutputKind: "data", LocalArtifact: "required", Configuration: "read", SelfUpdate: "possible"},
	{ID: EffectOptionalRemoteRead, Summary: "inspect local mirror state and optionally read remote drift", RemoteEffect: "read", LocalEffect: "read", CredentialAccess: "possible", NetworkBound: "unknown", ProcessEffect: "none", ReplayClass: "replay_safe", OutputKind: "data", LocalArtifact: "none", Configuration: "read", SelfUpdate: "possible"},
	{ID: EffectProse, Summary: "emit static human-oriented guidance", RemoteEffect: "none", LocalEffect: "none", CredentialAccess: "none", NetworkBound: "none", ProcessEffect: "none", ReplayClass: "replay_safe", OutputKind: "prose", LocalArtifact: "none", Configuration: "none", SelfUpdate: "disabled"},
	{ID: EffectPure, Summary: "emit static process-local data", RemoteEffect: "none", LocalEffect: "none", CredentialAccess: "none", NetworkBound: "none", ProcessEffect: "none", ReplayClass: "replay_safe", OutputKind: "data", LocalArtifact: "none", Configuration: "none", SelfUpdate: "disabled"},
	{ID: EffectRemoteDownload, Summary: "download backend content to caller-selected local output", RemoteEffect: "read", LocalEffect: "download", CredentialAccess: "required", NetworkBound: "unknown", ProcessEffect: "none", ReplayClass: "non_replay_safe", OutputKind: "data", LocalArtifact: "required", Configuration: "read", SelfUpdate: "possible"},
	{ID: EffectRemoteOpen, Summary: "resolve backend content and launch a local browser", RemoteEffect: "read", LocalEffect: "none", CredentialAccess: "required", NetworkBound: "fixed", ProcessEffect: "launch", ReplayClass: "non_replay_safe", OutputKind: "data", LocalArtifact: "none", Configuration: "read", SelfUpdate: "possible"},
	{ID: EffectRemotePull, Summary: "mirror a backend selection and optional binary assets", RemoteEffect: "read", LocalEffect: "write", CredentialAccess: "required", NetworkBound: "unknown", ProcessEffect: "none", ReplayClass: "non_replay_safe", OutputKind: "data", LocalArtifact: "required", Configuration: "read", SelfUpdate: "possible"},
	{ID: EffectRemoteRead, Summary: "read a backend selection without claiming a static request bound", RemoteEffect: "read", LocalEffect: "none", CredentialAccess: "required", NetworkBound: "unknown", ProcessEffect: "none", ReplayClass: "replay_safe", OutputKind: "data", LocalArtifact: "none", Configuration: "read", SelfUpdate: "possible"},
	{ID: EffectRemoteReadCapped, Summary: "read a backend selection behind a mandatory internal safety cap", RemoteEffect: "read", LocalEffect: "none", CredentialAccess: "required", NetworkBound: "required_internal_cap", ProcessEffect: "none", ReplayClass: "replay_safe", OutputKind: "data", LocalArtifact: "none", Configuration: "read", SelfUpdate: "possible"},
	{ID: EffectRemoteReadCaller, Summary: "read a backend selection behind a caller-supplied physical request budget", RemoteEffect: "read", LocalEffect: "none", CredentialAccess: "required", NetworkBound: "caller", ProcessEffect: "none", ReplayClass: "replay_safe", OutputKind: "data", LocalArtifact: "none", Configuration: "read", SelfUpdate: "possible"},
	{ID: EffectRemoteReadFixed, Summary: "read a backend selection with a statically fixed request plan", RemoteEffect: "read", LocalEffect: "none", CredentialAccess: "required", NetworkBound: "fixed", ProcessEffect: "none", ReplayClass: "replay_safe", OutputKind: "data", LocalArtifact: "none", Configuration: "read", SelfUpdate: "possible"},
	{ID: EffectRemoteReadLocal, Summary: "read a backend selection and produce optional local output", RemoteEffect: "read", LocalEffect: "write", CredentialAccess: "required", NetworkBound: "unknown", ProcessEffect: "none", ReplayClass: "non_replay_safe", OutputKind: "data", LocalArtifact: "possible", Configuration: "read", SelfUpdate: "possible"},
	{ID: EffectRemoteReadWithLocal, Summary: "read backend and local state without claiming a static request bound", RemoteEffect: "read", LocalEffect: "read", CredentialAccess: "required", NetworkBound: "unknown", ProcessEffect: "none", ReplayClass: "replay_safe", OutputKind: "data", LocalArtifact: "none", Configuration: "read", SelfUpdate: "possible"},
	{ID: EffectRemoteWrite, Summary: "mutate caller-selected backend targets without claiming a static request bound", RemoteEffect: "write", LocalEffect: "none", CredentialAccess: "required", NetworkBound: "unknown", ProcessEffect: "none", ReplayClass: "non_replay_safe", OutputKind: "data", LocalArtifact: "none", Configuration: "read", SelfUpdate: "possible"},
	{ID: EffectRemoteWriteWithLocal, Summary: "read caller-selected local input and mutate backend targets without claiming a static request bound", RemoteEffect: "write", LocalEffect: "read", CredentialAccess: "required", NetworkBound: "unknown", ProcessEffect: "none", ReplayClass: "non_replay_safe", OutputKind: "data", LocalArtifact: "none", Configuration: "read", SelfUpdate: "possible"},
	{ID: EffectRemoteWriteLocal, Summary: "mutate caller-selected backend and local state with possible durable artifacts without claiming a static request bound", RemoteEffect: "write", LocalEffect: "write", CredentialAccess: "required", NetworkBound: "unknown", ProcessEffect: "none", ReplayClass: "non_replay_safe", OutputKind: "data", LocalArtifact: "possible", Configuration: "read", SelfUpdate: "possible"},
	{ID: EffectSetup, Summary: "optionally configure local service credentials with interactive remote identity checks", RemoteEffect: "read", LocalEffect: "write", CredentialAccess: "possible", NetworkBound: "unknown", ProcessEffect: "none", ReplayClass: "non_replay_safe", OutputKind: "data", LocalArtifact: "none", Configuration: "write", SelfUpdate: "disabled"},
	{ID: EffectStdioServer, Summary: "serve the hard read-only CLI/MCP surface over stdio", RemoteEffect: "read", LocalEffect: "read", CredentialAccess: "possible", NetworkBound: "unknown", ProcessEffect: "none", ReplayClass: "mixed", OutputKind: "protocol", LocalArtifact: "none", Configuration: "read", SelfUpdate: "disabled"},
}

// EffectProfiles returns every canonical profile ordered by identifier.
func EffectProfiles() []EffectProfile {
	out := append([]EffectProfile(nil), effectProfiles...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// EffectProfileByID resolves one canonical profile without exposing mutable
// package-owned storage.
func EffectProfileByID(id string) (EffectProfile, bool) {
	for _, profile := range effectProfiles {
		if profile.ID == id {
			return profile, true
		}
	}
	return EffectProfile{}, false
}
