package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	standaloneProcessMaxRequestBytes   = 1 << 20
	standaloneProcessMaxResponseBytes  = 1 << 20
	standaloneProcessMaxDeadline       = 15 * time.Minute
	standaloneProcessCancellationGrace = 100 * time.Millisecond
)

type standaloneProcessConfiguration struct {
	Source      string `json:"source"`
	Path        string `json:"path,omitempty"`
	Environment string `json:"environment"`
}

type standaloneProcessRequest struct {
	Schema               string                         `json:"schema"`
	SchemaVersion        int                            `json:"schema_version"`
	ContractVersion      string                         `json:"contract_version"`
	Command              string                         `json:"command"`
	Mode                 string                         `json:"mode"`
	DeadlineMilliseconds int                            `json:"deadline_milliseconds"`
	Configuration        standaloneProcessConfiguration `json:"configuration"`
	Arguments            standaloneProcessArguments     `json:"arguments"`
}

type standaloneProcessArguments []string

func (arguments *standaloneProcessArguments) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return fmt.Errorf("process arguments must be an array")
	}
	var values []string
	if err := json.Unmarshal(data, &values); err != nil {
		return err
	}
	if values == nil {
		return fmt.Errorf("process arguments must be an array")
	}
	*arguments = values
	return nil
}

type standaloneProcessExecution struct {
	outcome standaloneOutcome
	failure *standaloneFailure
}

type standaloneProcessExecutor func(context.Context, []string) (standaloneOutcome, *standaloneFailure)

func runStandaloneProcess(stdin io.Reader, stdout, stderr io.Writer, args []string) *standaloneFailure {
	return runStandaloneProcessWithExecutor(stdin, stdout, stderr, args, executeStandaloneContext)
}

func runStandaloneProcessWithExecutor(
	stdin io.Reader,
	stdout, stderr io.Writer,
	args []string,
	execute standaloneProcessExecutor,
) *standaloneFailure {
	if len(args) != 0 {
		failure := standaloneFail(standaloneUsageError, "process_accepts_no_arguments")
		standaloneWriteFailure(stderr, failure)
		return failure
	}
	data, err := io.ReadAll(io.LimitReader(stdin, standaloneProcessMaxRequestBytes+1))
	if err != nil || len(data) > standaloneProcessMaxRequestBytes {
		failure := standaloneFail(standaloneInputError, "process_request_too_large")
		standaloneWriteFailure(stdout, failure)
		return failure
	}
	var request standaloneProcessRequest
	if err := standaloneDecodeClosedJSON(data, &request, 32, 256); err != nil {
		failure := standaloneFail(standaloneInputError, "invalid_process_request")
		standaloneWriteFailure(stdout, failure)
		return failure
	}
	commandArgs, failure := standaloneValidateProcessRequest(request)
	if failure != nil {
		standaloneWriteFailure(stdout, failure)
		return failure
	}

	deadline := time.Duration(request.DeadlineMilliseconds) * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	completed := make(chan standaloneProcessExecution, 1)
	go func() {
		deferred := standaloneProcessExecution{}
		defer func() {
			if recover() != nil {
				deferred = standaloneProcessExecution{failure: standaloneFail(standaloneInternalError, "command_panicked")}
			}
			completed <- deferred
		}()
		deferred.outcome, deferred.failure = execute(ctx, commandArgs)
		deferred.outcome.outputMode = "json"
	}()

	var execution standaloneProcessExecution
	select {
	case execution = <-completed:
	case <-ctx.Done():
		cancel()
		grace := time.NewTimer(standaloneProcessCancellationGrace)
		select {
		case <-completed:
			if !grace.Stop() {
				select {
				case <-grace.C:
				default:
				}
			}
			execution.failure = standaloneFail(standaloneInterruptedError, "deadline_exceeded")
			execution.failure.retrySafe = true
		case <-grace.C:
			execution.failure = standaloneFail(standaloneOutcomeUnknownError, "deadline_outcome_unknown")
		}
	}
	response := &standaloneBoundedBuffer{maximum: standaloneProcessMaxResponseBytes}
	if execution.failure != nil {
		standaloneWriteFailure(response, execution.failure)
	} else if err := standaloneWriteOutcome(response, execution.outcome); err != nil {
		execution.failure = standaloneFail(standaloneInternalError, "process_response_too_large")
		response = &standaloneBoundedBuffer{maximum: standaloneProcessMaxResponseBytes}
		standaloneWriteFailure(response, execution.failure)
	}
	if response.overflow {
		execution.failure = standaloneFail(standaloneInternalError, "process_response_too_large")
		response = &standaloneBoundedBuffer{maximum: standaloneProcessMaxResponseBytes}
		standaloneWriteFailure(response, execution.failure)
	}
	if _, err := io.Copy(stdout, bytes.NewReader(response.Bytes())); err != nil {
		return standaloneFail(standaloneInternalError, "process_output_failed")
	}
	return execution.failure
}

