package agentadapter

import (
	"reflect"
	"sort"
)

type OrchestrationProfile string

const (
	ProfileGenericChild        OrchestrationProfile = "generic_child"
	ProfileParallelChildren    OrchestrationProfile = "parallel_children"
	ProfileSingle              OrchestrationProfile = "single"
	ProfileSpecializedChildren OrchestrationProfile = "specialized_children"
)

type EventType string

const (
	EventHandoff  EventType = "handoff"
	EventStart    EventType = "start"
	EventTerminal EventType = "terminal"
)

type Role string

const (
	RoleGenericChild     Role = "generic_child"
	RolePrimary          Role = "primary"
	RoleSpecializedChild Role = "specialized_child"
)

type TerminalState string

const (
	TerminalCanceled  TerminalState = "canceled"
	TerminalFailed    TerminalState = "failed"
	TerminalSucceeded TerminalState = "succeeded"
	TerminalUnknown   TerminalState = "unknown"
)

type ActivationMode string

const (
	ActivationDeveloperAndForced    ActivationMode = "developer_and_forced_injection"
	ActivationDeveloperInstructions ActivationMode = "developer_instructions"
	ActivationForcedInjection       ActivationMode = "forced_injection"
	ActivationNative                ActivationMode = "native"
	ActivationUnavailable           ActivationMode = "unavailable"
)

type UseEvidence string

const (
	UseEvidenceObserved     UseEvidence = "observed"
	UseEvidenceSelfReported UseEvidence = "self_reported"
	UseEvidenceUnavailable  UseEvidence = "unavailable"
)

type MetricState string

const (
	MetricNotApplicable MetricState = "not_applicable"
	MetricObserved      MetricState = "observed"
	MetricUnknown       MetricState = "unknown"
	MetricUnsupported   MetricState = "unsupported"
)

type Metric struct {
	State MetricState `json:"state"`
	Value *uint64     `json:"value,omitempty"`
}

type Usage struct {
	EstimatedCostMicroUSD Metric `json:"estimated_cost_microusd"`
	InputTokens           Metric `json:"input_tokens"`
	OutputTokens          Metric `json:"output_tokens"`
	ToolCalls             Metric `json:"tool_calls"`
	EvidenceItems         Metric `json:"evidence_items"`
}

type Activation struct {
	Mode        ActivationMode `json:"mode"`
	UseEvidence UseEvidence    `json:"use_evidence"`
}

type Start struct {
	Role         Role           `json:"role"`
	Capabilities []CapabilityID `json:"capabilities"`
	Activation   Activation     `json:"activation"`
}

type Terminal struct {
	State TerminalState `json:"state"`
	Usage Usage         `json:"usage"`
}

type Handoff struct {
	Consumed bool `json:"consumed"`
}

type Event struct {
	Sequence uint32    `json:"sequence"`
	NodeID   string    `json:"node_id"`
	ParentID string    `json:"parent_id,omitempty"`
	Type     EventType `json:"type"`
	Start    *Start    `json:"start,omitempty"`
	Terminal *Terminal `json:"terminal,omitempty"`
	Handoff  *Handoff  `json:"handoff,omitempty"`
}

type IssueCode string

const (
	IssueCapabilitySubset  IssueCode = "capability_subset_invalid"
	IssueCycle             IssueCode = "cycle"
	IssueDuplicateHandoff  IssueCode = "duplicate_handoff"
	IssueDuplicateSequence IssueCode = "duplicate_sequence"
	IssueDuplicateStart    IssueCode = "duplicate_start"
	IssueDuplicateTerminal IssueCode = "duplicate_terminal"
	IssueIncomplete        IssueCode = "incomplete_node"
	IssueMissingPrimary    IssueCode = "missing_primary"
	IssueOrphan            IssueCode = "orphan"
	IssueOverDepth         IssueCode = "over_depth"
	IssueOverNodes         IssueCode = "over_nodes"
	IssueProfile           IssueCode = "profile_mismatch"
	IssueSequenceGap       IssueCode = "sequence_gap"
	IssueTerminalOrder     IssueCode = "terminal_order"
	IssueTreeUnattributed  IssueCode = "tree_usage_unattributed"
	IssueUnknownTerminal   IssueCode = "unknown_terminal"
)

