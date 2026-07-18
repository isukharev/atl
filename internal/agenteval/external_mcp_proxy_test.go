package agenteval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestExternalMCPProxyFiltersAndEnforcesExactCalls(t *testing.T) {
	fixture := newExternalMCPFixture(t, false, false)
	defer fixture.server.Close()
	proxy, audit := startTestExternalProxy(t, fixture)
	endpoint, token := proxy.Endpoint()
	client := &http.Client{}
	callLocal := func(body string, auth string) (int, []byte) {
		req, _ := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Authorization", "Bearer "+auth)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, data
	}
	status, data := callLocal(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`, token)
	if status != 200 || bytes.Contains(data, []byte("hidden_create")) || !bytes.Contains(data, []byte("safe_lookup")) {
		t.Fatalf("filtered list status=%d body=%s", status, data)
	}
	allowed := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"safe_lookup","arguments":{"key":"A"}}}`
	if status, _ = callLocal(allowed, token); status != 200 {
		t.Fatalf("allowed call status=%d", status)
	}
	if status, _ = callLocal(allowed, token); status != 403 {
		t.Fatalf("exhausted call status=%d", status)
	}
	if status, _ = callLocal(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"safe_lookup","arguments":{"key":"B"}}}`, token); status != 403 {
		t.Fatalf("argument drift status=%d", status)
	}
	if status, _ = callLocal(`{"jsonrpc":"2.0","id":4,"method":"resources/read","params":{}}`, token); status != 403 {
		t.Fatalf("unknown method status=%d", status)
	}
	if status, _ = callLocal(`{"jsonrpc":"2.0","id":5,"method":"ping"}`, "wrong"); status != 401 {
		t.Fatalf("wrong auth status=%d", status)
	}
	if err := proxy.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls, failures, denials, _, families, err := readExternalMCPAudit(audit)
	if err != nil || calls != 1 || failures != 0 || denials != 4 || len(families) != 1 || atomic.LoadInt32(&fixture.toolCalls) != 1 {
		t.Fatalf("audit calls=%d failures=%d denials=%d families=%v upstream=%d err=%v", calls, failures, denials, families, fixture.toolCalls, err)
	}
}

func TestExternalMCPProxyPreflightRejectsCatalogAndReadOnlyDrift(t *testing.T) {
	for name, mutate := range map[string]func(*externalMCPFixture, ExternalMCPProfile) ExternalMCPProfile{
		"digest": func(_ *externalMCPFixture, p ExternalMCPProfile) ExternalMCPProfile {
			p.CatalogSHA256 = strings.Repeat("0", 64)
			return p
		},
		"annotation": func(f *externalMCPFixture, p ExternalMCPProfile) ExternalMCPProfile {
			f.writeAnnotation = true
			mutated := append([]json.RawMessage(nil), f.catalog...)
			mutated[0] = json.RawMessage(strings.ReplaceAll(string(mutated[0]), `"readOnlyHint":true`, `"readOnlyHint":false`))
			digest, _, _ := externalMCPCatalogIdentity(mutated)
			p.CatalogSHA256 = digest
			return p
		},
		"malformed annotation": func(f *externalMCPFixture, p ExternalMCPProfile) ExternalMCPProfile {
			mutated := append([]json.RawMessage(nil), f.catalog...)
			mutated[0] = json.RawMessage(strings.ReplaceAll(string(mutated[0]), `"readOnlyHint":true`, `"readOnlyHint":"true"`))
			f.catalog = mutated
			digest, _, _ := externalMCPCatalogIdentity(mutated)
			p.CatalogSHA256 = digest
			return p
		},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newExternalMCPFixture(t, false, false)
			profile := mutate(fixture, fixture.profile)
			defer fixture.server.Close()
			audit := filepath.Join(t.TempDir(), "audit.jsonl")
			if proxy, err := startExternalMCPProxyWithClient(context.Background(), profile, map[string]string{"X-Test-Auth": "secret"}, []string{"secret", fixture.server.URL}, audit, fixture.server.Client()); err == nil {
				_ = proxy.Close(context.Background())
				t.Fatal("unsafe catalog passed")
			}
		})
	}
}

func TestExternalMCPProxyPreflightAcceptsMissingOptionalReadOnlyAnnotations(t *testing.T) {
	fixture := newExternalMCPFixture(t, false, false)
	defer fixture.server.Close()
	mutated := append([]json.RawMessage(nil), fixture.catalog...)
	mutated[0] = json.RawMessage(strings.ReplaceAll(string(mutated[0]), `,"annotations":{"readOnlyHint":true,"destructiveHint":false}`, ""))
	fixture.catalog = mutated
	digest, _, err := externalMCPCatalogIdentity(mutated)
	if err != nil {
		t.Fatal(err)
	}
	profile := fixture.profile
	profile.CatalogSHA256 = digest
	auditDir := t.TempDir()
	if err := os.Chmod(auditDir, 0o700); err != nil {
		t.Fatal(err)
	}
	audit := filepath.Join(auditDir, "audit.jsonl")
	proxy, err := startExternalMCPProxyWithClient(context.Background(), profile, map[string]string{"X-Test-Auth": "secret"}, []string{"secret", fixture.server.URL}, audit, fixture.server.Client())
	if err != nil {
		t.Fatalf("owner-reviewed exact tool without optional annotations was rejected: %v", err)
	}
	if err := proxy.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestExternalMCPProxyPreflightAcceptsFiniteReviewedSchemaVariant(t *testing.T) {
	fixture := newExternalMCPFixture(t, false, false)
	defer fixture.server.Close()
	mutated := append([]json.RawMessage(nil), fixture.catalog...)
	mutated[0] = json.RawMessage(strings.ReplaceAll(string(mutated[0]), `"key":{"type":"string"}`, `"key":{"type":"string","description":"Reviewed variant"}`))
	fixture.catalog = mutated
	catalogDigest, byName, err := externalMCPCatalogIdentity(mutated)
	if err != nil {
		t.Fatal(err)
	}
	var selected struct {
		InputSchema json.RawMessage `json:"inputSchema"`
	}
	if err := json.Unmarshal(byName["safe_lookup"], &selected); err != nil {
		t.Fatal(err)
	}
	schemaDigest, err := canonicalJSONSHA(selected.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	profile := fixture.profile
	profile.CatalogSHA256 = catalogDigest
	profile.Tools[0].InputSchemaSHA256Alternates = []string{schemaDigest}
	auditDir := t.TempDir()
	if err := os.Chmod(auditDir, 0o700); err != nil {
		t.Fatal(err)
	}
	proxy, err := startExternalMCPProxyWithClient(context.Background(), profile, map[string]string{"X-Test-Auth": "secret"}, []string{"secret", fixture.server.URL}, filepath.Join(auditDir, "audit.jsonl"), fixture.server.Client())
	if err != nil {
		t.Fatalf("reviewed schema variant was rejected: %v", err)
	}
	if err := proxy.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	profile.Tools[0].InputSchemaSHA256Alternates = []string{profile.Tools[0].InputSchemaSHA256}
	if err := profile.Validate(); err == nil {
		t.Fatal("duplicate schema digest variant passed profile validation")
	}
}

func TestExternalMCPProxyStripsReservedClientMetadataBeforeUpstream(t *testing.T) {
	fixture := newExternalMCPFixture(t, false, false)
	defer fixture.server.Close()
	proxy, _ := startTestExternalProxy(t, fixture)
	endpoint, token := proxy.Endpoint()
	body := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"safe_lookup","arguments":{"key":"A"},"_meta":{"progressToken":"private-client-token"}}}`
	status, _ := postExternalMCP(t, endpoint, token, body, "application/json", "application/json, text/event-stream")
	if status != http.StatusOK {
		t.Fatalf("reserved client metadata call status=%d", status)
	}
	if atomic.LoadInt32(&fixture.callMeta) != 0 {
		t.Fatal("client metadata reached the upstream MCP server")
	}
	if err := proxy.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestExternalMCPProxyNeverReplaysAmbiguousCallOrCredentialEcho(t *testing.T) {
	for name, ambiguous := range map[string]bool{"ambiguous": true, "credential_echo": false} {
		t.Run(name, func(t *testing.T) {
			fixture := newExternalMCPFixture(t, ambiguous, !ambiguous)
			defer fixture.server.Close()
			proxy, _ := startTestExternalProxy(t, fixture)
			endpoint, token := proxy.Endpoint()
			body := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"safe_lookup","arguments":{"key":"A"}}}`
			req, _ := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			req.Header.Set("Authorization", "Bearer "+token)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != 502 || atomic.LoadInt32(&fixture.toolCalls) != 1 {
				t.Fatalf("status=%d calls=%d", resp.StatusCode, fixture.toolCalls)
			}
			if err := proxy.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestExternalMCPProxyRejectsDuplicateAndCursorDrift(t *testing.T) {
	for name, mutate := range map[string]func(*externalMCPFixture){"duplicate": func(f *externalMCPFixture) { f.catalog = append(f.catalog, f.catalog[0]) }, "cursor": func(f *externalMCPFixture) { f.repeatCursor = true }} {
		t.Run(name, func(t *testing.T) {
			f := newExternalMCPFixture(t, false, false)
			mutate(f)
			defer f.server.Close()
			directory := t.TempDir()
			_ = os.Chmod(directory, 0o700)
			proxy, err := startExternalMCPProxyWithClient(context.Background(), f.profile, map[string]string{"X-Test-Auth": "secret"}, []string{"secret"}, filepath.Join(directory, "audit"), f.server.Client())
			if err == nil {
				_ = proxy.Close(context.Background())
				t.Fatal("catalog drift passed")
			}
		})
	}
}

func TestExternalMCPProxyAcceptsReorderedIdenticalCatalog(t *testing.T) {
	f := newExternalMCPFixture(t, false, false)
	f.catalog[0], f.catalog[1] = f.catalog[1], f.catalog[0]
	defer f.server.Close()
	proxy, _ := startTestExternalProxy(t, f)
	if err := proxy.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestExternalMCPProxyAcceptsOnlyExplicitReviewedCatalogVariant(t *testing.T) {
	f := newExternalMCPFixture(t, false, false)
	mutated := append([]json.RawMessage(nil), f.catalog...)
	mutated[1] = json.RawMessage(strings.Replace(string(mutated[1]), "Create value", "Reviewed variant", 1))
	digest, _, err := externalMCPCatalogIdentity(mutated)
	if err != nil {
		t.Fatal(err)
	}
	f.profile.CatalogSHA256Alternates = []string{digest}
	f.catalog = mutated
	defer f.server.Close()
	proxy, _ := startTestExternalProxy(t, f)
	if err := proxy.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	f = newExternalMCPFixture(t, false, false)
	f.catalog[1] = json.RawMessage(strings.Replace(string(f.catalog[1]), "Create value", "Unreviewed variant", 1))
	defer f.server.Close()
	if _, err := startExternalMCPProxyWithClient(context.Background(), f.profile, map[string]string{"X-Test-Auth": "secret"}, []string{"secret"}, filepath.Join(t.TempDir(), "audit"), f.server.Client()); err == nil {
		t.Fatal("unreviewed catalog variant passed")
	}
}

func TestExternalMCPCatalogIdentityIsOrderAndObjectKeyIndependent(t *testing.T) {
	first := []json.RawMessage{
		json.RawMessage(`{"name":"b","description":"second","inputSchema":{"type":"object"}}`),
		json.RawMessage(`{"name":"a","description":"first","inputSchema":{"type":"object"}}`),
	}
	second := []json.RawMessage{
		json.RawMessage(`{"inputSchema":{"type":"object"},"description":"first","name":"a"}`),
		json.RawMessage(`{"inputSchema":{"type":"object"},"description":"second","name":"b"}`),
	}
	want, _, err := externalMCPCatalogIdentity(first)
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := externalMCPCatalogIdentity(second)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("reordered identity changed: got %s want %s", got, want)
	}
	second[1] = json.RawMessage(`{"inputSchema":{"type":"object"},"description":"changed","name":"b"}`)
	changed, _, err := externalMCPCatalogIdentity(second)
	if err != nil {
		t.Fatal(err)
	}
	if changed == want {
		t.Fatal("semantic catalog drift did not change identity")
	}
	if _, _, err := externalMCPCatalogIdentity(append(first, first[0])); err == nil {
		t.Fatal("duplicate catalog identity passed")
	}
	invalid := append([]byte(`{"name":"broken","description":"`), 0xff)
	invalid = append(invalid, []byte(`","inputSchema":{"type":"object"}}`)...)
	if _, _, err := externalMCPCatalogIdentity([]json.RawMessage{json.RawMessage(invalid)}); err == nil {
		t.Fatal("invalid UTF-8 catalog identity passed")
	}
}

func TestExternalMCPProxyConcurrentCallIsDeniedAtomically(t *testing.T) {
	f := newExternalMCPFixture(t, false, false)
	f.block = make(chan struct{})
	f.started = make(chan struct{}, 1)
	f.profile.Tools[0].MaxInvocations = 2
	defer f.server.Close()
	proxy, audit := startTestExternalProxy(t, f)
	endpoint, token := proxy.Endpoint()
	invoke := func() int {
		body := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"safe_lookup","arguments":{"key":"A"}}}`
		req, _ := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}
	first := make(chan int, 1)
	go func() { first <- invoke() }()
	<-f.started
	if status := invoke(); status != 403 {
		t.Fatalf("concurrent status=%d", status)
	}
	close(f.block)
	if status := <-first; status != 200 {
		t.Fatalf("first status=%d", status)
	}
	if err := proxy.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls, _, denials, _, _, err := readExternalMCPAudit(audit)
	if err != nil || calls != 1 || denials != 1 || atomic.LoadInt32(&f.toolCalls) != 1 {
		t.Fatalf("calls=%d denials=%d upstream=%d err=%v", calls, denials, f.toolCalls, err)
	}
}

func TestExternalMCPProxyCancellationStopsActiveUpstreamCall(t *testing.T) {
	fixture := newExternalMCPFixture(t, false, false)
	fixture.block = make(chan struct{})
	fixture.started = make(chan struct{}, 1)
	fixture.canceled = make(chan struct{}, 1)
	defer fixture.server.Close()
	proxy, audit := startTestExternalProxy(t, fixture)
	endpoint, token := proxy.Endpoint()
	callDone := make(chan struct {
		status int
		err    error
	}, 1)
	go func() {
		status, _, err := doExternalMCP(endpoint, token,
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"safe_lookup","arguments":{"key":"A"}}}`,
			"application/json", "application/json, text/event-stream")
		callDone <- struct {
			status int
			err    error
		}{status: status, err: err}
	}()
	<-fixture.started
	cancelStatus, _ := postExternalMCP(t, endpoint, token,
		fmt.Sprintf(`{"jsonrpc":"2.0","method":%q,"params":{"requestId":2,"reason":"stop"}}`, externalMCPCancelledMethod),
		"application/json", "application/json, text/event-stream")
	if cancelStatus != http.StatusAccepted {
		t.Fatalf("cancel status=%d", cancelStatus)
	}
	select {
	case <-fixture.canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream request was not canceled")
	}
	select {
	case result := <-callDone:
		if result.err != nil || result.status != http.StatusBadGateway {
			t.Fatalf("tool result status=%d err=%v", result.status, result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled tool call did not finish")
	}
	if err := proxy.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls, failures, denials, _, _, err := readExternalMCPAudit(audit)
	if err != nil || calls != 1 || failures != 1 || denials != 0 {
		t.Fatalf("calls=%d failures=%d denials=%d err=%v", calls, failures, denials, err)
	}
}

func TestExternalMCPProxyRejectsInvalidRPCResponsesAndAuditsFailures(t *testing.T) {
	for name, expectedStatus := range map[string]int{
		"wrong_id":       http.StatusBadGateway,
		"missing_id":     http.StatusBadGateway,
		"rpc_error":      http.StatusOK,
		"tool_error":     http.StatusOK,
		"malformed":      http.StatusBadGateway,
		"escaped_secret": http.StatusBadGateway,
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newExternalMCPFixture(t, false, false)
			fixture.responseMode = name
			defer fixture.server.Close()
			proxy, audit := startTestExternalProxy(t, fixture)
			endpoint, token := proxy.Endpoint()
			status, _ := postExternalMCP(t, endpoint, token,
				`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"safe_lookup","arguments":{"key":"A"}}}`,
				"application/json", "application/json, text/event-stream")
			if status != expectedStatus {
				t.Fatalf("status=%d want=%d", status, expectedStatus)
			}
			if err := proxy.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			calls, failures, denials, _, _, err := readExternalMCPAudit(audit)
			if err != nil || calls != 1 || failures != 1 || denials != 0 || atomic.LoadInt32(&fixture.toolCalls) != 1 {
				t.Fatalf("calls=%d failures=%d denials=%d upstream=%d err=%v", calls, failures, denials, fixture.toolCalls, err)
			}
		})
	}
}

