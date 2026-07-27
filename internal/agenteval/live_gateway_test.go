package agenteval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSingleStackGatewayDialContextSelectsOnlyResolvedFamily(t *testing.T) {
	lookupErr := errors.New("lookup failed")
	tests := []struct {
		name      string
		network   string
		address   string
		addresses []net.IPAddr
		lookupErr error
		want      string
		lookups   int
	}{
		{name: "IPv4 literal", network: "tcp", address: "192.0.2.1:443", want: "tcp4"},
		{name: "IPv6 literal", network: "tcp", address: "[2001:db8::1]:443", want: "tcp6"},
		{name: "zoned IPv6 literal", network: "tcp", address: "[fe80::1%eth0]:443", want: "tcp6"},
		{name: "IPv4 only DNS", network: "tcp", address: "example.test:443", addresses: []net.IPAddr{{IP: net.ParseIP("192.0.2.1")}}, want: "tcp4", lookups: 1},
		{name: "IPv6 only DNS", network: "tcp", address: "example.test:443", addresses: []net.IPAddr{{IP: net.ParseIP("2001:db8::1")}}, want: "tcp6", lookups: 1},
		{name: "mixed DNS", network: "tcp", address: "example.test:443", addresses: []net.IPAddr{{IP: net.ParseIP("192.0.2.1")}, {IP: net.ParseIP("2001:db8::1")}}, want: "tcp", lookups: 1},
		{name: "empty DNS", network: "tcp", address: "example.test:443", want: "tcp", lookups: 1},
		{name: "resolution failure", network: "tcp", address: "example.test:443", lookupErr: lookupErr, want: "tcp", lookups: 1},
		{name: "malformed address", network: "tcp", address: "example.test", want: "tcp"},
		{name: "explicit IPv4 family", network: "tcp4", address: "example.test:443", want: "tcp4"},
		{name: "explicit IPv6 family", network: "tcp6", address: "example.test:443", want: "tcp6"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolver := &stubGatewayResolver{addresses: test.addresses, err: test.lookupErr}
			var gotNetwork, gotAddress string
			dialErr := errors.New("dial stopped")
			dial := singleStackGatewayDialContext(func(_ context.Context, network, address string) (net.Conn, error) {
				gotNetwork, gotAddress = network, address
				return nil, dialErr
			}, resolver)
			if _, err := dial(context.Background(), test.network, test.address); !errors.Is(err, dialErr) {
				t.Fatalf("dial error = %v, want %v", err, dialErr)
			}
			if gotNetwork != test.want || gotAddress != test.address || resolver.lookups != test.lookups {
				t.Fatalf("network=%q address=%q lookups=%d, want network=%q address=%q lookups=%d", gotNetwork, gotAddress, resolver.lookups, test.want, test.address, test.lookups)
			}
		})
	}
}

type stubGatewayResolver struct {
	addresses []net.IPAddr
	err       error
	lookups   int
}

func (r *stubGatewayResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	r.lookups++
	return r.addresses, r.err
}

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

