package agenteval

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type ExternalMCPAuditRecord struct {
	Sequence      int64  `json:"sequence"`
	Capability    string `json:"capability,omitempty"`
	Decision      string `json:"decision"`
	Success       bool   `json:"success,omitempty"`
	RequestBytes  int64  `json:"request_bytes,omitempty"`
	ResponseBytes int64  `json:"response_bytes,omitempty"`
	DurationMS    int64  `json:"duration_ms,omitempty"`
	IdentityHMAC  string `json:"identity_hmac,omitempty"`
}

type ExternalMCPProxy struct {
	profile         ExternalMCPProfile
	headers         map[string]string
	canaries        []string
	upstream        *http.Client
	upstreamSession string
	tools           map[string]externalMCPTool
	capability      string
	listener        net.Listener
	server          *http.Server
	audit           *os.File
	auditPath       string
	auditKey        []byte
	mu              sync.Mutex
	sequence        int64
	counts          map[string]int
	active          map[string]context.CancelFunc
	totalBytes      int64
	inflight        int
	closed          bool
	auditErr        error
}

type externalMCPTool struct {
	policy  ExternalMCPToolPolicy
	raw     json.RawMessage
	allowed map[string]bool
}

type externalRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type externalRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

type externalToolCatalog struct {
	Tools      []json.RawMessage `json:"tools"`
	NextCursor string            `json:"nextCursor,omitempty"`
}

// externalMCPCancelledMethod preserves the protocol-defined method spelling.
const externalMCPCancelledMethod = "notifications/cancelled" //nolint:misspell

func StartExternalMCPProxy(ctx context.Context, profile ExternalMCPProfile, headers map[string]string, canaries []string, auditPath string) (*ExternalMCPProxy, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	client := &http.Client{Transport: transport, Timeout: time.Duration(profile.TimeoutSeconds) * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return startExternalMCPProxyWithClient(ctx, profile, headers, canaries, auditPath, client)
}

func startExternalMCPProxyWithClient(ctx context.Context, profile ExternalMCPProfile, headers map[string]string, canaries []string, auditPath string, client *http.Client) (*ExternalMCPProxy, error) {
	if err := profile.Validate(); err != nil {
		return nil, err
	}
	if err := requireOwnerOnly("external MCP audit directory", filepathDir(auditPath), true); err != nil {
		return nil, err
	}
	audit, err := os.OpenFile(auditPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create external MCP audit: %w", err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		_ = audit.Close()
		return nil, err
	}
	if client == nil {
		_ = audit.Close()
		return nil, fmt.Errorf("external MCP client is missing")
	}
	proxy := &ExternalMCPProxy{profile: profile, headers: headers, canaries: append([]string(nil), canaries...), upstream: client, audit: audit, auditPath: auditPath, auditKey: key, counts: map[string]int{}, active: map[string]context.CancelFunc{}, tools: map[string]externalMCPTool{}}
	preflightCtx, cancelPreflight := context.WithTimeout(ctx, time.Duration(profile.TimeoutSeconds)*time.Second)
	err = proxy.preflight(preflightCtx)
	cancelPreflight()
	if err != nil {
		closeExternalMCPAfterStartFailure(proxy)
		return nil, err
	}
	capability, err := randomGatewayCapability()
	if err != nil {
		closeExternalMCPAfterStartFailure(proxy)
		return nil, err
	}
	proxy.capability = capability
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		closeExternalMCPAfterStartFailure(proxy)
		return nil, err
	}
	proxy.listener = listener
	proxy.server = &http.Server{Handler: proxy, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: time.Duration(profile.TimeoutSeconds) * time.Second, WriteTimeout: time.Duration(profile.TimeoutSeconds) * time.Second, IdleTimeout: 5 * time.Second}
	go func() { _ = proxy.server.Serve(listener) }()
	return proxy, nil
}

