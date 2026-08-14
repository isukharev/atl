package agenteval

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"path/filepath"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/agenteval/lifecycle"
)

const (
	// HarborQualificationSchema is an evaluator-owned, content-minimized
	// descriptor for the optional Harbor substrate. It is deliberately a
	// provider-free qualification record: no Harbor or Python runtime is
	// resolved, downloaded, or executed by this slice.
	HarborQualificationSchema          = "agent-eval/harbor-qualification"
	HarborQualificationSchemaVersion   = 1
	HarborQualificationContractVersion = "0.1.0-pre-release"
	HarborQualificationMaxBytes        = 64 << 10

	harborPackage               = "harbor"
	harborVersion               = "0.20.0"
	harborSourceCommit          = "459ff6ec99417589b7f679d14ddf3b3f0ae4f1dc"
	harborSourceArchive         = "harbor-v0.20.0.tar.gz"
	harborSourceArchiveRoute    = "github_api_tarball:harbor-framework/harbor@v0.20.0"
	harborSourceArchiveSHA256   = "6a3dc4e87706e56bff3ee373f2a210ce4e7859a329ebf21759411158d1a91aa1"
	harborProjectManifest       = "pyproject.toml"
	harborProjectManifestSHA256 = "be8cbfa23e4ae1c1780d751fcdeae8c778298caff5e5fe63e8fdd3e2e65b20c0"
	harborLockfile              = "uv.lock"
	harborLockfileSHA256        = "3a2a76d7d000544dfebc91f566187750eee5a452e27899b27e4905cfa6691388"
	harborRuntimeFloor          = "python>=3.12"
	harborLicense               = "Apache-2.0"

	harborQualificationProbeSchema        = "agent-eval/harbor-qualification-probe"
	harborQualificationProbeVersion       = 1
	harborQualificationProbeKind          = "synthetic_one_attempt_failure"
	harborQualificationProbeOutcome       = "failed"
	harborQualificationProbeFailure       = "injected_failure"
	harborQualificationProbeRuntimeSafety = false
)

var ErrHarborQualification = errors.New("harbor_qualification_invalid")

// HarborQualificationIdentity records only reviewed source, package, runtime,
// and dependency-lock identities. The executable and container slots are
// explicitly unselected in this provider-free slice; accepting a runtime
// adapter requires a separate review with concrete immutable image/input
// identities.
type HarborQualificationIdentity struct {
	Package               string `json:"package"`
	Version               string `json:"version"`
	SourceCommit          string `json:"source_commit"`
	SourceArchive         string `json:"source_archive"`
	SourceArchiveRoute    string `json:"source_archive_route"`
	SourceArchiveSHA256   string `json:"source_archive_sha256"`
	ProjectManifest       string `json:"project_manifest"`
	ProjectManifestSHA256 string `json:"project_manifest_sha256"`
	Lockfile              string `json:"lockfile"`
	LockfileSHA256        string `json:"lockfile_sha256"`
	RuntimeFloor          string `json:"runtime_floor"`
	License               string `json:"license"`
	ExecutableInput       string `json:"executable_input"`
	ContainerImage        string `json:"container_image"`
}

// HarborOneAttemptPolicy is the evaluator-owned deny-by-default policy that a
// future Harbor adapter would have to satisfy. It records every relevant
// retry/cache/telemetry/upload and authority choice without granting Harbor
// execution authority.
type HarborOneAttemptPolicy struct {
	NAttempts        uint32 `json:"n_attempts"`
	FrameworkRetries uint32 `json:"framework_retries"`
	TrialRetries     uint32 `json:"trial_retries"`
	AgentRetries     uint32 `json:"agent_retries"`
	VerifierRetries  uint32 `json:"verifier_retries"`
	Cache            bool   `json:"cache"`
	Telemetry        bool   `json:"telemetry"`
	Upload           bool   `json:"upload"`
	Network          string `json:"network"`
	Credentials      string `json:"credentials"`
	PermissionPolicy string `json:"permission_policy"`
	Sandbox          string `json:"sandbox"`
	RawArtifacts     string `json:"raw_artifacts"`
	ScoringAuthority string `json:"scoring_authority"`
	Registry         string `json:"registry"`
	ResourcePolicy   string `json:"resource_policy"`
	ExecutablePolicy string `json:"executable_policy"`
	ContainerPolicy  string `json:"container_policy"`
}