func TestLiveGatewayRewritesOnlySameOriginJSONResourceURLs(t *testing.T) {
	var upstreamCalls atomic.Int64
	var upstreamURL string
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		upstreamCalls.Add(1)
		if request.Header.Get("Authorization") != "Bearer upstream-secret" {
			http.Error(response, "missing credential", http.StatusUnauthorized)
			return
		}
		switch request.URL.RequestURI() {
		case "/jira/rest/api/2/issue/PROJ-1":
			response.Header().Set("Content-Type", "application/json; charset=utf-8")
			_ = json.NewEncoder(response).Encode(map[string]any{
				"content":     upstreamURL + "/secure/attachment/42/file.txt?download=1",
				"within_base": upstreamURL + "/jira/secure/attachment/42/file.txt",
				"foreign":     "https://other.invalid/secure/attachment/42/file.txt",
				"relative":    "/jira/secure/attachment/42/file.txt",
				"description": "see " + upstreamURL + "/secure/attachment/42/file.txt",
				"nested":      []any{upstreamURL + "/jira/secure/attachment/42/file.txt#preview"},
			})
		case "/secure/attachment/42/file.txt?download=1":
			response.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(response, upstreamURL+" must remain raw")
		case "/jira/secure/attachment/42/file.txt":
			_, _ = io.WriteString(response, "attachment")
		default:
			http.NotFound(response, request)
		}
	}))
	defer upstream.Close()
	upstreamURL = upstream.URL

	auditDir := t.TempDir()
	if err := os.Chmod(auditDir, 0o700); err != nil {
		t.Fatal(err)
	}
	auditPath := filepath.Join(auditDir, "audit.jsonl")
	gateway, err := StartLiveGateway(LiveGatewayConfig{
		AuditPath: auditPath, MaxRequests: 3, MaxConcurrent: 1,
		MaxResponseBytes: 4096, MaxTotalResponseBytes: 8192, RequestTimeout: 5 * time.Second,
		Services: map[string]LiveGatewayServiceConfig{
			"jira": {
				BaseURL: upstream.URL + "/jira", Token: "upstream-secret",
				Routes: []LiveGatewayRoute{
					{Name: "metadata", PathPrefix: "/rest/api/2/issue/PROJ-1", Exact: true},
					{Name: "attachment", PathPrefix: "/secure/attachment/42/file.txt", Exact: true, MaxRequests: 2},
				},
			},
			"confluence": {
				BaseURL: upstream.URL + "/jira", Token: "other-upstream-secret",
				Routes: []LiveGatewayRoute{
					{Name: "attachment", PathPrefix: "/secure/attachment/42/file.txt", Exact: true, MaxRequests: 1},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close(context.Background())
	endpoint := gateway.Endpoints()["jira"]
	otherEndpoint := gateway.Endpoints()["confluence"]

	metadataRequest, err := http.NewRequest(http.MethodGet, endpoint.BaseURL+"/rest/api/2/issue/PROJ-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	metadataRequest.Header.Set("Authorization", "Bearer "+endpoint.Token)
	metadataResponse, err := http.DefaultClient.Do(metadataRequest)
	if err != nil {
		t.Fatal(err)
	}
	if metadataResponse.StatusCode != http.StatusOK {
		t.Fatalf("metadata status=%d", metadataResponse.StatusCode)
	}
	metadataBody, err := io.ReadAll(metadataResponse.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := metadataResponse.Body.Close(); err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(metadataBody, &metadata); err != nil {
		t.Fatal(err)
	}
	contentURL, err := url.Parse(metadata["content"].(string))
	if err != nil {
		t.Fatal(err)
	}
	wantWithinBase := endpoint.BaseURL + "/secure/attachment/42/file.txt"
	wantNested := endpoint.BaseURL + "/secure/attachment/42/file.txt#preview"
	if contentURL.Scheme != "http" || contentURL.Host != strings.TrimPrefix(endpoint.BaseURL, "http://") ||
		!strings.HasPrefix(contentURL.Path, gatewayOriginRootPrefix+"/") || contentURL.Query().Get("download") != "1" ||
		metadata["within_base"] != wantWithinBase || metadata["foreign"] != "https://other.invalid/secure/attachment/42/file.txt" ||
		metadata["relative"] != "/jira/secure/attachment/42/file.txt" ||
		metadata["description"] != "see "+upstream.URL+"/secure/attachment/42/file.txt" ||
		metadata["nested"].([]any)[0] != wantNested {
		t.Fatalf("unexpected translated metadata: %#v", metadata)
	}

	attachmentRequest, err := http.NewRequest(http.MethodGet, metadata["content"].(string), nil)
	if err != nil {
		t.Fatal(err)
	}
	attachmentRequest.Header.Set("Authorization", "Bearer "+endpoint.Token)
	attachmentResponse, err := http.DefaultClient.Do(attachmentRequest)
	if err != nil {
		t.Fatal(err)
	}
	if attachmentResponse.StatusCode != http.StatusOK {
		t.Fatalf("attachment status=%d", attachmentResponse.StatusCode)
	}
	attachmentBody, err := io.ReadAll(attachmentResponse.Body)
	if closeErr := attachmentResponse.Body.Close(); err != nil || closeErr != nil {
		t.Fatalf("read=%v close=%v", err, closeErr)
	}
	if string(attachmentBody) != upstream.URL+" must remain raw" || upstreamCalls.Load() != 2 {
		t.Fatalf("body=%q calls=%d", attachmentBody, upstreamCalls.Load())
	}

	for name, mutate := range map[string]func(*url.URL){
		"query tamper": func(candidate *url.URL) { candidate.RawQuery = "download=2" },
		"path tamper":  func(candidate *url.URL) { candidate.Path = strings.Replace(candidate.Path, "file.txt", "other.txt", 1) },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := *contentURL
			mutate(&candidate)
			request, requestErr := http.NewRequest(http.MethodGet, candidate.String(), nil)
			if requestErr != nil {
				t.Fatal(requestErr)
			}
			request.Header.Set("Authorization", "Bearer "+endpoint.Token)
			response, requestErr := http.DefaultClient.Do(request)
			if requestErr != nil {
				t.Fatal(requestErr)
			}
			_ = response.Body.Close()
			if response.StatusCode != http.StatusForbidden || upstreamCalls.Load() != 2 {
				t.Fatalf("status=%d upstream calls=%d", response.StatusCode, upstreamCalls.Load())
			}
		})
	}
	otherBase, err := url.Parse(otherEndpoint.BaseURL)
	if err != nil {
		t.Fatal(err)
	}
	serviceReplay := *contentURL
	serviceReplay.Scheme = otherBase.Scheme
	serviceReplay.Host = otherBase.Host
	replayRequest, err := http.NewRequest(http.MethodGet, serviceReplay.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	replayRequest.Header.Set("Authorization", "Bearer "+otherEndpoint.Token)
	replayResponse, err := http.DefaultClient.Do(replayRequest)
	if err != nil {
		t.Fatal(err)
	}
	_ = replayResponse.Body.Close()
	if replayResponse.StatusCode != http.StatusForbidden || upstreamCalls.Load() != 2 {
		t.Fatalf("cross-service replay status=%d upstream calls=%d", replayResponse.StatusCode, upstreamCalls.Load())
	}
	postRequest, err := http.NewRequest(http.MethodPost, contentURL.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	postRequest.Header.Set("Authorization", "Bearer "+endpoint.Token)
	postResponse, err := http.DefaultClient.Do(postRequest)
	if err != nil {
		t.Fatal(err)
	}
	_ = postResponse.Body.Close()
	if postResponse.StatusCode != http.StatusForbidden || upstreamCalls.Load() != 2 {
		t.Fatalf("signed origin-root write status=%d upstream calls=%d", postResponse.StatusCode, upstreamCalls.Load())
	}
	if err := gateway.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	audit, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	records := decodeLiveGatewayAudit(t, audit)
	var completed []LiveGatewayAuditRecord
	var denied int
	for _, record := range records {
		if record.Phase == "complete" && record.Decision == "allow" {
			completed = append(completed, record)
		}
		if record.Decision == "deny" {
			denied++
		}
	}
	if len(completed) != 2 || denied != 4 || completed[0].ResponseBytes != int64(len(metadataBody)) || completed[1].ResponseBytes != int64(len(attachmentBody)) {
		t.Fatalf("response byte audit mismatch: records=%+v metadata=%d attachment=%d", records, len(metadataBody), len(attachmentBody))
	}
}

func TestRewriteGatewayJSONURLsPreservesEscapedPathSemantics(t *testing.T) {
	upstream, err := url.Parse("https://upstream.example/base")
	if err != nil {
		t.Fatal(err)
	}
	downstream, err := url.Parse("http://127.0.0.1:12345")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "top-level", body: `"https://upstream.example/base/resource"`, want: `"http://127.0.0.1:12345/resource"`},
		{name: "encoded slash", body: `{"url":"https://upstream.example/base/secure%2Fattachment"}`, want: `{"url":"https://upstream.example/base/secure%2Fattachment"}`},
		{name: "encoded dot", body: `{"url":"https://upstream.example/base/%2e%2e/admin"}`, want: `{"url":"https://upstream.example/base/%2e%2e/admin"}`},
		{name: "encoded percent", body: `{"url":"https://upstream.example/base/100%25"}`, want: `{"url":"http://127.0.0.1:12345/100%25"}`},
		{name: "encoded unicode", body: `{"url":"https://upstream.example/base/%E2%9C%93"}`, want: `{"url":"http://127.0.0.1:12345/%E2%9C%93"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := rewriteGatewayJSONURLs([]byte(test.body), "application/problem+json", upstream, downstream, nil)
			if string(got) != test.want {
				t.Fatalf("got %s, want %s", got, test.want)
			}
		})
	}
}

func TestLiveGatewayBudgetsRewrittenResponseBytes(t *testing.T) {
	upstream, err := url.Parse("https://u.example")
	if err != nil {
		t.Fatal(err)
	}
	downstream, err := url.Parse("http://" + strings.Repeat("d", 80) + ".invalid")
	if err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"url":"https://u.example/resource"}`)
	rewritten := rewriteGatewayJSONURLs(original, "application/json", upstream, downstream, nil)
	if len(rewritten) <= len(original) {
		t.Fatalf("test requires expansion: original=%d rewritten=%d", len(original), len(rewritten))
	}

	t.Run("per response", func(t *testing.T) {
		state := &liveGatewayState{config: LiveGatewayConfig{
			MaxResponseBytes:      int64(len(rewritten) - 1),
			MaxTotalResponseBytes: int64(len(rewritten) * 2),
		}}
		service := &liveGatewayService{base: upstream, downstream: downstream, state: state}
		if _, ok := service.reviewGatewayResponseBody(original, "application/json"); ok || state.totalBytes != 0 {
			t.Fatalf("expanded response passed per-response budget: total=%d", state.totalBytes)
		}
	})

	t.Run("total", func(t *testing.T) {
		state := &liveGatewayState{config: LiveGatewayConfig{
			MaxResponseBytes:      int64(len(rewritten)),
			MaxTotalResponseBytes: int64(len(rewritten) - 1),
		}}
		service := &liveGatewayService{base: upstream, downstream: downstream, state: state}
		if _, ok := service.reviewGatewayResponseBody(original, "application/json"); ok || state.totalBytes != 0 {
			t.Fatalf("expanded response passed total budget: total=%d", state.totalBytes)
		}
		state.config.MaxTotalResponseBytes = int64(len(rewritten))
		got, ok := service.reviewGatewayResponseBody(original, "application/json")
		if !ok || !bytes.Equal(got, rewritten) || state.totalBytes != int64(len(rewritten)) {
			t.Fatalf("reviewed response mismatch: ok=%v total=%d got=%s", ok, state.totalBytes, got)
		}
	})
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

func TestLiveGatewayForwardsOnlyExactBudgetedReviewedWrite(t *testing.T) {
	var upstreamCalls atomic.Int64
	var upstreamBody, upstreamAuthorization, upstreamContentType, upstreamCookie, upstreamUnreviewedHeader string
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		upstreamCalls.Add(1)
		data, _ := io.ReadAll(request.Body)
		upstreamBody = string(data)
		upstreamAuthorization = request.Header.Get("Authorization")
		upstreamContentType = request.Header.Get("Content-Type")
		upstreamCookie = request.Header.Get("Cookie")
		upstreamUnreviewedHeader = request.Header.Get("X-Unreviewed")
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(response, `{"created":true}`)
	}))
	defer upstream.Close()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	auditPath := filepath.Join(directory, "audit.jsonl")
	gateway, err := StartLiveGateway(LiveGatewayConfig{
		AuditPath: auditPath,
		Services: map[string]LiveGatewayServiceConfig{"jira": {
			BaseURL: upstream.URL, Token: "upstream-secret",
			Routes: []LiveGatewayRoute{{Name: "create", PathPrefix: "/rest/api/2/issue", Exact: true, Methods: []string{"POST"}, MaxRequests: 1, MaxRequestBytes: 64}},
		}},
		MaxRequests: 1, MaxConcurrent: 1, MaxWrites: 1, MaxRequestBytes: 64, MaxTotalRequestBytes: 64,
		MaxResponseBytes: 1024, MaxTotalResponseBytes: 1024, RequestTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := gateway.Endpoints()["jira"]
	call := func(path string, body string) int {
		request, requestErr := http.NewRequest(http.MethodPost, endpoint.BaseURL+path, strings.NewReader(body))
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		request.Header.Set("Authorization", "Bearer "+endpoint.Token)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Cookie", "ambient=secret")
		request.Header.Set("X-Unreviewed", "secret")
		response, callErr := http.DefaultClient.Do(request)
		if callErr != nil {
			t.Fatal(callErr)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		return response.StatusCode
	}
	body := `{"summary":"reviewed fixture"}`
	if status := call("/rest/api/2/issue", body); status != http.StatusCreated {
		t.Fatalf("write status=%d", status)
	}
	methods, duplicates, observed, err := readLiveGatewayRecords(auditPath)
	if err != nil || !observed || duplicates != 0 || methods["POST"] != 1 {
		t.Fatalf("write methods=%v duplicates=%d observed=%t err=%v", methods, duplicates, observed, err)
	}
	if status := call("/rest/api/2/issue/other", body); status < 400 {
		t.Fatalf("non-exact route status=%d", status)
	}
	if upstreamCalls.Load() != 1 || upstreamBody != body || upstreamAuthorization != "Bearer upstream-secret" || upstreamContentType != "application/json" || upstreamCookie != "" || upstreamUnreviewedHeader != "" {
		t.Fatalf("calls=%d body=%q auth=%q content-type=%q cookie=%q unreviewed=%q", upstreamCalls.Load(), upstreamBody, upstreamAuthorization, upstreamContentType, upstreamCookie, upstreamUnreviewedHeader)
	}
	if err := gateway.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	audit, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"reviewed fixture", "/rest/api/2/issue", "upstream-secret", endpoint.Token} {
		if bytes.Contains(audit, []byte(forbidden)) {
			t.Fatalf("write audit leaked %q: %s", forbidden, audit)
		}
	}
	if _, _, _, err := readLiveGatewayRecords(auditPath); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("denied request was not retained as terminal safety evidence: %v", err)
	}
}

func TestLiveGatewayForwardsOnlyReviewedAtlassianTokens(t *testing.T) {
	tests := []struct {
		name    string
		service string
		tokens  []string
		want    string
	}{
		{name: "Confluence documented token", service: "confluence", tokens: []string{"nocheck"}, want: "nocheck"},
		{name: "Confluence shared token", service: "confluence", tokens: []string{"no-check"}, want: "no-check"},
		{name: "Jira shared token", service: "jira", tokens: []string{"no-check"}, want: "no-check"},
		{name: "Confluence token on Jira", service: "jira", tokens: []string{"nocheck"}},
		{name: "unsupported spelling", service: "confluence", tokens: []string{"NoCheck"}},
		{name: "duplicate values", service: "confluence", tokens: []string{"nocheck", "nocheck"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var got string
			upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				got = request.Header.Get("X-Atlassian-Token")
				response.WriteHeader(http.StatusNoContent)
			}))
			defer upstream.Close()
			directory := t.TempDir()
			if err := os.Chmod(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			gateway, err := StartLiveGateway(LiveGatewayConfig{
				AuditPath: filepath.Join(directory, "audit.jsonl"),
				Services: map[string]LiveGatewayServiceConfig{test.service: {
					BaseURL: upstream.URL, Token: "upstream-secret",
					Routes: []LiveGatewayRoute{{Name: "upload", PathPrefix: "/upload", Exact: true, Methods: []string{http.MethodPost}, MaxRequests: 1, MaxRequestBytes: 16}},
				}},
				MaxRequests: 1, MaxConcurrent: 1, MaxWrites: 1, MaxRequestBytes: 16, MaxTotalRequestBytes: 16,
				MaxResponseBytes: 1024, MaxTotalResponseBytes: 1024, RequestTimeout: 5 * time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := gateway.Close(context.Background()); err != nil {
					t.Error(err)
				}
			}()
			endpoint := gateway.Endpoints()[test.service]
			request, err := http.NewRequest(http.MethodPost, endpoint.BaseURL+"/upload", strings.NewReader(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Authorization", "Bearer "+endpoint.Token)
			request.Header.Set("Content-Type", "application/json")
			for _, token := range test.tokens {
				request.Header.Add("X-Atlassian-Token", token)
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, response.Body)
			if err := response.Body.Close(); err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != http.StatusNoContent || got != test.want {
				t.Fatalf("status=%d token=%q, want status=%d token=%q", response.StatusCode, got, http.StatusNoContent, test.want)
			}
		})
	}
}

func TestLiveGatewayPartitionsOneExactPathByMethod(t *testing.T) {
	var upstreamCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		response.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(response, `{"ok":true}`)
	}))
	defer upstream.Close()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	routes := []LiveGatewayRoute{
		{Name: "resource_read", PathPrefix: "/rest/api/resource/42", Exact: true, Methods: []string{"GET"}, MaxRequests: 1},
		{Name: "resource_write", PathPrefix: "/rest/api/resource/42", Exact: true, Methods: []string{"PUT"}, MaxRequests: 1, MaxRequestBytes: 64},
	}
	gateway, err := StartLiveGateway(LiveGatewayConfig{
		AuditPath:   filepath.Join(directory, "audit.jsonl"),
		Services:    map[string]LiveGatewayServiceConfig{"jira": {BaseURL: upstream.URL, Token: "token", Routes: routes}},
		MaxRequests: 2, MaxConcurrent: 1, MaxWrites: 1, MaxRequestBytes: 64, MaxTotalRequestBytes: 64,
		MaxResponseBytes: 1024, MaxTotalResponseBytes: 2048, RequestTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close(context.Background())
	endpoint := gateway.Endpoints()["jira"]
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		var body io.Reader
		if method == http.MethodPut {
			body = strings.NewReader(`{"updated":true}`)
		}
		request, requestErr := http.NewRequest(method, endpoint.BaseURL+"/rest/api/resource/42", body)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		request.Header.Set("Authorization", "Bearer "+endpoint.Token)
		if method == http.MethodPut {
			request.Header.Set("Content-Type", "application/json")
		}
		response, callErr := http.DefaultClient.Do(request)
		if callErr != nil {
			t.Fatal(callErr)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s status=%d", method, response.StatusCode)
		}
	}
	if upstreamCalls.Load() != 2 {
		t.Fatalf("upstream calls=%d, want 2", upstreamCalls.Load())
	}
	for _, invalid := range [][]LiveGatewayRoute{
		append(append([]LiveGatewayRoute(nil), routes...), LiveGatewayRoute{Name: "overlap", PathPrefix: "/rest/api/resource/42", Exact: true, Methods: []string{"GET"}, MaxRequests: 1}),
		{routes[0], LiveGatewayRoute{Name: "mixed_exactness", PathPrefix: "/rest/api/resource/42", Methods: []string{"PUT"}, MaxRequests: 1, MaxRequestBytes: 64}},
		{{Name: "prefix_read", PathPrefix: "/rest/api/resource", Methods: []string{"GET"}, MaxRequests: 1}, {Name: "prefix_write", PathPrefix: "/rest/api/resource", Methods: []string{"PUT"}, MaxRequests: 1, MaxRequestBytes: 64}},
	} {
		if err := validateLiveGatewayRoutes(invalid); err == nil {
			t.Fatalf("invalid shared-prefix routes validated: %+v", invalid)
		}
	}
}

func TestLiveGatewayRejectsReviewedWriteBodyAndBudgetBeforeUpstream(t *testing.T) {
	var upstreamCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		response.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	start := func(t *testing.T) (*LiveGateway, LiveGatewayEndpoint) {
		t.Helper()
		directory := t.TempDir()
		if err := os.Chmod(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		gateway, err := StartLiveGateway(LiveGatewayConfig{
			AuditPath: filepath.Join(directory, "audit.jsonl"),
			Services: map[string]LiveGatewayServiceConfig{"jira": {BaseURL: upstream.URL, Token: "token", Routes: []LiveGatewayRoute{
				{Name: "create", PathPrefix: "/rest/api/2/issue", Exact: true, Methods: []string{"POST"}, MaxRequests: 1, MaxRequestBytes: 4},
			}}},
			MaxRequests: 1, MaxConcurrent: 1, MaxWrites: 1, MaxRequestBytes: 4, MaxTotalRequestBytes: 4,
			MaxResponseBytes: 16, MaxTotalResponseBytes: 16, RequestTimeout: 5 * time.Second,
		})
		if err != nil {
			t.Fatal(err)
		}
		return gateway, gateway.Endpoints()["jira"]
	}
	t.Run("oversize", func(t *testing.T) {
		gateway, endpoint := start(t)
		defer gateway.Close(context.Background())
		request, _ := http.NewRequest(http.MethodPost, endpoint.BaseURL+"/rest/api/2/issue", strings.NewReader("12345"))
		request.Header.Set("Authorization", "Bearer "+endpoint.Token)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode < 400 {
			t.Fatalf("oversize status=%d", response.StatusCode)
		}
	})
	t.Run("method", func(t *testing.T) {
		gateway, endpoint := start(t)
		defer gateway.Close(context.Background())
		request, _ := http.NewRequest(http.MethodPut, endpoint.BaseURL+"/rest/api/2/issue", strings.NewReader("1234"))
		request.Header.Set("Authorization", "Bearer "+endpoint.Token)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode < 400 {
			t.Fatalf("method status=%d", response.StatusCode)
		}
	})
	t.Run("content type", func(t *testing.T) {
		gateway, endpoint := start(t)
		defer gateway.Close(context.Background())
		request, _ := http.NewRequest(http.MethodPost, endpoint.BaseURL+"/rest/api/2/issue", strings.NewReader("1234"))
		request.Header.Set("Authorization", "Bearer "+endpoint.Token)
		request.Header.Set("Content-Type", "text/plain")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode < 400 {
			t.Fatalf("content-type status=%d", response.StatusCode)
		}
	})
	if upstreamCalls.Load() != 0 {
		t.Fatalf("upstream observed %d rejected writes", upstreamCalls.Load())
	}
}

func TestReviewedGatewayContentTypeAllowsOnlyJSONAndMultipart(t *testing.T) {
	for _, header := range []string{"application/json", "application/json; charset=utf-8", "multipart/form-data; boundary=reviewed"} {
		if forwarded, ok := reviewedGatewayContentType(header, 1); !ok || forwarded != header {
			t.Fatalf("reviewed content type %q rejected", header)
		}
	}
	for _, header := range []string{"", "text/plain", "multipart/form-data", "application/x-www-form-urlencoded"} {
		if _, ok := reviewedGatewayContentType(header, 1); ok {
			t.Fatalf("unreviewed content type %q accepted", header)
		}
	}
	if forwarded, ok := reviewedGatewayContentType("", 0); !ok || forwarded != "" {
		t.Fatal("empty-body request did not accept an absent content type")
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
	if status := call(); status != http.StatusOK || upstreamCalls.Load() != 2 {
		t.Fatalf("post-concurrency status=%d calls=%d", status, upstreamCalls.Load())
	}
}

func TestLiveGatewayConcurrencyDenialConsumesNoWriteBudgets(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var upstreamCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		if upstreamCalls.Add(1) == 1 {
			close(started)
			<-release
		}
		_, _ = io.WriteString(response, `{}`)
	}))
	defer upstream.Close()
	auditDir := t.TempDir()
	if err := os.Chmod(auditDir, 0o700); err != nil {
		t.Fatal(err)
	}
	const issuePath = "/rest/api/2/issue/PROJ-1"
	gateway, err := StartLiveGateway(LiveGatewayConfig{
		AuditPath:   filepath.Join(auditDir, "audit.jsonl"),
		MaxRequests: 2, MaxConcurrent: 1, MaxWrites: 2,
		MaxRequestBytes: 2, MaxTotalRequestBytes: 4,
		MaxResponseBytes: 1024, MaxTotalResponseBytes: 2048, RequestTimeout: 5 * time.Second,
		Services: map[string]LiveGatewayServiceConfig{
			"jira": {
				BaseURL: upstream.URL, Token: "upstream-secret",
				Routes: []LiveGatewayRoute{{
					Name: "issue_write", PathPrefix: issuePath, Exact: true,
					Methods: []string{http.MethodPut}, MaxRequests: 2, MaxRequestBytes: 2,
				}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close(context.Background())
	endpoint := gateway.Endpoints()["jira"]
	call := func() int {
		request, _ := http.NewRequest(http.MethodPut, endpoint.BaseURL+issuePath, strings.NewReader(`{}`))
		request.Header.Set("Authorization", "Bearer "+endpoint.Token)
		request.Header.Set("Content-Type", "application/json")
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
	if status := call(); status != http.StatusOK || upstreamCalls.Load() != 2 {
		t.Fatalf("post-concurrency status=%d calls=%d", status, upstreamCalls.Load())
	}
}

func TestLiveGatewayCloseWaitsForHandlersBeforeKeyTeardown(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	releaseRoundTrip := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() { close(releaseRoundTrip) })
	}
	roundTripDone := make(chan struct{})
	upstream := httptest.NewServer(http.NotFoundHandler())
	defer upstream.Close()
	gateway, _ := startTestLiveGateway(t, upstream.URL, 1, 1024, 1024)
	t.Cleanup(func() {
		release()
		_ = gateway.Close(context.Background())
	})
	originalKey := bytes.Clone(gateway.state.hmacKey)
	gateway.state.upstreamHTTP.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		close(started)
		<-request.Context().Done()
		close(canceled)
		<-releaseRoundTrip
		close(roundTripDone)
		return nil, request.Context().Err()
	})
	endpoint := gateway.Endpoints()["jira"]
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		request, err := http.NewRequest(http.MethodGet, endpoint.BaseURL+"/rest/api/2/field", nil)
		if err != nil {
			return
		}
		request.Header.Set("Authorization", "Bearer "+endpoint.Token)
		response, err := http.DefaultClient.Do(request)
		if err == nil {
			_ = response.Body.Close()
		}
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream request did not start")
	}
	closeContext, cancel := context.WithCancel(context.Background())
	cancel()
	closeDone := make(chan error, 1)
	go func() {
		closeDone <- gateway.Close(closeContext)
	}()
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("gateway upstream operation was not canceled")
	}
	select {
	case err := <-closeDone:
		t.Fatalf("gateway closed before its active handler: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if !bytes.Equal(gateway.state.hmacKey, originalKey) {
		t.Fatal("gateway HMAC key changed before handler shutdown")
	}
	release()
	select {
	case err := <-closeDone:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("close error=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("gateway close did not finish after its active handler")
	}
	select {
	case <-roundTripDone:
	default:
		t.Fatal("gateway key teardown raced an active upstream operation")
	}
	for _, value := range gateway.state.hmacKey {
		if value != 0 {
			t.Fatal("gateway HMAC key was not cleared after handler shutdown")
		}
	}
	select {
	case <-clientDone:
	case <-time.After(2 * time.Second):
		t.Fatal("gateway client did not stop")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
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
	if err := gateway.Close(context.Background()); !errors.Is(err, errLiveGatewayAuditUnavailable) {
		t.Fatalf("close audit error=%v, want latched failure", err)
	}
}

func TestLiveGatewayAuditCapFailsCloseAndEvidenceIngestion(t *testing.T) {
	var upstreamCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		_, _ = io.WriteString(response, `{}`)
	}))
	defer upstream.Close()
	gateway, _ := startTestLiveGateway(t, upstream.URL, 1, 1024, 1024)
	endpoint := gateway.Endpoints()["jira"]

	gateway.state.mu.Lock()
	gateway.state.auditBytes = maxLiveGatewayAuditBytes
	gateway.state.mu.Unlock()

	request, _ := http.NewRequest(http.MethodGet, endpoint.BaseURL+"/rest/api/2/field", nil)
	request.Header.Set("Authorization", "Bearer "+endpoint.Token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusBadGateway || upstreamCalls.Load() != 0 {
		t.Fatalf("audit-cap status=%d calls=%d", response.StatusCode, upstreamCalls.Load())
	}
	if _, _, _, err := closeAndReadLiveGatewayRecords(gateway); !errors.Is(err, errLiveGatewayAuditUnavailable) {
		t.Fatalf("evidence ingestion error=%v, want latched audit failure", err)
	}
	if err := gateway.state.writeAudit(LiveGatewayAuditRecord{}); !errors.Is(err, errLiveGatewayAuditUnavailable) {
		t.Fatalf("subsequent audit error=%v, want latched failure", err)
	}
}

func TestLiveGatewayAuditWriteFailureKeepsCauseOutOfTextAndResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(response, `{}`)
	}))
	defer upstream.Close()
	gateway, auditPath := startTestLiveGateway(t, upstream.URL, 1, 1024, 1024)
	endpoint := gateway.Endpoints()["jira"]
	if err := gateway.state.audit.Close(); err != nil {
		t.Fatal(err)
	}

	err := gateway.state.writeAudit(LiveGatewayAuditRecord{Phase: "preflight", Service: "jira", Method: http.MethodGet, Decision: "forward"})
	if !errors.Is(err, errLiveGatewayAuditUnavailable) {
		t.Fatalf("audit write error=%v, want the audit sentinel", err)
	}
	if !errors.Is(err, os.ErrClosed) {
		t.Fatalf("audit write error=%v lost its filesystem cause", err)
	}
	var pathErr *fs.PathError
	if !errors.As(err, &pathErr) || pathErr.Op != "write" || pathErr.Path != auditPath {
		t.Fatalf("audit write error=%v does not expose the concrete path error", err)
	}
	if got, want := err.Error(), errLiveGatewayAuditUnavailable.Error()+": write"; got != want {
		t.Fatalf("audit write text=%q, want %q", got, want)
	}
	for _, forbidden := range []string{auditPath, filepath.Dir(auditPath)} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("audit write text leaked %q: %q", forbidden, err.Error())
		}
	}

	// The latch is now set, so the next write reports the latch rather than a
	// fresh filesystem cause, and the request still gets the static body.
	latched := gateway.state.writeAudit(LiveGatewayAuditRecord{})
	if !errors.Is(latched, errLiveGatewayAuditUnavailable) || errors.Is(latched, os.ErrClosed) {
		t.Fatalf("latched audit error=%v, want the sentinel without a filesystem cause", latched)
	}
	if got, want := latched.Error(), errLiveGatewayAuditUnavailable.Error()+": latched"; got != want {
		t.Fatalf("latched audit text=%q, want %q", got, want)
	}

	status, body := liveGatewayProbe(t, endpoint.BaseURL+"/rest/api/2/field", "Bearer "+endpoint.Token)
	if status != http.StatusBadGateway || body != gatewayAuditUnavailableMessage+"\n" {
		t.Fatalf("unaudited request status=%d body=%q", status, body)
	}
	if _, _, _, err := closeAndReadLiveGatewayRecords(gateway); !errors.Is(err, errLiveGatewayAuditUnavailable) {
		t.Fatalf("evidence ingestion error=%v, want latched audit failure", err)
	}
}

