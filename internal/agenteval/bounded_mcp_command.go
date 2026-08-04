package agenteval

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxSyntheticMCPNotifications = 32

type boundedMCPCommand struct {
	command *exec.Cmd
	tree    *boundedProcessTree
	stdin   io.WriteCloser
	stdout  io.ReadCloser
	reader  *boundedJSONLineReader
	stderr  *synchronizedBoundedBuffer

	timeout            time.Duration
	maxMessageBytes    int64
	maxStructuredBytes int64
	waitDone           chan struct{}
	waitErr            error

	mu        sync.Mutex
	nextID    int64
	stopped   bool
	forced    bool
	closeOnce sync.Once
	closeErr  error
}

type boundedJSONLineReader struct {
	limited    *io.LimitedReader
	reader     *bufio.Reader
	maxMessage int64
}

type synchronizedBoundedBuffer struct {
	mu       sync.Mutex
	buffer   bytes.Buffer
	maximum  int64
	exceeded bool
}

func startBoundedMCPCommand(
	ctx context.Context,
	binary string,
	args []string,
	directory string,
	environment []string,
	timeout time.Duration,
	maxStdoutBytes int64,
	maxStderrBytes int64,
) (*boundedMCPCommand, error) {
	command := exec.Command(binary, args...)
	tree, err := prepareProcessTree(command)
	if err != nil {
		return nil, fmt.Errorf("prepare ATL MCP process tree")
	}
	command.Dir = directory
	command.Env = append([]string(nil), environment...)
	command.WaitDelay = 250 * time.Millisecond
	stdin, err := command.StdinPipe()
	if err != nil {
		_ = tree.close()
		return nil, fmt.Errorf("open ATL MCP stdin")
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		_ = tree.close()
		return nil, fmt.Errorf("open ATL MCP stdout")
	}
	stderr := &synchronizedBoundedBuffer{maximum: maxStderrBytes}
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = tree.close()
		return nil, fmt.Errorf("start ATL MCP process")
	}
	if err := tree.attach(); err != nil {
		_ = tree.kill()
		_ = command.Process.Kill()
		_ = command.Wait()
		_ = stdin.Close()
		_ = stdout.Close()
		_ = tree.close()
		return nil, fmt.Errorf("attach ATL MCP process tree")
	}

	limited := &io.LimitedReader{R: stdout, N: maxStdoutBytes + 1}
	process := &boundedMCPCommand{
		command: command, tree: tree, stdin: stdin, stdout: stdout,
		reader: &boundedJSONLineReader{
			limited: limited, reader: bufio.NewReaderSize(limited, 64<<10),
			maxMessage: maxStdoutBytes,
		},
		stderr: stderr, timeout: timeout,
		maxMessageBytes:    maxStdoutBytes,
		maxStructuredBytes: maxStdoutBytes,
		waitDone:           make(chan struct{}), nextID: 1,
	}
	go func() {
		process.waitErr = command.Wait()
		close(process.waitDone)
	}()

	process.mu.Lock()
	initializeErr := process.initializeLocked(ctx)
	process.mu.Unlock()
	if initializeErr != nil {
		_ = process.Close()
		return nil, initializeErr
	}
	return process, nil
}

func (p *boundedMCPCommand) initializeLocked(ctx context.Context) error {
	request := map[string]any{
		"jsonrpc": "2.0",
		"id":      p.nextID,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-11-25",
			"capabilities":    map[string]any{},
			"clientInfo": map[string]any{
				"name":    "atl-agent-eval",
				"version": "1",
			},
		},
	}
	response, err := p.exchangeLocked(ctx, request, p.nextID)
	if err != nil {
		return fmt.Errorf("initialize ATL MCP process: %w", err)
	}
	if response.err != nil {
		return fmt.Errorf("initialize ATL MCP process: protocol error")
	}
	if err := validateSyntheticMCPInitializeResult(response.result); err != nil {
		return fmt.Errorf("initialize ATL MCP process: %w", err)
	}
	p.nextID++
	return p.writeMessageLocked(ctx, map[string]any{
		"jsonrpc": "2.0", "method": "notifications/initialized",
	})
}

