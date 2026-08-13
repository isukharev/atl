package mcpserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	modernMCPProtocolVersion = "2026-07-28"
	legacyMCPProtocolVersion = "2025-11-25"
	stdioProcessHelperEnv    = "ATL_MCP_STDIO_PROCESS_HELPER"
)

type rawMCPPeer struct {
	t             *testing.T
	ctx           context.Context
	serverSession *mcp.ServerSession
	connection    mcp.Connection
}

func connectRawMCPPeer(t *testing.T) *rawMCPPeer {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	serverTransport, peerTransport := mcp.NewInMemoryTransports()
	serverSession, err := New("test", Dependencies{}).Connect(ctx, serverTransport, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	connection, err := peerTransport.Connect(ctx)
	if err != nil {
		_ = serverSession.Close()
		cancel()
		t.Fatal(err)
	}
	peer := &rawMCPPeer{t: t, ctx: ctx, serverSession: serverSession, connection: connection}
	t.Cleanup(func() {
		_ = connection.Close()
		_ = serverSession.Close()
		cancel()
	})
	return peer
}

func (p *rawMCPPeer) call(raw string) *jsonrpc.Response {
	p.t.Helper()
	p.write(raw)
	message, err := p.connection.Read(p.ctx)
	if err != nil {
		p.t.Fatalf("read response: %v", err)
	}
	response, ok := message.(*jsonrpc.Response)
	if !ok {
		p.t.Fatalf("response type=%T", message)
	}
	return response
}

func (p *rawMCPPeer) write(raw string) {
	p.t.Helper()
	message, err := jsonrpc.DecodeMessage([]byte(raw))
	if err != nil {
		p.t.Fatalf("decode request: %v", err)
	}
	if err := p.connection.Write(p.ctx, message); err != nil {
		p.t.Fatalf("write request: %v", err)
	}
}

func TestServerSupportsModernDiscoveryWithoutInitialize(t *testing.T) {
	peer := connectRawMCPPeer(t)
	discovery := peer.call(`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"modern-test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`)
	if discovery.Error != nil {
		t.Fatalf("discover error: %v", discovery.Error)
	}
	var result mcp.DiscoverResult
	if err := json.Unmarshal(discovery.Result, &result); err != nil {
		t.Fatalf("decode discover result: %v", err)
	}
	for _, version := range []string{modernMCPProtocolVersion, legacyMCPProtocolVersion} {
		if !slices.Contains(result.SupportedVersions, version) {
			t.Fatalf("supported versions=%v, missing %s", result.SupportedVersions, version)
		}
	}
	info, ok := result.Meta[mcp.MetaKeyServerInfo].(map[string]any)
	if !ok || info["name"] != "atl" || info["version"] != "test" {
		t.Fatalf("claimed server info=%#v", result.Meta[mcp.MetaKeyServerInfo])
	}
	if result.Instructions == "" || result.Capabilities == nil || result.Capabilities.Tools == nil {
		t.Fatalf("discover result omitted capabilities or instructions: %+v", result)
	}

	listed := peer.call(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"modern-test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`)
	if listed.Error != nil || len(listed.Result) == 0 {
		t.Fatalf("modern tools/list result=%s error=%v", listed.Result, listed.Error)
	}
}

func TestServerPreservesLegacyInitializeFallback(t *testing.T) {
	peer := connectRawMCPPeer(t)
	initialized := peer.call(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"legacy-test","version":"1"}}}`)
	if initialized.Error != nil {
		t.Fatalf("initialize error: %v", initialized.Error)
	}
	var result mcp.InitializeResult
	if err := json.Unmarshal(initialized.Result, &result); err != nil {
		t.Fatalf("decode initialize result: %v", err)
	}
	if result.ProtocolVersion != legacyMCPProtocolVersion || result.ServerInfo == nil || result.ServerInfo.Name != "atl" {
		t.Fatalf("initialize result=%+v", result)
	}
	peer.write(`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`)
	listed := peer.call(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
	if listed.Error != nil || len(listed.Result) == 0 {
		t.Fatalf("legacy tools/list result=%s error=%v", listed.Result, listed.Error)
	}
}

func TestServerRejectsUnsupportedModernProtocolStructurally(t *testing.T) {
	peer := connectRawMCPPeer(t)
	response := peer.call(`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2099-01-01","io.modelcontextprotocol/clientInfo":{"name":"future-test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`)
	var rpcErr *jsonrpc.Error
	if !errors.As(response.Error, &rpcErr) || rpcErr.Code != mcp.CodeUnsupportedProtocolVersion {
		t.Fatalf("error=%T %v", response.Error, response.Error)
	}
	var data mcp.UnsupportedProtocolVersionData
	if err := json.Unmarshal(rpcErr.Data, &data); err != nil {
		t.Fatalf("decode unsupported-version data: %v", err)
	}
	if data.Requested != "2099-01-01" || !slices.Contains(data.Supported, modernMCPProtocolVersion) || !slices.Contains(data.Supported, legacyMCPProtocolVersion) {
		t.Fatalf("unsupported-version data=%+v", data)
	}
}

func TestServerStdioSupportsModernDiscoveryWithoutInitialize(t *testing.T) {
	process := startRawMCPStdioProcess(t)
	discovery := process.call(`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"modern-stdio-test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`, 1)
	if discovery.Error != nil {
		t.Fatalf("discover error: %v", discovery.Error)
	}
	var result mcp.DiscoverResult
	if err := json.Unmarshal(discovery.Result, &result); err != nil {
		t.Fatalf("decode discover result: %v", err)
	}
	for _, version := range []string{modernMCPProtocolVersion, legacyMCPProtocolVersion} {
		if !slices.Contains(result.SupportedVersions, version) {
			t.Fatalf("supported versions=%v, missing %s", result.SupportedVersions, version)
		}
	}

	listed := process.call(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"modern-stdio-test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`, 2)
	assertRawStdioToolInventory(t, listed, true)
	resources := process.call(`{"jsonrpc":"2.0","id":3,"method":"resources/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"modern-stdio-test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`, 3)
	assertRawStdioResourceInventory(t, resources, true)
	read := process.call(`{"jsonrpc":"2.0","id":4,"method":"resources/read","params":{"uri":"atl://capabilities","_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"modern-stdio-test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`, 4)
	assertRawStdioCapabilitiesResource(t, read, true)
	process.close()
}

func TestServerStdioPreservesLegacyInitializeFallback(t *testing.T) {
	process := startRawMCPStdioProcess(t)
	initialized := process.call(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"legacy-stdio-test","version":"1"}}}`, 1)
	if initialized.Error != nil {
		t.Fatalf("initialize error: %v", initialized.Error)
	}
	var result mcp.InitializeResult
	if err := json.Unmarshal(initialized.Result, &result); err != nil {
		t.Fatalf("decode initialize result: %v", err)
	}
	if result.ProtocolVersion != legacyMCPProtocolVersion || result.ServerInfo == nil || result.ServerInfo.Name != "atl" {
		t.Fatalf("initialize result=%+v", result)
	}
	process.notify(`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`)
	listed := process.call(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`, 2)
	assertRawStdioToolInventory(t, listed, false)
	resources := process.call(`{"jsonrpc":"2.0","id":3,"method":"resources/list","params":{}}`, 3)
	assertRawStdioResourceInventory(t, resources, false)
	read := process.call(`{"jsonrpc":"2.0","id":4,"method":"resources/read","params":{"uri":"atl://capabilities"}}`, 4)
	assertRawStdioCapabilitiesResource(t, read, false)
	process.close()
}

