package agenteval

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLiveGatewayBrokersCredentialsAndWritesPrivateAudit(t *testing.T) {
	var upstreamCalls atomic.Int64
	var upstreamAuthorization, upstreamPath, upstreamCookie string
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		upstreamCalls.Add(1)
		upstreamAuthorization = request.Header.Get("Authorization")
		upstreamPath = request.URL.RequestURI()
		upstreamCookie = request.Header.Get("Cookie")
		response.Header().Set("Content-Type", "application/json")
		response.Header().Set("Set-Cookie", "session=private")
		_, _ = io.WriteString(response, `{"ok":true}`)
	}))
	defer upstream.Close()

	gateway, auditPath := startTestLiveGateway(t, upstream.URL+"/jira", 10, 1024, 4096)
	endpoint := gateway.Endpoints()["jira"]
	if endpoint.Token == "" || endpoint.Token == "upstream-secret" || !strings.HasPrefix(endpoint.BaseURL, "http://127.0.0.1:") {
		t.Fatalf("unsafe endpoint: %+v", endpoint)
	}
	request, err := http.NewRequest(http.MethodGet, endpoint.BaseURL+"/rest/api/2/issue/PROJ-1?fields=summary", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+endpoint.Token)
	request.Header.Set("Cookie", "model-cookie=unsafe")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	if closeErr := response.Body.Close(); err != nil || closeErr != nil {
		t.Fatalf("read=%v close=%v", err, closeErr)
	}
	if response.StatusCode != http.StatusOK || string(body) != `{"ok":true}` || response.Header.Get("Set-Cookie") != "" {
		t.Fatalf("status=%d body=%s headers=%v", response.StatusCode, body, response.Header)
	}
	if upstreamCalls.Load() != 1 || upstreamAuthorization != "Bearer upstream-secret" || upstreamCookie != "" || upstreamPath != "/jira/rest/api/2/issue/PROJ-1?fields=summary" {
		t.Fatalf("calls=%d auth=%q cookie=%q path=%q", upstreamCalls.Load(), upstreamAuthorization, upstreamCookie, upstreamPath)
	}
	if err := gateway.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	audit, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"PROJ-1", "fields", "summary", "upstream-secret", endpoint.Token, upstream.URL} {
		if bytes.Contains(audit, []byte(secret)) {
			t.Fatalf("audit leaked %q: %s", secret, audit)
		}
	}
	records := decodeLiveGatewayAudit(t, audit)
	if len(records) != 2 || records[0].Decision != "forward" || records[1].Decision != "allow" || records[0].RequestHMAC != records[1].RequestHMAC || len(records[0].RequestHMAC) != 64 || records[1].ResponseBytes != int64(len(body)) {
		t.Fatalf("records=%+v", records)
	}
	info, err := os.Stat(auditPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%v err=%v", info.Mode(), err)
	}
}

func TestLiveGatewayRejectsUnsafeRequestsBeforeUpstream(t *testing.T) {
	var upstreamCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		_, _ = io.WriteString(response, `{}`)
	}))
	defer upstream.Close()
	gateway, _ := startTestLiveGateway(t, upstream.URL, 20, 1024, 4096)
	defer gateway.Close(context.Background())
	endpoint := gateway.Endpoints()["jira"]

	tests := []struct {
		name   string
		method string
		path   string
		body   io.Reader
		auth   bool
		header map[string]string
	}{
		{name: "missing auth", method: http.MethodGet, path: "/rest/api/2/field"},
		{name: "wrong auth", method: http.MethodGet, path: "/rest/api/2/field", header: map[string]string{"Authorization": "Bearer wrong"}},
		{name: "write", method: http.MethodPost, path: "/rest/api/2/issue", auth: true},
		{name: "get body", method: http.MethodGet, path: "/rest/api/2/field", body: strings.NewReader(`{}`), auth: true},
		{name: "override", method: http.MethodGet, path: "/rest/api/2/field", auth: true, header: map[string]string{"X-HTTP-Method-Override": "DELETE"}},
		{name: "query override", method: http.MethodGet, path: "/rest/api/2/field?_method=DELETE", auth: true},
		{name: "foreign route", method: http.MethodGet, path: "/secure/admin", auth: true},
		{name: "unclean route", method: http.MethodGet, path: "/rest/api/../admin", auth: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, err := http.NewRequest(test.method, endpoint.BaseURL+test.path, test.body)
			if err != nil {
				t.Fatal(err)
			}
			if test.auth {
				request.Header.Set("Authorization", "Bearer "+endpoint.Token)
			}
			for name, value := range test.header {
				request.Header.Set(name, value)
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if response.StatusCode < 400 {
				t.Fatalf("unsafe request status=%d", response.StatusCode)
			}
		})
	}
	if upstreamCalls.Load() != 0 {
		t.Fatalf("upstream observed %d blocked requests", upstreamCalls.Load())
	}
}

