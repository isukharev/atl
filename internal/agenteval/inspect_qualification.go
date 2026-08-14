package agenteval

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/agenteval/lifecycle"
)

const (
	// InspectQualificationSchema is an evaluator-owned, content-minimized
	// preflight contract for the optional Inspect AI adapter. It is deliberately
	// separate from the built-in provider registry: this slice qualifies a
	// pinned external substrate without making it a required dependency or an
	// execution authority.
	InspectQualificationSchema          = "agent-eval/inspect-ai-qualification"
	InspectQualificationSchemaVersion   = 1
	InspectQualificationContractVersion = "0.1.0-pre-release"
	InspectQualificationMaxBytes        = 64 << 10

	inspectAIPackage                       = "inspect-ai"
	inspectAIVersion                       = "0.3.252"
	inspectAISourceCommit                  = "d105c61478c3fc86ff87d79b355c020869ee6a9b"
	inspectAIWheelFilename                 = "inspect_ai-0.3.252-py3-none-any.whl"
	inspectAIWheelSHA256                   = "3be38f02d303b433e80e003c5181e609ad56594f45169b3709523781cdcd2ebc"
	inspectAISourceArchiveFilename         = "inspect_ai-0.3.252.tar.gz"
	inspectAISourceArchiveSHA256           = "9e5abaaf7930a57c2d0d593a2123c60cd7300eeec7602a3a00bcfaa9e5efd820"
	inspectAIRuntimeFloor                  = "python>=3.10"
	inspectAILicense                       = "MIT License"
	inspectQualificationProbeSchema        = "agent-eval/inspect-ai-qualification-probe"
	inspectQualificationProbeVersion       = 1
	inspectQualificationProbeKind          = "synthetic_one_attempt_failure"
	inspectQualificationProbeOutcome       = "failed"
	inspectQualificationProbeFailure       = "injected_failure"
	inspectQualificationProbeRuntimeSafety = false
)

var ErrInspectQualification = errors.New("inspect_qualification_invalid")

// InspectQualificationIdentity records only reviewed source/package/runtime
// identities. It intentionally contains no URL, local path, environment
// value, credential, prompt, or provider configuration.
type InspectQualificationIdentity struct {
	Package               string `json:"package"`
	Version               string `json:"version"`
	SourceCommit          string `json:"source_commit"`
	WheelFilename         string `json:"wheel_filename"`
	WheelSHA256           string `json:"wheel_sha256"`
	SourceArchiveFilename string `json:"source_archive_filename"`
	SourceArchiveSHA256   string `json:"source_archive_sha256"`
	RuntimeFloor          string `json:"runtime_floor"`
	License               string `json:"license"`
}

// InspectOneAttemptPolicy is the evaluator-owned policy that must be bound
// before an optional Inspect trial is admitted. A nonzero retry layer, cache,
// telemetry, upload, network, or credential access is a qualification failure.
type InspectOneAttemptPolicy struct {
	FrameworkRetries uint32 `json:"framework_retries"`
	EvalSetRetries   uint32 `json:"eval_set_retries"`
	TaskRetries      uint32 `json:"task_retries"`
	ModelRetries     uint32 `json:"model_retries"`
	Cache            bool   `json:"cache"`
	Telemetry        bool   `json:"telemetry"`
	Upload           bool   `json:"upload"`
	Network          string `json:"network"`
	Credentials      string `json:"credentials"`
	PermissionPolicy string `json:"permission_policy"`
	Sandbox          string `json:"sandbox"`
	RawArtifacts     string `json:"raw_artifacts"`
	Projection       string `json:"projection"`
	ScoringAuthority string `json:"scoring_authority"`
}

// InspectSyntheticCoverage makes absent evidence explicit. The provider-free
// probe does not observe usage, traces, artifacts, or scorer output; each
// dimension therefore remains unknown rather than being silently treated as
// zero or complete.
type InspectSyntheticCoverage struct {
	Usage     string `json:"usage"`
	Trace     string `json:"trace"`
	Artifacts string `json:"artifacts"`
	Scoring   string `json:"scoring"`
}

// InspectQualification binds the exact reviewed Inspect identity and the
// no-replay policy. ContractSHA256 is over the same value with that field
// empty, so callers can verify the binding before any external entry point.
type InspectQualification struct {
	Schema          string                       `json:"schema"`
	SchemaVersion   int                          `json:"schema_version"`
	ContractVersion string                       `json:"contract_version"`
	Identity        InspectQualificationIdentity `json:"identity"`
	Policy          InspectOneAttemptPolicy      `json:"policy"`
	ContractSHA256  string                       `json:"contract_sha256"`
}

