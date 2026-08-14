package atif

import (
	"bytes"
	"encoding/json"
	"time"
	"unicode/utf8"
)

// Seal validates a complete normalized event set and creates a content-bound
// owner-private ATIF projection. It never reads a provider or filesystem.
func Seal(input EventSet) (Projection, error) {
	if len(input.Events) == 0 || len(input.Events) > MaxSteps || !matchesCount(input.DeclaredEvents, len(input.Events)) {
		return Projection{}, fail(ErrorInvalidEventSet)
	}
	if err := validateProducer(input.Producer); err != nil {
		return Projection{}, fail(ErrorInvalidEventSet)
	}
	if !validOptionalText(input.ModelName, MaxIdentifier) {
		return Projection{}, fail(ErrorInvalidEventSet)
	}
	if err := preflightEvents(input.Events, input.DeclaredEvents); err != nil {
		return Projection{}, err
	}
	events := cloneEvents(input.Events)
	if err := validateEvents(events, input.DeclaredEvents); err != nil {
		return Projection{}, err
	}
	sourceSHA256, err := sourceDigest(EventSet{
		Producer: input.Producer, ModelName: input.ModelName,
		DeclaredEvents: input.DeclaredEvents, Events: events,
	})
	if err != nil {
		return Projection{}, fail(ErrorInvalidEventSet)
	}
	if input.SourceSHA256 != "" && (!validDigest(input.SourceSHA256) || input.SourceSHA256 != sourceSHA256) {
		return Projection{}, fail(ErrorInvalidEventSet)
	}

	document := Document{
		SchemaVersion: ATIFSchemaVersion,
		Agent:         Agent{Name: input.Producer.Name, Version: input.Producer.Version, ModelName: input.ModelName},
		Steps:         make([]Step, len(events)),
		Extra: Binding{
			Schema: BindingSchema, Version: BindingVersion,
			SourceSHA256: sourceSHA256, Producer: input.Producer,
			Privacy:  PrivacyOwnerPrivate,
			Coverage: Coverage{DeclaredEvents: input.DeclaredEvents, ProjectedSteps: input.DeclaredEvents, Complete: true},
		},
	}
	for index, event := range events {
		document.Steps[index] = Step{
			StepID: event.StepID, Timestamp: event.Timestamp, Source: event.Role,
			Message: event.Message, ToolCalls: cloneToolCalls(event.ToolCalls),
			Extra: StepExtra{State: event.State},
		}
		for callIndex := range document.Steps[index].ToolCalls {
			document.Steps[index].ToolCalls[callIndex].Arguments = append(json.RawMessage(nil), event.ToolCalls[callIndex].Arguments...)
		}
		if len(event.Results) != 0 {
			document.Steps[index].Observation = &Observation{Results: append([]ObservationResult(nil), event.Results...)}
		}
	}
	projectionSHA256, err := projectionDigest(document)
	if err != nil {
		return Projection{}, fail(ErrorInvalidProjection)
	}
	document.Extra.ProjectionSHA256 = projectionSHA256
	projection := Projection{Document: document, SourceSHA256: sourceSHA256, ProjectionSHA256: projectionSHA256}
	if err := Validate(projection); err != nil {
		return Projection{}, err
	}
	return cloneProjection(projection), nil
}

// Project is the descriptive alias used by composition roots that treat the
// ATIF document as an interchange projection rather than a storage seal.
func Project(input EventSet) (Projection, error) { return Seal(input) }

// Validate checks a sealed projection without mutating it or touching the
// destination filesystem.
func Validate(projection Projection) error {
	if err := validateDocument(projection.Document); err != nil {
		return err
	}
	if projection.SourceSHA256 != projection.Document.Extra.SourceSHA256 {
		return fail(ErrorInvalidBinding)
	}
	if projection.ProjectionSHA256 != projection.Document.Extra.ProjectionSHA256 {
		return fail(ErrorInvalidProjection)
	}
	sourceSHA256, err := sourceDigestFromDocument(projection.Document)
	if err != nil || sourceSHA256 != projection.Document.Extra.SourceSHA256 {
		return fail(ErrorInvalidBinding)
	}
	digest, err := projectionDigest(projection.Document)
	if err != nil || digest != projection.Document.Extra.ProjectionSHA256 {
		return fail(ErrorInvalidProjection)
	}
	return nil
}

func validateProducer(producer Producer) error {
	if !validIdentifier(producer.Name) || !validIdentifier(producer.Version) {
		return fail(ErrorInvalidEventSet)
	}
	return nil
}