func TestLiveGatewayBlocksRedirectsAndResponseBudget(t *testing.T) {
	var redirectTargetCalls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirectTargetCalls.Add(1)
	}))
	defer target.Close()
	var mode atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		if mode.Load() == 0 {
			response.Header().Set("Location", target.URL+"/private")
			response.WriteHeader(http.StatusFound)
			return
		}
		if mode.Load() == 1 {
			_, _ = io.WriteString(response, "12345")
			return
		}
		_, _ = io.WriteString(response, "123")
	}))
	defer upstream.Close()
	gateway, _ := startTestLiveGateway(t, upstream.URL, 4, 4, 4)
	defer gateway.Close(context.Background())
	endpoint := gateway.Endpoints()["jira"]
	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	get := func() int {
		request, _ := http.NewRequest(http.MethodGet, endpoint.BaseURL+"/rest/api/2/field", nil)
		request.Header.Set("Authorization", "Bearer "+endpoint.Token)
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		return response.StatusCode
	}
	if status := get(); status != http.StatusBadGateway || redirectTargetCalls.Load() != 0 {
		t.Fatalf("redirect status=%d target_calls=%d", status, redirectTargetCalls.Load())
	}
	mode.Store(1)
	if status := get(); status != http.StatusBadGateway {
		t.Fatalf("oversized status=%d", status)
	}
	mode.Store(2)
	if status := get(); status != http.StatusOK {
		t.Fatalf("first total-budget status=%d", status)
	}
	if status := get(); status != http.StatusBadGateway {
		t.Fatalf("second total-budget status=%d", status)
	}
}