func TestLiveGatewayPreflightDenialAuditFailureIsFatal(t *testing.T) {
	var upstreamCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		_, _ = io.WriteString(response, `{}`)
	}))
	defer upstream.Close()
	gateway, auditPath := startTestLiveGateway(t, upstream.URL, 2, 1024, 1024)
	endpoint := gateway.Endpoints()["jira"]
	target := endpoint.BaseURL + "/rest/api/2/field"

	status, body := liveGatewayProbe(t, target, "Bearer not-the-capability")
	if status != http.StatusUnauthorized || !strings.Contains(body, gatewayDeniedMessage) {
		t.Fatalf("audited denial status=%d body=%q", status, body)
	}

	if err := gateway.state.audit.Close(); err != nil {
		t.Fatal(err)
	}
	status, body = liveGatewayProbe(t, target, "Bearer not-the-capability")
	if status != http.StatusBadGateway || !strings.Contains(body, gatewayAuditUnavailableMessage) || strings.Contains(body, gatewayDeniedMessage) {
		t.Fatalf("unaudited denial status=%d body=%q", status, body)
	}
	if upstreamCalls.Load() != 0 {
		t.Fatalf("upstream calls=%d, want none", upstreamCalls.Load())
	}
	if _, _, _, err := closeAndReadLiveGatewayRecords(gateway); !errors.Is(err, errLiveGatewayAuditUnavailable) {
		t.Fatalf("evidence ingestion error=%v, want latched audit failure", err)
	}
	data, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	records := decodeLiveGatewayAudit(t, data)
	if len(records) != 1 || records[0].Phase != "preflight" || records[0].Decision != "deny" || records[0].Reason != "ingress_auth" {
		t.Fatalf("audit records=%+v, want only the audited denial", records)
	}
}