type Observation struct {
	Schema                string               `json:"schema"`
	SchemaVersion         int                  `json:"schema_version"`
	ContractVersion       string               `json:"contract_version"`
	AttemptID             string               `json:"attempt_id"`
	AdapterContractSHA256 string               `json:"adapter_contract_sha256"`
	Profile               OrchestrationProfile `json:"profile"`
	Events                []Event              `json:"events"`
	ReportedTreeUsage     *Usage               `json:"reported_tree_usage,omitempty"`
	Coverage              bool                 `json:"coverage"`
	Issues                []IssueCode          `json:"issues"`
	ParentUsage           Usage                `json:"parent_usage"`
	TreeUsage             Usage                `json:"tree_usage"`
	ConsumedChildEvidence Metric               `json:"consumed_child_evidence"`
}

type nodeProjection struct {
	id               string
	parent           string
	start            *Start
	startSequence    uint32
	terminal         *Terminal
	terminalSequence uint32
	handoff          *Handoff
	handoffSequence  uint32
}

func UnknownMetric() Metric              { return Metric{State: MetricUnknown} }
func NotApplicableMetric() Metric        { return Metric{State: MetricNotApplicable} }
func UnsupportedMetric() Metric          { return Metric{State: MetricUnsupported} }
func ObservedMetric(value uint64) Metric { return Metric{State: MetricObserved, Value: &value} }

func Normalize(contract Contract, attemptID string, profile OrchestrationProfile, events []Event) (Observation, error) {
	return normalize(contract, attemptID, profile, events, nil)
}

func NormalizeWithReportedTreeUsage(contract Contract, attemptID string, profile OrchestrationProfile, events []Event,
	reported Usage) (Observation, error) {
	return normalize(contract, attemptID, profile, events, &reported)
}

func normalize(contract Contract, attemptID string, profile OrchestrationProfile, events []Event, reported *Usage) (Observation, error) {
	if ValidateContract(contract) != nil || !validSHA256(attemptID) || !profile.valid() || len(events) == 0 || len(events) > MaxEvents {
		return Observation{}, contractError("observation_input")
	}
	for _, event := range events {
		if !validEvent(event) {
			return Observation{}, contractError("event")
		}
	}
	if reported != nil && !validUsage(*reported) {
		return Observation{}, contractError("reported_tree_usage")
	}
	contractSHA, err := ContractSHA256(contract)
	if err != nil {
		return Observation{}, err
	}
	copyEvents := cloneEvents(events)
	nodes, issues, parent := inspectEvents(contract, profile, copyEvents)
	parentUsage := UnknownUsage()
	if parent != nil && parent.terminal != nil {
		parentUsage = cloneUsage(parent.terminal.Usage)
	}
	treeUsage := aggregateNodeUsage(nodes)
	var reportedCopy *Usage
	if reported != nil {
		value := cloneUsage(*reported)
		reportedCopy = &value
		if !reflect.DeepEqual(treeUsage, value) {
			issues[IssueTreeUnattributed] = struct{}{}
		}
		treeUsage = value
	}
	consumed := aggregateConsumedEvidence(nodes)
	issueList := sortedIssues(issues)
	return Observation{Schema: ObservationSchema, SchemaVersion: SchemaVersion, ContractVersion: ContractVersion,
		AttemptID: attemptID, AdapterContractSHA256: contractSHA, Profile: profile, Events: copyEvents,
		ReportedTreeUsage: reportedCopy, Coverage: len(issueList) == 0, Issues: issueList, ParentUsage: parentUsage, TreeUsage: treeUsage,
		ConsumedChildEvidence: consumed}, nil
}