func (p *boundedMCPCommand) call(ctx context.Context, invocation MCPInvocation) (SyntheticMCPResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped {
		return SyntheticMCPResult{}, fmt.Errorf("ATL MCP process is closed")
	}
	id := p.nextID
	p.nextID++
	var arguments map[string]any
	if err := json.Unmarshal(invocation.Arguments, &arguments); err != nil {
		return SyntheticMCPResult{}, fmt.Errorf("ATL MCP invocation arguments are invalid")
	}
	response, err := p.exchangeLocked(ctx, map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  "tools/call",
		"params": map[string]any{
			"name": invocation.Tool, "arguments": arguments,
		},
	}, id)
	if err != nil {
		return SyntheticMCPResult{}, err
	}
	if response.err != nil {
		return SyntheticMCPResult{}, fmt.Errorf("ATL MCP tool returned a protocol error")
	}
	return decodeSyntheticMCPToolResult(response.result, p.maxStructuredBytes)
}

type syntheticMCPResponse struct {
	result json.RawMessage
	err    json.RawMessage
}

func (p *boundedMCPCommand) exchangeLocked(ctx context.Context, request any, id int64) (syntheticMCPResponse, error) {
	operationCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	if err := p.writeMessageLocked(operationCtx, request); err != nil {
		_ = p.stopLocked(25 * time.Millisecond)
		return syntheticMCPResponse{}, err
	}
	notifications := 0
	for {
		line, err := p.readMessageLocked(operationCtx)
		if err != nil {
			_ = p.stopLocked(25 * time.Millisecond)
			return syntheticMCPResponse{}, err
		}
		response, notification, err := decodeSyntheticMCPResponse(line, id)
		if err != nil {
			_ = p.stopLocked(25 * time.Millisecond)
			return syntheticMCPResponse{}, err
		}
		if !notification {
			return response, nil
		}
		notifications++
		if notifications > maxSyntheticMCPNotifications {
			_ = p.stopLocked(0)
			return syntheticMCPResponse{}, fmt.Errorf("ATL MCP process exceeded its notification bound")
		}
	}
}

func (p *boundedMCPCommand) writeMessageLocked(ctx context.Context, value any) error {
	data, err := json.Marshal(value)
	if err != nil || int64(len(data)+1) > p.maxMessageBytes {
		return fmt.Errorf("ATL MCP request exceeds its message bound")
	}
	data = append(data, '\n')
	done := make(chan error, 1)
	go func() {
		_, writeErr := p.stdin.Write(data)
		done <- writeErr
	}()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("write ATL MCP request")
		}
		return nil
	case <-ctx.Done():
		_ = p.stopLocked(0)
		<-done
		return fmt.Errorf("write ATL MCP request: %w", ctx.Err())
	}
}

func (p *boundedMCPCommand) readMessageLocked(ctx context.Context) ([]byte, error) {
	type readResult struct {
		data []byte
		err  error
	}
	done := make(chan readResult, 1)
	go func() {
		data, err := p.reader.readLine()
		done <- readResult{data: data, err: err}
	}()
	select {
	case result := <-done:
		if p.stderr.overflowed() {
			p.forced = true
			return nil, fmt.Errorf("ATL MCP stderr exceeded its output bound")
		}
		if result.err != nil {
			if strings.Contains(result.err.Error(), "bound") {
				p.forced = true
			}
			return nil, result.err
		}
		return result.data, nil
	case <-ctx.Done():
		_ = p.stopLocked(0)
		<-done
		return nil, fmt.Errorf("read ATL MCP response: %w", ctx.Err())
	}
}