func TestLiveGatewayEnforcesConcurrencyBeforeUpstream(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var upstreamCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		started <- struct{}{}
		<-release
		_, _ = io.WriteString(response, `{}`)
	}))
	defer upstream.Close()
	auditDir := t.TempDir()
	if err := os.Chmod(auditDir, 0o700); err != nil {
		t.Fatal(err)
	}
	gateway, err := StartLiveGateway(LiveGatewayConfig{
		AuditPath: filepath.Join(auditDir, "audit.jsonl"), MaxRequests: 2, MaxConcurrent: 1,
		MaxResponseBytes: 1024, MaxTotalResponseBytes: 2048, RequestTimeout: 5 * time.Second,
		Services: map[string]LiveGatewayServiceConfig{
			"jira": {BaseURL: upstream.URL, Token: "upstream-secret", Routes: []LiveGatewayRoute{{Name: "jira_api", PathPrefix: "/rest/api"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close(context.Background())
	endpoint := gateway.Endpoints()["jira"]
	call := func() int {
		request, _ := http.NewRequest(http.MethodGet, endpoint.BaseURL+"/rest/api/2/field", nil)
		request.Header.Set("Authorization", "Bearer "+endpoint.Token)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			return 0
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		return response.StatusCode
	}
	first := make(chan int, 1)
	go func() { first <- call() }()
	<-started
	if status := call(); status != http.StatusTooManyRequests || upstreamCalls.Load() != 1 {
		t.Fatalf("concurrent status=%d calls=%d", status, upstreamCalls.Load())
	}
	close(release)
	if status := <-first; status != http.StatusOK {
		t.Fatalf("first status=%d", status)
	}
}

func TestLiveGatewayEnforcesRequestAndAuditBoundaries(t *testing.T) {
	var upstreamCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		_, _ = io.WriteString(response, `{}`)
	}))
	defer upstream.Close()
	gateway, _ := startTestLiveGateway(t, upstream.URL, 1, 1024, 1024)
	endpoint := gateway.Endpoints()["jira"]
	request := func() int {
		req, _ := http.NewRequest(http.MethodGet, endpoint.BaseURL+"/rest/api/2/field", nil)
		req.Header.Set("Authorization", "Bearer "+endpoint.Token)
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		return response.StatusCode
	}
	if status := request(); status != http.StatusOK {
		t.Fatalf("first status=%d", status)
	}
	if status := request(); status != http.StatusTooManyRequests || upstreamCalls.Load() != 1 {
		t.Fatalf("second status=%d calls=%d", status, upstreamCalls.Load())
	}
	if err := gateway.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	gateway, _ = startTestLiveGateway(t, upstream.URL, 1, 1024, 1024)
	endpoint = gateway.Endpoints()["jira"]
	if err := gateway.state.audit.Close(); err != nil {
		t.Fatal(err)
	}
	if status := request(); status != http.StatusBadGateway || upstreamCalls.Load() != 1 {
		t.Fatalf("audit failure status=%d calls=%d", status, upstreamCalls.Load())
	}
	_ = gateway.Close(context.Background())
}

func TestLiveGatewayRejectsUnsafeConfiguration(t *testing.T) {
	auditDir := t.TempDir()
	if err := os.Chmod(auditDir, 0o700); err != nil {
		t.Fatal(err)
	}
	base := LiveGatewayConfig{
		AuditPath: filepath.Join(auditDir, "audit.jsonl"), MaxRequests: 1, MaxConcurrent: 1,
		MaxResponseBytes: 1024, MaxTotalResponseBytes: 1024, RequestTimeout: time.Second,
		Services: map[string]LiveGatewayServiceConfig{"jira": {BaseURL: "https://example.invalid", Token: "token", Routes: []LiveGatewayRoute{{Name: "api", PathPrefix: "/rest/api"}}}},
	}
	for name, mutate := range map[string]func(*LiveGatewayConfig){
		"http upstream": func(config *LiveGatewayConfig) {
			service := config.Services["jira"]
			service.BaseURL = "http://example.invalid"
			config.Services["jira"] = service
		},
		"foreign service": func(config *LiveGatewayConfig) {
			config.Services = map[string]LiveGatewayServiceConfig{"other": config.Services["jira"]}
		},
		"invalid route": func(config *LiveGatewayConfig) {
			service := config.Services["jira"]
			service.Routes[0].PathPrefix = "/rest/../admin"
			config.Services["jira"] = service
		},
		"response budget": func(config *LiveGatewayConfig) { config.MaxTotalResponseBytes = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := base
			candidate.Services = cloneGatewayServices(base.Services)
			mutate(&candidate)
			if _, err := StartLiveGateway(candidate); err == nil {
				t.Fatal("unsafe gateway config passed")
			}
		})
	}
	if err := os.WriteFile(base.AuditPath, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := StartLiveGateway(base); err == nil {
		t.Fatal("pre-existing audit path passed")
	}
}

func startTestLiveGateway(t *testing.T, upstream string, maxRequests int, maxResponseBytes, maxTotalBytes int64) (*LiveGateway, string) {
	t.Helper()
	auditDir := t.TempDir()
	if err := os.Chmod(auditDir, 0o700); err != nil {
		t.Fatal(err)
	}
	auditPath := filepath.Join(auditDir, "audit.jsonl")
	gateway, err := StartLiveGateway(LiveGatewayConfig{
		AuditPath: auditPath, MaxRequests: maxRequests, MaxConcurrent: 1,
		MaxResponseBytes: maxResponseBytes, MaxTotalResponseBytes: maxTotalBytes,
		RequestTimeout: 5 * time.Second,
		Services: map[string]LiveGatewayServiceConfig{
			"jira": {BaseURL: upstream, Token: "upstream-secret", Routes: []LiveGatewayRoute{{Name: "jira_api", PathPrefix: "/rest/api"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return gateway, auditPath
}

func decodeLiveGatewayAudit(t *testing.T, data []byte) []LiveGatewayAuditRecord {
	t.Helper()
	var records []LiveGatewayAuditRecord
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var record LiveGatewayAuditRecord
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&record); err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	return records
}

func cloneGatewayServices(source map[string]LiveGatewayServiceConfig) map[string]LiveGatewayServiceConfig {
	out := make(map[string]LiveGatewayServiceConfig, len(source))
	for name, service := range source {
		service.Routes = append([]LiveGatewayRoute(nil), service.Routes...)
		out[name] = service
	}
	return out
}