func closeExternalMCPAfterStartFailure(proxy *ExternalMCPProxy) {
	timeout := time.Duration(proxy.profile.TimeoutSeconds) * time.Second
	if timeout > 5*time.Second {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_ = proxy.Close(ctx)
}

func (p *ExternalMCPProxy) Endpoint() (string, string) {
	return "http://" + p.listener.Addr().String() + "/mcp", p.capability
}

func (p *ExternalMCPProxy) Close(ctx context.Context) error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	incomplete := p.inflight != 0
	for _, cancel := range p.active {
		cancel()
	}
	p.mu.Unlock()
	var first error
	if p.server != nil {
		if err := p.server.Shutdown(ctx); err != nil {
			first = err
		}
	}
	if p.upstreamSession != "" {
		if err := p.closeUpstreamSession(ctx); first == nil && err != nil {
			first = fmt.Errorf("close external MCP upstream session")
		}
	}
	if p.audit != nil {
		if err := p.audit.Sync(); first == nil && err != nil {
			first = err
		}
		if err := p.audit.Close(); first == nil && err != nil {
			first = err
		}
	}
	if first == nil && p.auditErr != nil {
		first = fmt.Errorf("external MCP proxy audit failed")
	}
	for i := range p.auditKey {
		p.auditKey[i] = 0
	}
	for key := range p.headers {
		p.headers[key] = ""
	}
	if incomplete && first == nil {
		first = fmt.Errorf("external MCP proxy audit is incomplete")
	}
	return first
}