func ValidateObservation(contract Contract, observation Observation) error {
	if observation.Schema != ObservationSchema || observation.SchemaVersion != SchemaVersion ||
		observation.ContractVersion != ContractVersion || !validSHA256(observation.AttemptID) ||
		!validSHA256(observation.AdapterContractSHA256) || !observation.Profile.valid() ||
		len(observation.Events) == 0 || len(observation.Events) > MaxEvents || !validUsage(observation.ParentUsage) ||
		!validUsage(observation.TreeUsage) || observation.ReportedTreeUsage != nil && !validUsage(*observation.ReportedTreeUsage) ||
		!validMetric(observation.ConsumedChildEvidence) {
		return contractError("observation_shape")
	}
	for _, event := range observation.Events {
		if !validEvent(event) {
			return contractError("event")
		}
	}
	want, err := normalize(contract, observation.AttemptID, observation.Profile, observation.Events, observation.ReportedTreeUsage)
	if err != nil || !reflect.DeepEqual(observation, want) {
		return contractError("observation_projection")
	}
	return nil
}

func inspectEvents(contract Contract, profile OrchestrationProfile, events []Event) (map[string]*nodeProjection, map[IssueCode]struct{}, *nodeProjection) {
	issues := map[IssueCode]struct{}{}
	nodes := map[string]*nodeProjection{}
	seenSequence := map[uint32]bool{}
	supported := map[CapabilityID]bool{}
	for _, capability := range contract.Capabilities {
		supported[capability.ID] = capability.Support == SupportSupported
	}
	for index := range events {
		event := &events[index]
		if event.Sequence != uint32(index+1) { // #nosec G115 -- MaxEvents bounds the index.
			issues[IssueSequenceGap] = struct{}{}
		}
		if seenSequence[event.Sequence] {
			issues[IssueDuplicateSequence] = struct{}{}
		}
		seenSequence[event.Sequence] = true
		node := nodes[event.NodeID]
		if node == nil {
			node = &nodeProjection{id: event.NodeID, parent: event.ParentID}
			nodes[event.NodeID] = node
		} else if node.parent != event.ParentID {
			issues[IssueOrphan] = struct{}{}
		}
		switch event.Type {
		case EventStart:
			if node.start != nil {
				issues[IssueDuplicateStart] = struct{}{}
			} else {
				node.start, node.startSequence = event.Start, event.Sequence
			}
			for capabilityIndex, capability := range event.Start.Capabilities {
				if !supported[capability] || capabilityIndex > 0 && event.Start.Capabilities[capabilityIndex-1] >= capability {
					issues[IssueCapabilitySubset] = struct{}{}
				}
			}
			for _, capability := range activationCapabilities(event.Start.Activation) {
				if !supported[capability] || !capabilityPresent(event.Start.Capabilities, capability) {
					issues[IssueCapabilitySubset] = struct{}{}
				}
			}
		case EventTerminal:
			if node.terminal != nil {
				issues[IssueDuplicateTerminal] = struct{}{}
			} else {
				node.terminal, node.terminalSequence = event.Terminal, event.Sequence
			}
			if event.Terminal.State == TerminalUnknown {
				issues[IssueUnknownTerminal] = struct{}{}
			}
		case EventHandoff:
			if node.handoff != nil {
				issues[IssueDuplicateHandoff] = struct{}{}
			} else {
				node.handoff, node.handoffSequence = event.Handoff, event.Sequence
			}
		}
	}
	if len(nodes) > MaxNodes {
		issues[IssueOverNodes] = struct{}{}
	}
	var primary *nodeProjection
	for _, node := range nodes {
		if node.start == nil || node.terminal == nil {
			issues[IssueIncomplete] = struct{}{}
		}
		if node.start != nil && node.terminal != nil && node.terminalSequence <= node.startSequence {
			issues[IssueTerminalOrder] = struct{}{}
		}
		if node.handoff != nil && (node.parent == "" || node.terminal == nil || node.handoffSequence <= node.terminalSequence) {
			issues[IssueTerminalOrder] = struct{}{}
		}
		if node.parent == "" {
			if node.start != nil && node.start.Role == RolePrimary {
				if primary != nil {
					issues[IssueMissingPrimary] = struct{}{}
				}
				if primary == nil || node.startSequence < primary.startSequence {
					primary = node
				}
			} else {
				issues[IssueMissingPrimary] = struct{}{}
			}
		} else if nodes[node.parent] == nil {
			issues[IssueOrphan] = struct{}{}
		}
		if node.start != nil && node.parent != "" {
			parent := nodes[node.parent]
			if node.start.Role == RolePrimary {
				issues[IssueProfile] = struct{}{}
			}
			if parent != nil && parent.start != nil {
				if node.startSequence <= parent.startSequence || parent.terminal != nil && node.terminal != nil &&
					node.terminalSequence >= parent.terminalSequence || node.handoff != nil && parent.terminal != nil &&
					node.handoffSequence >= parent.terminalSequence {
					issues[IssueTerminalOrder] = struct{}{}
				}
				if !capabilitySubset(node.start.Capabilities, parent.start.Capabilities) {
					issues[IssueCapabilitySubset] = struct{}{}
				}
			}
		}
		if depth, cycle := nodeDepth(nodes, node); cycle {
			issues[IssueCycle] = struct{}{}
		} else if depth > MaxDepth {
			issues[IssueOverDepth] = struct{}{}
		}
	}
	if primary == nil {
		issues[IssueMissingPrimary] = struct{}{}
	}
	if !profileMatches(profile, nodes) {
		issues[IssueProfile] = struct{}{}
	}
	profileCapability := profileCapability(profile)
	if primary == nil || !capabilitySupported(contract, profileCapability) ||
		primary.start == nil || !capabilityPresent(primary.start.Capabilities, profileCapability) {
		issues[IssueProfile] = struct{}{}
	}
	return nodes, issues, primary
}