func validateEvents(events []Event, declared uint32) error {
	if len(events) == 0 || len(events) > MaxSteps || !matchesCount(declared, len(events)) {
		return fail(ErrorInvalidEventSet)
	}
	toolCalls := 0
	results := 0
	for index, event := range events {
		if event.StepID != uint32(index+1) || !validRole(event.Role) || !validState(event.State) ||
			!validOptionalText(event.Timestamp, MaxIdentifier) || !validText(event.Message, MaxTextBytes) {
			return fail(ErrorInvalidEvent)
		}
		if event.Timestamp != "" {
			parsed, err := time.Parse(time.RFC3339Nano, event.Timestamp)
			if err != nil || parsed.Format(time.RFC3339Nano) != event.Timestamp {
				return fail(ErrorInvalidEvent)
			}
		}
		if event.Role != RoleAgent && len(event.ToolCalls) != 0 {
			return fail(ErrorInvalidToolCall)
		}
		if event.Role == RoleUser && len(event.Results) != 0 {
			return fail(ErrorInvalidObservation)
		}
		if len(event.ToolCalls) > MaxToolCalls-toolCalls {
			return fail(ErrorLimitExceeded)
		}
		toolCalls += len(event.ToolCalls)
		seenCalls := make(map[string]struct{}, len(event.ToolCalls))
		resolvedCalls := make(map[string]struct{}, len(event.Results))
		for _, call := range event.ToolCalls {
			if !validToolCall(call) {
				return fail(ErrorInvalidToolCall)
			}
			if _, duplicate := seenCalls[call.ToolCallID]; duplicate {
				return fail(ErrorInvalidToolCall)
			}
			seenCalls[call.ToolCallID] = struct{}{}
		}
		if len(event.Results) > MaxResults-results {
			return fail(ErrorLimitExceeded)
		}
		results += len(event.Results)
		for _, result := range event.Results {
			if !validObservationResult(result) {
				return fail(ErrorInvalidObservation)
			}
			if result.SourceCallID == "" {
				continue
			}
			if _, exists := seenCalls[result.SourceCallID]; !exists {
				return fail(ErrorInvalidObservation)
			}
			if _, duplicate := resolvedCalls[result.SourceCallID]; duplicate {
				return fail(ErrorInvalidObservation)
			}
			resolvedCalls[result.SourceCallID] = struct{}{}
		}
		if len(resolvedCalls) != len(seenCalls) {
			return fail(ErrorInvalidObservation)
		}
	}
	return nil
}

func validateDocument(document Document) error {
	if document.SchemaVersion != ATIFSchemaVersion || document.Steps == nil || len(document.Steps) == 0 || len(document.Steps) > MaxSteps {
		return fail(ErrorInvalidProjection)
	}
	if err := validateProducer(document.Extra.Producer); err != nil {
		return fail(ErrorInvalidBinding)
	}
	if document.Agent.Name != document.Extra.Producer.Name || document.Agent.Version != document.Extra.Producer.Version ||
		!validOptionalText(document.Agent.ModelName, MaxIdentifier) {
		return fail(ErrorInvalidProjection)
	}
	if document.Extra.Schema != BindingSchema || document.Extra.Version != BindingVersion ||
		document.Extra.Privacy != PrivacyOwnerPrivate || !validDigest(document.Extra.SourceSHA256) ||
		!validDigest(document.Extra.ProjectionSHA256) || !matchesCount(document.Extra.Coverage.DeclaredEvents, len(document.Steps)) ||
		document.Extra.Coverage.ProjectedSteps != document.Extra.Coverage.DeclaredEvents || !document.Extra.Coverage.Complete {
		return fail(ErrorInvalidBinding)
	}
	if err := preflightSteps(document.Steps, document.Extra.Coverage.DeclaredEvents); err != nil {
		return err
	}
	events := make([]Event, len(document.Steps))
	for index, step := range document.Steps {
		if step.Observation != nil && len(step.Observation.Results) == 0 {
			return fail(ErrorInvalidObservation)
		}
		var results []ObservationResult
		if step.Observation != nil {
			results = append([]ObservationResult(nil), step.Observation.Results...)
		}
		events[index] = Event{
			StepID: step.StepID, Timestamp: step.Timestamp, Role: step.Source,
			State: step.Extra.State, Message: step.Message,
			ToolCalls: cloneToolCalls(step.ToolCalls), Results: results,
		}
	}
	if err := validateEvents(events, document.Extra.Coverage.DeclaredEvents); err != nil {
		return err
	}
	return validateCanonicalDocumentSize(document)
}

func validateCanonicalDocumentSize(document Document) error {
	data, err := json.Marshal(document)
	if err != nil {
		return fail(ErrorInvalidProjection)
	}
	if len(data)+1 > MaxDocumentBytes {
		return fail(ErrorLimitExceeded)
	}
	return nil
}