// HarborSyntheticCoverage keeps missing cost/token/trace/artifact/verifier
// and lifecycle evidence explicit. The synthetic probe does not run Harbor,
// so none of these dimensions may be represented as zero or complete.
type HarborSyntheticCoverage struct {
	Cost      string `json:"cost"`
	Tokens    string `json:"tokens"`
	Trace     string `json:"trace"`
	Artifacts string `json:"artifacts"`
	Verifier  string `json:"verifier"`
	Lifecycle string `json:"lifecycle"`
}

type HarborQualificationStatus string

const (
	HarborQualificationDeferred HarborQualificationStatus = "deferred"
)

// HarborQualification binds the exact reviewed Harbor source and policy. It
// is not an adoption or runtime-safety verdict.
type HarborQualification struct {
	Schema          string                      `json:"schema"`
	SchemaVersion   int                         `json:"schema_version"`
	ContractVersion string                      `json:"contract_version"`
	Identity        HarborQualificationIdentity `json:"identity"`
	Policy          HarborOneAttemptPolicy      `json:"policy"`
	ContractSHA256  string                      `json:"contract_sha256"`
}

// HarborSyntheticAttempt is a ledger-bound provider-free failure record. It
// proves only one retained failed attempt and no replay; it does not prove
// Harbor execution, sandboxing, compatibility, or runtime safety.
type HarborSyntheticAttempt struct {
	Schema              string                    `json:"schema"`
	SchemaVersion       int                       `json:"schema_version"`
	QualificationSHA256 string                    `json:"qualification_sha256"`
	PlanSHA256          string                    `json:"plan_sha256"`
	TerminalEventSHA256 string                    `json:"terminal_event_sha256"`
	ProjectionSHA256    string                    `json:"projection_sha256"`
	Probe               string                    `json:"probe"`
	AttemptID           string                    `json:"attempt_id"`
	Outcome             string                    `json:"outcome"`
	FailureCode         string                    `json:"failure_code"`
	AttemptsStarted     uint32                    `json:"attempts_started"`
	RetryAttempts       uint32                    `json:"retry_attempts"`
	FailureRetained     bool                      `json:"failure_retained"`
	Replayable          bool                      `json:"replayable"`
	RuntimeSafetyProven bool                      `json:"runtime_safety_proven"`
	Coverage            HarborSyntheticCoverage   `json:"coverage"`
	Adoption            HarborQualificationStatus `json:"adoption"`
}

func harborPinnedIdentity() HarborQualificationIdentity {
	return HarborQualificationIdentity{
		Package:               harborPackage,
		Version:               harborVersion,
		SourceCommit:          harborSourceCommit,
		SourceArchive:         harborSourceArchive,
		SourceArchiveRoute:    harborSourceArchiveRoute,
		SourceArchiveSHA256:   harborSourceArchiveSHA256,
		ProjectManifest:       harborProjectManifest,
		ProjectManifestSHA256: harborProjectManifestSHA256,
		Lockfile:              harborLockfile,
		LockfileSHA256:        harborLockfileSHA256,
		RuntimeFloor:          harborRuntimeFloor,
		License:               harborLicense,
		ExecutableInput:       "none_selected",
		ContainerImage:        "none_selected",
	}
}

func harborPinnedPolicy() HarborOneAttemptPolicy {
	return HarborOneAttemptPolicy{
		NAttempts:        1,
		Network:          "deny",
		Credentials:      "none",
		PermissionPolicy: "evaluator_owned",
		Sandbox:          "required",
		RawArtifacts:     "owner_private",
		ScoringAuthority: "evaluator_owned",
		Registry:         "deny",
		ResourcePolicy:   "evaluator_owned",
		ExecutablePolicy: "none_selected",
		ContainerPolicy:  "none_selected",
	}
}

// PinnedHarborQualification returns the exact reviewed provider-free
// descriptor. It performs no dependency resolution and no runtime action.
func PinnedHarborQualification() (HarborQualification, error) {
	value := HarborQualification{
		Schema:          HarborQualificationSchema,
		SchemaVersion:   HarborQualificationSchemaVersion,
		ContractVersion: HarborQualificationContractVersion,
		Identity:        harborPinnedIdentity(),
		Policy:          harborPinnedPolicy(),
	}
	digest, err := harborQualificationDigest(value)
	if err != nil {
		return HarborQualification{}, err
	}
	value.ContractSHA256 = digest
	if err := value.Validate(); err != nil {
		return HarborQualification{}, err
	}
	return value, nil
}