func validEvent(event Event) bool {
	if event.Sequence == 0 || event.Sequence > MaxEvents || !validIdentifier(event.NodeID) ||
		(event.ParentID != "" && !validIdentifier(event.ParentID)) || event.NodeID == event.ParentID {
		return false
	}
	switch event.Type {
	case EventStart:
		return event.Start != nil && event.Terminal == nil && event.Handoff == nil && event.Start.Role.valid() &&
			event.Start.Activation.valid() && len(event.Start.Capabilities) > 0 && len(event.Start.Capabilities) <= MaxCapabilities
	case EventTerminal:
		return event.Start == nil && event.Terminal != nil && event.Handoff == nil && event.Terminal.State.valid() && validUsage(event.Terminal.Usage)
	case EventHandoff:
		return event.Start == nil && event.Terminal == nil && event.Handoff != nil
	default:
		return false
	}
}

func UnknownUsage() Usage {
	return Usage{EstimatedCostMicroUSD: UnknownMetric(), InputTokens: UnknownMetric(), OutputTokens: UnknownMetric(), ToolCalls: UnknownMetric(), EvidenceItems: UnknownMetric()}
}

func aggregateNodeUsage(nodes map[string]*nodeProjection) Usage {
	usages := make([]Usage, 0, len(nodes))
	for _, node := range nodes {
		if node.terminal == nil {
			return UnknownUsage()
		}
		usages = append(usages, node.terminal.Usage)
	}
	return Usage{EstimatedCostMicroUSD: aggregateMetrics(usages, func(usage Usage) Metric { return usage.EstimatedCostMicroUSD }),
		InputTokens:   aggregateMetrics(usages, func(usage Usage) Metric { return usage.InputTokens }),
		OutputTokens:  aggregateMetrics(usages, func(usage Usage) Metric { return usage.OutputTokens }),
		ToolCalls:     aggregateMetrics(usages, func(usage Usage) Metric { return usage.ToolCalls }),
		EvidenceItems: aggregateMetrics(usages, func(usage Usage) Metric { return usage.EvidenceItems })}
}

