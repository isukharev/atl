package agenteval

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/isukharev/atl/internal/agenteval/core"
	"github.com/isukharev/atl/internal/agenteval/extension"
	profileatl "github.com/isukharev/atl/internal/agenteval/profile/atl"
)

// newATLCoreProfile composes the neutral profile from the pinned evaluator-
// owned ATL capability projection. Product routing stays in its existing
// owners; the profile receives only closed capability identities.
func newATLCoreProfile(factory profileatl.RuntimeFactory) (*profileatl.Profile, error) {
	extensionDescriptor := (atlBuiltInExtensionProfile{}).Capabilities()
	if err := extension.ValidateDescriptor(extensionDescriptor); err != nil {
		return nil, fmt.Errorf("compose ATL extension profile policy: %w", err)
	}
	catalog, err := PinnedCapabilityCatalog()
	if err != nil {
		return nil, err
	}
	capabilities := make([]core.Capability, len(catalog.Capabilities))
	for index, capability := range catalog.Capabilities {
		capabilities[index] = core.Capability{
			ID:      core.CapabilityID(capability.ID),
			Support: core.SupportSupported,
		}
	}
	profile := profileatl.New(capabilities, factory)
	if string(profile.Descriptor().ID) != extensionDescriptor.ID {
		return nil, fmt.Errorf("compose ATL extension profile identity")
	}
	return profile, nil
}

// atlBuiltInExtensionProfile is the in-process implementation of the shared
// profile role vocabulary. Capabilities returns the structural role contract;
// Validate routes the profile.validate operation through the same neutral-core
// admission used by RunHeadless. It is not a process manifest or registry.
type atlBuiltInExtensionProfile struct{}

// atlExtensionProfileDescriptor projects the built-in in-process ATL profile
// through the same closed role/operation vocabulary as external components.
// It intentionally has no executable manifest: selected-binary qualification
// and product routing remain in their existing compatibility owners.
func atlExtensionProfileDescriptor() extension.Descriptor {
	return (atlBuiltInExtensionProfile{}).Capabilities()
}

func (atlBuiltInExtensionProfile) Capabilities() extension.Descriptor {
	operations := extension.OperationsForRole(extension.RoleProfile)
	capabilities := make([]extension.CapabilityClaim, len(operations))
	for index, operation := range operations {
		capabilities[index] = extension.CapabilityClaim{
			ID: extension.CapabilityFor(extension.RoleProfile, operation), State: extension.CapabilitySupported,
		}
	}
	return extension.Descriptor{
		ID: string(profileatl.ProfileID), Version: "1", Role: extension.RoleProfile,
		Operations: operations, Capabilities: capabilities,
	}
}

// projectATLRunContract converts an already strictly decoded and validated ATL
// contract into in-memory neutral identities. Durable source bytes remain
// owned by the compatibility facade and are never reconstructed from this
// projection.
func projectATLRunContract(contract resolvedRunContract) (core.Plan, error) {
	specBytes, err := json.Marshal(contract.spec)
	if err != nil {
		return core.Plan{}, fmt.Errorf("project ATL run spec")
	}
	scenarioBytes, err := json.Marshal(contract.scenario)
	if err != nil {
		return core.Plan{}, fmt.Errorf("project ATL scenario")
	}
	rubricBytes, err := json.Marshal(contract.rubric)
	if err != nil {
		return core.Plan{}, fmt.Errorf("project ATL rubric: %w", err)
	}
	fixtureBytes, err := json.Marshal(contract.fixture)
	if err != nil {
		return core.Plan{}, fmt.Errorf("project ATL fixture: %w", err)
	}
	workspaceDigest, err := digestWorkspaceTree(contract.workspaceTemplate)
	if err != nil {
		return core.Plan{}, fmt.Errorf("project ATL workspace: %w", err)
	}

	checks := make([]core.Check, len(contract.scenario.RequiredChecks))
	for index, check := range contract.scenario.RequiredChecks {
		checks[index] = core.Check{ID: core.CheckID(check), Weight: 1}
	}
	capabilities := make([]core.CapabilityID, len(contract.scenario.RequiredCapabilities))
	for index, capability := range contract.scenario.RequiredCapabilities {
		capabilities[index] = core.CapabilityID(capability)
	}
	resources := make([]core.ResourceID, len(contract.scenario.RequiredMetrics))
	for index, resource := range contract.scenario.RequiredMetrics {
		resources[index] = core.ResourceID(resource)
	}

	fixtureIdentity := atlCoreIdentity(
		"fixture",
		contract.prompt,
		contract.providerPrompt,
		contract.responseSchema,
		rubricBytes,
		fixtureBytes,
		[]byte(workspaceDigest),
	)
	return core.Plan{
		ID:       core.PlanID(atlCoreIdentity("plan", scenarioBytes, specBytes, []byte(fixtureIdentity))),
		Profile:  profileatl.ProfileID,
		Attempts: uint32(contract.spec.Repetitions), // #nosec G115 -- strict RunSpec validation bounds repetitions to 1..20 before projection.
		Task: core.Task{
			ID:                   core.TaskID(contract.scenario.ID),
			RequiredCapabilities: capabilities,
			Checks:               checks,
			Resources:            resources,
		},
		Fixture:   core.Fixture{ID: core.FixtureID(fixtureIdentity)},
		Treatment: core.Treatment{ID: core.TreatmentID(atlCoreIdentity("treatment", specBytes))},
	}, nil
}