// InspectQualificationStatus is intentionally not an adoption verdict. The
// offline contract and synthetic probe are evidence inputs for a later,
// separately reviewed adoption decision.
type InspectQualificationStatus string

const (
	InspectQualificationDeferred InspectQualificationStatus = "deferred"
)

// InspectSyntheticAttempt is a content-free provider-free probe result. It
// proves only the one-attempt failure/replay policy; it does not claim that
// Inspect or an agent runtime was executed, isolated, or adopted.
type InspectSyntheticAttempt struct {
	Schema              string                     `json:"schema"`
	SchemaVersion       int                        `json:"schema_version"`
	QualificationSHA256 string                     `json:"qualification_sha256"`
	PlanSHA256          string                     `json:"plan_sha256"`
	TerminalEventSHA256 string                     `json:"terminal_event_sha256"`
	ProjectionSHA256    string                     `json:"projection_sha256"`
	Probe               string                     `json:"probe"`
	AttemptID           string                     `json:"attempt_id"`
	Outcome             string                     `json:"outcome"`
	FailureCode         string                     `json:"failure_code"`
	AttemptsStarted     uint32                     `json:"attempts_started"`
	RetryAttempts       uint32                     `json:"retry_attempts"`
	FailureRetained     bool                       `json:"failure_retained"`
	Replayable          bool                       `json:"replayable"`
	RuntimeSafetyProven bool                       `json:"runtime_safety_proven"`
	Coverage            InspectSyntheticCoverage   `json:"coverage"`
	Adoption            InspectQualificationStatus `json:"adoption"`
}

// PinnedInspectQualification returns the exact reviewed Inspect AI identity
// and the only policy admitted by this offline slice. No dependency is
// resolved and no process, provider, network, or credential is touched.
func PinnedInspectQualification() (InspectQualification, error) {
	value := InspectQualification{
		Schema:          InspectQualificationSchema,
		SchemaVersion:   InspectQualificationSchemaVersion,
		ContractVersion: InspectQualificationContractVersion,
		Identity: InspectQualificationIdentity{
			Package:               inspectAIPackage,
			Version:               inspectAIVersion,
			SourceCommit:          inspectAISourceCommit,
			WheelFilename:         inspectAIWheelFilename,
			WheelSHA256:           inspectAIWheelSHA256,
			SourceArchiveFilename: inspectAISourceArchiveFilename,
			SourceArchiveSHA256:   inspectAISourceArchiveSHA256,
			RuntimeFloor:          inspectAIRuntimeFloor,
			License:               inspectAILicense,
		},
		Policy: InspectOneAttemptPolicy{
			FrameworkRetries: 0,
			EvalSetRetries:   0,
			TaskRetries:      0,
			ModelRetries:     0,
			Cache:            false,
			Telemetry:        false,
			Upload:           false,
			Network:          "deny",
			Credentials:      "none",
			PermissionPolicy: "evaluator_owned",
			Sandbox:          "required",
			RawArtifacts:     "owner_private",
			Projection:       "content_minimized",
			ScoringAuthority: "evaluator_owned",
		},
	}
	digest, err := inspectQualificationDigest(value)
	if err != nil {
		return InspectQualification{}, err
	}
	value.ContractSHA256 = digest
	if err := value.Validate(); err != nil {
		return InspectQualification{}, err
	}
	return value, nil
}

func (value InspectQualification) Validate() error {
	if value.Schema != InspectQualificationSchema || value.SchemaVersion != InspectQualificationSchemaVersion ||
		value.ContractVersion != InspectQualificationContractVersion ||
		value.Identity != (InspectQualificationIdentity{
			Package: inspectAIPackage, Version: inspectAIVersion, SourceCommit: inspectAISourceCommit,
			WheelFilename: inspectAIWheelFilename, WheelSHA256: inspectAIWheelSHA256,
			SourceArchiveFilename: inspectAISourceArchiveFilename, SourceArchiveSHA256: inspectAISourceArchiveSHA256,
			RuntimeFloor: inspectAIRuntimeFloor, License: inspectAILicense,
		}) || value.Policy != (InspectOneAttemptPolicy{
		FrameworkRetries: 0, EvalSetRetries: 0, TaskRetries: 0, ModelRetries: 0,
		Network: "deny", Credentials: "none", PermissionPolicy: "evaluator_owned",
		Sandbox: "required", RawArtifacts: "owner_private", Projection: "content_minimized",
		ScoringAuthority: "evaluator_owned",
	}) || !validSHA256(value.ContractSHA256) {
		return inspectQualificationError("shape", nil)
	}
	want, err := inspectQualificationDigest(value)
	if err != nil || want != value.ContractSHA256 {
		return inspectQualificationError("digest", err)
	}
	return nil
}