func TestLiveGatewayCompletionDenialAuditFailureIsFatal(t *testing.T) {
	var gateway *LiveGateway
	var upstreamCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		// The preflight forward record is already durable when the upstream is
		// reached, so closing the audit here makes the completion record the
		// first failing write of the second request.
		if upstreamCalls.Add(1) == 2 {
			if err := gateway.state.audit.Close(); err != nil {
				t.Error(err)
			}
		}
		response.Header().Set("Location", "/rest/api/2/other")
		response.WriteHeader(http.StatusFound)
	}))
	defer upstream.Close()
	gateway, auditPath := startTestLiveGateway(t, upstream.URL, 2, 1024, 1024)
	endpoint := gateway.Endpoints()["jira"]
	target := endpoint.BaseURL + "/rest/api/2/field"

	status, body := liveGatewayProbe(t, target, "Bearer "+endpoint.Token)
	if status != http.StatusBadGateway || !strings.Contains(body, gatewayUpstreamFailedMessage) {
		t.Fatalf("audited upstream denial status=%d body=%q", status, body)
	}

	status, body = liveGatewayProbe(t, target, "Bearer "+endpoint.Token)
	if status != http.StatusBadGateway || !strings.Contains(body, gatewayAuditUnavailableMessage) || strings.Contains(body, gatewayUpstreamFailedMessage) {
		t.Fatalf("unaudited upstream denial status=%d body=%q", status, body)
	}
	if upstreamCalls.Load() != 2 {
		t.Fatalf("upstream calls=%d, want 2", upstreamCalls.Load())
	}
	if _, _, _, err := closeAndReadLiveGatewayRecords(gateway); !errors.Is(err, errLiveGatewayAuditUnavailable) {
		t.Fatalf("evidence ingestion error=%v, want latched audit failure", err)
	}
	data, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	records := decodeLiveGatewayAudit(t, data)
	if len(records) != 3 || records[1].Phase != "complete" || records[1].Decision != "deny" ||
		records[2].Phase != "preflight" || records[2].Decision != "forward" {
		t.Fatalf("audit records=%+v, want a forwarded request with no completion record", records)
	}
}