func (value HarborQualification) Validate() error {
	if value.Schema != HarborQualificationSchema || value.SchemaVersion != HarborQualificationSchemaVersion ||
		value.ContractVersion != HarborQualificationContractVersion || value.Identity != harborPinnedIdentity() ||
		value.Policy != harborPinnedPolicy() || !validSHA256(value.ContractSHA256) {
		return harborQualificationError("shape", nil)
	}
	want, err := harborQualificationDigest(value)
	if err != nil || want != value.ContractSHA256 {
		return harborQualificationError("digest", err)
	}
	return nil
}

func HarborQualificationSHA256(value HarborQualification) (string, error) {
	if err := value.Validate(); err != nil {
		return "", err
	}
	return value.ContractSHA256, nil
}

func EncodeHarborQualification(value HarborQualification) ([]byte, error) {
	if err := value.Validate(); err != nil {
		return nil, err
	}
	return encodeHarborQualificationJSON(value)
}

func DecodeHarborQualification(reader io.Reader) (HarborQualification, error) {
	var value HarborQualification
	if err := decodeHarborQualificationJSON(reader, &value, func() error { return value.Validate() }, func() ([]byte, error) {
		return json.Marshal(value)
	}); err != nil {
		return HarborQualification{}, err
	}
	return value, nil
}

// NewHarborSyntheticFailure binds the public probe to a completed ledger
// inspection. A caller cannot substitute arbitrary plan, event, or result
// digests as evidence.
func NewHarborSyntheticFailure(qualification HarborQualification, inspection AttemptLedgerInspection) (HarborSyntheticAttempt, error) {
	if err := qualification.Validate(); err != nil {
		return HarborSyntheticAttempt{}, harborQualificationError("probe_input", err)
	}
	if err := validateHarborSyntheticInspection(qualification, inspection); err != nil {
		return HarborSyntheticAttempt{}, err
	}
	return HarborSyntheticAttempt{
		Schema:              harborQualificationProbeSchema,
		SchemaVersion:       harborQualificationProbeVersion,
		QualificationSHA256: qualification.ContractSHA256,
		PlanSHA256:          inspection.Plan.PlanSHA256,
		TerminalEventSHA256: inspection.Events[len(inspection.Events)-1].EventSHA256,
		ProjectionSHA256:    harborSyntheticProjectionSHA256(inspection.Projection),
		Probe:               harborQualificationProbeKind,
		AttemptID:           inspection.Plan.AttemptID,
		Outcome:             harborQualificationProbeOutcome,
		FailureCode:         harborQualificationProbeFailure,
		AttemptsStarted:     qualification.Policy.NAttempts,
		RetryAttempts:       0,
		FailureRetained:     true,
		Replayable:          false,
		RuntimeSafetyProven: harborQualificationProbeRuntimeSafety,
		Coverage:            harborSyntheticUnknownCoverage(),
		Adoption:            HarborQualificationDeferred,
	}, nil
}

// RunHarborSyntheticFailure records one injected pre-spawn failure in the
// append-only evaluator ledger. It does not enter Harbor, Python, a provider,
// a registry, a network, or a credential store.
func RunHarborSyntheticFailure(qualification HarborQualification, ledgerRoot string) (HarborSyntheticAttempt, AttemptLedgerInspection, error) {
	if err := qualification.Validate(); err != nil || ledgerRoot == "" {
		return HarborSyntheticAttempt{}, AttemptLedgerInspection{}, harborQualificationError("run_input", err)
	}
	store, err := CreateAttemptLedgerStore(ledgerRoot, bytes.NewReader(make([]byte, 32)))
	if err != nil {
		cause := err
		if harborQualificationLedgerConflict(err, ledgerRoot) {
			cause = errors.Join(ErrAttemptLedgerConflict, err)
		}
		return HarborSyntheticAttempt{}, AttemptLedgerInspection{}, harborQualificationError("ledger", cause)
	}
	binding, err := harborSyntheticFailureBinding(qualification)
	if err != nil {
		return HarborSyntheticAttempt{}, AttemptLedgerInspection{}, err
	}
	plan, err := allocateHarborSyntheticAttempt(store, binding)
	if err != nil {
		return HarborSyntheticAttempt{}, AttemptLedgerInspection{}, harborQualificationError("allocate", err)
	}
	session, err := NewDurableAttemptSession(store, plan)
	if err != nil {
		return HarborSyntheticAttempt{}, AttemptLedgerInspection{}, harborQualificationError("session", err)
	}
	if err := session.Commit(); err != nil {
		return HarborSyntheticAttempt{}, AttemptLedgerInspection{}, harborQualificationError("commit", err)
	}
	if err := session.FailBeforeSpawn(); err != nil {
		return HarborSyntheticAttempt{}, AttemptLedgerInspection{}, harborQualificationError("failure", err)
	}
	inspection, err := store.Inspect(plan.AttemptID)
	if err != nil || !inspection.Complete || !inspection.Projection.Terminal || inspection.Projection.State != lifecycle.StateFailed || len(inspection.Events) != 2 {
		return HarborSyntheticAttempt{}, AttemptLedgerInspection{}, harborQualificationError("inspection", err)
	}
	attempt, err := NewHarborSyntheticFailure(qualification, inspection)
	if err != nil {
		return HarborSyntheticAttempt{}, AttemptLedgerInspection{}, err
	}
	return attempt, inspection, nil
}