func TestServerStdioRejectsUnsupportedModernProtocolStructurally(t *testing.T) {
	process := startRawMCPStdioProcess(t)
	response := process.call(`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2099-01-01","io.modelcontextprotocol/clientInfo":{"name":"future-stdio-test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`, 1)
	var rpcErr *jsonrpc.Error
	if !errors.As(response.Error, &rpcErr) || rpcErr.Code != mcp.CodeUnsupportedProtocolVersion {
		t.Fatalf("error=%T %v", response.Error, response.Error)
	}
	var data mcp.UnsupportedProtocolVersionData
	if err := json.Unmarshal(rpcErr.Data, &data); err != nil {
		t.Fatalf("decode unsupported-version data: %v", err)
	}
	if data.Requested != "2099-01-01" || !slices.Contains(data.Supported, modernMCPProtocolVersion) || !slices.Contains(data.Supported, legacyMCPProtocolVersion) {
		t.Fatalf("unsupported-version data=%+v", data)
	}
	process.close()
}

func TestSDKClientNegotiatesRecognizedFutureRejectionWithoutLegacyDowngrade(t *testing.T) {
	// ATL owns the server response, while the dependency owns the client's retry
	// choice. Rewrite only the first client probe to a future version so the real
	// ATL server emits UnsupportedProtocolVersion with its supported eras.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := NewForService("test", Dependencies{}, ServiceOffline).Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = serverSession.Close() }()

	probe := &futureDiscoverTransport{Transport: clientTransport}
	client := mcp.NewClient(&mcp.Implementation{Name: "atl-negotiation-test", Version: "1"}, nil)
	clientSession, err := client.Connect(ctx, probe, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = clientSession.Close() }()
	if initialized := clientSession.InitializeResult(); initialized == nil || initialized.ProtocolVersion != modernMCPProtocolVersion {
		t.Fatalf("negotiated initialize result=%+v", initialized)
	}
	if got, want := probe.methodsSnapshot(), []string{"server/discover", "server/discover"}; !slices.Equal(got, want) {
		t.Fatalf("client methods=%v want=%v; recognized modern response must not enter legacy initialize", got, want)
	}
}