func admitATLCoreRunContract(contract resolvedRunContract) error {
	return (atlBuiltInExtensionProfile{}).Validate(contract)
}

func (atlBuiltInExtensionProfile) Validate(contract resolvedRunContract) error {
	plan, err := projectATLRunContract(contract)
	if err != nil {
		return err
	}
	profile, err := newATLCoreProfile(nil)
	if err != nil {
		return err
	}
	registry, err := core.NewRegistry(profile)
	if err != nil {
		return fmt.Errorf("compose ATL core profile: %w", err)
	}
	engine, err := core.NewEngine(registry)
	if err != nil {
		return fmt.Errorf("compose ATL core engine: %w", err)
	}
	if _, err := engine.Admit(plan); err != nil {
		return fmt.Errorf("admit ATL run contract: %w", err)
	}
	return nil
}

func atlCoreResultStatus(eligibility string, passed bool) (string, error) {
	assessment, err := assessATLCoreResult(eligibility, passed)
	if err != nil {
		return "", fmt.Errorf("assess ATL result: %w", err)
	}
	switch assessment.Outcome {
	case core.OutcomeSucceeded:
		return "pass", nil
	case core.OutcomeFailed:
		return "fail", nil
	case core.OutcomeNotApplicable:
		return "ineligible", nil
	default:
		return "", fmt.Errorf("assess ATL result: unexpected core outcome")
	}
}

func summarizeATLCoreResults(results []Result) (core.Aggregate, error) {
	assessments := make([]core.Assessment, 0, len(results))
	for _, result := range results {
		assessment, err := assessATLCoreResult(result.EffectiveEligibility(), result.Status == "pass")
		if err != nil {
			return core.Aggregate{}, fmt.Errorf("project ATL result: %w", err)
		}
		assessments = append(assessments, assessment)
	}
	summary, err := core.Summarize(atlCoreResultTask(), assessments)
	if err != nil {
		return core.Aggregate{}, fmt.Errorf("summarize ATL results: %w", err)
	}
	return summary, nil
}

func assessATLCoreResult(eligibility string, passed bool) (core.Assessment, error) {
	presence := core.PresenceObserved
	if eligibility != EligibilitySupported {
		presence = core.PresenceNotApplicable
		passed = false
	}
	observation := core.Observation{Checks: []core.CheckObservation{{
		ID: "deterministic-status", Presence: presence, Passed: passed,
	}}}
	grade := core.Grade{Checks: []core.CheckGrade{{
		ID: "deterministic-status", Presence: presence, Passed: passed,
	}}}
	return core.Assess(atlCoreResultTask(), observation, grade)
}

func atlCoreResultTask() core.Task {
	return core.Task{
		ID:     "atl-result-status",
		Checks: []core.Check{{ID: "deterministic-status", Weight: 1}},
	}
}

func atlCoreIdentity(domain string, values ...[]byte) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte("agent-eval/atl-core/" + domain + "/v1\x00"))
	for _, value := range values {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(value)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write(value)
	}
	return domain + "-" + hex.EncodeToString(hash.Sum(nil))
}