func TestExternalMCPProxySelectsMatchingSSEResponseAfterServerMessages(t *testing.T) {
	for _, mode := range []string{"sse_notification", "sse_request"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newExternalMCPFixture(t, false, false)
			fixture.responseMode = mode
			defer fixture.server.Close()
			proxy, audit := startTestExternalProxy(t, fixture)
			endpoint, token := proxy.Endpoint()
			status, body := postExternalMCP(t, endpoint, token,
				`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"safe_lookup","arguments":{"key":"A"}}}`,
				"application/json", "application/json, text/event-stream")
			if status != http.StatusOK || !bytes.Contains(body, []byte(`"id":2`)) || bytes.Contains(body, []byte("notifications/progress")) || bytes.Contains(body, []byte("sampling/createMessage")) {
				t.Fatalf("status=%d body=%s", status, body)
			}
			if err := proxy.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			calls, failures, denials, _, _, err := readExternalMCPAudit(audit)
			if err != nil || calls != 1 || failures != 0 || denials != 0 {
				t.Fatalf("calls=%d failures=%d denials=%d err=%v", calls, failures, denials, err)
			}
		})
	}
}

func TestExternalMCPProxyRejectsCredentialInSelectedCatalogDescription(t *testing.T) {
	fixture := newExternalMCPFixture(t, false, false)
	defer fixture.server.Close()
	fixture.catalog[0] = json.RawMessage(strings.Replace(string(fixture.catalog[0]), "Read one value", "Read secret value", 1))
	fixture.rehash()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	proxy, err := startExternalMCPProxyWithClient(context.Background(), fixture.profile, map[string]string{"X-Test-Auth": "secret"}, []string{"secret"}, filepath.Join(directory, "audit.jsonl"), fixture.server.Client())
	if err == nil {
		_ = proxy.Close(context.Background())
		t.Fatal("selected catalog leaked a credential canary")
	}
}