func standaloneValidateProcessRequest(request standaloneProcessRequest) ([]string, *standaloneFailure) {
	if request.Schema != "agent-eval/process-request" || request.SchemaVersion != 1 || request.ContractVersion != standaloneContractVersion {
		return nil, standaloneFail(standaloneCompatibilityError, "unsupported_process_version")
	}
	if request.Mode != "execute" && request.Mode != "dry-run" && request.Mode != "explain" {
		return nil, standaloneFail(standaloneUsageError, "invalid_process_mode")
	}
	if request.DeadlineMilliseconds < 1 || request.DeadlineMilliseconds > int(standaloneProcessMaxDeadline/time.Millisecond) {
		return nil, standaloneFail(standaloneUsageError, "invalid_process_deadline")
	}
	if request.Command == "" || strings.ContainsAny(request.Command, " \t\r\n") {
		return nil, standaloneFail(standaloneUsageError, "invalid_process_command")
	}
	if request.Arguments == nil {
		return nil, standaloneFail(standaloneInputError, "invalid_process_arguments")
	}
	if standaloneProcessForbiddenMaintainerCommand(request.Command) {
		return nil, standaloneFail(standaloneCompatibilityError, "operation_unavailable")
	}
	for _, argument := range request.Arguments {
		if argument == "" || strings.IndexByte(argument, 0) >= 0 {
			return nil, standaloneFail(standaloneUsageError, "invalid_process_argument")
		}
		for _, reserved := range []string{"--config", "--project", "--environment", "--output", "--dry-run", "--explain"} {
			if argument == reserved || strings.HasPrefix(argument, reserved+"=") {
				return nil, standaloneFail(standaloneUsageError, "process_option_conflict")
			}
		}
	}
	args := append([]string{request.Command}, []string(request.Arguments)...)
	descriptor, consumed, ok := standaloneDescriptorForInvocation(args)
	if !ok || consumed == 0 || len(descriptor.Children) != 0 {
		return nil, standaloneFail(standaloneUsageError, "invalid_process_command")
	}
	commandPath := strings.Join(args[:consumed], " ")
	if commandPath == "process" || commandPath == "help" || strings.HasPrefix(commandPath, "completion ") || !descriptor.ProcessAPI || !descriptor.Available {
		return nil, standaloneFail(standaloneCompatibilityError, "operation_unavailable")
	}
	authority, found := standaloneAuthorityProfileFor(commandPath, "default")
	if !found || !standaloneProcessAuthorityAllowed(commandPath, authority.standaloneAuthorityDimensions) {
		return nil, standaloneFail(standaloneCompatibilityError, "operation_unavailable")
	}
	if !standaloneInvocation(args) {
		return nil, standaloneFail(standaloneCompatibilityError, "operation_unavailable")
	}
	if commandPath == "version" || commandPath == "capabilities" || commandPath == "schema inspect" || strings.HasPrefix(commandPath, "migrate ") {
		if request.Configuration.Source != "none" || request.Configuration.Path != "" || request.Configuration.Environment != "none" {
			return nil, standaloneFail(standaloneConfigurationError, "configuration_not_allowed")
		}
	} else {
		switch request.Configuration.Source {
		case "none":
			if request.Configuration.Path != "" {
				return nil, standaloneFail(standaloneConfigurationError, "invalid_process_configuration")
			}
		case "config":
			if request.Configuration.Path == "" {
				return nil, standaloneFail(standaloneConfigurationError, "invalid_process_configuration")
			}
			args = append(args, "--config", request.Configuration.Path)
		case "project":
			if request.Configuration.Path == "" {
				return nil, standaloneFail(standaloneConfigurationError, "invalid_process_configuration")
			}
			args = append(args, "--project", request.Configuration.Path)
		default:
			return nil, standaloneFail(standaloneConfigurationError, "invalid_process_configuration")
		}
		if request.Configuration.Environment != "none" && request.Configuration.Environment != "portable-v1" {
			return nil, standaloneFail(standaloneConfigurationError, "unknown_environment_projection")
		}
		args = append(args, "--environment", request.Configuration.Environment)
	}
	switch request.Mode {
	case "dry-run":
		args = append(args, "--dry-run")
	case "explain":
		args = append(args, "--explain")
	}
	return args, nil
}

func standaloneProcessAuthorityAllowed(command string, authority standaloneAuthorityDimensions) bool {
	if authority.ProcessSpawn || authority.ProviderContact || authority.BackendContact || authority.Network || authority.CredentialAccess {
		return false
	}
	switch command {
	case "migrate apply":
		return authority.LocalRead && authority.LocalWrite && authority.PrivateWorkspaceAccess
	case "migrate preview":
		return authority.LocalRead && !authority.LocalWrite && authority.PrivateWorkspaceAccess
	default:
		return !authority.LocalWrite && !authority.PrivateWorkspaceAccess
	}
}

func standaloneProcessForbiddenMaintainerCommand(command string) bool {
	switch command {
	case "aggregate", "aggregate-root", "assess", "attempt-ledger", "evaluate", "inventory", "private",
		"review-template", "validate-comparison-set", "validate-pair", "validate-run",
		"verify-agent-adapter", "verify-atl-capabilities", "verify-codex-skill-package", "verify-execution-backend", "verify-extension-protocol":
		return true
	default:
		return false
	}
}

type standaloneBoundedBuffer struct {
	bytes.Buffer
	maximum  int
	overflow bool
}

func (buffer *standaloneBoundedBuffer) Write(data []byte) (int, error) {
	if buffer.overflow {
		return 0, fmt.Errorf("response exceeds bound")
	}
	remaining := buffer.maximum - buffer.Len()
	if len(data) > remaining {
		written := 0
		if remaining > 0 {
			written, _ = buffer.Buffer.Write(data[:remaining])
		}
		buffer.overflow = true
		return written, fmt.Errorf("response exceeds bound")
	}
	return buffer.Buffer.Write(data)
}
