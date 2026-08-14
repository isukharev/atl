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
	if len(input.Events) == 0 || len(input.Events) > MaxSteps || input.DeclaredEvents != uint32(len(input.Events)) {
		return Projection{}, fail(ErrorInvalidEventSet)
	}
	if err := validateProducer(input.Producer); err != nil {
		return Projection{}, fail(ErrorInvalidEventSet)
	}
	if !validOptionalText(input.ModelName, MaxIdentifier) {
		return Projection{}, fail(ErrorInvalidEventSet)
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
			Coverage: Coverage{DeclaredEvents: input.DeclaredEvents, ProjectedSteps: uint32(len(events)), Complete: true},
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
	if projection.SourceSHA256 != "" && projection.SourceSHA256 != projection.Document.Extra.SourceSHA256 {
		return fail(ErrorInvalidBinding)
	}
	if projection.ProjectionSHA256 != "" && projection.ProjectionSHA256 != projection.Document.Extra.ProjectionSHA256 {
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
	if len(events) == 0 || len(events) > MaxSteps || declared != uint32(len(events)) {
		return fail(ErrorInvalidEventSet)
	}
	seenCalls := make(map[string]struct{}, len(events))
	resolvedCalls := make(map[string]struct{}, len(events))
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
	}
	if len(resolvedCalls) != len(seenCalls) {
		return fail(ErrorInvalidObservation)
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
		!validDigest(document.Extra.ProjectionSHA256) || document.Extra.Coverage.DeclaredEvents != uint32(len(document.Steps)) ||
		document.Extra.Coverage.ProjectedSteps != uint32(len(document.Steps)) || !document.Extra.Coverage.Complete {
		return fail(ErrorInvalidBinding)
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
	return nil
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