func InspectQualificationSHA256(value InspectQualification) (string, error) {
	if err := value.Validate(); err != nil {
		return "", err
	}
	return value.ContractSHA256, nil
}

func EncodeInspectQualification(value InspectQualification) ([]byte, error) {
	if err := value.Validate(); err != nil {
		return nil, err
	}
	return encodeInspectQualificationJSON(value)
}

func DecodeInspectQualification(reader io.Reader) (InspectQualification, error) {
	var value InspectQualification
	if err := decodeInspectQualificationJSON(reader, &value, func() error { return value.Validate() }, func() ([]byte, error) {
		return json.Marshal(value)
	}); err != nil {
		return InspectQualification{}, err
	}
	return value, nil
}

// NewInspectSyntheticFailure creates the fixed provider-free failure probe
// from a completed ledger inspection. Requiring the inspection keeps the
// public projection bound to a plan and terminal event instead of accepting a
// caller-supplied digest as proof that an attempt was persisted.
func NewInspectSyntheticFailure(qualification InspectQualification, inspection AttemptLedgerInspection) (InspectSyntheticAttempt, error) {
	if err := qualification.Validate(); err != nil {
		return InspectSyntheticAttempt{}, inspectQualificationError("probe_input", err)
	}
	if err := validateInspectSyntheticInspection(qualification, inspection); err != nil {
		return InspectSyntheticAttempt{}, err
	}
	return InspectSyntheticAttempt{
		Schema:              inspectQualificationProbeSchema,
		SchemaVersion:       inspectQualificationProbeVersion,
		QualificationSHA256: qualification.ContractSHA256,
		PlanSHA256:          inspection.Plan.PlanSHA256,
		TerminalEventSHA256: inspection.Events[len(inspection.Events)-1].EventSHA256,
		ProjectionSHA256:    inspectSyntheticProjectionSHA256(inspection.Projection),
		Probe:               inspectQualificationProbeKind,
		AttemptID:           inspection.Plan.AttemptID,
		Outcome:             inspectQualificationProbeOutcome,
		FailureCode:         inspectQualificationProbeFailure,
		AttemptsStarted:     1,
		RetryAttempts:       0,
		FailureRetained:     true,
		Replayable:          false,
		RuntimeSafetyProven: inspectQualificationProbeRuntimeSafety,
		Coverage:            inspectSyntheticUnknownCoverage(),
		Adoption:            InspectQualificationDeferred,
	}, nil
}

