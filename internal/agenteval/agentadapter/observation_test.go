package agentadapter

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestObservationSeparatesParentTreeAndConsumedEvidence(t *testing.T) {
	contract := testContract(t)
	parentUsage := Usage{EstimatedCostMicroUSD: ObservedMetric(10), InputTokens: ObservedMetric(20), OutputTokens: ObservedMetric(3), ToolCalls: ObservedMetric(1), EvidenceItems: ObservedMetric(2)}
	childUsage := Usage{EstimatedCostMicroUSD: ObservedMetric(4), InputTokens: ObservedMetric(5), OutputTokens: ObservedMetric(1), ToolCalls: ObservedMetric(2), EvidenceItems: ObservedMetric(7)}
	capabilities := []CapabilityID{CapabilityActivationEvidence, CapabilityActivationForcedInjection, CapabilityActivationNative,
		CapabilityGenericChild, CapabilityTrajectory}
	events := []Event{
		{Sequence: 1, NodeID: "parent", Type: EventStart, Start: &Start{Role: RolePrimary, Capabilities: capabilities, Activation: Activation{Mode: ActivationNative, UseEvidence: UseEvidenceObserved}}},
		{Sequence: 2, NodeID: "child", ParentID: "parent", Type: EventStart, Start: &Start{Role: RoleGenericChild, Capabilities: []CapabilityID{CapabilityActivationEvidence, CapabilityActivationForcedInjection}, Activation: Activation{Mode: ActivationForcedInjection, UseEvidence: UseEvidenceSelfReported}}},
		{Sequence: 3, NodeID: "child", ParentID: "parent", Type: EventTerminal, Terminal: &Terminal{State: TerminalSucceeded, Usage: childUsage}},
		{Sequence: 4, NodeID: "child", ParentID: "parent", Type: EventHandoff, Handoff: &Handoff{Consumed: true}},
		{Sequence: 5, NodeID: "parent", Type: EventTerminal, Terminal: &Terminal{State: TerminalSucceeded, Usage: parentUsage}},
	}
	observation, err := Normalize(contract, strings.Repeat("d", 64), ProfileGenericChild, events)
	if err != nil {
		t.Fatal(err)
	}
	if !observation.Coverage || len(observation.Issues) != 0 || !reflect.DeepEqual(observation.ParentUsage, parentUsage) {
		t.Fatalf("observation=%+v", observation)
	}
	if got := observation.TreeUsage.InputTokens; got.State != MetricObserved || got.Value == nil || *got.Value != 25 {
		t.Fatalf("tree input=%+v", got)
	}
	if got := observation.ConsumedChildEvidence; got.State != MetricObserved || got.Value == nil || *got.Value != 7 {
		t.Fatalf("consumed evidence=%+v", got)
	}
	*parentUsage.InputTokens.Value = 999
	if got := observation.ParentUsage.InputTokens; got.Value == nil || *got.Value != 20 {
		t.Fatalf("caller mutation changed normalized parent usage: %+v", got)
	}
	notConsumedEvents := cloneEvents(events)
	notConsumedEvents[3].Handoff.Consumed = false
	notConsumed, err := Normalize(contract, strings.Repeat("f", 64), ProfileGenericChild, notConsumedEvents)
	if err != nil || notConsumed.ConsumedChildEvidence.State != MetricNotApplicable ||
		notConsumed.ConsumedChildEvidence.Value != nil {
		t.Fatalf("unused child evidence was counted: observation=%+v err=%v", notConsumed, err)
	}
	reported := parentUsage
	reported.InputTokens = ObservedMetric(99)
	unattributed, err := NormalizeWithReportedTreeUsage(contract, strings.Repeat("e", 64), ProfileGenericChild, events, reported)
	if err != nil || unattributed.Coverage || !containsIssue(unattributed.Issues, IssueTreeUnattributed) ||
		unattributed.TreeUsage.InputTokens.Value == nil || *unattributed.TreeUsage.InputTokens.Value != 99 {
		t.Fatalf("unattributed=%+v err=%v", unattributed, err)
	}
	data, err := EncodeObservation(contract, observation)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeObservation(bytes.NewReader(data), contract)
	if err != nil || !reflect.DeepEqual(decoded, observation) {
		t.Fatalf("decode=%+v err=%v", decoded, err)
	}
	future := bytes.Replace(data, []byte(`"schema_version":1`), []byte(`"schema_version":2`), 1)
	if _, err := DecodeObservation(bytes.NewReader(future), contract); !errors.Is(err, ErrContract) {
		t.Fatalf("future error=%v", err)
	}
}

