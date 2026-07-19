// Package agenteval defines the privacy-safe contracts used to measure atl's
// agent-facing workflows. It intentionally stores aggregate trajectory data,
// never prompts, command arguments, backend URLs, or response bodies.
package agenteval

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

const (
	ScenarioSchemaVersion                = 1
	ObservationSchemaVersion             = 4
	ResultSchemaVersion                  = 6
	LegacyPromptBoundResultSchemaVersion = 5
	PanelResultSchemaVersion             = 4
	LegacyResultSchemaVersion            = 3
)

const (
	BenchmarkCategoryNeutralCommon = "neutral-common"
	BenchmarkCategorySurfaceNative = "surface-native"
	BenchmarkCategoryRouteFixed    = "route-fixed"

	SurfaceCLISkill          = "cli-skill"
	SurfaceATLMCP            = "atl-mcp"
	SurfaceExternalMCP       = "external-mcp"
	SurfaceLegacyUnspecified = "legacy-unspecified"

	EligibilitySupported             = "supported"
	EligibilityUnsupportedCapability = "unsupported-capability"
	EligibilityInvalidatedDrift      = "invalidated-backend-drift"
	BackendObservationHTTP           = "observed-http"
	BackendObservationOpaqueMCP      = "opaque-mcp"
	SafetyAssuranceObservedHTTP      = "observed-http-policy"
	SafetyAssuranceReviewedROMCP     = "reviewed-ro-mcp-interface"
)

const (
	maxContractListEntries = 256
	maxObservedMethodCount = 1_000_000
)