func aggregateConsumedEvidence(nodes map[string]*nodeProjection) Metric {
	var values []Metric
	for _, node := range nodes {
		if node.parent == "" || node.handoff == nil || !node.handoff.Consumed {
			continue
		}
		if node.terminal == nil {
			return UnknownMetric()
		}
		values = append(values, node.terminal.Usage.EvidenceItems)
	}
	if len(values) == 0 {
		return NotApplicableMetric()
	}
	return aggregateMetricValues(values)
}

func aggregateMetrics(usages []Usage, project func(Usage) Metric) Metric {
	values := make([]Metric, len(usages))
	for index, usage := range usages {
		values[index] = project(usage)
	}
	return aggregateMetricValues(values)
}

func aggregateMetricValues(metrics []Metric) Metric {
	var total uint64
	allNotApplicable := true
	unsupported := false
	for _, metric := range metrics {
		if metric.State == MetricNotApplicable {
			continue
		}
		allNotApplicable = false
		if metric.State == MetricUnsupported {
			unsupported = true
			continue
		}
		if metric.State != MetricObserved || metric.Value == nil || total > ^uint64(0)-*metric.Value {
			return UnknownMetric()
		}
		total += *metric.Value
	}
	if allNotApplicable {
		return NotApplicableMetric()
	}
	if unsupported {
		return UnsupportedMetric()
	}
	return ObservedMetric(total)
}

func profileMatches(profile OrchestrationProfile, nodes map[string]*nodeProjection) bool {
	children := 0
	roles := map[Role]int{}
	for _, node := range nodes {
		if node.parent != "" {
			children++
			if node.start != nil {
				roles[node.start.Role]++
			}
		}
	}
	switch profile {
	case ProfileSingle:
		return len(nodes) == 1 && children == 0
	case ProfileGenericChild:
		return len(nodes) == 2 && roles[RoleGenericChild] == 1
	case ProfileSpecializedChildren:
		return children > 0 && roles[RoleSpecializedChild] == children
	case ProfileParallelChildren:
		return children >= 2 && children <= MaxNodes-1
	default:
		return false
	}
}

func nodeDepth(nodes map[string]*nodeProjection, node *nodeProjection) (int, bool) {
	depth := 0
	seen := map[string]bool{}
	for node.parent != "" {
		if seen[node.id] {
			return 0, true
		}
		seen[node.id] = true
		depth++
		node = nodes[node.parent]
		if node == nil {
			return depth, false
		}
	}
	return depth, false
}

func capabilitySubset(child, parent []CapabilityID) bool {
	allowed := make(map[CapabilityID]bool, len(parent))
	for _, capability := range parent {
		allowed[capability] = true
	}
	for _, capability := range child {
		if !allowed[capability] {
			return false
		}
	}
	return true
}

func validMetric(metric Metric) bool {
	return metric.State == MetricObserved && metric.Value != nil ||
		(metric.State == MetricUnknown || metric.State == MetricNotApplicable || metric.State == MetricUnsupported) && metric.Value == nil
}

func validUsage(usage Usage) bool {
	return validMetric(usage.EstimatedCostMicroUSD) && validMetric(usage.InputTokens) && validMetric(usage.OutputTokens) &&
		validMetric(usage.ToolCalls) && validMetric(usage.EvidenceItems)
}

func (profile OrchestrationProfile) valid() bool {
	return profile == ProfileGenericChild || profile == ProfileParallelChildren || profile == ProfileSingle || profile == ProfileSpecializedChildren
}