func TestObservationMakesMalformedTreesUncovered(t *testing.T) {
	contract := testContract(t)
	usage := UnknownUsage()
	base := []Event{
		{Sequence: 1, NodeID: "parent", Type: EventStart, Start: &Start{Role: RolePrimary, Capabilities: []CapabilityID{CapabilitySingle}, Activation: Activation{Mode: ActivationUnavailable, UseEvidence: UseEvidenceUnavailable}}},
		{Sequence: 2, NodeID: "parent", Type: EventTerminal, Terminal: &Terminal{State: TerminalSucceeded, Usage: usage}},
	}
	tests := map[string]struct {
		events  []Event
		profile OrchestrationProfile
		issue   IssueCode
	}{
		"sequence gap":       {events: []Event{base[0], withSequence(base[1], 3)}, profile: ProfileSingle, issue: IssueSequenceGap},
		"duplicate sequence": {events: []Event{base[0], withSequence(base[1], 1)}, profile: ProfileSingle, issue: IssueDuplicateSequence},
		"duplicate start":    {events: []Event{base[0], withSequence(base[0], 2), withSequence(base[1], 3)}, profile: ProfileSingle, issue: IssueDuplicateStart},
		"duplicate terminal": {events: []Event{base[0], base[1], withSequence(base[1], 3)}, profile: ProfileSingle, issue: IssueDuplicateTerminal},
		"unknown terminal":   {events: []Event{base[0], {Sequence: 2, NodeID: "parent", Type: EventTerminal, Terminal: &Terminal{State: TerminalUnknown, Usage: usage}}}, profile: ProfileSingle, issue: IssueUnknownTerminal},
		"incomplete":         {events: base[:1], profile: ProfileSingle, issue: IssueIncomplete},
		"orphan":             {events: []Event{base[0], {Sequence: 2, NodeID: "child", ParentID: "missing", Type: EventStart, Start: &Start{Role: RoleGenericChild, Capabilities: []CapabilityID{CapabilitySingle}, Activation: Activation{Mode: ActivationUnavailable, UseEvidence: UseEvidenceUnavailable}}}, withSequence(base[1], 3)}, profile: ProfileGenericChild, issue: IssueOrphan},
		"capability subset":  {events: []Event{base[0], {Sequence: 2, NodeID: "child", ParentID: "parent", Type: EventStart, Start: &Start{Role: RoleGenericChild, Capabilities: []CapabilityID{CapabilityMCP}, Activation: Activation{Mode: ActivationUnavailable, UseEvidence: UseEvidenceUnavailable}}}, {Sequence: 3, NodeID: "child", ParentID: "parent", Type: EventTerminal, Terminal: &Terminal{State: TerminalSucceeded, Usage: usage}}, {Sequence: 4, NodeID: "child", ParentID: "parent", Type: EventHandoff, Handoff: &Handoff{Consumed: false}}, withSequence(base[1], 5)}, profile: ProfileGenericChild, issue: IssueCapabilitySubset},
		"profile mismatch":   {events: base, profile: ProfileGenericChild, issue: IssueProfile},
		"cycle": {events: append(append([]Event{base[0]}, graphNodeEvents(2, "first", "second", RoleGenericChild, usage)...),
			graphNodeEvents(4, "second", "first", RoleGenericChild, usage)...),
			profile: ProfileParallelChildren, issue: IssueCycle},
		"over depth": {events: append(append(append([]Event{base[0]}, graphNodeEvents(2, "one", "parent", RoleSpecializedChild, usage)...),
			graphNodeEvents(4, "two", "one", RoleSpecializedChild, usage)...), graphNodeEvents(6, "three", "two", RoleSpecializedChild, usage)...),
			profile: ProfileSpecializedChildren, issue: IssueOverDepth},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			observation, err := Normalize(contract, strings.Repeat("d", 64), test.profile, test.events)
			if err != nil {
				t.Fatal(err)
			}
			if observation.Coverage || !containsIssue(observation.Issues, test.issue) {
				t.Fatalf("coverage=%v issues=%v", observation.Coverage, observation.Issues)
			}
			if _, err := EncodeObservation(contract, observation); err != nil {
				t.Fatalf("content-minimized uncovered projection must remain readable: %v", err)
			}
		})
	}
}