func TestLiveGatewayMissingAuditPathFailsClosed(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(response, `{}`)
	}))
	defer upstream.Close()
	gateway, auditPath := startTestLiveGateway(t, upstream.URL, 1, 1024, 1024)
	gateway.state.config.AuditPath = auditPath + ".missing"
	if _, _, _, err := closeAndReadLiveGatewayRecords(gateway); !errors.Is(err, errLiveGatewayAuditUnavailable) {
		t.Fatalf("missing-path evidence error=%v, want bound audit failure", err)
	}
}

func TestLiveGatewaySameFileTruncationFailsClosed(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(response, `{}`)
	}))
	defer upstream.Close()
	gateway, auditPath := startTestLiveGateway(t, upstream.URL, 1, 1024, 1024)
	endpoint := gateway.Endpoints()["jira"]
	request, _ := http.NewRequest(http.MethodGet, endpoint.BaseURL+"/rest/api/2/field", nil)
	request.Header.Set("Authorization", "Bearer "+endpoint.Token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("request status=%d", response.StatusCode)
	}
	if err := os.Truncate(auditPath, 0); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := closeAndReadLiveGatewayRecords(gateway); !errors.Is(err, errLiveGatewayAuditUnavailable) {
		t.Fatalf("truncated evidence error=%v, want content-bound audit failure", err)
	}
}