var (
	identifierRE      = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]{0,127}$`)
	pathComponentIDRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	methodRE          = regexp.MustCompile(`^[A-Z][A-Z0-9-]{0,31}$`)
)

// validatePathComponentID is deliberately stricter than identifierRE. Some
// contract identifiers are namespaces and legitimately contain '/', while a
// scenario id and run variant become on-disk directory names. Keeping the two
// vocabularies separate prevents a valid semantic identifier from becoming a
// path traversal primitive.
func validatePathComponentID(name, value string) error {
	if !pathComponentIDRE.MatchString(value) || value == "." || value == ".." || strings.ContainsAny(value, `/\`) {
		return fmt.Errorf("invalid %s %q", name, value)
	}
	return nil
}

var metricNames = map[string]struct{}{
	"agent_turns": {}, "tool_calls": {}, "atl_invocations": {}, "interface_invocations": {},
	"delegations": {}, "backend_requests": {}, "duplicate_backend_requests": {},
	"remote_writes": {},
	"output_bytes":  {}, "input_tokens": {}, "output_tokens": {},
	"main_thread_input_tokens": {}, "main_thread_output_tokens": {},
	"estimated_cost_microusd": {}, "duration_millis": {},
	"capability_families": {},
}

// Scenario is provider-neutral: the same task and budgets can be evaluated by
// deterministic workflows, Claude Code, Codex, or a future runner.
type Scenario struct {
	SchemaVersion          int      `json:"schema_version"`
	ID                     string   `json:"id"`
	Category               string   `json:"category,omitempty"`
	TaskClass              string   `json:"task_class"`
	Description            string   `json:"description"`
	DataClass              string   `json:"data_class"`
	RequiredCapabilities   []string `json:"required_capabilities"`
	RequiredChecks         []string `json:"required_checks"`
	RequiredSemanticChecks []string `json:"required_semantic_checks,omitempty"`
	RequiredMetrics        []string `json:"required_metrics"`
	Budgets                Budgets  `json:"budgets"`
}

// Budgets are hard upper bounds. Zero means zero, not unlimited, so every
// potentially expensive or mutating dimension remains explicit.
type Budgets struct {
	MaxAgentTurns               int      `json:"max_agent_turns"`
	MaxToolCalls                int      `json:"max_tool_calls"`
	MaxATLInvocations           int      `json:"max_atl_invocations"`
	MaxInterfaceInvocations     int      `json:"max_interface_invocations,omitempty"`
	MaxDelegations              int      `json:"max_delegations"`
	MaxBackendRequests          int      `json:"max_backend_requests"`
	MaxDuplicateBackendRequests int      `json:"max_duplicate_backend_requests"`
	MaxRemoteWrites             int      `json:"max_remote_writes"`
	MaxOutputBytes              int64    `json:"max_output_bytes"`
	MaxInputTokens              int64    `json:"max_input_tokens"`
	MaxOutputTokens             int64    `json:"max_output_tokens"`
	MaxMainThreadInputTokens    int64    `json:"max_main_thread_input_tokens"`
	MaxMainThreadOutputTokens   int64    `json:"max_main_thread_output_tokens"`
	MaxEstimatedCostMicroUSD    int64    `json:"max_estimated_cost_microusd"`
	MaxDurationMillis           int64    `json:"max_duration_millis"`
	AllowedHTTPMethods          []string `json:"allowed_http_methods"`
}

// Runtime identifies the tested system without retaining task content.
type Runtime struct {
	Provider             string `json:"provider"`
	AgentVersion         string `json:"agent_version,omitempty"`
	Model                string `json:"model,omitempty"`
	Reasoning            string `json:"reasoning,omitempty"`
	ATLVersion           string `json:"atl_version"`
	PluginVersion        string `json:"plugin_version,omitempty"`
	SkillDigest          string `json:"skill_digest,omitempty"`
	SkillActivation      string `json:"skill_activation,omitempty"`
	PromptContractSHA256 string `json:"prompt_contract_sha256,omitempty"`
}

// Observation is the minimal aggregate trace accepted from a runner. HTTP
// paths, tool arguments, prompts, and output bodies are deliberately absent.
type Observation struct {
	SchemaVersion           int                      `json:"schema_version"`
	ScenarioID              string                   `json:"scenario_id"`
	Variant                 string                   `json:"variant"`
	Surface                 string                   `json:"surface,omitempty"`
	Eligibility             string                   `json:"eligibility,omitempty"`
	UnavailableCapabilities []string                 `json:"unavailable_capabilities,omitempty"`
	BackendObservation      string                   `json:"backend_observation,omitempty"`
	SafetyAssurance         string                   `json:"safety_assurance,omitempty"`
	Runtime                 Runtime                  `json:"runtime"`
	Metrics                 InputMetrics             `json:"metrics"`
	Coverage                map[string]bool          `json:"coverage"`
	HTTPMethods             map[string]int           `json:"http_methods"`
	Checks                  map[string]bool          `json:"checks"`
	Warnings                []string                 `json:"warnings,omitempty"`
	CapabilityFamilies      []CapabilityFamilyMetric `json:"capability_families,omitempty"`
}

type InputMetrics struct {
	AgentTurns               int   `json:"agent_turns"`
	ToolCalls                int   `json:"tool_calls"`
	ATLInvocations           int   `json:"atl_invocations"`
	InterfaceInvocations     int   `json:"interface_invocations,omitempty"`
	Delegations              int   `json:"delegations"`
	DuplicateBackendRequests int   `json:"duplicate_backend_requests"`
	OutputBytes              int64 `json:"output_bytes"`
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	MainThreadInputTokens    int64 `json:"main_thread_input_tokens"`
	MainThreadOutputTokens   int64 `json:"main_thread_output_tokens"`
	EstimatedCostMicroUSD    int64 `json:"estimated_cost_microusd"`
	DurationMillis           int64 `json:"duration_millis"`
}

// Metrics is normalized by Evaluate. Backend request and write counts are
// derived from HTTPMethods rather than trusted from a runner.
type Metrics struct {
	AgentTurns               int   `json:"agent_turns"`
	ToolCalls                int   `json:"tool_calls"`
	ATLInvocations           int   `json:"atl_invocations"`
	InterfaceInvocations     int   `json:"interface_invocations,omitempty"`
	Delegations              int   `json:"delegations"`
	BackendRequests          int   `json:"backend_requests"`
	DuplicateBackendRequests int   `json:"duplicate_backend_requests"`
	RemoteWrites             int   `json:"remote_writes"`
	OutputBytes              int64 `json:"output_bytes"`
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	MainThreadInputTokens    int64 `json:"main_thread_input_tokens"`
	MainThreadOutputTokens   int64 `json:"main_thread_output_tokens"`
	EstimatedCostMicroUSD    int64 `json:"estimated_cost_microusd"`
	DurationMillis           int64 `json:"duration_millis"`
}

// Violation is structured so public aggregate reports do not need to retain a
// backend error or model-generated explanation.
type Violation struct {
	Code     string `json:"code"`
	Subject  string `json:"subject"`
	Observed int64  `json:"observed,omitempty"`
	Limit    int64  `json:"limit,omitempty"`
}

type Result struct {
	SchemaVersion           int                             `json:"schema_version"`
	ScenarioID              string                          `json:"scenario_id"`
	TaskClass               string                          `json:"task_class"`
	DataClass               string                          `json:"data_class"`
	Category                string                          `json:"category,omitempty"`
	Variant                 string                          `json:"variant"`
	Surface                 string                          `json:"surface,omitempty"`
	Eligibility             string                          `json:"eligibility,omitempty"`
	UnavailableCapabilities []string                        `json:"unavailable_capabilities,omitempty"`
	BackendObservation      string                          `json:"backend_observation,omitempty"`
	SafetyAssurance         string                          `json:"safety_assurance,omitempty"`
	Runtime                 Runtime                         `json:"runtime"`
	Status                  string                          `json:"status"`
	Metrics                 Metrics                         `json:"metrics"`
	Coverage                map[string]bool                 `json:"coverage"`
	HTTPMethods             map[string]int                  `json:"http_methods"`
	Checks                  map[string]bool                 `json:"checks"`
	Violations              []Violation                     `json:"violations"`
	Warnings                []string                        `json:"warnings,omitempty"`
	Qualitative             *QualitativeAssessment          `json:"qualitative,omitempty"`
	QualitativeReviewSet    *QualitativeReviewSetAssessment `json:"qualitative_review_set,omitempty"`
	CapabilityFamilies      []CapabilityFamilyMetric        `json:"capability_families,omitempty"`
}

func (s Scenario) Validate() error {
	if s.SchemaVersion != ScenarioSchemaVersion {
		return fmt.Errorf("unsupported scenario schema_version %d", s.SchemaVersion)
	}
	if err := validatePathComponentID("scenario id", s.ID); err != nil {
		return err
	}
	if !validBenchmarkCategory(s.EffectiveCategory()) {
		return fmt.Errorf("invalid benchmark category %q", s.Category)
	}
	if !identifierRE.MatchString(s.TaskClass) {
		return fmt.Errorf("invalid task_class %q", s.TaskClass)
	}
	if strings.TrimSpace(s.Description) == "" || len(s.Description) > 512 {
		return fmt.Errorf("description must contain 1..512 bytes")
	}
	if s.DataClass != "synthetic" && s.DataClass != "private-local" {
		return fmt.Errorf("data_class must be synthetic or private-local")
	}
	if err := validateIdentifierList("required_capabilities", s.RequiredCapabilities, false); err != nil {
		return err
	}
	if err := validateIdentifierList("required_checks", s.RequiredChecks, true); err != nil {
		return err
	}
	if err := validateIdentifierList("required_semantic_checks", s.RequiredSemanticChecks, s.EffectiveCategory() != BenchmarkCategoryRouteFixed); err != nil {
		return err
	}
	required := make(map[string]struct{}, len(s.RequiredChecks))
	for _, name := range s.RequiredChecks {
		required[name] = struct{}{}
	}
	for _, name := range s.RequiredSemanticChecks {
		if _, ok := required[name]; !ok {
			return fmt.Errorf("required_semantic_checks entry %q is not in required_checks", name)
		}
	}
	if s.EffectiveCategory() == BenchmarkCategoryNeutralCommon {
		if s.Budgets.MaxInterfaceInvocations < 1 {
			return fmt.Errorf("neutral-common scenarios require max_interface_invocations>0")
		}
		if !containsString(s.RequiredMetrics, "interface_invocations") || containsString(s.RequiredMetrics, "atl_invocations") {
			return fmt.Errorf("neutral-common scenarios require interface_invocations and forbid atl_invocations")
		}
	}
	if err := validateMetricList("required_metrics", s.RequiredMetrics); err != nil {
		return err
	}
	return s.Budgets.validate()
}

func (b Budgets) validate() error {
	values := map[string]int64{
		"max_agent_turns":                int64(b.MaxAgentTurns),
		"max_tool_calls":                 int64(b.MaxToolCalls),
		"max_atl_invocations":            int64(b.MaxATLInvocations),
		"max_interface_invocations":      int64(b.MaxInterfaceInvocations),
		"max_delegations":                int64(b.MaxDelegations),
		"max_backend_requests":           int64(b.MaxBackendRequests),
		"max_duplicate_backend_requests": int64(b.MaxDuplicateBackendRequests),
		"max_remote_writes":              int64(b.MaxRemoteWrites),
		"max_output_bytes":               b.MaxOutputBytes,
		"max_input_tokens":               b.MaxInputTokens,
		"max_output_tokens":              b.MaxOutputTokens,
		"max_main_thread_input_tokens":   b.MaxMainThreadInputTokens,
		"max_main_thread_output_tokens":  b.MaxMainThreadOutputTokens,
		"max_estimated_cost_microusd":    b.MaxEstimatedCostMicroUSD,
		"max_duration_millis":            b.MaxDurationMillis,
	}
	for name, value := range values {
		if value < 0 {
			return fmt.Errorf("%s must be non-negative", name)
		}
	}
	if b.MaxDelegations > 3 {
		return fmt.Errorf("max_delegations must not exceed 3")
	}
	if len(b.AllowedHTTPMethods) > maxContractListEntries {
		return fmt.Errorf("allowed_http_methods exceeds %d entries", maxContractListEntries)
	}
	seen := map[string]struct{}{}
	for _, method := range b.AllowedHTTPMethods {
		if !methodRE.MatchString(method) {
			return fmt.Errorf("invalid HTTP method %q", method)
		}
		if _, ok := seen[method]; ok {
			return fmt.Errorf("duplicate allowed HTTP method %q", method)
		}
		seen[method] = struct{}{}
	}
	return nil
}

func (b Budgets) EffectiveMaxInterfaceInvocations() int {
	if b.MaxInterfaceInvocations != 0 {
		return b.MaxInterfaceInvocations
	}
	return b.MaxATLInvocations
}

func (o Observation) Validate() error {
	if o.SchemaVersion != ObservationSchemaVersion {
		return fmt.Errorf("unsupported observation schema_version %d", o.SchemaVersion)
	}
	if !identifierRE.MatchString(o.ScenarioID) {
		return fmt.Errorf("invalid observation scenario_id %q", o.ScenarioID)
	}
	if !identifierRE.MatchString(o.Variant) {
		return fmt.Errorf("invalid observation variant %q", o.Variant)
	}
	if !validResultSurface(o.EffectiveSurface()) {
		return fmt.Errorf("invalid observation surface %q", o.Surface)
	}
	if err := validateEligibility(o.EffectiveEligibility(), o.UnavailableCapabilities); err != nil {
		return fmt.Errorf("observation eligibility: %w", err)
	}
	if err := validateBackendAssurance(o.EffectiveSurface(), o.BackendObservation, o.SafetyAssurance, o.Coverage, o.HTTPMethods); err != nil {
		return fmt.Errorf("observation backend assurance: %w", err)
	}
	if err := o.Runtime.validate(); err != nil {
		return err
	}
	if err := o.Metrics.validate(); err != nil {
		return err
	}
	if len(o.Coverage) > len(metricNames) || len(o.HTTPMethods) > maxContractListEntries || len(o.Checks) > maxContractListEntries || len(o.Warnings) > maxContractListEntries {
		return fmt.Errorf("observation exceeds %d entries in a bounded collection", maxContractListEntries)
	}
	for name := range o.Coverage {
		if _, ok := metricNames[name]; !ok {
			return fmt.Errorf("unknown covered metric %q", name)
		}
	}
	if !o.Coverage["backend_requests"] && len(o.HTTPMethods) != 0 {
		return fmt.Errorf("http_methods require backend_requests coverage")
	}
	if o.Coverage["duplicate_backend_requests"] && !o.Coverage["backend_requests"] {
		return fmt.Errorf("duplicate_backend_requests coverage requires backend_requests coverage")
	}
	if o.Coverage["remote_writes"] && !o.Coverage["backend_requests"] {
		return fmt.Errorf("remote_writes coverage requires backend_requests coverage")
	}
	if !o.Coverage["capability_families"] && len(o.CapabilityFamilies) != 0 {
		return fmt.Errorf("capability families require coverage")
	}
	if _, err := normalizeCapabilityFamilies(o.CapabilityFamilies); err != nil {
		return err
	}
	var backendRequests int
	for method, count := range o.HTTPMethods {
		if !methodRE.MatchString(method) || count < 0 || count > maxObservedMethodCount {
			return fmt.Errorf("invalid HTTP method observation %q=%d", method, count)
		}
		backendRequests += count
	}
	if o.Metrics.DuplicateBackendRequests > backendRequests {
		return fmt.Errorf("duplicate backend requests exceed total requests")
	}
	if o.Coverage["input_tokens"] && o.Coverage["main_thread_input_tokens"] && o.Metrics.MainThreadInputTokens > o.Metrics.InputTokens {
		return fmt.Errorf("main-thread input tokens exceed total input tokens")
	}
	if o.Coverage["output_tokens"] && o.Coverage["main_thread_output_tokens"] && o.Metrics.MainThreadOutputTokens > o.Metrics.OutputTokens {
		return fmt.Errorf("main-thread output tokens exceed total output tokens")
	}
	for check := range o.Checks {
		if !identifierRE.MatchString(check) {
			return fmt.Errorf("invalid check name %q", check)
		}
	}
	if err := validateIdentifierList("warnings", o.Warnings, false); err != nil {
		return err
	}
	return validateUnobservedMetrics(o.Metrics, o.Coverage)
}

func (r Runtime) validate() error {
	if !identifierRE.MatchString(r.Provider) {
		return fmt.Errorf("invalid runtime provider %q", r.Provider)
	}
	if strings.TrimSpace(r.ATLVersion) == "" || len(r.ATLVersion) > 128 || strings.ContainsAny(r.ATLVersion, "\r\n\x00") {
		return fmt.Errorf("runtime atl_version must contain 1..128 bytes")
	}
	for name, value := range map[string]string{
		"agent_version":          r.AgentVersion,
		"model":                  r.Model,
		"reasoning":              r.Reasoning,
		"plugin_version":         r.PluginVersion,
		"skill_digest":           r.SkillDigest,
		"skill_activation":       r.SkillActivation,
		"prompt_contract_sha256": r.PromptContractSHA256,
	} {
		if len(value) > 256 || strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("runtime %s is invalid", name)
		}
	}
	if r.SkillActivation != "" && r.SkillActivation != SkillActivationImplicit && r.SkillActivation != SkillActivationExplicit &&
		r.SkillActivation != SkillActivationDeveloper && r.SkillActivation != SkillActivationCombined {
		return fmt.Errorf("runtime skill_activation is invalid")
	}
	if r.PromptContractSHA256 != "" && !validSHA256(r.PromptContractSHA256) {
		return fmt.Errorf("runtime prompt_contract_sha256 is invalid")
	}
	return nil
}

func (m InputMetrics) validate() error {
	values := map[string]int64{
		"agent_turns": int64(m.AgentTurns), "tool_calls": int64(m.ToolCalls),
		"atl_invocations": int64(m.ATLInvocations), "interface_invocations": int64(m.InterfaceInvocations), "delegations": int64(m.Delegations),
		"duplicate_backend_requests": int64(m.DuplicateBackendRequests), "output_bytes": m.OutputBytes,
		"input_tokens": m.InputTokens, "output_tokens": m.OutputTokens,
		"main_thread_input_tokens": m.MainThreadInputTokens, "main_thread_output_tokens": m.MainThreadOutputTokens,
		"estimated_cost_microusd": m.EstimatedCostMicroUSD, "duration_millis": m.DurationMillis,
	}
	for name, value := range values {
		if value < 0 {
			return fmt.Errorf("observation %s must be non-negative", name)
		}
	}
	return nil
}

func validateIdentifierList(name string, values []string, requireOne bool) error {
	if requireOne && len(values) == 0 {
		return fmt.Errorf("%s must contain at least one entry", name)
	}
	if len(values) > maxContractListEntries {
		return fmt.Errorf("%s exceeds %d entries", name, maxContractListEntries)
	}
	seen := map[string]struct{}{}
	for _, value := range values {
		if !identifierRE.MatchString(value) {
			return fmt.Errorf("invalid %s entry %q", name, value)
		}
		if _, ok := seen[value]; ok {
			return fmt.Errorf("duplicate %s entry %q", name, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateMetricList(name string, values []string) error {
	if len(values) == 0 {
		return fmt.Errorf("%s must contain at least one entry", name)
	}
	if len(values) > len(metricNames) {
		return fmt.Errorf("%s exceeds %d entries", name, len(metricNames))
	}
	seen := map[string]struct{}{}
	for _, value := range values {
		if _, ok := metricNames[value]; !ok {
			return fmt.Errorf("unknown %s entry %q", name, value)
		}
		if _, ok := seen[value]; ok {
			return fmt.Errorf("duplicate %s entry %q", name, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateUnobservedMetrics(metrics InputMetrics, coverage map[string]bool) error {
	values := map[string]int64{
		"agent_turns": int64(metrics.AgentTurns), "tool_calls": int64(metrics.ToolCalls),
		"atl_invocations": int64(metrics.ATLInvocations), "interface_invocations": int64(metrics.InterfaceInvocations), "delegations": int64(metrics.Delegations),
		"duplicate_backend_requests": int64(metrics.DuplicateBackendRequests), "output_bytes": metrics.OutputBytes,
		"input_tokens": metrics.InputTokens, "output_tokens": metrics.OutputTokens,
		"main_thread_input_tokens": metrics.MainThreadInputTokens, "main_thread_output_tokens": metrics.MainThreadOutputTokens,
		"estimated_cost_microusd": metrics.EstimatedCostMicroUSD,
		"duration_millis":         metrics.DurationMillis,
	}
	for name, value := range values {
		if !coverage[name] && value != 0 {
			return fmt.Errorf("unobserved metric %s must be zero", name)
		}
	}
	return nil
}

func (s Scenario) EffectiveCategory() string {
	if s.Category == "" {
		return BenchmarkCategoryRouteFixed
	}
	return s.Category
}

func validBenchmarkCategory(category string) bool {
	switch category {
	case BenchmarkCategoryNeutralCommon, BenchmarkCategorySurfaceNative, BenchmarkCategoryRouteFixed:
		return true
	default:
		return false
	}
}

func (o Observation) EffectiveSurface() string {
	if o.Surface == "" {
		return SurfaceLegacyUnspecified
	}
	return o.Surface
}

func (o Observation) EffectiveEligibility() string {
	if o.Eligibility == "" {
		return EligibilitySupported
	}
	return o.Eligibility
}

func (r Result) EffectiveCategory() string {
	if r.Category == "" {
		return BenchmarkCategoryRouteFixed
	}
	return r.Category
}

func (r Result) EffectiveSurface() string {
	if r.Surface == "" {
		return SurfaceLegacyUnspecified
	}
	return r.Surface
}

func (r Result) EffectiveEligibility() string {
	if r.Eligibility == "" {
		return EligibilitySupported
	}
	return r.Eligibility
}

func validateEligibility(eligibility string, unavailable []string) error {
	switch eligibility {
	case EligibilitySupported:
		if len(unavailable) != 0 {
			return fmt.Errorf("supported result cannot declare unavailable capabilities")
		}
	case EligibilityUnsupportedCapability:
		if len(unavailable) == 0 || len(unavailable) > 32 {
			return fmt.Errorf("unsupported capability result requires 1..32 capability ids")
		}
		if err := validateIdentifierList("unavailable_capabilities", unavailable, true); err != nil {
			return err
		}
	case EligibilityInvalidatedDrift:
		if len(unavailable) != 0 {
			return fmt.Errorf("backend drift result cannot declare unavailable capabilities")
		}
	default:
		return fmt.Errorf("invalid eligibility %q", eligibility)
	}
	return nil
}

func containsString(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func validRunSurface(surface string) bool {
	switch surface {
	case SurfaceCLISkill, SurfaceATLMCP, SurfaceExternalMCP:
		return true
	default:
		return false
	}
}

func validResultSurface(surface string) bool {
	return surface == SurfaceLegacyUnspecified || validRunSurface(surface)
}

func validateBackendAssurance(surface, observation, assurance string, coverage map[string]bool, methods map[string]int) error {
	// Older observation/result files predate these additive fields. Preserve
	// their decoding, but never accept a partial or surface-inconsistent claim.
	if observation == "" && assurance == "" {
		if surface == SurfaceExternalMCP {
			return fmt.Errorf("external MCP requires explicit opaque assurance")
		}
		return nil
	}
	if observation == "" || assurance == "" {
		return fmt.Errorf("observation and assurance must be declared together")
	}
	if surface == SurfaceExternalMCP {
		if observation != BackendObservationOpaqueMCP || assurance != SafetyAssuranceReviewedROMCP {
			return fmt.Errorf("external MCP requires opaque reviewed-RO assurance")
		}
		if coverage["backend_requests"] || coverage["duplicate_backend_requests"] || coverage["remote_writes"] || len(methods) != 0 {
			return fmt.Errorf("opaque MCP cannot claim internal backend coverage")
		}
		return nil
	}
	if observation != BackendObservationHTTP || assurance != SafetyAssuranceObservedHTTP {
		return fmt.Errorf("non-external surface requires observed HTTP policy assurance")
	}
	if !coverage["backend_requests"] || !coverage["duplicate_backend_requests"] || !coverage["remote_writes"] {
		return fmt.Errorf("observed HTTP assurance requires backend coverage")
	}
	return nil
}

func sortedStringMap(in map[string]int) map[string]int {
	// encoding/json sorts map keys, but rebuilding the map after validation also
	// ensures callers cannot mutate the result through an aliased input map.
	keys := make([]string, 0, len(in))
	for key := range in {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make(map[string]int, len(in))
	for _, key := range keys {
		out[key] = in[key]
	}
	return out
}

func sortedBoolMap(in map[string]bool) map[string]bool {
	keys := make([]string, 0, len(in))
	for key := range in {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make(map[string]bool, len(in))
	for _, key := range keys {
		out[key] = in[key]
	}
	return out
}