// RunInspectSyntheticFailure records the fixed injected failure in the
// evaluator's append-only attempt ledger without entering an Inspect process.
// The returned inspection is the source of the failed/terminal claims in the
// content-minimized projection; callers cannot substitute a successful or
// replayable result by editing the projection alone.
func RunInspectSyntheticFailure(qualification InspectQualification, ledgerRoot string) (InspectSyntheticAttempt, AttemptLedgerInspection, error) {
	if err := qualification.Validate(); err != nil || ledgerRoot == "" {
		return InspectSyntheticAttempt{}, AttemptLedgerInspection{}, inspectQualificationError("run_input", err)
	}
	// The fixed nonce is intentional: this is a provider-free synthetic probe,
	// so equal qualification inputs produce equal content-minimized identities.
	store, err := CreateAttemptLedgerStore(ledgerRoot, bytes.NewReader(make([]byte, 32)))
	if err != nil {
		cause := err
		// A concurrent creator can hold the ledger lock while its header is
		// being initialized. Treat that bounded single-use contention as a
		// conflict too: the probe must fail closed rather than expose a
		// scheduler-dependent busy classification.
		if errors.Is(err, ErrAttemptLedgerBusy) {
			cause = errors.Join(ErrAttemptLedgerConflict, err)
		}
		return InspectSyntheticAttempt{}, AttemptLedgerInspection{}, inspectQualificationError("ledger", cause)
	}
	binding, err := inspectSyntheticFailureBinding(qualification)
	if err != nil {
		return InspectSyntheticAttempt{}, AttemptLedgerInspection{}, err
	}
	plan, err := allocateInspectSyntheticAttempt(store, binding)
	if err != nil {
		return InspectSyntheticAttempt{}, AttemptLedgerInspection{}, inspectQualificationError("allocate", err)
	}
	session, err := NewDurableAttemptSession(store, plan)
	if err != nil {
		return InspectSyntheticAttempt{}, AttemptLedgerInspection{}, inspectQualificationError("session", err)
	}
	if err := session.Commit(); err != nil {
		return InspectSyntheticAttempt{}, AttemptLedgerInspection{}, inspectQualificationError("commit", err)
	}
	// The injected failure is a pre-spawn failure: this proves one retained
	// failed attempt while deliberately exercising no external runtime.
	if err := session.FailBeforeSpawn(); err != nil {
		return InspectSyntheticAttempt{}, AttemptLedgerInspection{}, inspectQualificationError("failure", err)
	}
	inspection, err := store.Inspect(plan.AttemptID)
	if err != nil || !inspection.Projection.Terminal || inspection.Projection.State != lifecycle.StateFailed || len(inspection.Events) != 2 {
		return InspectSyntheticAttempt{}, AttemptLedgerInspection{}, inspectQualificationError("inspection", err)
	}
	attempt, err := NewInspectSyntheticFailure(qualification, inspection)
	if err != nil {
		return InspectSyntheticAttempt{}, AttemptLedgerInspection{}, err
	}
	return attempt, inspection, nil
}

// allocateInspectSyntheticAttempt reserves the only synthetic attempt while
// holding the ledger's existing write lock.  Checking the ledger before
// Allocate would leave a check-then-allocate race: two callers could both
// observe an empty ledger and append a second terminal probe.  This helper is
// intentionally local to the qualification slice; it does not change the
// general-purpose ledger allocation policy used by real evaluations.
func allocateInspectSyntheticAttempt(store *AttemptLedgerStore, binding lifecycle.Binding) (lifecycle.Plan, error) {
	if store == nil {
		return lifecycle.Plan{}, attemptLedgerError("synthetic_store")
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
		return lifecycle.Plan{}, attemptLedgerError("synthetic_attempt_exists", ErrAttemptLedgerConflict)
	}
	return store.writePlanLocked(1, binding, "", "")
}

