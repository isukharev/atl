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
	ScenarioSchemaVersion    = 1
	ObservationSchemaVersion = 1
	ResultSchemaVersion      = 1
)

const (
	maxContractListEntries = 256
	maxObservedMethodCount = 1_000_000
)

var (
	identifierRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]{0,127}$`)
	methodRE     = regexp.MustCompile(`^[A-Z][A-Z0-9-]{0,31}$`)
)

// Scenario is provider-neutral: the same task and budgets can be evaluated by
// deterministic workflows, Claude Code, Codex, or a future runner.
type Scenario struct {
	SchemaVersion        int      `json:"schema_version"`
	ID                   string   `json:"id"`
	TaskClass            string   `json:"task_class"`
	Description          string   `json:"description"`
	DataClass            string   `json:"data_class"`
	RequiredCapabilities []string `json:"required_capabilities"`
	RequiredChecks       []string `json:"required_checks"`
	Budgets              Budgets  `json:"budgets"`
}

// Budgets are hard upper bounds. Zero means zero, not unlimited, so every
// potentially expensive or mutating dimension remains explicit.
type Budgets struct {
	MaxAgentTurns            int      `json:"max_agent_turns"`
	MaxToolCalls             int      `json:"max_tool_calls"`
	MaxATLInvocations        int      `json:"max_atl_invocations"`
	MaxBackendRequests       int      `json:"max_backend_requests"`
	MaxRemoteWrites          int      `json:"max_remote_writes"`
	MaxOutputBytes           int64    `json:"max_output_bytes"`
	MaxInputTokens           int64    `json:"max_input_tokens"`
	MaxOutputTokens          int64    `json:"max_output_tokens"`
	MaxEstimatedCostMicroUSD int64    `json:"max_estimated_cost_microusd"`
	MaxDurationMillis        int64    `json:"max_duration_millis"`
	AllowedHTTPMethods       []string `json:"allowed_http_methods"`
}

// Runtime identifies the tested system without retaining task content.
type Runtime struct {
	Provider      string `json:"provider"`
	AgentVersion  string `json:"agent_version,omitempty"`
	Model         string `json:"model,omitempty"`
	Reasoning     string `json:"reasoning,omitempty"`
	ATLVersion    string `json:"atl_version"`
	PluginVersion string `json:"plugin_version,omitempty"`
	SkillDigest   string `json:"skill_digest,omitempty"`
}

// Observation is the minimal aggregate trace accepted from a runner. HTTP
// paths, tool arguments, prompts, and output bodies are deliberately absent.
type Observation struct {
	SchemaVersion int             `json:"schema_version"`
	ScenarioID    string          `json:"scenario_id"`
	Variant       string          `json:"variant"`
	Runtime       Runtime         `json:"runtime"`
	Metrics       InputMetrics    `json:"metrics"`
	HTTPMethods   map[string]int  `json:"http_methods"`
	Checks        map[string]bool `json:"checks"`
	Warnings      []string        `json:"warnings,omitempty"`
}

type InputMetrics struct {
	AgentTurns            int   `json:"agent_turns"`
	ToolCalls             int   `json:"tool_calls"`
	ATLInvocations        int   `json:"atl_invocations"`
	OutputBytes           int64 `json:"output_bytes"`
	InputTokens           int64 `json:"input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	EstimatedCostMicroUSD int64 `json:"estimated_cost_microusd"`
	DurationMillis        int64 `json:"duration_millis"`
}