type rawMCPStdioProcess struct {
	t         *testing.T
	command   *exec.Cmd
	stdin     io.WriteCloser
	stdout    *bufio.Reader
	stderr    bytes.Buffer
	cancel    context.CancelFunc
	requests  int
	responses int
	closed    bool
}

func startRawMCPStdioProcess(t *testing.T) *rawMCPStdioProcess {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMCPRawStdioProcessHelper$")
	command.Env = append(os.Environ(), stdioProcessHelperEnv+"=1")
	stdin, err := command.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		cancel()
		t.Fatal(err)
	}
	process := &rawMCPStdioProcess{
		t: t, command: command, stdin: stdin,
		stdout: bufio.NewReader(io.LimitReader(stdout, 2<<20)), cancel: cancel,
	}
	command.Stderr = &process.stderr
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if process.closed {
			return
		}
		_ = process.stdin.Close()
		if process.command.Process != nil {
			_ = process.command.Process.Kill()
		}
		_ = process.command.Wait()
		process.cancel()
	})
	return process
}

func (p *rawMCPStdioProcess) call(raw string, id int64) *jsonrpc.Response {
	p.t.Helper()
	p.requests++
	p.write(raw)
	line, err := p.stdout.ReadBytes('\n')
	if err != nil {
		p.t.Fatalf("read stdio response: %v", err)
	}
	if len(line) == 0 || line[len(line)-1] != '\n' {
		p.t.Fatalf("stdio response is not one JSONL frame: %q", line)
	}
	message, err := jsonrpc.DecodeMessage(bytes.TrimSuffix(line, []byte{'\n'}))
	if err != nil {
		p.t.Fatalf("decode stdio response: %v", err)
	}
	response, ok := message.(*jsonrpc.Response)
	if !ok || response.ID.Raw() != id {
		p.t.Fatalf("stdio message=%T id=%v, want response id=%d", message, response.ID.Raw(), id)
	}
	p.responses++
	return response
}

func (p *rawMCPStdioProcess) notify(raw string) {
	p.t.Helper()
	p.write(raw)
}