func (r *boundedJSONLineReader) readLine() ([]byte, error) {
	var message bytes.Buffer
	for {
		fragment, err := r.reader.ReadSlice('\n')
		if int64(message.Len()+len(fragment)) > r.maxMessage {
			return nil, fmt.Errorf("ATL MCP response exceeded its message bound")
		}
		_, _ = message.Write(fragment)
		if err == nil {
			break
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if r.limited.N <= 0 {
			return nil, fmt.Errorf("ATL MCP stdout exceeded its total output bound")
		}
		return nil, fmt.Errorf("read ATL MCP response")
	}
	if r.limited.N <= 0 {
		return nil, fmt.Errorf("ATL MCP stdout exceeded its total output bound")
	}
	line := bytes.TrimSpace(message.Bytes())
	if len(line) == 0 {
		return nil, fmt.Errorf("ATL MCP response is empty")
	}
	return append([]byte(nil), line...), nil
}

func decodeSyntheticMCPResponse(data []byte, expectedID int64) (syntheticMCPResponse, bool, error) {
	if err := validateJSONNoDuplicateKeys(data); err != nil {
		return syntheticMCPResponse{}, false, fmt.Errorf("ATL MCP response contains duplicate JSON members")
	}
	var envelope map[string]json.RawMessage
	if err := decodeStrictJSONObject(data, &envelope); err != nil {
		return syntheticMCPResponse{}, false, fmt.Errorf("decode ATL MCP response")
	}
	var version string
	if err := json.Unmarshal(envelope["jsonrpc"], &version); err != nil || version != "2.0" {
		return syntheticMCPResponse{}, false, fmt.Errorf("ATL MCP response has an invalid protocol version")
	}
	if methodRaw, notification := envelope["method"]; notification {
		var method string
		if _, hasID := envelope["id"]; hasID || len(envelope) < 2 || len(envelope) > 3 ||
			json.Unmarshal(methodRaw, &method) != nil || method == "" {
			return syntheticMCPResponse{}, false, fmt.Errorf("ATL MCP server request is not supported")
		}
		for member := range envelope {
			if member != "jsonrpc" && member != "method" && member != "params" {
				return syntheticMCPResponse{}, false, fmt.Errorf("ATL MCP notification has an unknown member")
			}
		}
		return syntheticMCPResponse{}, true, nil
	}
	if len(envelope) != 3 {
		return syntheticMCPResponse{}, false, fmt.Errorf("ATL MCP response has an invalid member set")
	}
	if string(bytes.TrimSpace(envelope["id"])) != strconv.FormatInt(expectedID, 10) {
		return syntheticMCPResponse{}, false, fmt.Errorf("ATL MCP response id does not match the request")
	}
	result, hasResult := envelope["result"]
	rpcErr, hasError := envelope["error"]
	if hasResult == hasError || (!hasResult && !hasError) {
		return syntheticMCPResponse{}, false, fmt.Errorf("ATL MCP response must contain exactly one result or error")
	}
	for member := range envelope {
		if member != "jsonrpc" && member != "id" && member != "result" && member != "error" {
			return syntheticMCPResponse{}, false, fmt.Errorf("ATL MCP response has an unknown member")
		}
	}
	return syntheticMCPResponse{result: result, err: rpcErr}, false, nil
}

func validateSyntheticMCPInitializeResult(result json.RawMessage) error {
	if validateJSONNoDuplicateKeys(result) != nil {
		return fmt.Errorf("initialize result contains duplicate JSON members")
	}
	var document map[string]json.RawMessage
	if err := decodeStrictJSONObject(result, &document); err != nil {
		return fmt.Errorf("initialize result is not one JSON object")
	}
	for member := range document {
		if member != "protocolVersion" && member != "capabilities" && member != "serverInfo" && member != "instructions" {
			return fmt.Errorf("initialize result has an unknown member")
		}
	}
	var protocolVersion string
	if err := json.Unmarshal(document["protocolVersion"], &protocolVersion); err != nil || protocolVersion != "2025-11-25" {
		return fmt.Errorf("initialize result has an unexpected protocol version")
	}
	var capabilities map[string]json.RawMessage
	if err := json.Unmarshal(document["capabilities"], &capabilities); err != nil || capabilities == nil {
		return fmt.Errorf("initialize result has invalid capabilities")
	}
	var serverInfo map[string]json.RawMessage
	if err := json.Unmarshal(document["serverInfo"], &serverInfo); err != nil || serverInfo == nil {
		return fmt.Errorf("initialize result has invalid serverInfo")
	}
	var name, version string
	if err := json.Unmarshal(serverInfo["name"], &name); err != nil || name == "" {
		return fmt.Errorf("initialize result has invalid server name")
	}
	if err := json.Unmarshal(serverInfo["version"], &version); err != nil || version == "" {
		return fmt.Errorf("initialize result has invalid server version")
	}
	return nil
}

func decodeSyntheticMCPToolResult(result json.RawMessage, maximum int64) (SyntheticMCPResult, error) {
	if validateJSONNoDuplicateKeys(result) != nil {
		return SyntheticMCPResult{}, fmt.Errorf("ATL MCP tool result contains duplicate JSON members")
	}
	var document map[string]json.RawMessage
	if err := decodeStrictJSONObject(result, &document); err != nil {
		return SyntheticMCPResult{}, fmt.Errorf("decode ATL MCP tool result")
	}
	for member := range document {
		if member != "content" && member != "structuredContent" && member != "isError" && member != "_meta" {
			return SyntheticMCPResult{}, fmt.Errorf("ATL MCP tool result has an unknown member")
		}
	}
	if _, ok := document["content"]; !ok {
		return SyntheticMCPResult{}, fmt.Errorf("ATL MCP tool result is missing content")
	}
	var content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	contentDecoder := json.NewDecoder(bytes.NewReader(document["content"]))
	contentDecoder.DisallowUnknownFields()
	if err := contentDecoder.Decode(&content); err != nil || contentDecoder.Decode(new(any)) != io.EOF || len(content) == 0 {
		return SyntheticMCPResult{}, fmt.Errorf("ATL MCP tool result has invalid content")
	}
	textContent := make([]string, 0, len(content))
	var textBytes int64
	for _, block := range content {
		textBytes += int64(len(block.Text))
		if block.Type != "text" || block.Text == "" || textBytes > maximum {
			return SyntheticMCPResult{}, fmt.Errorf("ATL MCP tool text content is invalid or oversized")
		}
		textContent = append(textContent, block.Text)
	}
	var isError bool
	if raw := document["isError"]; raw != nil && json.Unmarshal(raw, &isError) != nil {
		return SyntheticMCPResult{}, fmt.Errorf("ATL MCP tool result has invalid isError")
	}
	structured := bytes.TrimSpace(document["structuredContent"])
	if isError && (len(structured) == 0 || bytes.Equal(structured, []byte("null"))) {
		return SyntheticMCPResult{IsError: true, TextContent: textContent}, nil
	}
	if len(structured) == 0 || int64(len(structured)) > maximum {
		return SyntheticMCPResult{}, fmt.Errorf("ATL MCP structured content is missing or oversized")
	}
	if validateJSONNoDuplicateKeys(structured) != nil {
		return SyntheticMCPResult{}, fmt.Errorf("ATL MCP structured content contains duplicate JSON members")
	}
	var object map[string]json.RawMessage
	if err := decodeStrictJSONObject(structured, &object); err != nil || object == nil {
		return SyntheticMCPResult{}, fmt.Errorf("ATL MCP structured content is not one JSON object")
	}
	return SyntheticMCPResult{
		IsError: isError, StructuredContent: append(json.RawMessage(nil), structured...), TextContent: textContent,
	}, nil
}

func (p *boundedMCPCommand) Close() error {
	p.closeOnce.Do(func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.closeErr = p.stopLocked(250 * time.Millisecond)
	})
	return p.closeErr
}

