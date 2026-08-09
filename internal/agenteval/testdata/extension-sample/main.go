// Command extension-sample is a dependency-free, out-of-package protocol-v1
// fixture. It intentionally imports no evaluator Go package: the process wire
// is the extension boundary.
package main

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
)

type capabilityClaim struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

type initializePayload struct {
	OfferedProtocolVersions []int    `json:"offered_protocol_versions"`
	RequiredCapabilities    []string `json:"required_capabilities"`
}

type initializedPayload struct {
	SelectedProtocolVersion int               `json:"selected_protocol_version"`
	Capabilities            []capabilityClaim `json:"capabilities"`
}

type invokePayload struct {
	InvocationID  string            `json:"invocation_id"`
	Control       string            `json:"control"`
	Operation     string            `json:"operation"`
	Configuration []json.RawMessage `json:"configuration"`
	Inputs        []json.RawMessage `json:"inputs"`
	Policy        json.RawMessage   `json:"policy"`
}

type resultPayload struct {
	InvocationID string            `json:"invocation_id"`
	Operation    string            `json:"operation"`
	Outputs      []json.RawMessage `json:"outputs"`
}

type cancelPayload struct {
	InvocationID string `json:"invocation_id"`
	Operation    string `json:"operation"`
}

type frame struct {
	Schema           string              `json:"schema"`
	SchemaVersion    int                 `json:"schema_version"`
	ProtocolVersion  int                 `json:"protocol_version"`
	Direction        string              `json:"direction"`
	SessionID        string              `json:"session_id"`
	AttemptID        string              `json:"attempt_id"`
	Sequence         uint32              `json:"sequence"`
	Role             string              `json:"role"`
	ComponentID      string              `json:"component_id"`
	ComponentVersion string              `json:"component_version"`
	ExecutableSHA256 string              `json:"executable_sha256"`
	Type             string              `json:"type"`
	Initialize       *initializePayload  `json:"initialize,omitempty"`
	Initialized      *initializedPayload `json:"initialized,omitempty"`
	Invoke           *invokePayload      `json:"invoke,omitempty"`
	Result           *resultPayload      `json:"result,omitempty"`
	Cancel           *cancelPayload      `json:"cancel,omitempty"`
	Canceled         *cancelPayload      `json:"canceled,omitempty"`
}

func main() {
	reader := bufio.NewReader(os.Stdin)
	initialize, ok := readFrame(reader)
	if !ok || initialize.Type != "initialize" || initialize.Initialize == nil {
		os.Exit(2)
	}
	claims := make([]capabilityClaim, len(initialize.Initialize.RequiredCapabilities))
	for index, id := range initialize.Initialize.RequiredCapabilities {
		claims[index] = capabilityClaim{ID: id, State: "supported"}
	}
	initialized := response(initialize)
	initialized.Sequence = 2
	initialized.Type = "initialized"
	initialized.Initialized = &initializedPayload{SelectedProtocolVersion: 1, Capabilities: claims}
	if !writeFrame(initialized) {
		os.Exit(3)
	}

	invoke, ok := readFrame(reader)
	if !ok || invoke.Type != "invoke" || invoke.Invoke == nil {
		os.Exit(4)
	}
	if invoke.Invoke.Control == "await_cancel" {
		cancel, ok := readFrame(reader)
		if !ok || cancel.Type != "cancel" || cancel.Cancel == nil ||
			cancel.Cancel.InvocationID != invoke.Invoke.InvocationID || cancel.Cancel.Operation != invoke.Invoke.Operation {
			os.Exit(7)
		}
		canceled := response(cancel)
		canceled.Sequence = 5
		canceled.Type = "canceled"
		canceled.Canceled = &cancelPayload{InvocationID: cancel.Cancel.InvocationID, Operation: cancel.Cancel.Operation}
		if !writeFrame(canceled) {
			os.Exit(8)
		}
		if _, err := reader.ReadByte(); err != io.EOF {
			os.Exit(9)
		}
		return
	}
	if invoke.Invoke.Control != "execute" {
		os.Exit(10)
	}
	result := response(invoke)
	result.Sequence = 4
	result.Type = "result"
	result.Result = &resultPayload{InvocationID: invoke.Invoke.InvocationID, Operation: invoke.Invoke.Operation, Outputs: []json.RawMessage{}}
	if !writeFrame(result) {
		os.Exit(5)
	}
	if _, err := reader.ReadByte(); err != io.EOF {
		os.Exit(6)
	}
}

func readFrame(reader *bufio.Reader) (frame, bool) {
	line, err := reader.ReadBytes('\n')
	if err != nil || len(line) < 2 || len(line) > 1<<20+1 {
		return frame{}, false
	}
	var value frame
	if err := json.Unmarshal(line[:len(line)-1], &value); err != nil {
		return frame{}, false
	}
	return value, true
}

func writeFrame(value frame) bool {
	data, err := json.Marshal(value)
	if err != nil || len(data) > 1<<20 {
		return false
	}
	data = append(data, '\n')
	written, err := os.Stdout.Write(data)
	return err == nil && written == len(data)
}

func response(request frame) frame {
	return frame{
		Schema: request.Schema, SchemaVersion: request.SchemaVersion, ProtocolVersion: request.ProtocolVersion,
		Direction: "extension_to_host", SessionID: request.SessionID, AttemptID: request.AttemptID,
		Role: request.Role, ComponentID: request.ComponentID, ComponentVersion: request.ComponentVersion,
		ExecutableSHA256: request.ExecutableSHA256,
	}
}