func (role Role) valid() bool {
	return role == RoleGenericChild || role == RolePrimary || role == RoleSpecializedChild
}

func (state TerminalState) valid() bool {
	return state == TerminalCanceled || state == TerminalFailed || state == TerminalSucceeded || state == TerminalUnknown
}

func (activation Activation) valid() bool {
	mode := activation.Mode == ActivationDeveloperAndForced || activation.Mode == ActivationDeveloperInstructions ||
		activation.Mode == ActivationForcedInjection || activation.Mode == ActivationNative || activation.Mode == ActivationUnavailable
	evidence := activation.UseEvidence == UseEvidenceObserved || activation.UseEvidence == UseEvidenceSelfReported || activation.UseEvidence == UseEvidenceUnavailable
	return mode && evidence && (activation.Mode != ActivationUnavailable || activation.UseEvidence == UseEvidenceUnavailable)
}

func activationCapabilities(activation Activation) []CapabilityID {
	capabilities := make([]CapabilityID, 0, 3)
	if activation.UseEvidence != UseEvidenceUnavailable {
		capabilities = append(capabilities, CapabilityActivationEvidence)
	}
	switch activation.Mode {
	case ActivationNative:
		capabilities = append(capabilities, CapabilityActivationNative)
	case ActivationForcedInjection:
		capabilities = append(capabilities, CapabilityActivationForcedInjection)
	case ActivationDeveloperInstructions:
		capabilities = append(capabilities, CapabilityActivationDeveloperInstructions)
	case ActivationDeveloperAndForced:
		capabilities = append(capabilities, CapabilityActivationDeveloperInstructions, CapabilityActivationForcedInjection)
	}
	return capabilities
}

func sortedIssues(issues map[IssueCode]struct{}) []IssueCode {
	result := make([]IssueCode, 0, len(issues))
	for issue := range issues {
		result = append(result, issue)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func cloneEvents(events []Event) []Event {
	result := make([]Event, len(events))
	for index, event := range events {
		result[index] = event
		if event.Start != nil {
			copyStart := *event.Start
			copyStart.Capabilities = append([]CapabilityID(nil), event.Start.Capabilities...)
			result[index].Start = &copyStart
		}
		if event.Terminal != nil {
			copyTerminal := *event.Terminal
			copyTerminal.Usage = cloneUsage(event.Terminal.Usage)
			result[index].Terminal = &copyTerminal
		}
		if event.Handoff != nil {
			copyHandoff := *event.Handoff
			result[index].Handoff = &copyHandoff
		}
	}
	return result
}

func cloneUsage(usage Usage) Usage {
	return Usage{
		EstimatedCostMicroUSD: cloneMetric(usage.EstimatedCostMicroUSD),
		InputTokens:           cloneMetric(usage.InputTokens),
		OutputTokens:          cloneMetric(usage.OutputTokens),
		ToolCalls:             cloneMetric(usage.ToolCalls),
		EvidenceItems:         cloneMetric(usage.EvidenceItems),
	}
}

func cloneMetric(metric Metric) Metric {
	if metric.Value == nil {
		return metric
	}
	value := *metric.Value
	metric.Value = &value
	return metric
}

func profileCapability(profile OrchestrationProfile) CapabilityID {
	switch profile {
	case ProfileSingle:
		return CapabilitySingle
	case ProfileGenericChild:
		return CapabilityGenericChild
	case ProfileSpecializedChildren:
		return CapabilitySpecializedChildren
	case ProfileParallelChildren:
		return CapabilityParallelChildren
	default:
		return ""
	}
}

func capabilitySupported(contract Contract, want CapabilityID) bool {
	for _, capability := range contract.Capabilities {
		if capability.ID == want {
			return capability.Support == SupportSupported
		}
	}
	return false
}

func capabilityPresent(capabilities []CapabilityID, want CapabilityID) bool {
	for _, capability := range capabilities {
		if capability == want {
			return true
		}
	}
	return false
}