func TestExternalMCPProxyRequiresStrictPOSTMediaTypes(t *testing.T) {
	fixture := newExternalMCPFixture(t, false, false)
	defer fixture.server.Close()
	proxy, audit := startTestExternalProxy(t, fixture)
	endpoint, token := proxy.Endpoint()
	body := `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`
	for name, media := range map[string]struct{ contentType, accept string }{
		"jsonp":        {"application/jsonp", "application/json, text/event-stream"},
		"missing_sse":  {"application/json", "application/json"},
		"missing_json": {"application/json", "text/event-stream"},
		"json_q_zero":  {"application/json", "application/json;q=0, text/event-stream"},
		"sse_q_zero":   {"application/json", "application/json, text/event-stream;q=0"},
	} {
		t.Run(name, func(t *testing.T) {
			status, _ := postExternalMCP(t, endpoint, token, body, media.contentType, media.accept)
			if status != http.StatusBadRequest {
				t.Fatalf("status=%d", status)
			}
		})
	}
	for name, invalidBody := range map[string]string{
		"missing_request_id": `{"jsonrpc":"2.0","method":"tools/call","params":{"name":"safe_lookup","arguments":{"key":"A"}}}`,
		"null_request_id":    `{"jsonrpc":"2.0","id":null,"method":"tools/list","params":{}}`,
	} {
		t.Run(name, func(t *testing.T) {
			status, _ := postExternalMCP(t, endpoint, token, invalidBody, "application/json", "application/json, text/event-stream")
			if status != http.StatusBadRequest {
				t.Fatalf("status=%d", status)
			}
		})
	}
	status, _ := postExternalMCP(t, endpoint, token, body, "application/json; charset=utf-8", "application/json, text/event-stream")
	if status != http.StatusOK {
		t.Fatalf("valid request status=%d", status)
	}
	if err := proxy.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, _, denials, _, _, err := readExternalMCPAudit(audit)
	if err != nil || denials != 7 {
		t.Fatalf("denials=%d err=%v", denials, err)
	}
}

