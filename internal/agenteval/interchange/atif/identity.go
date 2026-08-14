package atif

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
)

type sourceCore struct {
	Producer       Producer `json:"producer"`
	ModelName      string   `json:"model_name,omitempty"`
	DeclaredEvents uint32   `json:"declared_events"`
	Events         []Event  `json:"events"`
}

func sourceDigest(input EventSet) (string, error) {
	data, err := json.Marshal(sourceCore{
		Producer: input.Producer, ModelName: input.ModelName,
		DeclaredEvents: input.DeclaredEvents, Events: cloneEvents(input.Events),
	})
	if err != nil {
		return "", err
	}
	return digestDomain("source", data), nil
}

func sourceDigestFromDocument(document Document) (string, error) {
	events := make([]Event, len(document.Steps))
	for index, step := range document.Steps {
		var results []ObservationResult
		if step.Observation != nil {
			results = append([]ObservationResult(nil), step.Observation.Results...)
		}
		events[index] = Event{
			StepID: step.StepID, Timestamp: step.Timestamp, Role: step.Source,
			State: step.Extra.State, Message: step.Message,
			ToolCalls: append([]ToolCall(nil), step.ToolCalls...), Results: results,
		}
	}
	return sourceDigest(EventSet{
		Producer: document.Extra.Producer, ModelName: document.Agent.ModelName,
		DeclaredEvents: document.Extra.Coverage.DeclaredEvents, Events: events,
	})
}

func projectionDigest(document Document) (string, error) {
	copy := cloneDocument(document)
	copy.Extra.ProjectionSHA256 = ""
	data, err := json.Marshal(copy)
	if err != nil {
		return "", err
	}
	return digestDomain("projection", data), nil
}

func digestDomain(domain string, data []byte) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte("agent-eval/atif/" + domain + "/v1\x00"))
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(data)))
	_, _ = hash.Write(length[:])
	_, _ = hash.Write(data)
	return hex.EncodeToString(hash.Sum(nil))
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range []byte(value) {
		if character >= '0' && character <= '9' || character >= 'a' && character <= 'f' {
			continue
		}
		return false
	}
	return true
}

func cloneEvents(input []Event) []Event {
	if input == nil {
		return nil
	}
	output := make([]Event, len(input))
	for index, event := range input {
		output[index] = event
		output[index].ToolCalls = cloneToolCalls(event.ToolCalls)
		output[index].Results = append([]ObservationResult(nil), event.Results...)
		for callIndex := range output[index].ToolCalls {
			output[index].ToolCalls[callIndex].Arguments = append(json.RawMessage(nil), event.ToolCalls[callIndex].Arguments...)
		}
	}
	return output
}

func cloneToolCalls(input []ToolCall) []ToolCall {
	if input == nil {
		return nil
	}
	output := make([]ToolCall, len(input))
	copy(output, input)
	return output
}

func cloneDocument(input Document) Document {
	output := input
	output.Steps = make([]Step, len(input.Steps))
	for index, step := range input.Steps {
		output.Steps[index] = step
		output.Steps[index].ToolCalls = cloneToolCalls(step.ToolCalls)
		for callIndex := range output.Steps[index].ToolCalls {
			output.Steps[index].ToolCalls[callIndex].Arguments = append(json.RawMessage(nil), step.ToolCalls[callIndex].Arguments...)
		}
		if step.Observation != nil {
			output.Steps[index].Observation = &Observation{Results: append([]ObservationResult(nil), step.Observation.Results...)}
		}
	}
	return output
}