func (p *rawMCPStdioProcess) write(raw string) {
	p.t.Helper()
	if _, err := io.WriteString(p.stdin, raw+"\n"); err != nil {
		p.t.Fatalf("write stdio request: %v", err)
	}
}

func (p *rawMCPStdioProcess) close() {
	p.t.Helper()
	if p.closed {
		p.t.Fatal("stdio process closed twice")
	}
	if err := p.stdin.Close(); err != nil {
		p.t.Fatalf("close stdio input: %v", err)
	}
	extra, readErr := io.ReadAll(p.stdout)
	waitErr := p.command.Wait()
	p.closed = true
	p.cancel()
	if readErr != nil {
		p.t.Fatalf("read trailing stdio output: %v", readErr)
	}
	if waitErr != nil {
		p.t.Fatalf("stdio server exit: %v", waitErr)
	}
	if p.requests != p.responses || len(extra) != 0 {
		p.t.Fatalf("stdio responses=%d requests=%d trailing stdout=%q", p.responses, p.requests, extra)
	}
	if p.stderr.Len() != 0 {
		p.t.Fatalf("stdio server wrote stderr: %q", p.stderr.String())
	}
}

func assertRawStdioToolInventory(t *testing.T, response *jsonrpc.Response, modern bool) {
	t.Helper()
	document := assertRawStdioCachedResult(t, response, "tools/list", "tools", modern)
	var tools []*mcp.Tool
	if err := json.Unmarshal(document["tools"], &tools); err != nil {
		t.Fatalf("decode tools/list inventory: %v", err)
	}
	got := make([]string, 0, len(tools))
	for _, tool := range tools {
		got = append(got, tool.Name)
	}
	slices.Sort(got)
	want := []string{"confluence_mirror_snapshot", "jira_mirror_snapshot"}
	if !slices.Equal(got, want) {
		t.Fatalf("stdio tool inventory=%v want=%v", got, want)
	}
}

func assertRawStdioResourceInventory(t *testing.T, response *jsonrpc.Response, modern bool) {
	t.Helper()
	document := assertRawStdioCachedResult(t, response, "resources/list", "resources", modern)
	var resources []map[string]string
	if err := json.Unmarshal(document["resources"], &resources); err != nil {
		t.Fatalf("decode resources/list inventory: %v", err)
	}
	want := []map[string]string{{
		"uri":         CapabilitiesResourceURI,
		"name":        "atl-capabilities",
		"title":       "atl capability routes",
		"description": "Static content-free CLI and MCP capability routing metadata.",
		"mimeType":    capabilitiesResourceMIMEType,
	}}
	if !reflect.DeepEqual(resources, want) {
		t.Fatalf("stdio resource inventory=%v want=%v", resources, want)
	}
}

func assertRawStdioCapabilitiesResource(t *testing.T, response *jsonrpc.Response, modern bool) {
	t.Helper()
	document := assertRawStdioCachedResult(t, response, "resources/read", "contents", modern)
	var rawContents []map[string]json.RawMessage
	if err := json.Unmarshal(document["contents"], &rawContents); err != nil {
		t.Fatalf("decode resources/read contents: %v", err)
	}
	if len(rawContents) != 1 || !slices.Equal(rawJSONMemberNames(rawContents[0]), []string{"mimeType", "text", "uri"}) {
		t.Fatalf("resources/read content shape=%s", document["contents"])
	}
	var contents []struct {
		URI      string `json:"uri"`
		MIMEType string `json:"mimeType"`
		Text     string `json:"text"`
	}
	if err := json.Unmarshal(document["contents"], &contents); err != nil {
		t.Fatalf("decode resources/read text content: %v", err)
	}
	if contents[0].URI != CapabilitiesResourceURI || contents[0].MIMEType != capabilitiesResourceMIMEType {
		t.Fatalf("resources/read content=%+v", contents[0])
	}
	assertCapabilitiesResourceJSON(t, contents[0].Text)
}