// Metrics is normalized by Evaluate. Backend request and write counts are
// derived from HTTPMethods rather than trusted from a runner.
type Metrics struct {
	AgentTurns            int   `json:"agent_turns"`
	ToolCalls             int   `json:"tool_calls"`
	ATLInvocations        int   `json:"atl_invocations"`
	BackendRequests       int   `json:"backend_requests"`
	RemoteWrites          int   `json:"remote_writes"`
	OutputBytes           int64 `json:"output_bytes"`
	InputTokens           int64 `json:"input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	EstimatedCostMicroUSD int64 `json:"estimated_cost_microusd"`
	DurationMillis        int64 `json:"duration_millis"`
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
	SchemaVersion int             `json:"schema_version"`
	ScenarioID    string          `json:"scenario_id"`
	TaskClass     string          `json:"task_class"`
	DataClass     string          `json:"data_class"`
	Variant       string          `json:"variant"`
	Runtime       Runtime         `json:"runtime"`
	Status        string          `json:"status"`
	Metrics       Metrics         `json:"metrics"`
	HTTPMethods   map[string]int  `json:"http_methods"`
	Checks        map[string]bool `json:"checks"`
	Violations    []Violation     `json:"violations"`
	Warnings      []string        `json:"warnings,omitempty"`
}

func (s Scenario) Validate() error {
	if s.SchemaVersion != ScenarioSchemaVersion {
		return fmt.Errorf("unsupported scenario schema_version %d", s.SchemaVersion)
	}
	if !identifierRE.MatchString(s.ID) {
		return fmt.Errorf("invalid scenario id %q", s.ID)
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
	return s.Budgets.validate()
}

func (b Budgets) validate() error {
	values := map[string]int64{
		"max_agent_turns":             int64(b.MaxAgentTurns),
		"max_tool_calls":              int64(b.MaxToolCalls),
		"max_atl_invocations":         int64(b.MaxATLInvocations),
		"max_backend_requests":        int64(b.MaxBackendRequests),
		"max_remote_writes":           int64(b.MaxRemoteWrites),
		"max_output_bytes":            b.MaxOutputBytes,
		"max_input_tokens":            b.MaxInputTokens,
		"max_output_tokens":           b.MaxOutputTokens,
		"max_estimated_cost_microusd": b.MaxEstimatedCostMicroUSD,
		"max_duration_millis":         b.MaxDurationMillis,
	}
	for name, value := range values {
		if value < 0 {
			return fmt.Errorf("%s must be non-negative", name)
		}
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
	if err := o.Runtime.validate(); err != nil {
		return err
	}
	if err := o.Metrics.validate(); err != nil {
		return err
	}
	if len(o.HTTPMethods) > maxContractListEntries || len(o.Checks) > maxContractListEntries || len(o.Warnings) > maxContractListEntries {
		return fmt.Errorf("observation exceeds %d entries in a bounded collection", maxContractListEntries)
	}
	for method, count := range o.HTTPMethods {
		if !methodRE.MatchString(method) || count < 0 || count > maxObservedMethodCount {
			return fmt.Errorf("invalid HTTP method observation %q=%d", method, count)
		}
	}
	for check := range o.Checks {
		if !identifierRE.MatchString(check) {
			return fmt.Errorf("invalid check name %q", check)
		}
	}
	return validateIdentifierList("warnings", o.Warnings, false)
}

func (r Runtime) validate() error {
	if !identifierRE.MatchString(r.Provider) {
		return fmt.Errorf("invalid runtime provider %q", r.Provider)
	}
	if strings.TrimSpace(r.ATLVersion) == "" || len(r.ATLVersion) > 128 || strings.ContainsAny(r.ATLVersion, "\r\n\x00") {
		return fmt.Errorf("runtime atl_version must contain 1..128 bytes")
	}
	for name, value := range map[string]string{
		"agent_version":  r.AgentVersion,
		"model":          r.Model,
		"reasoning":      r.Reasoning,
		"plugin_version": r.PluginVersion,
		"skill_digest":   r.SkillDigest,
	} {
		if len(value) > 256 || strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("runtime %s is invalid", name)
		}
	}
	return nil
}

func (m InputMetrics) validate() error {
	values := map[string]int64{
		"agent_turns": int64(m.AgentTurns), "tool_calls": int64(m.ToolCalls),
		"atl_invocations": int64(m.ATLInvocations), "output_bytes": m.OutputBytes,
		"input_tokens": m.InputTokens, "output_tokens": m.OutputTokens,
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