func (p *ExternalMCPProxy) closeBounded() error {
	timeout := time.Duration(p.profile.TimeoutSeconds) * time.Second
	if timeout > 10*time.Second {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return p.Close(ctx)
}

func (p *ExternalMCPProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !validGatewayAuthorization(r.Header.Get("Authorization"), p.capability) {
		p.deny("", 0)
		http.Error(w, "external MCP proxy denied request", http.StatusUnauthorized)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && !loopbackOrigin(origin) {
		p.deny("", 0)
		http.Error(w, "external MCP proxy denied request", http.StatusForbidden)
		return
	}
	if r.URL.Path != "/mcp" || r.URL.RawQuery != "" {
		p.deny("", 0)
		http.Error(w, "external MCP proxy denied request", http.StatusNotFound)
		return
	}
	switch r.Method {
	case http.MethodGet:
		if !strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/event-stream") {
			p.deny("", 0)
			http.Error(w, "external MCP proxy denied request", http.StatusNotAcceptable)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, ": ready\n\n")
	case http.MethodDelete:
		w.WriteHeader(http.StatusNoContent)
	case http.MethodPost:
		p.handlePost(w, r)
	default:
		p.deny("", 0)
		w.Header().Set("Allow", "GET, POST, DELETE")
		http.Error(w, "external MCP proxy denied request", http.StatusMethodNotAllowed)
	}
}

func (p *ExternalMCPProxy) handlePost(w http.ResponseWriter, r *http.Request) {
	mediaType, _, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaErr != nil || !strings.EqualFold(mediaType, "application/json") || !acceptsExternalMCPResponse(r.Header.Get("Accept")) || r.ContentLength > p.profile.MaxRequestBytes {
		p.deny("", 0)
		http.Error(w, "external MCP proxy denied request", http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, p.profile.MaxRequestBytes+1))
	if err != nil || int64(len(body)) > p.profile.MaxRequestBytes {
		p.deny("", int64(len(body)))
		http.Error(w, "external MCP proxy denied request", http.StatusRequestEntityTooLarge)
		return
	}
	if err := validateJSONNoDuplicateKeys(body); err != nil {
		p.deny("", int64(len(body)))
		http.Error(w, "external MCP proxy denied request", http.StatusBadRequest)
		return
	}
	var request externalRPCRequest
	if decodeStrictJSONObject(body, &request) != nil || request.JSONRPC != "2.0" || request.Method == "" {
		p.deny("", int64(len(body)))
		http.Error(w, "external MCP proxy denied request", http.StatusBadRequest)
		return
	}
	switch request.Method {
	case "initialize":
		if _, err := externalRPCIDKey(request.ID); err != nil {
			p.deny("", int64(len(body)))
			http.Error(w, "external MCP proxy denied request", http.StatusBadRequest)
			return
		}
		p.localInitialize(w, request.ID)
	case "notifications/initialized":
		if len(request.ID) != 0 {
			p.deny("", int64(len(body)))
			http.Error(w, "external MCP proxy denied request", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	case externalMCPCancelledMethod:
		if len(request.ID) != 0 || !p.cancelRequest(request.Params) {
			p.deny("", int64(len(body)))
			http.Error(w, "external MCP proxy denied request", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	case "ping":
		if _, err := externalRPCIDKey(request.ID); err != nil {
			p.deny("", int64(len(body)))
			http.Error(w, "external MCP proxy denied request", http.StatusBadRequest)
			return
		}
		p.writeJSONRPC(w, request.ID, json.RawMessage(`{}`))
	case "tools/list":
		if _, err := externalRPCIDKey(request.ID); err != nil {
			p.deny("", int64(len(body)))
			http.Error(w, "external MCP proxy denied request", http.StatusBadRequest)
			return
		}
		p.localToolsList(w, request.ID)
	case "tools/call":
		if _, err := externalRPCIDKey(request.ID); err != nil {
			p.deny("", int64(len(body)))
			http.Error(w, "external MCP proxy denied request", http.StatusBadRequest)
			return
		}
		p.toolCall(r.Context(), w, request, body)
	default:
		p.deny("", int64(len(body)))
		http.Error(w, "external MCP proxy denied request", http.StatusForbidden)
	}
}

func (p *ExternalMCPProxy) localInitialize(w http.ResponseWriter, id json.RawMessage) {
	result, _ := json.Marshal(map[string]any{"protocolVersion": p.profile.ProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": externalMCPServerName, "version": "1"}})
	p.writeJSONRPC(w, id, result)
}
func (p *ExternalMCPProxy) localToolsList(w http.ResponseWriter, id json.RawMessage) {
	names := make([]string, 0, len(p.tools))
	for name := range p.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	tools := make([]json.RawMessage, 0, len(names))
	for _, name := range names {
		tools = append(tools, p.tools[name].raw)
	}
	result, _ := json.Marshal(map[string]any{"tools": tools})
	p.writeJSONRPC(w, id, result)
}
func (p *ExternalMCPProxy) writeJSONRPC(w http.ResponseWriter, id, result json.RawMessage) {
	response, _ := json.Marshal(externalRPCResponse{JSONRPC: "2.0", ID: id, Result: result})
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(response)
}

func (p *ExternalMCPProxy) toolCall(ctx context.Context, w http.ResponseWriter, request externalRPCRequest, raw []byte) {
	started := time.Now()
	requestID, _ := externalRPCIDKey(request.ID)
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if decodeStrictJSONObject(request.Params, &params) != nil {
		p.deny("", int64(len(raw)))
		http.Error(w, "external MCP proxy denied request", http.StatusForbidden)
		return
	}
	tool, ok := p.tools[params.Name]
	canonical, err := canonicalJSONObject(params.Arguments)
	callCtx, cancel := context.WithCancel(ctx)
	if !ok || err != nil || !tool.allowed[string(canonical)] || !p.acquire(tool.policy, requestID, cancel) {
		cancel()
		capability := ""
		if ok {
			capability = tool.policy.Capability
		}
		p.deny(capability, int64(len(raw)))
		http.Error(w, "external MCP proxy denied request", http.StatusForbidden)
		return
	}
	defer p.release(requestID)
	upstream, contentType, rpcFailed, err := p.upstreamCall(callCtx, raw, request.ID, false, false)
	if err == nil && !rpcFailed {
		var resultFailed bool
		resultFailed, err = externalMCPToolResultFailed(upstream)
		rpcFailed = resultFailed
	}
	success := err == nil && !rpcFailed
	if err == nil && containsCanary(upstream, p.canaries) {
		err = fmt.Errorf("external MCP response contained protected material")
		success = false
	}
	if err != nil {
		p.record(tool.policy.Capability, "allow", false, int64(len(raw)), 0, time.Since(started), params.Name+"\x00"+string(canonical))
		http.Error(w, "external MCP upstream call failed", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	written, writeErr := w.Write(upstream)
	delivered := writeErr == nil && written == len(upstream)
	p.record(tool.policy.Capability, "allow", success && delivered, int64(len(raw)), int64(written), time.Since(started), params.Name+"\x00"+string(canonical))
}

func externalMCPToolResultFailed(raw []byte) (bool, error) {
	var response externalRPCResponse
	if decodeStrictJSONObject(raw, &response) != nil || len(response.Result) == 0 {
		return false, fmt.Errorf("invalid MCP tool result")
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(response.Result, &result); err != nil || result == nil {
		return false, fmt.Errorf("invalid MCP tool result")
	}
	if rawFlag, ok := result["isError"]; ok {
		var failed bool
		if err := json.Unmarshal(rawFlag, &failed); err != nil {
			return false, fmt.Errorf("invalid MCP tool error flag")
		}
		return failed, nil
	}
	return false, nil
}

func (p *ExternalMCPProxy) preflight(ctx context.Context) error {
	initID := json.RawMessage(`1`)
	initBody, _ := json.Marshal(externalRPCRequest{JSONRPC: "2.0", ID: initID, Method: "initialize", Params: json.RawMessage(fmt.Sprintf(`{"protocolVersion":%q,"capabilities":{},"clientInfo":{"name":"atl-agent-eval","version":"1"}}`, p.profile.ProtocolVersion))})
	raw, _, rpcFailed, err := p.upstreamCall(ctx, initBody, initID, false, true)
	if err != nil {
		return fmt.Errorf("external MCP initialize failed")
	}
	var initResp externalRPCResponse
	if rpcFailed || decodeStrictJSONObject(raw, &initResp) != nil || len(initResp.Error) > 0 {
		return fmt.Errorf("external MCP initialize response is invalid")
	}
	var initResult struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if json.Unmarshal(initResp.Result, &initResult) != nil || initResult.ProtocolVersion != p.profile.ProtocolVersion {
		return fmt.Errorf("external MCP protocol drift")
	}
	notify, _ := json.Marshal(externalRPCRequest{JSONRPC: "2.0", Method: "notifications/initialized"})
	if _, _, _, err := p.upstreamCall(ctx, notify, nil, true, false); err != nil {
		return fmt.Errorf("external MCP initialization notification failed")
	}
	var catalog []json.RawMessage
	cursor := ""
	seenCursors := map[string]bool{}
	for page := 0; page < 20; page++ {
		params := map[string]string{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		paramsRaw, _ := json.Marshal(params)
		body, _ := json.Marshal(externalRPCRequest{JSONRPC: "2.0", ID: json.RawMessage(fmt.Sprintf("%d", page+2)), Method: "tools/list", Params: paramsRaw})
		requestID := json.RawMessage(fmt.Sprintf("%d", page+2))
		data, _, rpcFailed, err := p.upstreamCall(ctx, body, requestID, false, false)
		if err != nil {
			return fmt.Errorf("external MCP catalog failed")
		}
		var response externalRPCResponse
		if rpcFailed || decodeStrictJSONObject(data, &response) != nil || len(response.Error) > 0 {
			return fmt.Errorf("external MCP catalog response is invalid")
		}
		var listed externalToolCatalog
		if json.Unmarshal(response.Result, &listed) != nil {
			return fmt.Errorf("external MCP catalog result is invalid")
		}
		catalog = append(catalog, listed.Tools...)
		if listed.NextCursor == "" {
			break
		}
		if seenCursors[listed.NextCursor] {
			return fmt.Errorf("external MCP catalog cursor repeated")
		}
		seenCursors[listed.NextCursor] = true
		cursor = listed.NextCursor
		if page == 19 {
			return fmt.Errorf("external MCP catalog pagination exceeded")
		}
	}
	digest, byName, err := externalMCPCatalogIdentity(catalog)
	if err != nil {
		return err
	}
	if !p.profile.acceptsCatalogDigest(digest) {
		return fmt.Errorf("external MCP catalog drift")
	}
	for _, policy := range p.profile.Tools {
		raw, ok := byName[policy.Name]
		if !ok {
			return fmt.Errorf("external MCP reviewed tool is absent")
		}
		var value struct {
			InputSchema json.RawMessage `json:"inputSchema"`
			Annotations struct {
				ReadOnly    *bool `json:"readOnlyHint"`
				Destructive *bool `json:"destructiveHint"`
			} `json:"annotations"`
		}
		_ = json.Unmarshal(raw, &value)
		schemaDigest, err := canonicalJSONSHA(value.InputSchema)
		if err != nil || schemaDigest != policy.InputSchemaSHA256 || value.Annotations.ReadOnly == nil || !*value.Annotations.ReadOnly || (value.Annotations.Destructive != nil && *value.Annotations.Destructive) || looksMutatingMCPTool(policy.Name) || containsCanary(raw, p.canaries) {
			return fmt.Errorf("external MCP reviewed tool is not safely read-only")
		}
		allowed := map[string]bool{}
		for _, args := range policy.AllowedArguments {
			canonical, _ := canonicalJSONObject(args)
			allowed[string(canonical)] = true
		}
		p.tools[policy.Name] = externalMCPTool{policy: policy, raw: raw, allowed: allowed}
	}
	return nil
}

func (p ExternalMCPProfile) acceptsCatalogDigest(digest string) bool {
	if digest == p.CatalogSHA256 {
		return true
	}
	for _, alternate := range p.CatalogSHA256Alternates {
		if digest == alternate {
			return true
		}
	}
	return false
}

func externalMCPCatalogIdentity(catalog []json.RawMessage) (string, map[string]json.RawMessage, error) {
	type catalogEntry struct {
		name      string
		canonical json.RawMessage
	}
	entries := make([]catalogEntry, 0, len(catalog))
	byName := make(map[string]json.RawMessage, len(catalog))
	for _, raw := range catalog {
		var identity struct {
			Name string `json:"name"`
		}
		canonical, err := canonicalJSON(raw)
		if err != nil || json.Unmarshal(raw, &identity) != nil || identity.Name == "" {
			return "", nil, fmt.Errorf("external MCP catalog contains invalid tool")
		}
		if _, exists := byName[identity.Name]; exists {
			return "", nil, fmt.Errorf("external MCP catalog contains duplicate tools")
		}
		byName[identity.Name] = raw
		entries = append(entries, catalogEntry{name: identity.Name, canonical: canonical})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	canonical := make([]json.RawMessage, 0, len(entries))
	for _, entry := range entries {
		canonical = append(canonical, entry.canonical)
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", nil, fmt.Errorf("canonicalize external MCP catalog: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), byName, nil
}

func (p *ExternalMCPProxy) upstreamCall(ctx context.Context, body []byte, expectedID json.RawMessage, allowNoContent, allowNewSession bool) ([]byte, string, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.profile.UpstreamURL, bytes.NewReader(body))
	if err != nil {
		return nil, "", false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for key, value := range p.headers {
		req.Header.Set(key, value)
	}
	if p.upstreamSession != "" {
		req.Header.Set("Mcp-Session-Id", p.upstreamSession)
	}
	req.Header.Set("MCP-Protocol-Version", p.profile.ProtocolVersion)
	resp, err := p.upstream.Do(req)
	if err != nil {
		return nil, "", false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return nil, "", false, fmt.Errorf("redirect denied")
	}
	if resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusNoContent {
		if !allowNoContent || len(expectedID) != 0 {
			return nil, "", false, fmt.Errorf("response missing")
		}
		return nil, "application/json", false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", false, fmt.Errorf("upstream status")
	}
	if session := resp.Header.Get("Mcp-Session-Id"); session != "" {
		if !validExternalMCPSessionID(session) {
			return nil, "", false, fmt.Errorf("invalid session")
		}
		if p.upstreamSession == "" && !allowNewSession {
			return nil, "", false, fmt.Errorf("late session")
		}
		if p.upstreamSession != "" && session != p.upstreamSession {
			return nil, "", false, fmt.Errorf("session drift")
		}
		p.upstreamSession = session
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, p.profile.MaxResponseBytes+1))
	if err != nil || int64(len(data)) > p.profile.MaxResponseBytes {
		return nil, "", false, fmt.Errorf("response oversized")
	}
	if allowNoContent && len(expectedID) == 0 && len(bytes.TrimSpace(data)) == 0 {
		return nil, "application/json", false, nil
	}
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0]))
	switch contentType {
	case "application/json":
		data, err = validateExternalRPCResponse(data, expectedID)
	case "text/event-stream":
		data, err = matchingExternalSSEResponse(data, expectedID)
		contentType = "application/json"
	default:
		return nil, "", false, fmt.Errorf("invalid content type")
	}
	if err != nil {
		return nil, "", false, err
	}
	var envelope externalRPCResponse
	if decodeStrictJSONObject(data, &envelope) != nil {
		return nil, "", false, fmt.Errorf("invalid JSON-RPC response")
	}
	p.mu.Lock()
	if p.totalBytes+int64(len(data)) > p.profile.MaxTotalResponseBytes {
		p.mu.Unlock()
		return nil, "", false, fmt.Errorf("total response budget")
	}
	p.totalBytes += int64(len(data))
	p.mu.Unlock()
	return bytes.TrimSpace(data), contentType, len(envelope.Error) > 0, nil
}

func matchingExternalSSEResponse(raw []byte, expectedID json.RawMessage) ([]byte, error) {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	var data bytes.Buffer
	var matched []byte
	consume := func() error {
		candidate := bytes.TrimSpace(data.Bytes())
		data.Reset()
		if len(candidate) == 0 {
			return nil
		}
		kind, err := classifyExternalRPCMessage(candidate, expectedID)
		if err != nil {
			return err
		}
		if kind != "response" {
			return nil
		}
		if matched != nil {
			return fmt.Errorf("multiple JSON-RPC responses")
		}
		matched = append([]byte(nil), candidate...)
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := consume(); err != nil {
				return nil, err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if scanner.Err() != nil {
		return nil, scanner.Err()
	}
	if err := consume(); err != nil {
		return nil, err
	}
	if matched == nil {
		return nil, fmt.Errorf("matching JSON-RPC response is absent")
	}
	return validateExternalRPCResponse(matched, expectedID)
}

func classifyExternalRPCMessage(raw, expectedID json.RawMessage) (string, error) {
	if err := validateJSONNoDuplicateKeys(raw); err != nil {
		return "", err
	}
	var envelope map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&envelope); err != nil || envelope == nil || decoder.Decode(new(any)) != io.EOF {
		return "", fmt.Errorf("invalid JSON-RPC SSE message")
	}
	var version string
	if err := json.Unmarshal(envelope["jsonrpc"], &version); err != nil || version != "2.0" {
		return "", fmt.Errorf("invalid JSON-RPC SSE version")
	}
	if methodRaw, ok := envelope["method"]; ok {
		var method string
		if err := json.Unmarshal(methodRaw, &method); err != nil || method == "" {
			return "", fmt.Errorf("invalid JSON-RPC SSE request")
		}
		return "request", nil
	}
	actualID, err := externalRPCIDKey(envelope["id"])
	if err != nil {
		return "", fmt.Errorf("invalid JSON-RPC response id")
	}
	wantID, err := externalRPCIDKey(expectedID)
	if err != nil || actualID != wantID {
		return "", fmt.Errorf("JSON-RPC response id mismatch")
	}
	return "response", nil
}

func validateExternalRPCResponse(raw, expectedID json.RawMessage) ([]byte, error) {
	if kind, err := classifyExternalRPCMessage(raw, expectedID); err != nil || kind != "response" {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("expected JSON-RPC response")
	}
	var response externalRPCResponse
	if err := decodeStrictJSONObject(raw, &response); err != nil || response.JSONRPC != "2.0" {
		return nil, fmt.Errorf("invalid JSON-RPC response")
	}
	hasResult, hasError := len(response.Result) > 0, len(response.Error) > 0
	if hasResult == hasError {
		return nil, fmt.Errorf("JSON-RPC response must contain exactly one result or error")
	}
	if hasError {
		var rpcError struct {
			Code    *int            `json:"code"`
			Message *string         `json:"message"`
			Data    json.RawMessage `json:"data,omitempty"`
		}
		if err := decodeStrictJSONObject(response.Error, &rpcError); err != nil || rpcError.Code == nil || rpcError.Message == nil || *rpcError.Message == "" {
			return nil, fmt.Errorf("invalid JSON-RPC error")
		}
	}
	return append([]byte(nil), bytes.TrimSpace(raw)...), nil
}

func externalRPCIDKey(raw json.RawMessage) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if len(raw) == 0 || decoder.Decode(&value) != nil || decoder.Decode(new(any)) != io.EOF {
		return "", fmt.Errorf("missing JSON-RPC id")
	}
	switch value := value.(type) {
	case string:
		if value == "" || len(value) > 256 || strings.ContainsAny(value, "\r\n\x00") {
			return "", fmt.Errorf("invalid JSON-RPC id")
		}
		return "s:" + value, nil
	case json.Number:
		if _, err := value.Int64(); err != nil {
			return "", fmt.Errorf("invalid JSON-RPC id")
		}
		return "n:" + value.String(), nil
	default:
		return "", fmt.Errorf("invalid JSON-RPC id")
	}
}

func acceptsExternalMCPResponse(value string) bool {
	wantsJSON, wantsSSE := false, false
	for _, part := range strings.Split(value, ",") {
		mediaType, params, err := mime.ParseMediaType(strings.TrimSpace(part))
		if err != nil {
			continue
		}
		if rawQ, ok := params["q"]; ok {
			q, err := strconv.ParseFloat(rawQ, 64)
			if err != nil || q <= 0 || q > 1 {
				continue
			}
		}
		switch strings.ToLower(mediaType) {
		case "application/json":
			wantsJSON = true
		case "text/event-stream":
			wantsSSE = true
		}
	}
	return wantsJSON && wantsSSE
}

func validExternalMCPSessionID(value string) bool {
	if value == "" || len(value) > 4096 {
		return false
	}
	for _, b := range []byte(value) {
		if b < 0x21 || b > 0x7e {
			return false
		}
	}
	return true
}

func (p *ExternalMCPProxy) acquire(policy ExternalMCPToolPolicy, requestID string, cancel context.CancelFunc) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.inflight >= p.profile.MaxConcurrent || p.counts[policy.Name] >= policy.MaxInvocations || p.active[requestID] != nil {
		return false
	}
	p.counts[policy.Name]++
	p.inflight++
	p.active[requestID] = cancel
	return true
}
func (p *ExternalMCPProxy) release(requestID string) {
	p.mu.Lock()
	if cancel := p.active[requestID]; cancel != nil {
		cancel()
		delete(p.active, requestID)
	}
	p.inflight--
	p.mu.Unlock()
}

func (p *ExternalMCPProxy) cancelRequest(raw json.RawMessage) bool {
	var params struct {
		RequestID json.RawMessage `json:"requestId"`
		Reason    string          `json:"reason,omitempty"`
	}
	if decodeStrictJSONObject(raw, &params) != nil || len(params.Reason) > 1024 || strings.ContainsAny(params.Reason, "\x00") {
		return false
	}
	id, err := externalRPCIDKey(params.RequestID)
	if err != nil {
		return false
	}
	p.mu.Lock()
	cancel := p.active[id]
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return true
}
func (p *ExternalMCPProxy) deny(capability string, size int64) {
	p.record(capability, "deny", false, size, 0, 0, "")
}
func (p *ExternalMCPProxy) record(capability, decision string, success bool, requestBytes, responseBytes int64, duration time.Duration, identity string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sequence++
	record := ExternalMCPAuditRecord{Sequence: p.sequence, Capability: capability, Decision: decision, Success: success, RequestBytes: requestBytes, ResponseBytes: responseBytes, DurationMS: duration.Milliseconds()}
	if identity != "" {
		mac := hmac.New(sha256.New, p.auditKey)
		_, _ = mac.Write([]byte(identity))
		record.IdentityHMAC = hex.EncodeToString(mac.Sum(nil))
	}
	data, _ := json.Marshal(record)
	data = append(data, '\n')
	if p.auditErr == nil {
		if _, err := p.audit.Write(data); err != nil {
			p.auditErr = err
		} else if err := p.audit.Sync(); err != nil {
			p.auditErr = err
		}
	}
}
func loopbackOrigin(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.User != nil || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	host := parsed.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (p *ExternalMCPProxy) closeUpstreamSession(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, p.profile.UpstreamURL, nil)
	if err != nil {
		return err
	}
	for key, value := range p.headers {
		req.Header.Set(key, value)
	}
	req.Header.Set("Mcp-Session-Id", p.upstreamSession)
	req.Header.Set("MCP-Protocol-Version", p.profile.ProtocolVersion)
	resp, err := p.upstream.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusMethodNotAllowed {
		return fmt.Errorf("unexpected session close status")
	}
	return nil
}

func containsCanary(data []byte, canaries []string) bool {
	for _, value := range canaries {
		if value != "" && bytes.Contains(data, []byte(value)) {
			return true
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	for {
		var value any
		if err := decoder.Decode(&value); err == io.EOF {
			break
		} else if err != nil {
			break
		}
		if jsonValueContainsCanary(value, canaries) {
			return true
		}
	}
	return false
}

func jsonValueContainsCanary(value any, canaries []string) bool {
	contains := func(text string) bool {
		for _, canary := range canaries {
			if canary != "" && strings.Contains(text, canary) {
				return true
			}
		}
		return false
	}
	switch value := value.(type) {
	case string:
		return contains(value)
	case []any:
		for _, child := range value {
			if jsonValueContainsCanary(child, canaries) {
				return true
			}
		}
	case map[string]any:
		for key, child := range value {
			if contains(key) || jsonValueContainsCanary(child, canaries) {
				return true
			}
		}
	}
	return false
}
func filepathDir(path string) string {
	index := strings.LastIndexAny(path, "/\\")
	if index < 0 {
		return "."
	}
	if index == 0 {
		return path[:1]
	}
	return path[:index]
}

func readExternalMCPAudit(path string) (calls, failures, denials int, outputBytes int64, families []CapabilityFamilyMetric, err error) {
	data, err := readBoundedFile(path, 4<<20)
	if err != nil {
		return 0, 0, 0, 0, nil, err
	}
	values := map[string]CapabilityFamilyMetric{}
	var sequence int64
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var record ExternalMCPAuditRecord
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&record) != nil || decoder.Decode(new(any)) != io.EOF || record.Sequence != sequence+1 {
			return 0, 0, 0, 0, nil, fmt.Errorf("invalid external MCP audit")
		}
		sequence = record.Sequence
		if record.Decision == "deny" {
			denials++
			continue
		}
		if record.Decision != "allow" || record.Capability == "" {
			return 0, 0, 0, 0, nil, fmt.Errorf("invalid external MCP audit decision")
		}
		calls++
		outputBytes += record.ResponseBytes
		if !record.Success {
			failures++
		}
		mergeCapabilityFamily(values, record.Capability, !record.Success, record.ResponseBytes)
	}
	return calls, failures, denials, outputBytes, capabilityFamilySlice(values), nil
}