func inspectSyntheticFailureBinding(qualification InspectQualification) (lifecycle.Binding, error) {
	digest := func(label string) (string, error) {
		return contentMinimizedAttemptDigest("inspect-synthetic-"+label, qualification.ContractSHA256)
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

func (value InspectSyntheticAttempt) Validate(qualification InspectQualification) error {
	if qualification.Validate() != nil || value.Schema != inspectQualificationProbeSchema ||
		value.SchemaVersion != inspectQualificationProbeVersion || value.QualificationSHA256 != qualification.ContractSHA256 ||
		!validSHA256(value.PlanSHA256) || !validSHA256(value.TerminalEventSHA256) || !validSHA256(value.ProjectionSHA256) || value.Probe != inspectQualificationProbeKind || !validSHA256(value.AttemptID) ||
		value.Outcome != inspectQualificationProbeOutcome || value.FailureCode != inspectQualificationProbeFailure ||
		value.AttemptsStarted != 1 || value.RetryAttempts != 0 || !value.FailureRetained || value.Replayable ||
		value.RuntimeSafetyProven || value.Coverage != inspectSyntheticUnknownCoverage() || value.Adoption != InspectQualificationDeferred {
		return inspectQualificationError("probe", nil)
	}
	return nil
}

func EncodeInspectSyntheticAttempt(qualification InspectQualification, value InspectSyntheticAttempt, inspection AttemptLedgerInspection) ([]byte, error) {
	if err := validateInspectSyntheticAttempt(qualification, value, inspection); err != nil {
		return nil, err
	}
	return encodeInspectQualificationJSON(value)
}

func DecodeInspectSyntheticAttempt(reader io.Reader, qualification InspectQualification, inspection AttemptLedgerInspection) (InspectSyntheticAttempt, error) {
	var value InspectSyntheticAttempt
	if err := decodeInspectQualificationJSON(reader, &value, func() error { return validateInspectSyntheticAttempt(qualification, value, inspection) }, func() ([]byte, error) {
		return json.Marshal(value)
	}); err != nil {
		return InspectSyntheticAttempt{}, err
	}
	return value, nil
}

func inspectSyntheticUnknownCoverage() InspectSyntheticCoverage {
	return InspectSyntheticCoverage{Usage: "unknown", Trace: "unknown", Artifacts: "unknown", Scoring: "unknown"}
}

func validateInspectSyntheticAttempt(qualification InspectQualification, value InspectSyntheticAttempt, inspection AttemptLedgerInspection) error {
	if err := value.Validate(qualification); err != nil {
		return err
	}
	if err := validateInspectSyntheticInspection(qualification, inspection); err != nil {
		return err
	}
	if value.AttemptID != inspection.Plan.AttemptID || value.PlanSHA256 != inspection.Plan.PlanSHA256 ||
		value.TerminalEventSHA256 != inspection.Events[len(inspection.Events)-1].EventSHA256 ||
		value.ProjectionSHA256 != inspectSyntheticProjectionSHA256(inspection.Projection) {
		return inspectQualificationError("probe_binding", nil)
	}
	return nil
}

func inspectSyntheticProjectionSHA256(projection lifecycle.Projection) string {
	digest, _ := contentMinimizedAttemptDigest("inspect-synthetic-projection", projection)
	return digest
}

func validateInspectSyntheticInspection(qualification InspectQualification, inspection AttemptLedgerInspection) error {
	if !inspection.Complete || inspection.TailCode != "" || inspection.Plan.Ordinal != 1 ||
		lifecycle.ValidatePlan(inspection.Plan) != nil || len(inspection.Events) != 2 {
		return inspectQualificationError("probe_ledger", nil)
	}
	binding, err := inspectSyntheticFailureBinding(qualification)
	if err != nil || inspection.Plan.Binding != binding {
		return inspectQualificationError("probe_binding", nil)
	}
	projected, err := lifecycle.Project(inspection.Plan, inspection.Events)
	if err != nil || projected != inspection.Projection || !inspection.Projection.Terminal ||
		inspection.Projection.State != lifecycle.StateFailed || inspection.Projection.Sequence != 2 ||
		inspection.Events[0].To != lifecycle.StateCommitted || inspection.Events[1].To != lifecycle.StateFailed ||
		inspection.Events[1].Evidence.ErrorClass != lifecycle.ErrorSpawnFailure {
		return inspectQualificationError("probe_ledger", nil)
	}
	return nil
}

func inspectQualificationDigest(value InspectQualification) (string, error) {
	value.ContractSHA256 = ""
	return contentMinimizedAttemptDigest("inspect-qualification", value)
}

func encodeInspectQualificationJSON(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil || len(data)+1 > InspectQualificationMaxBytes {
		return nil, inspectQualificationError("encode", err)
	}
	return append(data, '\n'), nil
}

func decodeInspectQualificationJSON(reader io.Reader, target any, validate func() error, canonical func() ([]byte, error)) error {
	if reader == nil {
		return inspectQualificationError("reader", nil)
	}
	limited := &io.LimitedReader{R: reader, N: InspectQualificationMaxBytes + 1}
	data, err := io.ReadAll(limited)
	if err != nil || limited.N <= 0 || len(data) < 3 || data[len(data)-1] != '\n' ||
		bytes.IndexByte(data[:len(data)-1], '\n') >= 0 || bytes.IndexByte(data, '\r') >= 0 || !utf8.Valid(data) {
		return inspectQualificationError("encoding", err)
	}
	body := data[:len(data)-1]
	if err := validateJSONNoDuplicateKeys(body); err != nil {
		return inspectQualificationError("duplicate", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil || decoder.Decode(new(any)) != io.EOF {
		return inspectQualificationError("decode", err)
	}
	if err := validate(); err != nil {
		return err
	}
	canonicalData, err := canonical()
	if err != nil || !bytes.Equal(canonicalData, body) {
		return inspectQualificationError("canonical", err)
	}
	return nil
}

func inspectQualificationError(code string, cause error) error {
	// Never render parser, filesystem, or caller-controlled field text. The
	// stable sentinel and closed reason are sufficient for callers and keep the
	// public qualification boundary content-minimized. Retaining the cause in
	// the coded error's unwrap tree preserves errors.Is/errors.As without
	// exposing cause text through Error().
	return codedError(ErrInspectQualification, code, cause)
}