func TestExternalMCPProxyRejectsDuplicateJSONKeysBeforeForwarding(t *testing.T) {
	fixture := newExternalMCPFixture(t, false, false)
	defer fixture.server.Close()
	proxy, audit := startTestExternalProxy(t, fixture)
	endpoint, token := proxy.Endpoint()
	for name, body := range map[string]string{
		"outer_method":  `{"jsonrpc":"2.0","id":2,"method":"tools/list","method":"tools/call","params":{"name":"safe_lookup","arguments":{"key":"A"}}}`,
		"folded_method": `{"jsonrpc":"2.0","id":2,"method":"tools/list","Method":"tools/call","params":{"name":"safe_lookup","arguments":{"key":"A"}}}`,
		"params_name":   `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"other","name":"safe_lookup","arguments":{"key":"A"}}}`,
		"nested_args":   `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"safe_lookup","arguments":{"key":"B","key":"A"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			status, _ := postExternalMCP(t, endpoint, token, body, "application/json", "application/json, text/event-stream")
			if status != http.StatusBadRequest {
				t.Fatalf("status=%d", status)
			}
		})
	}
	if err := proxy.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, _, denials, _, _, err := readExternalMCPAudit(audit)
	if err != nil || denials != 4 || atomic.LoadInt32(&fixture.toolCalls) != 0 {
		t.Fatalf("denials=%d upstream=%d err=%v", denials, fixture.toolCalls, err)
	}
}

func TestExternalMCPProfileAndCatalogRejectDuplicateJSONKeys(t *testing.T) {
	profile := validExternalTestProfile()
	profile.Tools[0].AllowedArguments = []json.RawMessage{json.RawMessage(`{"key":"B","key":"A"}`)}
	if err := profile.Validate(); err == nil {
		t.Fatal("duplicate reviewed argument key passed")
	}

	fixture := newExternalMCPFixture(t, false, false)
	defer fixture.server.Close()
	fixture.catalog[0] = json.RawMessage(strings.Replace(string(fixture.catalog[0]), `"additionalProperties":false`, `"additionalProperties":true,"additionalProperties":false`, 1))
	fixture.rehash()
	directory := t.TempDir()
	_ = os.Chmod(directory, 0o700)
	if proxy, err := startExternalMCPProxyWithClient(context.Background(), fixture.profile, map[string]string{"X-Test-Auth": "secret"}, []string{"secret"}, filepath.Join(directory, "audit.jsonl"), fixture.server.Client()); err == nil {
		_ = proxy.Close(context.Background())
		t.Fatal("duplicate catalog schema key passed")
	}
}

func TestExternalMCPSessionValidationLateAssignmentAndCleanup(t *testing.T) {
	for name, value := range map[string]string{
		"space": "bad session", "delete": "bad\x7f", "non_ascii": "bad-π",
	} {
		t.Run(name, func(t *testing.T) {
			if validExternalMCPSessionID(value) {
				t.Fatalf("invalid session %q passed", value)
			}
		})
	}
	if !validExternalMCPSessionID("session-._~123") {
		t.Fatal("visible ASCII session was rejected")
	}

	t.Run("late_session", func(t *testing.T) {
		fixture := newExternalMCPFixture(t, false, false)
		fixture.lateSession = true
		defer fixture.server.Close()
		directory := t.TempDir()
		_ = os.Chmod(directory, 0o700)
		if proxy, err := startExternalMCPProxyWithClient(context.Background(), fixture.profile, map[string]string{"X-Test-Auth": "secret"}, []string{"secret"}, filepath.Join(directory, "audit.jsonl"), fixture.server.Client()); err == nil {
			_ = proxy.Close(context.Background())
			t.Fatal("late session assignment passed")
		}
	})

	t.Run("delete_cleanup", func(t *testing.T) {
		fixture := newExternalMCPFixture(t, false, false)
		defer fixture.server.Close()
		proxy, _ := startTestExternalProxy(t, fixture)
		if err := proxy.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := atomic.LoadInt32(&fixture.deleteCalls); got != 1 {
			t.Fatalf("DELETE calls=%d", got)
		}
	})
}

func TestExternalMCPFinalArtifactCanarySemanticScan(t *testing.T) {
	for _, data := range [][]byte{
		[]byte(`{"answer":"se\u0063ret"}`),
		[]byte(`{"nested":{"answer":"prefix-secret-suffix"}}`),
	} {
		if !containsCanary(data, []string{"secret"}) {
			t.Fatalf("escaped final canary was missed: %s", data)
		}
	}
}

func postExternalMCP(t *testing.T, endpoint, token, body, contentType, accept string) (int, []byte) {
	t.Helper()
	status, data, err := doExternalMCP(endpoint, token, body, contentType, accept)
	if err != nil {
		t.Fatal(err)
	}
	return status, data
}

func doExternalMCP(endpoint, token, body, contentType, accept string) (int, []byte, error) {
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", accept)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, data, nil
}

type externalMCPFixture struct {
	server                           *httptest.Server
	profile                          ExternalMCPProfile
	catalog                          []json.RawMessage
	toolCalls                        int32
	callMeta                         int32
	deleteCalls                      int32
	ambiguous, echo, writeAnnotation bool
	repeatCursor                     bool
	block                            chan struct{}
	started                          chan struct{}
	canceled                         chan struct{}
	responseMode                     string
	sessionID                        string
	lateSession                      bool
}

func newExternalMCPFixture(t *testing.T, ambiguous, echo bool) *externalMCPFixture {
	t.Helper()
	f := &externalMCPFixture{ambiguous: ambiguous, echo: echo, sessionID: "session-1"}
	safe := json.RawMessage(`{"name":"safe_lookup","description":"Read one value","inputSchema":{"type":"object","properties":{"key":{"type":"string"}},"required":["key"],"additionalProperties":false},"annotations":{"readOnlyHint":true,"destructiveHint":false}}`)
	hidden := json.RawMessage(`{"name":"hidden_create","description":"Create value","inputSchema":{"type":"object"},"annotations":{"readOnlyHint":false,"destructiveHint":true}}`)
	f.catalog = []json.RawMessage{safe, hidden}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test-Auth") != "secret" {
			http.Error(w, "denied", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodDelete {
			if r.Header.Get("Mcp-Session-Id") != f.sessionID {
				http.Error(w, "bad session", http.StatusBadRequest)
				return
			}
			atomic.AddInt32(&f.deleteCalls, 1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		data, _ := io.ReadAll(r.Body)
		var request externalRPCRequest
		_ = json.Unmarshal(data, &request)
		switch request.Method {
		case "initialize":
			w.Header().Set("Content-Type", "text/event-stream")
			if !f.lateSession {
				w.Header().Set("Mcp-Session-Id", f.sessionID)
			}
			fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"protocolVersion\":\"2025-06-18\",\"capabilities\":{\"tools\":{}}}}\n\n")
		case "notifications/initialized":
			w.WriteHeader(202)
		case "tools/list":
			if f.lateSession {
				w.Header().Set("Mcp-Session-Id", f.sessionID)
			}
			catalog := append([]json.RawMessage(nil), f.catalog...)
			if f.writeAnnotation {
				catalog[0] = json.RawMessage(strings.ReplaceAll(string(catalog[0]), `"readOnlyHint":true`, `"readOnlyHint":false`))
			}
			listed := map[string]any{"tools": catalog}
			if f.repeatCursor {
				listed["nextCursor"] = "same"
			}
			result, _ := json.Marshal(listed)
			writeRPCResult(w, request.ID, result)
		case "tools/call":
			atomic.AddInt32(&f.toolCalls, 1)
			var forwardedParams map[string]json.RawMessage
			_ = json.Unmarshal(request.Params, &forwardedParams)
			if _, present := forwardedParams["_meta"]; present {
				atomic.StoreInt32(&f.callMeta, 1)
			}
			if f.started != nil {
				select {
				case f.started <- struct{}{}:
				default:
				}
			}
			if f.block != nil {
				select {
				case <-f.block:
				case <-r.Context().Done():
					if f.canceled != nil {
						select {
						case f.canceled <- struct{}{}:
						default:
						}
					}
					return
				}
			}
			if f.ambiguous {
				panic(http.ErrAbortHandler)
			}
			switch f.responseMode {
			case "wrong_id":
				writeRPCResult(w, json.RawMessage(`999`), json.RawMessage(`{"content":[]}`))
			case "missing_id":
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"jsonrpc":"2.0","result":{"content":[]}}`)
			case "rpc_error":
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32000,"message":"failed"}}`, request.ID)
			case "tool_error":
				writeRPCResult(w, request.ID, json.RawMessage(`{"content":[{"type":"text","text":"failed"}],"isError":true}`))
			case "malformed":
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{`)
			case "sse_notification":
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{}}\n\ndata: {\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"content\":[]}}\n\n", request.ID)
			case "sse_request":
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"id\":\"server-1\",\"method\":\"sampling/createMessage\",\"params\":{}}\n\ndata: {\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"content\":[]}}\n\n", request.ID)
			case "escaped_secret":
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"se\u0063ret"}]}}`, request.ID)
			default:
				value := "ok"
				if f.echo {
					value = "secret"
				}
				result, _ := json.Marshal(map[string]any{"content": []map[string]string{{"type": "text", "text": value}}})
				writeRPCResult(w, request.ID, result)
			}
		default:
			http.Error(w, "unexpected", 500)
		}
	})
	f.server = httptest.NewTLSServer(handler)
	catalogDigest, _, _ := externalMCPCatalogIdentity(f.catalog)
	var safeValue struct {
		InputSchema json.RawMessage `json:"inputSchema"`
	}
	_ = json.Unmarshal(safe, &safeValue)
	schemaDigest, _ := canonicalJSONSHA(safeValue.InputSchema)
	f.profile = ExternalMCPProfile{SchemaVersion: 1, UpstreamURL: f.server.URL, ProtocolVersion: "2025-06-18", CatalogSHA256: catalogDigest, ReviewedRO: true, Headers: []ExternalMCPHeader{{Name: "X-Test-Auth", ValueFrom: "jira.credential"}}, Tools: []ExternalMCPToolPolicy{{Name: "safe_lookup", Capability: "jira.issue.field", InputSchemaSHA256: schemaDigest, MaxInvocations: 1, AllowedArguments: []json.RawMessage{json.RawMessage(`{"key":"A"}`)}}}, MaxRequestBytes: 1 << 20, MaxResponseBytes: 1 << 20, MaxTotalResponseBytes: 4 << 20, MaxConcurrent: 1, TimeoutSeconds: 10}
	return f
}
func (f *externalMCPFixture) rehash() {
	digest, _, _ := externalMCPCatalogIdentity(f.catalog)
	f.profile.CatalogSHA256 = digest
}
func startTestExternalProxy(t *testing.T, f *externalMCPFixture) (*ExternalMCPProxy, string) {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	audit := filepath.Join(directory, "audit.jsonl")
	proxy, err := startExternalMCPProxyWithClient(context.Background(), f.profile, map[string]string{"X-Test-Auth": "secret"}, []string{"secret", f.server.URL}, audit, f.server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return proxy, audit
}
func writeRPCResult(w http.ResponseWriter, id, result json.RawMessage) {
	data, _ := json.Marshal(externalRPCResponse{JSONRPC: "2.0", ID: id, Result: result})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}
func TestExternalMCPProfileRejectsLiteralOrUnsafePolicy(t *testing.T) {
	profile := ExternalMCPProfile{SchemaVersion: 1, UpstreamURL: "http://example.invalid/mcp", ProtocolVersion: "2025-06-18", CatalogSHA256: strings.Repeat("0", 64), ReviewedRO: true}
	if profile.Validate() == nil {
		t.Fatal("insecure profile passed")
	}
	_ = os.ErrNotExist
}

func TestLoadExternalMCPProfileRequiresOwnerOnlyOutsideRepository(t *testing.T) {
	fixture := newExternalMCPFixture(t, false, false)
	defer fixture.server.Close()
	directory := t.TempDir()
	_ = os.Chmod(directory, 0o700)
	path := filepath.Join(directory, "profile.json")
	data, _ := json.Marshal(fixture.profile)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	repository := t.TempDir()
	loaded, err := LoadExternalMCPProfile(path, repository)
	if err != nil || loaded.UpstreamURL != fixture.profile.UpstreamURL {
		t.Fatalf("load err=%v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadExternalMCPProfile(path, repository); err == nil {
		t.Fatal("world-readable profile passed")
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadExternalMCPProfile(path, repository); err == nil {
			t.Fatal("profile in a shared directory passed")
		}
		if err := os.Chmod(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	insideDir := filepath.Join(repository, "private")
	if err := os.Mkdir(insideDir, 0o700); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(insideDir, "profile.json")
	if err := os.WriteFile(inside, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadExternalMCPProfile(inside, repository); err == nil {
		t.Fatal("profile inside the repository passed")
	}
	if runtime.GOOS != "windows" {
		linkDir := t.TempDir()
		if err := os.Chmod(linkDir, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(linkDir, "profile.json")
		if err := os.Symlink(path, link); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadExternalMCPProfile(link, repository); err == nil {
			t.Fatal("symlink profile passed")
		}
	}
	duplicatePath := filepath.Join(directory, "duplicate.json")
	duplicateData := bytes.Replace(data, []byte("{"), []byte(`{"schema_version":1,`), 1)
	if err := os.WriteFile(duplicatePath, duplicateData, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadExternalMCPProfile(duplicatePath, repository); err == nil {
		t.Fatal("profile with duplicate JSON key passed")
	}
}