func assertRawStdioCachedResult(t *testing.T, response *jsonrpc.Response, method, payloadMember string, modern bool) map[string]json.RawMessage {
	t.Helper()
	if response.Error != nil {
		t.Fatalf("%s error: %v", method, response.Error)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(response.Result, &document); err != nil {
		t.Fatalf("decode %s result: %v", method, err)
	}
	wantMembers := []string{"cacheScope", payloadMember, "ttlMs"}
	if modern {
		wantMembers = append(wantMembers, "_meta", "resultType")
		slices.Sort(wantMembers)
	}
	if got := rawJSONMemberNames(document); !slices.Equal(got, wantMembers) {
		t.Fatalf("%s members=%v want=%v: %s", method, got, wantMembers, response.Result)
	}
	var ttlMs int
	if err := json.Unmarshal(document["ttlMs"], &ttlMs); err != nil || ttlMs != 0 {
		t.Fatalf("%s ttlMs=%s: %v", method, document["ttlMs"], err)
	}
	var cacheScope string
	if err := json.Unmarshal(document["cacheScope"], &cacheScope); err != nil || cacheScope != "public" {
		t.Fatalf("%s cacheScope=%s: %v", method, document["cacheScope"], err)
	}
	if modern {
		var resultType string
		if err := json.Unmarshal(document["resultType"], &resultType); err != nil || resultType != "complete" {
			t.Fatalf("%s resultType=%s: %v", method, document["resultType"], err)
		}
		var meta map[string]any
		if err := json.Unmarshal(document["_meta"], &meta); err != nil {
			t.Fatalf("decode %s _meta: %v", method, err)
		}
		wantMeta := map[string]any{
			mcp.MetaKeyServerInfo: map[string]any{"name": "atl", "version": "test"},
		}
		if !reflect.DeepEqual(meta, wantMeta) {
			t.Fatalf("%s _meta=%v want=%v", method, meta, wantMeta)
		}
	}
	return document
}

func rawJSONMemberNames(document map[string]json.RawMessage) []string {
	members := make([]string, 0, len(document))
	for member := range document {
		members = append(members, member)
	}
	slices.Sort(members)
	return members
}

func TestMCPRawStdioProcessHelper(_ *testing.T) {
	if os.Getenv(stdioProcessHelperEnv) != "1" {
		return
	}
	if err := NewForService("test", Dependencies{}, ServiceOffline).Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		os.Exit(90)
	}
	os.Exit(0)
}

type futureDiscoverTransport struct {
	mcp.Transport
	mu        sync.Mutex
	rewritten bool
	methods   []string
}

func (t *futureDiscoverTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	connection, err := t.Transport.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &futureDiscoverConnection{Connection: connection, transport: t}, nil
}

func (t *futureDiscoverTransport) methodsSnapshot() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.methods)
}

type futureDiscoverConnection struct {
	mcp.Connection
	transport *futureDiscoverTransport
}

func (c *futureDiscoverConnection) Write(ctx context.Context, message jsonrpc.Message) error {
	request, ok := message.(*jsonrpc.Request)
	if !ok {
		return c.Connection.Write(ctx, message)
	}
	c.transport.mu.Lock()
	c.transport.methods = append(c.transport.methods, request.Method)
	rewrite := request.Method == "server/discover" && !c.transport.rewritten
	if rewrite {
		c.transport.rewritten = true
	}
	c.transport.mu.Unlock()
	if !rewrite {
		return c.Connection.Write(ctx, message)
	}
	var params map[string]any
	if err := json.Unmarshal(request.Params, &params); err != nil {
		return err
	}
	meta, ok := params["_meta"].(map[string]any)
	if !ok {
		return errors.New("discover metadata is missing")
	}
	meta[mcp.MetaKeyProtocolVersion] = "2099-01-01"
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	rewritten := *request
	rewritten.Params = raw
	return c.Connection.Write(ctx, &rewritten)
}
