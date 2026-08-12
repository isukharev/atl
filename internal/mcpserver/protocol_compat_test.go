package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	modernMCPProtocolVersion = "2026-07-28"
	legacyMCPProtocolVersion = "2025-11-25"
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