func harborQualificationLedgerConflict(err error, ledgerRoot string) bool {
	if errors.Is(err, ErrAttemptLedgerConflict) || errors.Is(err, ErrAttemptLedgerBusy) {
		return true
	}
	var classified interface{ Code() string }
	if !errors.As(err, &classified) || (classified.Code() != "root" &&
		(classified.Code() != "header_write" || !errors.Is(err, fs.ErrExist))) {
		return false
	}
	absRoot, absErr := filepath.Abs(ledgerRoot)
	return absErr == nil && absRoot == filepath.Clean(ledgerRoot) &&
		requirePrivateDirectory("harbor qualification ledger parent", filepath.Dir(absRoot)) == nil &&
		requirePrivateDirectory("harbor qualification ledger", absRoot) == nil
}

func allocateHarborSyntheticAttempt(store *AttemptLedgerStore, binding lifecycle.Binding) (lifecycle.Plan, error) {
	if store == nil {
		return lifecycle.Plan{}, attemptLedgerError("harbor_synthetic_store")
	}
	lock, err := store.lock()
	if err != nil {
		return lifecycle.Plan{}, err
	}
	defer func() { _ = lock.Unlock() }()
	existing, err := store.readAllLocked()
	if err != nil {
		return lifecycle.Plan{}, err
	}
	if len(existing) != 0 {
		return lifecycle.Plan{}, attemptLedgerError("harbor_synthetic_attempt_exists", ErrAttemptLedgerConflict)
	}
	return store.writePlanLocked(1, binding, "", "")
}

func harborSyntheticFailureBinding(qualification HarborQualification) (lifecycle.Binding, error) {
	digest := func(label string) (string, error) {
		return contentMinimizedAttemptDigest("harbor-synthetic-"+label, qualification.ContractSHA256)
	}
	identity := lifecycle.Identity{}
	for label, target := range map[string]*string{
		"experiment":  &identity.ExperimentSHA256,
		"task":        &identity.TaskSHA256,
		"skill":       &identity.SkillSHA256,
		"agent":       &identity.AgentSHA256,
		"model":       &identity.ModelSHA256,
		"environment": &identity.EnvironmentSHA256,
		"grader":      &identity.GraderSHA256,
		"budgets":     &identity.BudgetsSHA256,
		"adapter":     &identity.AdapterSHA256,
		"authority":   &identity.AuthoritySHA256,
	} {
		value, err := digest(label)
		if err != nil {
			return lifecycle.Binding{}, err
		}
		*target = value
	}
	return lifecycle.Binding{Privacy: lifecycle.PrivacyContentMinimized, Identity: identity}, nil
}

func (value HarborSyntheticAttempt) Validate(qualification HarborQualification) error {
	if qualification.Validate() != nil || value.Schema != harborQualificationProbeSchema ||
		value.SchemaVersion != harborQualificationProbeVersion || value.QualificationSHA256 != qualification.ContractSHA256 ||
		!validSHA256(value.PlanSHA256) || !validSHA256(value.TerminalEventSHA256) || !validSHA256(value.ProjectionSHA256) ||
		value.Probe != harborQualificationProbeKind || !validSHA256(value.AttemptID) || value.Outcome != harborQualificationProbeOutcome ||
		value.FailureCode != harborQualificationProbeFailure || value.AttemptsStarted != qualification.Policy.NAttempts || value.RetryAttempts != 0 ||
		!value.FailureRetained || value.Replayable || value.RuntimeSafetyProven || value.Coverage != harborSyntheticUnknownCoverage() ||
		value.Adoption != HarborQualificationDeferred {
		return harborQualificationError("probe", nil)
	}
	return nil
}

func EncodeHarborSyntheticAttempt(qualification HarborQualification, value HarborSyntheticAttempt, inspection AttemptLedgerInspection) ([]byte, error) {
	if err := validateHarborSyntheticAttempt(qualification, value, inspection); err != nil {
		return nil, err
	}
	return encodeHarborQualificationJSON(value)
}