func (p *boundedMCPCommand) stopLocked(grace time.Duration) error {
	if !p.stopped {
		p.stopped = true
		_ = p.stdin.Close()
	}
	if waitForMCPProcess(p.waitDone, grace) {
		cleanupErr := errors.Join(p.tree.kill(), p.tree.close())
		_ = p.stdout.Close()
		if p.waitErr != nil && !p.forced {
			return errors.Join(fmt.Errorf("ATL MCP process exited unsuccessfully"), cleanupErr)
		}
		return cleanupErr
	}
	p.forced = true
	_ = p.tree.interrupt()
	if waitForMCPProcess(p.waitDone, 100*time.Millisecond) {
		cleanupErr := errors.Join(p.tree.kill(), p.tree.close())
		_ = p.stdout.Close()
		return cleanupErr
	}
	_ = p.tree.kill()
	<-p.waitDone
	_ = p.stdout.Close()
	return p.tree.close()
}

func waitForMCPProcess(done <-chan struct{}, duration time.Duration) bool {
	if duration <= 0 {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

func (b *synchronizedBoundedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.maximum - int64(b.buffer.Len())
	if int64(len(data)) > remaining {
		if remaining > 0 {
			_, _ = b.buffer.Write(data[:remaining])
		}
		b.exceeded = true
		return 0, io.ErrShortWrite
	}
	return b.buffer.Write(data)
}

func (b *synchronizedBoundedBuffer) overflowed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.exceeded
}