func graphNodeEvents(sequence uint32, id, parent string, role Role, usage Usage) []Event {
	return []Event{{Sequence: sequence, NodeID: id, ParentID: parent, Type: EventStart,
		Start: &Start{Role: role, Capabilities: []CapabilityID{CapabilitySingle},
			Activation: Activation{Mode: ActivationUnavailable, UseEvidence: UseEvidenceUnavailable}}},
		{Sequence: sequence + 1, NodeID: id, ParentID: parent, Type: EventTerminal,
			Terminal: &Terminal{State: TerminalSucceeded, Usage: usage}}}
}

func TestMetricsPreserveUnknownNotApplicableAndObservedZero(t *testing.T) {
	zero := ObservedMetric(0)
	if zero.Value == nil || *zero.Value != 0 || !validMetric(zero) || validMetric(Metric{State: MetricUnknown, Value: zero.Value}) {
		t.Fatalf("zero/unknown semantics drifted: zero=%+v", zero)
	}
	if got := aggregateMetricValues([]Metric{ObservedMetric(0), UnknownMetric()}); got.State != MetricUnknown || got.Value != nil {
		t.Fatalf("unknown was imputed: %+v", got)
	}
	if got := aggregateMetricValues([]Metric{NotApplicableMetric(), NotApplicableMetric()}); got.State != MetricNotApplicable || got.Value != nil {
		t.Fatalf("not-applicable changed: %+v", got)
	}
	if got := aggregateMetricValues([]Metric{ObservedMetric(0), UnsupportedMetric()}); got.State != MetricUnsupported || got.Value != nil {
		t.Fatalf("unsupported changed: %+v", got)
	}
}

func TestNormalizeRejectsMalformedEventsWithoutPanicking(t *testing.T) {
	contract := testContract(t)
	for name, event := range map[string]Event{
		"nil start":    {Sequence: 1, NodeID: "primary", Type: EventStart},
		"nil terminal": {Sequence: 1, NodeID: "primary", Type: EventTerminal},
		"unknown type": {Sequence: 1, NodeID: "primary", Type: "future"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Normalize(contract, strings.Repeat("d", 64), ProfileSingle, []Event{event}); !errors.Is(err, ErrContract) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestMalformedMultiplePrimaryProjectionIsDeterministic(t *testing.T) {
	contract := testContract(t)
	firstUsage := UnknownUsage()
	firstUsage.InputTokens = ObservedMetric(1)
	secondUsage := UnknownUsage()
	secondUsage.InputTokens = ObservedMetric(2)
	events := []Event{
		{Sequence: 1, NodeID: "first", Type: EventStart, Start: &Start{Role: RolePrimary,
			Capabilities: []CapabilityID{CapabilitySingle}, Activation: Activation{Mode: ActivationUnavailable, UseEvidence: UseEvidenceUnavailable}}},
		{Sequence: 2, NodeID: "second", Type: EventStart, Start: &Start{Role: RolePrimary,
			Capabilities: []CapabilityID{CapabilitySingle}, Activation: Activation{Mode: ActivationUnavailable, UseEvidence: UseEvidenceUnavailable}}},
		{Sequence: 3, NodeID: "second", Type: EventTerminal, Terminal: &Terminal{State: TerminalSucceeded, Usage: secondUsage}},
		{Sequence: 4, NodeID: "first", Type: EventTerminal, Terminal: &Terminal{State: TerminalSucceeded, Usage: firstUsage}},
	}
	var baseline Observation
	for iteration := 0; iteration < 32; iteration++ {
		observation, err := Normalize(contract, strings.Repeat("d", 64), ProfileSingle, events)
		if err != nil || observation.Coverage || !containsIssue(observation.Issues, IssueMissingPrimary) {
			t.Fatalf("observation=%+v err=%v", observation, err)
		}
		if iteration == 0 {
			baseline = observation
		} else if !reflect.DeepEqual(observation, baseline) {
			t.Fatalf("malformed projection drifted:\nfirst=%+v\nnext=%+v", baseline, observation)
		}
	}
}

func withSequence(event Event, sequence uint32) Event {
	event.Sequence = sequence
	return event
}

func containsIssue(values []IssueCode, want IssueCode) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