func DecodeHarborSyntheticAttempt(reader io.Reader, qualification HarborQualification, inspection AttemptLedgerInspection) (HarborSyntheticAttempt, error) {
	var value HarborSyntheticAttempt
	if err := decodeHarborQualificationJSON(reader, &value, func() error {
		return validateHarborSyntheticAttempt(qualification, value, inspection)
	}, func() ([]byte, error) {
		return json.Marshal(value)
	}); err != nil {
		return HarborSyntheticAttempt{}, err
	}
	return value, nil
}

func harborSyntheticUnknownCoverage() HarborSyntheticCoverage {
	return HarborSyntheticCoverage{Cost: "unknown", Tokens: "unknown", Trace: "unknown", Artifacts: "unknown", Verifier: "unknown", Lifecycle: "unknown"}
}

func validateHarborSyntheticAttempt(qualification HarborQualification, value HarborSyntheticAttempt, inspection AttemptLedgerInspection) error {
	if err := value.Validate(qualification); err != nil {
		return err
	}
	if err := validateHarborSyntheticInspection(qualification, inspection); err != nil {
		return err
	}
	if value.AttemptID != inspection.Plan.AttemptID || value.PlanSHA256 != inspection.Plan.PlanSHA256 ||
		value.TerminalEventSHA256 != inspection.Events[len(inspection.Events)-1].EventSHA256 ||
		value.ProjectionSHA256 != harborSyntheticProjectionSHA256(inspection.Projection) {
		return harborQualificationError("probe_binding", nil)
	}
	return nil
}

func harborSyntheticProjectionSHA256(projection lifecycle.Projection) string {
	digest, _ := contentMinimizedAttemptDigest("harbor-synthetic-projection", projection)
	return digest
}

func validateHarborSyntheticInspection(qualification HarborQualification, inspection AttemptLedgerInspection) error {
	if !inspection.Complete || inspection.TailCode != "" || inspection.Plan.Ordinal != 1 || lifecycle.ValidatePlan(inspection.Plan) != nil || len(inspection.Events) != 2 {
		return harborQualificationError("probe_ledger", nil)
	}
	binding, err := harborSyntheticFailureBinding(qualification)
	if err != nil || inspection.Plan.Binding != binding {
		return harborQualificationError("probe_binding", nil)
	}
	projected, err := lifecycle.Project(inspection.Plan, inspection.Events)
	if err != nil || projected != inspection.Projection || !inspection.Projection.Terminal || inspection.Projection.State != lifecycle.StateFailed ||
		inspection.Projection.Sequence != 2 || inspection.Events[0].To != lifecycle.StateCommitted || inspection.Events[1].To != lifecycle.StateFailed ||
		inspection.Events[1].Evidence.ErrorClass != lifecycle.ErrorSpawnFailure {
		return harborQualificationError("probe_ledger", nil)
	}
	return nil
}

func harborQualificationDigest(value HarborQualification) (string, error) {
	value.ContractSHA256 = ""
	return contentMinimizedAttemptDigest("harbor-qualification", value)
}

func encodeHarborQualificationJSON(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil || len(data)+1 > HarborQualificationMaxBytes {
		return nil, harborQualificationError("encode", err)
	}
	return append(data, '\n'), nil
}

func decodeHarborQualificationJSON(reader io.Reader, target any, validate func() error, canonical func() ([]byte, error)) error {
	if reader == nil {
		return harborQualificationError("reader", nil)
	}
	limited := &io.LimitedReader{R: reader, N: HarborQualificationMaxBytes + 1}
	data, err := io.ReadAll(limited)
	if err != nil || limited.N <= 0 || len(data) < 3 || data[len(data)-1] != '\n' || bytes.IndexByte(data[:len(data)-1], '\n') >= 0 || bytes.IndexByte(data, '\r') >= 0 || !utf8.Valid(data) {
		return harborQualificationError("encoding", err)
	}
	body := data[:len(data)-1]
	if err := validateJSONNoDuplicateKeys(body); err != nil {
		return harborQualificationError("duplicate", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil || decoder.Decode(new(any)) != io.EOF {
		return harborQualificationError("decode", err)
	}
	if err := validate(); err != nil {
		return err
	}
	canonicalData, err := canonical()
	if err != nil || !bytes.Equal(canonicalData, body) {
		return harborQualificationError("canonical", err)
	}
	return nil
}

func harborQualificationError(code string, cause error) error {
	return codedError(ErrHarborQualification, code, cause)
}