func matchesCount(declared uint32, actual int) bool {
	return actual >= 0 && uint64(declared) == uint64(actual)
}

// preflightEvents and preflightSteps inspect caller-owned slices without
// cloning or decoding their nested payloads. This keeps hostile in-memory
// inputs bounded before the owned snapshot and digest allocations below.
func preflightEvents(events []Event, declared uint32) error {
	if len(events) == 0 || len(events) > MaxSteps || !matchesCount(declared, len(events)) {
		return fail(ErrorInvalidEventSet)
	}
	var bytes uint64
	toolCalls, results := 0, 0
	for _, event := range events {
		if err := preflightEvent(event, &toolCalls, &results, &bytes); err != nil {
			return err
		}
	}
	return nil
}

func preflightSteps(steps []Step, declared uint32) error {
	if len(steps) == 0 || len(steps) > MaxSteps || !matchesCount(declared, len(steps)) {
		return fail(ErrorInvalidProjection)
	}
	var bytes uint64
	toolCalls, results := 0, 0
	for _, step := range steps {
		var stepResults []ObservationResult
		if step.Observation != nil {
			stepResults = step.Observation.Results
		}
		event := Event{
			Timestamp: step.Timestamp, Message: step.Message,
			ToolCalls: step.ToolCalls, Results: stepResults,
		}
		if err := preflightEvent(event, &toolCalls, &results, &bytes); err != nil {
			return err
		}
	}
	return nil
}

func preflightEvent(event Event, toolCalls, results *int, bytes *uint64) error {
	if len(event.Message) > MaxTextBytes || len(event.Timestamp) > MaxIdentifier {
		return fail(ErrorInvalidEvent)
	}
	if len(event.ToolCalls) > MaxToolCalls-*toolCalls {
		return fail(ErrorLimitExceeded)
	}
	if len(event.Results) > MaxResults-*results {
		return fail(ErrorLimitExceeded)
	}
	*toolCalls += len(event.ToolCalls)
	*results += len(event.Results)
	if !addPreflightBytes(bytes, len(event.Message)) || !addPreflightBytes(bytes, len(event.Timestamp)) {
		return fail(ErrorLimitExceeded)
	}
	for _, call := range event.ToolCalls {
		if len(call.ToolCallID) > MaxIdentifier || len(call.FunctionName) > MaxIdentifier || len(call.Arguments) > MaxArgumentBytes {
			return fail(ErrorInvalidToolCall)
		}
		if !addPreflightBytes(bytes, len(call.ToolCallID)) || !addPreflightBytes(bytes, len(call.FunctionName)) || !addPreflightBytes(bytes, len(call.Arguments)) {
			return fail(ErrorLimitExceeded)
		}
	}
	for _, result := range event.Results {
		if len(result.SourceCallID) > MaxIdentifier || len(result.Content) > MaxTextBytes {
			return fail(ErrorInvalidObservation)
		}
		if !addPreflightBytes(bytes, len(result.SourceCallID)) || !addPreflightBytes(bytes, len(result.Content)) {
			return fail(ErrorLimitExceeded)
		}
	}
	return nil
}

func addPreflightBytes(total *uint64, amount int) bool {
	if amount < 0 || *total > MaxDocumentBytes-uint64(amount) {
		return false
	}
	*total += uint64(amount)
	return true
}

func validToolCall(call ToolCall) bool {
	if !validIdentifier(call.ToolCallID) || !validIdentifier(call.FunctionName) || len(call.Arguments) > MaxArgumentBytes {
		return false
	}
	canonical, err := canonicalJSONObject(call.Arguments)
	return err == nil && bytes.Equal(canonical, call.Arguments)
}

func validObservationResult(result ObservationResult) bool {
	return (result.SourceCallID == "" || validIdentifier(result.SourceCallID)) && validText(result.Content, MaxTextBytes)
}

func validRole(role Role) bool {
	return role == RoleSystem || role == RoleUser || role == RoleAgent
}

func validState(state State) bool {
	return state == StateStarted || state == StateCompleted || state == StateFailed || state == StateCanceled || state == StateSkipped
}

func validIdentifier(value string) bool {
	if value == "" || len(value) > MaxIdentifier || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func validOptionalText(value string, maximum int) bool {
	return value == "" || validText(value, maximum)
}

func validText(value string, maximum int) bool {
	return len(value) <= maximum && utf8.ValidString(value)
}

func cloneProjection(input Projection) Projection {
	output := input
	output.Document = cloneDocument(input.Document)
	return output
}