func TestLiveGatewaySameLengthTamperingFailsClosed(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(response, `{}`)
	}))
	defer upstream.Close()
	gateway, auditPath := startTestLiveGateway(t, upstream.URL, 1, 1024, 1024)
	endpoint := gateway.Endpoints()["jira"]
	request, _ := http.NewRequest(http.MethodGet, endpoint.BaseURL+"/rest/api/2/field", nil)
	request.Header.Set("Authorization", "Bearer "+endpoint.Token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("request status=%d", response.StatusCode)
	}
	data, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	marker := []byte(`"request_hmac":"`)
	offset := bytes.Index(data, marker)
	if offset < 0 {
		t.Fatalf("audit has no request identity: %s", data)
	}
	offset += len(marker)
	replacement := byte('0')
	if data[offset] == replacement {
		replacement = '1'
	}
	file, err := os.OpenFile(auditPath, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte{replacement}, int64(offset)); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := closeAndReadLiveGatewayRecords(gateway); !errors.Is(err, errLiveGatewayAuditUnavailable) {
		t.Fatalf("tampered evidence error=%v, want content-bound audit failure", err)
	}
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
		"reserved origin root route": func(config *LiveGatewayConfig) {
			service := config.Services["jira"]
			service.Routes[0].PathPrefix = gatewayOriginRootPrefix + "/resource"
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
	if _, err := StartLiveGateway(base); err == nil || strings.Contains(err.Error(), base.AuditPath) {
		t.Fatalf("pre-existing audit error=%v", err)
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

func liveGatewayProbe(t *testing.T, target, authorization string) (int, string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", authorization)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, string(body)
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

func TestLiveGatewayQueryOnlyRouteShapeIsExact(t *testing.T) {
	valid := LiveGatewayRoute{Name: "value_query", PathPrefix: "/rest/structure/2.0/value", Exact: true,
		Methods: []string{http.MethodPost}, QueryOnly: true, MaxRequests: 2, MaxRequestBytes: 4096}
	if err := validateLiveGatewayRoutes([]LiveGatewayRoute{valid}); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*LiveGatewayRoute){
		"prefix path":      func(route *LiveGatewayRoute) { route.Exact = false },
		"default methods":  func(route *LiveGatewayRoute) { route.Methods = nil },
		"extra method":     func(route *LiveGatewayRoute) { route.Methods = []string{http.MethodPost, http.MethodGet} },
		"mutating method":  func(route *LiveGatewayRoute) { route.Methods = []string{http.MethodPut} },
		"read method":      func(route *LiveGatewayRoute) { route.Methods = []string{http.MethodGet} },
		"zero count":       func(route *LiveGatewayRoute) { route.MaxRequests = 0 },
		"zero body budget": func(route *LiveGatewayRoute) { route.MaxRequestBytes = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			if err := validateLiveGatewayRoutes([]LiveGatewayRoute{candidate}); err == nil {
				t.Fatal("invalid query-only route accepted")
			}
		})
	}
}

func TestLiveGatewayQueryOnlyPOSTIsBoundedAndAudited(t *testing.T) {
	var upstreamCalls atomic.Int64
	var upstreamAtlassianToken, upstreamBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		upstreamCalls.Add(1)
		upstreamAtlassianToken = request.Header.Get("X-Atlassian-Token")
		body, _ := io.ReadAll(io.LimitReader(request.Body, 4096))
		upstreamBody = string(body)
		response.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(response, `{"rows":[]}`)
	}))
	defer upstream.Close()

	auditDir := t.TempDir()
	if err := os.Chmod(auditDir, 0o700); err != nil {
		t.Fatal(err)
	}
	auditPath := filepath.Join(auditDir, "audit.jsonl")
	const queryPath = "/rest/structure/2.0/value"
	const queryBody = `{"rows":[1],"values":["summary"]}`
	gateway, err := StartLiveGateway(LiveGatewayConfig{
		AuditPath: auditPath, MaxRequests: 8, MaxConcurrent: 1,
		MaxResponseBytes: 1024, MaxTotalResponseBytes: 4096,
		MaxRequestBytes: 256, MaxTotalRequestBytes: 512, MaxWrites: 1,
		RequestTimeout: 5 * time.Second,
		Services: map[string]LiveGatewayServiceConfig{"jira": {BaseURL: upstream.URL + "/jira", Token: "upstream-secret", Routes: []LiveGatewayRoute{
			{Name: "reads", PathPrefix: "/rest/api/2", MaxRequests: 2},
			// Two reviewed requests are allowed by the route, so a second denial can
			// only come from the global write budget.
			{Name: "value_query", PathPrefix: queryPath, Exact: true, Methods: []string{http.MethodPost},
				QueryOnly: true, MaxRequests: 2, MaxRequestBytes: 256},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := gateway.Endpoints()["jira"]
	send := func(method, path, contentType, body string, mutate func(*http.Request)) int {
		t.Helper()
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		request, err := http.NewRequest(method, endpoint.BaseURL+path, reader)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+endpoint.Token)
		if contentType != "" {
			request.Header.Set("Content-Type", contentType)
		}
		request.Header.Set("X-Atlassian-Token", "no-check")
		if mutate != nil {
			mutate(request)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		_ = response.Body.Close()
		return response.StatusCode
	}

	for name, probe := range map[string]func() int{
		"wrong method": func() int {
			return send(http.MethodPut, queryPath, "application/json", queryBody, nil)
		},
		"method override": func() int {
			return send(http.MethodPost, queryPath, "application/json", queryBody, func(request *http.Request) {
				request.Header.Set("X-HTTP-Method-Override", "DELETE")
			})
		},
		"oversize body": func() int {
			return send(http.MethodPost, queryPath, "application/json", `{"rows":["`+strings.Repeat("a", 400)+`"]}`, nil)
		},
		"multipart body": func() int {
			return send(http.MethodPost, queryPath, `multipart/form-data; boundary=abc`, queryBody, nil)
		},
		"missing content type": func() int {
			return send(http.MethodPost, queryPath, "", queryBody, nil)
		},
		"empty json body": func() int {
			return send(http.MethodPost, queryPath, "application/json", "", nil)
		},
		"invalid json body": func() int {
			return send(http.MethodPost, queryPath, "application/json", "not-json", nil)
		},
	} {
		t.Run(name, func(t *testing.T) {
			if status := probe(); status < 400 || status > 499 {
				t.Fatalf("status=%d", status)
			}
			if upstreamCalls.Load() != 0 {
				t.Fatalf("denied query-only request reached upstream: calls=%d", upstreamCalls.Load())
			}
		})
	}

	if status := send(http.MethodPost, queryPath, "application/json", queryBody, nil); status != http.StatusOK {
		t.Fatalf("reviewed query-only POST status=%d", status)
	}
	if upstreamCalls.Load() != 1 || upstreamBody != queryBody || upstreamAtlassianToken != "" {
		t.Fatalf("calls=%d body=%q atlassian-token=%q", upstreamCalls.Load(), upstreamBody, upstreamAtlassianToken)
	}
	if status := send(http.MethodPost, queryPath, "application/json", queryBody, nil); status != http.StatusTooManyRequests {
		t.Fatalf("second query-only POST status=%d", status)
	}
	if upstreamCalls.Load() != 1 {
		t.Fatalf("write budget did not bound query-only POST: calls=%d", upstreamCalls.Load())
	}

	if err := gateway.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{queryPath, "rows", "summary", "upstream-secret", endpoint.Token, "no-check"} {
		if bytes.Contains(data, []byte(forbidden)) {
			t.Fatalf("audit retained %q: %s", forbidden, data)
		}
	}
	records := decodeLiveGatewayAudit(t, data)
	writes, budgetDenials := 0, 0
	for _, record := range records {
		if len(record.RequestHMAC) != 64 {
			t.Fatalf("record without a bounded request identity: %+v", record)
		}
		if record.Method == http.MethodPost && record.Decision == "allow" {
			writes++
			if record.Route != "value_query" || record.RequestBytes != int64(len(queryBody)) {
				t.Fatalf("record=%+v", record)
			}
		}
		if record.Decision == "deny" && record.Reason == "write_budget" {
			budgetDenials++
		}
	}
	if writes != 1 || budgetDenials != 1 {
		t.Fatalf("writes=%d budget denials=%d records=%+v", writes, budgetDenials, records)
	}
}

func TestLiveGatewayQueryOnlyPOSTFailsClosedWhenAuditIsUnavailable(t *testing.T) {
	var upstreamCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		response.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(response, `{"rows":[]}`)
	}))
	defer upstream.Close()
	auditDir := t.TempDir()
	if err := os.Chmod(auditDir, 0o700); err != nil {
		t.Fatal(err)
	}
	gateway, err := StartLiveGateway(LiveGatewayConfig{
		AuditPath: filepath.Join(auditDir, "audit.jsonl"), MaxRequests: 4, MaxConcurrent: 1,
		MaxResponseBytes: 1024, MaxTotalResponseBytes: 4096,
		MaxRequestBytes: 256, MaxTotalRequestBytes: 512, MaxWrites: 1,
		RequestTimeout: 5 * time.Second,
		Services: map[string]LiveGatewayServiceConfig{"jira": {BaseURL: upstream.URL + "/jira", Token: "upstream-secret", Routes: []LiveGatewayRoute{
			{Name: "value_query", PathPrefix: "/rest/structure/2.0/value", Exact: true, Methods: []string{http.MethodPost},
				QueryOnly: true, MaxRequests: 1, MaxRequestBytes: 256},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gateway.Close(context.Background()) }()
	gateway.state.markAuditUnhealthy()
	endpoint := gateway.Endpoints()["jira"]
	request, err := http.NewRequest(http.MethodPost, endpoint.BaseURL+"/rest/structure/2.0/value", strings.NewReader(`{"rows":[1]}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+endpoint.Token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	_ = response.Body.Close()
	if response.StatusCode != http.StatusBadGateway || !strings.Contains(string(body), gatewayAuditUnavailableMessage) {
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
	if upstreamCalls.Load() != 0 {
		t.Fatalf("off-the-record query-only POST reached upstream: calls=%d", upstreamCalls.Load())
	}
}
