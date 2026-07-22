package agenteval

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const maxLiveGatewayAuditBytes = 4 << 20

// LiveGatewayConfig describes an evaluation-only credential boundary. The
// upstream tokens must remain in the parent runner; Endpoints returns only
// disposable ingress capabilities and loopback URLs for child atl processes.
type LiveGatewayConfig struct {
	AuditPath             string
	Services              map[string]LiveGatewayServiceConfig
	MaxRequests           int
	MaxConcurrent         int
	MaxResponseBytes      int64
	MaxTotalResponseBytes int64
	MaxRequestBytes       int64
	MaxTotalRequestBytes  int64
	MaxWrites             int
	RequestTimeout        time.Duration
}

type LiveGatewayServiceConfig struct {
	BaseURL string
	Token   string
	Routes  []LiveGatewayRoute
}

type LiveGatewayRoute struct {
	Name            string   `json:"name"`
	PathPrefix      string   `json:"path_prefix"`
	Exact           bool     `json:"exact,omitempty"`
	Methods         []string `json:"methods,omitempty"`
	MaxRequests     int      `json:"max_requests,omitempty"`
	MaxRequestBytes int64    `json:"max_request_bytes,omitempty"`
}

type LiveGatewayEndpoint struct {
	BaseURL string
	Token   string
}

type LiveGatewayAuditRecord struct {
	Sequence      int64  `json:"sequence"`
	Phase         string `json:"phase"`
	Service       string `json:"service"`
	Route         string `json:"route,omitempty"`
	Method        string `json:"method"`
	RequestHMAC   string `json:"request_hmac"`
	Decision      string `json:"decision"`
	Reason        string `json:"reason,omitempty"`
	StatusClass   string `json:"status_class,omitempty"`
	ResponseBytes int64  `json:"response_bytes,omitempty"`
	RequestBytes  int64  `json:"request_bytes,omitempty"`
	DurationMS    int64  `json:"duration_ms,omitempty"`
}

type LiveGateway struct {
	state     *liveGatewayState
	servers   []*http.Server
	listeners []net.Listener
	endpoints map[string]LiveGatewayEndpoint
	closeOnce sync.Once
	closeErr  error
}

type liveGatewayState struct {
	config            LiveGatewayConfig
	audit             *os.File
	hmacKey           []byte
	mu                sync.Mutex
	sequence          int64
	requests          int
	totalBytes        int64
	totalRequestBytes int64
	writes            int
	routeRequests     map[string]int
	auditBytes        int64
	concurrency       chan struct{}
	upstreamHTTP      *http.Client
}

type liveGatewayService struct {
	name       string
	base       *url.URL
	downstream *url.URL
	token      string
	capability string
	routes     []LiveGatewayRoute
	state      *liveGatewayState
}

func StartLiveGateway(config LiveGatewayConfig) (*LiveGateway, error) {
	if err := validateLiveGatewayConfig(config); err != nil {
		return nil, err
	}
	if err := requireOwnerOnly("live gateway audit directory", filepath.Dir(config.AuditPath), true); err != nil {
		return nil, err
	}
	audit, err := os.OpenFile(config.AuditPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create live gateway audit: %w", err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		_ = audit.Close()
		return nil, fmt.Errorf("create live gateway audit key: %w", err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = singleStackGatewayDialContext(transport.DialContext, net.DefaultResolver)
	state := &liveGatewayState{
		config: config, audit: audit, hmacKey: key,
		concurrency: make(chan struct{}, config.MaxConcurrent), routeRequests: map[string]int{},
		upstreamHTTP: &http.Client{
			Transport: transport,
			Timeout:   config.RequestTimeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
	gateway := &LiveGateway{state: state, endpoints: map[string]LiveGatewayEndpoint{}}
	names := make([]string, 0, len(config.Services))
	for name := range config.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		serviceConfig := config.Services[name]
		base, _ := url.Parse(serviceConfig.BaseURL)
		capability, err := randomGatewayCapability()
		if err != nil {
			_ = gateway.Close(context.Background())
			return nil, err
		}
		service := &liveGatewayService{
			name: name, base: base, token: serviceConfig.Token,
			capability: capability, routes: serviceConfig.Routes, state: state,
		}
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			_ = gateway.Close(context.Background())
			return nil, fmt.Errorf("start live gateway listener: %w", err)
		}
		service.downstream = &url.URL{Scheme: "http", Host: listener.Addr().String()}
		server := &http.Server{
			Handler:           service,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       config.RequestTimeout,
			WriteTimeout:      config.RequestTimeout,
			IdleTimeout:       5 * time.Second,
		}
		gateway.listeners = append(gateway.listeners, listener)
		gateway.servers = append(gateway.servers, server)
		gateway.endpoints[name] = LiveGatewayEndpoint{
			BaseURL: "http://" + listener.Addr().String(), Token: capability,
		}
		go func() { _ = server.Serve(listener) }()
	}
	return gateway, nil
}

type gatewayIPResolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

func singleStackGatewayDialContext(
	dial func(context.Context, string, string) (net.Conn, error),
	resolver gatewayIPResolver,
) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		return dial(ctx, gatewayDialNetwork(ctx, resolver, network, address), address)
	}
}

func gatewayDialNetwork(ctx context.Context, resolver gatewayIPResolver, network, address string) string {
	if network != "tcp" {
		return network
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return network
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if ip.Is4() {
			return "tcp4"
		}
		return "tcp6"
	}
	addresses, err := resolver.LookupIPAddr(ctx, host)
	if err != nil || len(addresses) == 0 {
		return network
	}
	var ipv4, ipv6 bool
	for _, address := range addresses {
		switch {
		case address.IP.To4() != nil:
			ipv4 = true
		case address.IP.To16() != nil:
			ipv6 = true
		}
	}
	switch {
	case ipv4 && !ipv6:
		return "tcp4"
	case ipv6 && !ipv4:
		return "tcp6"
	default:
		return network
	}
}

func (g *LiveGateway) Endpoints() map[string]LiveGatewayEndpoint {
	out := make(map[string]LiveGatewayEndpoint, len(g.endpoints))
	for name, endpoint := range g.endpoints {
		out[name] = endpoint
	}
	return out
}

func (g *LiveGateway) Close(ctx context.Context) error {
	g.closeOnce.Do(func() {
		for _, server := range g.servers {
			if err := server.Shutdown(ctx); err != nil && g.closeErr == nil {
				g.closeErr = err
			}
		}
		if g.state != nil && g.state.audit != nil {
			if err := g.state.audit.Sync(); err != nil && g.closeErr == nil {
				g.closeErr = err
			}
			if err := g.state.audit.Close(); err != nil && g.closeErr == nil {
				g.closeErr = err
			}
			for index := range g.state.hmacKey {
				g.state.hmacKey[index] = 0
			}
		}
	})
	return g.closeErr
}

func (s *liveGatewayService) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	started := time.Now()
	identity := s.state.requestIdentity(s.name, request.Method, request.URL.RequestURI(), nil)
	reject := func(status int, route, reason string) {
		_ = s.state.writeAudit(LiveGatewayAuditRecord{
			Phase: "preflight", Service: s.name, Route: route, Method: request.Method,
			RequestHMAC: identity, Decision: "deny", Reason: reason,
		})
		http.Error(response, "evaluation gateway denied request", status)
	}
	if !validGatewayAuthorization(request.Header.Get("Authorization"), s.capability) {
		reject(http.StatusUnauthorized, "", "ingress_auth")
		return
	}
	if hasGatewayMethodOverride(request) {
		reject(http.StatusBadRequest, "", "method_override")
		return
	}
	route, ok := matchLiveGatewayRoute(request.URL, request.Method, s.routes)
	if !ok {
		reject(http.StatusForbidden, "", "route")
		return
	}
	if !routeAllowsMethod(route, request.Method) {
		reject(http.StatusMethodNotAllowed, route.Name, "method")
		return
	}
	requestBody, err := readGatewayRequestBody(request, route.MaxRequestBytes)
	if err != nil {
		reject(http.StatusRequestEntityTooLarge, route.Name, "request_body")
		return
	}
	if (request.Method == http.MethodGet || request.Method == http.MethodHead) && len(requestBody) != 0 {
		reject(http.StatusBadRequest, route.Name, "request_body")
		return
	}
	contentType, ok := reviewedGatewayContentType(request.Header.Get("Content-Type"), len(requestBody))
	if !ok {
		reject(http.StatusBadRequest, route.Name, "content_type")
		return
	}
	identity = s.state.requestIdentity(s.name, request.Method, request.URL.RequestURI(), requestBody)
	if ok, reason := s.state.reserveRequest(s.name, route, request.Method, int64(len(requestBody))); !ok {
		reject(http.StatusTooManyRequests, route.Name, reason)
		return
	}
	select {
	case s.state.concurrency <- struct{}{}:
		defer func() { <-s.state.concurrency }()
	default:
		reject(http.StatusTooManyRequests, route.Name, "concurrency")
		return
	}
	if err := s.state.writeAudit(LiveGatewayAuditRecord{
		Phase: "preflight", Service: s.name, Route: route.Name, Method: request.Method,
		RequestHMAC: identity, RequestBytes: int64(len(requestBody)), Decision: "forward",
	}); err != nil {
		http.Error(response, "evaluation gateway audit unavailable", http.StatusBadGateway)
		return
	}
	target := gatewayUpstreamURL(s.base, request.URL)
	ctx, cancel := context.WithTimeout(request.Context(), s.state.config.RequestTimeout)
	defer cancel()
	// target inherits the validated, pinned scheme+host from s.base; only the
	// route-allowlisted path and query come from the loopback request.
	upstreamRequest, err := http.NewRequestWithContext(ctx, request.Method, target.String(), bytes.NewReader(requestBody)) //nolint:gosec // not an attacker-controlled origin
	if err != nil {
		s.completeDenied(response, request, route.Name, identity, int64(len(requestBody)), started, "request")
		return
	}
	upstreamRequest.Header.Set("Authorization", "Bearer "+s.token)
	upstreamRequest.Header.Set("Accept", "application/json")
	upstreamRequest.Header.Set("User-Agent", "atl-agent-eval-gateway")
	if contentType != "" {
		upstreamRequest.Header.Set("Content-Type", contentType)
	}
	if token := reviewedGatewayAtlassianToken(s.name, request.Header.Values("X-Atlassian-Token")); token != "" {
		upstreamRequest.Header.Set("X-Atlassian-Token", token)
	}
	upstreamResponse, err := s.state.upstreamHTTP.Do(upstreamRequest) //nolint:gosec // request origin is pinned above
	if err != nil {
		s.completeDenied(response, request, route.Name, identity, int64(len(requestBody)), started, "transport")
		return
	}
	defer upstreamResponse.Body.Close()
	if upstreamResponse.StatusCode >= 300 && upstreamResponse.StatusCode < 400 {
		s.completeDenied(response, request, route.Name, identity, int64(len(requestBody)), started, "redirect")
		return
	}
	responseBody, err := io.ReadAll(io.LimitReader(upstreamResponse.Body, s.state.config.MaxResponseBytes+1))
	if err != nil {
		s.completeDenied(response, request, route.Name, identity, int64(len(requestBody)), started, "response_read")
		return
	}
	responseContentType := upstreamResponse.Header.Get("Content-Type")
	responseBody, ok = s.reviewGatewayResponseBody(responseBody, responseContentType)
	if !ok {
		s.completeDenied(response, request, route.Name, identity, int64(len(requestBody)), started, "response_budget")
		return
	}
	record := LiveGatewayAuditRecord{
		Phase: "complete", Service: s.name, Route: route.Name, Method: request.Method,
		RequestHMAC: identity, Decision: "allow", StatusClass: gatewayStatusClass(upstreamResponse.StatusCode),
		RequestBytes: int64(len(requestBody)), ResponseBytes: int64(len(responseBody)), DurationMS: time.Since(started).Milliseconds(),
	}
	if err := s.state.writeAudit(record); err != nil {
		http.Error(response, "evaluation gateway audit unavailable", http.StatusBadGateway)
		return
	}
	if responseContentType != "" {
		response.Header().Set("Content-Type", responseContentType)
	}
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(upstreamResponse.StatusCode)
	if request.Method != http.MethodHead {
		_, _ = response.Write(responseBody) //nolint:gosec // reviewed origin, route, content, and byte budgets bound this proxy response
	}
}

func (s *liveGatewayService) reviewGatewayResponseBody(body []byte, contentType string) ([]byte, bool) {
	if int64(len(body)) > s.state.config.MaxResponseBytes {
		return nil, false
	}
	body = rewriteGatewayJSONURLs(body, contentType, s.base, s.downstream)
	if int64(len(body)) > s.state.config.MaxResponseBytes || !s.state.reserveResponseBytes(int64(len(body))) {
		return nil, false
	}
	return body, true
}

// rewriteGatewayJSONURLs keeps backend-provided same-origin resource links
// usable after the private-live child is bound to a disposable loopback URL.
// It never widens the gateway: every translated follow-up request must still
// carry the capability and pass the reviewed route, method, and byte budgets.
// Foreign origins, paths outside a configured upstream base path, non-JSON
// bodies, and JSON strings that are not themselves absolute URLs are unchanged.
func rewriteGatewayJSONURLs(body []byte, contentType string, upstream, downstream *url.URL) []byte {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || (mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json")) {
		return body
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return body
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return body
	}
	changed := false
	if text, ok := value.(string); ok {
		if rewritten, ok := rewriteGatewaySameOriginURL(text, upstream, downstream); ok {
			value = rewritten
			changed = true
		}
	} else {
		changed = rewriteGatewayJSONValue(value, upstream, downstream)
	}
	if !changed {
		return body
	}
	rewritten, err := json.Marshal(value)
	if err != nil {
		return body
	}
	return rewritten
}

func rewriteGatewayJSONValue(value any, upstream, downstream *url.URL) bool {
	changed := false
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if text, ok := child.(string); ok {
				if rewritten, ok := rewriteGatewaySameOriginURL(text, upstream, downstream); ok {
					typed[key] = rewritten
					changed = true
				}
				continue
			}
			changed = rewriteGatewayJSONValue(child, upstream, downstream) || changed
		}
	case []any:
		for index, child := range typed {
			if text, ok := child.(string); ok {
				if rewritten, ok := rewriteGatewaySameOriginURL(text, upstream, downstream); ok {
					typed[index] = rewritten
					changed = true
				}
				continue
			}
			changed = rewriteGatewayJSONValue(child, upstream, downstream) || changed
		}
	}
	return changed
}

func rewriteGatewaySameOriginURL(value string, upstream, downstream *url.URL) (string, bool) {
	parsed, err := url.Parse(value)
	if err != nil || !parsed.IsAbs() || parsed.Opaque != "" || parsed.User != nil || parsed.RawPath != "" {
		return value, false
	}
	if !strings.EqualFold(parsed.Scheme, upstream.Scheme) || !strings.EqualFold(parsed.Host, upstream.Host) {
		return value, false
	}
	basePath := strings.TrimRight(upstream.Path, "/")
	downstreamPath := parsed.Path
	if basePath != "" {
		switch {
		case parsed.Path == basePath:
			downstreamPath = "/"
		case strings.HasPrefix(parsed.Path, basePath+"/"):
			downstreamPath = strings.TrimPrefix(parsed.Path, basePath)
		default:
			return value, false
		}
	}
	rewritten := *downstream
	rewritten.Path = downstreamPath
	rewritten.RawPath = ""
	rewritten.RawQuery = parsed.RawQuery
	rewritten.ForceQuery = parsed.ForceQuery
	rewritten.Fragment = parsed.Fragment
	return rewritten.String(), true
}

func reviewedGatewayAtlassianToken(service string, values []string) string {
	if len(values) != 1 {
		return ""
	}
	switch values[0] {
	case "no-check":
		return values[0]
	case "nocheck":
		if service == "confluence" {
			return values[0]
		}
	}
	return ""
}

func (s *liveGatewayService) completeDenied(response http.ResponseWriter, request *http.Request, route, identity string, requestBytes int64, started time.Time, reason string) {
	_ = s.state.writeAudit(LiveGatewayAuditRecord{
		Phase: "complete", Service: s.name, Route: route, Method: request.Method,
		RequestHMAC: identity, RequestBytes: requestBytes, Decision: "deny", Reason: reason,
		DurationMS: time.Since(started).Milliseconds(),
	})
	http.Error(response, "evaluation gateway upstream request failed", http.StatusBadGateway)
}

func validateLiveGatewayConfig(config LiveGatewayConfig) error {
	if config.AuditPath == "" || len(config.Services) == 0 || len(config.Services) > 2 {
		return fmt.Errorf("live gateway requires an audit path and one or two services")
	}
	if config.MaxRequests < 1 || config.MaxRequests > 10_000 || config.MaxConcurrent < 1 || config.MaxConcurrent > 16 {
		return fmt.Errorf("live gateway request and concurrency budgets are invalid")
	}
	if config.MaxResponseBytes < 1 || config.MaxResponseBytes > 64<<20 || config.MaxTotalResponseBytes < config.MaxResponseBytes || config.MaxTotalResponseBytes > 256<<20 {
		return fmt.Errorf("live gateway response budgets are invalid")
	}
	if config.MaxWrites < 0 || config.MaxWrites > config.MaxRequests || config.MaxRequestBytes < 0 || config.MaxRequestBytes > 16<<20 ||
		config.MaxTotalRequestBytes < config.MaxRequestBytes || config.MaxTotalRequestBytes > 64<<20 ||
		(config.MaxWrites == 0) != (config.MaxRequestBytes == 0 && config.MaxTotalRequestBytes == 0) {
		return fmt.Errorf("live gateway write and request-body budgets are invalid")
	}
	if config.RequestTimeout < time.Second || config.RequestTimeout > 2*time.Minute {
		return fmt.Errorf("live gateway request timeout is invalid")
	}
	for name, service := range config.Services {
		if name != "jira" && name != "confluence" {
			return fmt.Errorf("unsupported live gateway service")
		}
		if service.Token == "" || len(service.Token) > 16<<10 || strings.ContainsAny(service.Token, "\r\n\x00") || len(service.Routes) == 0 || len(service.Routes) > 64 {
			return fmt.Errorf("live gateway service credentials or routes are invalid")
		}
		base, err := url.Parse(service.BaseURL)
		if err != nil || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || base.RawPath != "" {
			return fmt.Errorf("live gateway upstream URL is invalid")
		}
		if base.Path != "" && (path.Clean(base.Path) != base.Path || strings.Contains(base.Path, "//")) {
			return fmt.Errorf("live gateway upstream path is invalid")
		}
		scheme := strings.ToLower(base.Scheme)
		if scheme != "https" && (scheme != "http" || !isLoopbackHost(base.Hostname())) {
			return fmt.Errorf("live gateway upstream must use HTTPS")
		}
		if err := validateLiveGatewayRoutes(service.Routes); err != nil {
			return err
		}
	}
	return nil
}

func validateLiveGatewayRoutePolicy(services map[string][]LiveGatewayRoute) error {
	if len(services) == 0 || len(services) > 2 {
		return fmt.Errorf("live gateway route policy requires one or two services")
	}
	for service, routes := range services {
		if service != "jira" && service != "confluence" {
			return fmt.Errorf("unsupported live gateway service")
		}
		if err := validateLiveGatewayRoutes(routes); err != nil {
			return err
		}
	}
	return nil
}

func validateLiveGatewayRoutes(routes []LiveGatewayRoute) error {
	if len(routes) == 0 || len(routes) > 64 {
		return fmt.Errorf("live gateway requires 1..64 routes per service")
	}
	seenRoutes := map[string]struct{}{}
	seenPrefixExact := map[string]bool{}
	seenPrefixMethods := map[string]map[string]struct{}{}
	for _, route := range routes {
		if !identifierRE.MatchString(route.Name) || len(route.Name) > 64 || route.PathPrefix == "/" || route.PathPrefix == "" || route.PathPrefix[0] != '/' || path.Clean(route.PathPrefix) != route.PathPrefix || strings.Contains(route.PathPrefix, "//") {
			return fmt.Errorf("live gateway route is invalid")
		}
		if _, exists := seenRoutes[route.Name]; exists {
			return fmt.Errorf("live gateway route names must be unique per service")
		}
		seenRoutes[route.Name] = struct{}{}
		if route.MaxRequests < 0 || route.MaxRequests > 1000 || route.MaxRequestBytes < 0 || route.MaxRequestBytes > 16<<20 {
			return fmt.Errorf("live gateway route budgets are invalid")
		}
		seenMethods := map[string]struct{}{}
		for _, method := range route.Methods {
			if method != http.MethodGet && method != http.MethodHead && method != http.MethodPost && method != http.MethodPut && method != http.MethodPatch && method != http.MethodDelete {
				return fmt.Errorf("live gateway route method is invalid")
			}
			if _, exists := seenMethods[method]; exists {
				return fmt.Errorf("live gateway route methods must be unique")
			}
			seenMethods[method] = struct{}{}
		}
		if len(route.Methods) > 6 {
			return fmt.Errorf("live gateway route methods are invalid")
		}
		if exact, exists := seenPrefixExact[route.PathPrefix]; exists {
			if exact != route.Exact {
				return fmt.Errorf("live gateway routes sharing a prefix must use the same exactness")
			}
			if !route.Exact {
				return fmt.Errorf("live gateway routes may share only an exact path")
			}
		}
		seenPrefixExact[route.PathPrefix] = route.Exact
		prefixMethods := seenPrefixMethods[route.PathPrefix]
		if prefixMethods == nil {
			prefixMethods = map[string]struct{}{}
			seenPrefixMethods[route.PathPrefix] = prefixMethods
		}
		for _, method := range effectiveRouteMethods(route) {
			if _, exists := prefixMethods[method]; exists {
				return fmt.Errorf("live gateway route prefix methods must be disjoint per service")
			}
			prefixMethods[method] = struct{}{}
		}
		mutating := routeHasMutatingMethod(route)
		if mutating && (!route.Exact || route.MaxRequests < 1 || route.MaxRequestBytes < 1) {
			return fmt.Errorf("mutating live gateway routes require exact paths and positive request budgets")
		}
		if !mutating && route.MaxRequestBytes != 0 {
			return fmt.Errorf("read-only live gateway routes forbid request bodies")
		}
	}
	return nil
}

func (s *liveGatewayState) reserveRequest(service string, route LiveGatewayRoute, method string, requestBytes int64) (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.requests >= s.config.MaxRequests {
		return false, "request_budget"
	}
	key := service + "\x00" + route.Name
	if route.MaxRequests > 0 && s.routeRequests[key] >= route.MaxRequests {
		return false, "route_budget"
	}
	if requestBytes < 0 || requestBytes > s.config.MaxRequestBytes || s.totalRequestBytes+requestBytes > s.config.MaxTotalRequestBytes && requestBytes > 0 {
		return false, "request_body_budget"
	}
	if isMutatingHTTPMethod(method) {
		if s.writes >= s.config.MaxWrites {
			return false, "write_budget"
		}
		s.writes++
	}
	s.requests++
	s.routeRequests[key]++
	s.totalRequestBytes += requestBytes
	return true, ""
}

func (s *liveGatewayState) reserveResponseBytes(count int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if count < 0 || s.totalBytes+count > s.config.MaxTotalResponseBytes {
		return false
	}
	s.totalBytes += count
	return true
}

func (s *liveGatewayState) requestIdentity(service, method, requestURI string, body []byte) string {
	mac := hmac.New(sha256.New, s.hmacKey)
	bodySHA256 := sha256.Sum256(body)
	_, _ = mac.Write([]byte(service + "\x00" + method + "\x00" + requestURI + "\x00" + hex.EncodeToString(bodySHA256[:])))
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *liveGatewayState) writeAudit(record LiveGatewayAuditRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sequence++
	record.Sequence = s.sequence
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if s.auditBytes+int64(len(data)) > maxLiveGatewayAuditBytes {
		return fmt.Errorf("live gateway audit budget exceeded")
	}
	if _, err := s.audit.Write(data); err != nil {
		return err
	}
	if err := s.audit.Sync(); err != nil {
		return err
	}
	s.auditBytes += int64(len(data))
	return nil
}

func matchLiveGatewayRoute(requestURL *url.URL, method string, routes []LiveGatewayRoute) (LiveGatewayRoute, bool) {
	if requestURL == nil || requestURL.RawPath != "" || requestURL.Path == "" || requestURL.Path[0] != '/' || path.Clean(requestURL.Path) != requestURL.Path || strings.Contains(requestURL.Path, "//") {
		return LiveGatewayRoute{}, false
	}
	query, err := url.ParseQuery(requestURL.RawQuery)
	if err != nil {
		return LiveGatewayRoute{}, false
	}
	for key := range query {
		if strings.EqualFold(key, "_method") {
			return LiveGatewayRoute{}, false
		}
	}
	best := LiveGatewayRoute{}
	bestLength := -1
	for _, route := range routes {
		prefix := route.PathPrefix
		matches := requestURL.Path == prefix
		if !route.Exact {
			matches = matches || strings.HasSuffix(prefix, "/") && strings.HasPrefix(requestURL.Path, prefix) || strings.HasPrefix(requestURL.Path, prefix+"/")
		}
		if matches {
			if len(prefix) > bestLength || len(prefix) == bestLength && !routeAllowsMethod(best, method) && routeAllowsMethod(route, method) {
				best = route
				bestLength = len(prefix)
			}
		}
	}
	return best, bestLength >= 0
}

func effectiveRouteMethods(route LiveGatewayRoute) []string {
	if len(route.Methods) == 0 {
		return []string{http.MethodGet, http.MethodHead}
	}
	return route.Methods
}

func routeAllowsMethod(route LiveGatewayRoute, method string) bool {
	for _, allowed := range effectiveRouteMethods(route) {
		if allowed == method {
			return true
		}
	}
	return false
}

func routeHasMutatingMethod(route LiveGatewayRoute) bool {
	for _, method := range effectiveRouteMethods(route) {
		if isMutatingHTTPMethod(method) {
			return true
		}
	}
	return false
}

func isMutatingHTTPMethod(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}

func readGatewayRequestBody(request *http.Request, maxBytes int64) ([]byte, error) {
	if request.Body == nil || request.Body == http.NoBody {
		return nil, nil
	}
	if maxBytes < 0 || request.ContentLength > maxBytes || maxBytes == 0 && (request.ContentLength != 0 || len(request.TransferEncoding) != 0) {
		return nil, fmt.Errorf("request body exceeds route budget")
	}
	data, err := io.ReadAll(io.LimitReader(request.Body, maxBytes+1))
	if err != nil || int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("request body exceeds route budget")
	}
	return data, nil
}

func reviewedGatewayContentType(header string, bodyBytes int) (string, bool) {
	if header == "" {
		return "", bodyBytes == 0
	}
	mediaType, parameters, err := mime.ParseMediaType(header)
	if err != nil {
		return "", false
	}
	switch strings.ToLower(mediaType) {
	case "application/json":
		return header, true
	case "multipart/form-data":
		boundary := parameters["boundary"]
		return header, boundary != "" && len(boundary) <= 200
	default:
		return "", false
	}
}

func hasGatewayMethodOverride(request *http.Request) bool {
	for _, name := range []string{"X-HTTP-Method-Override", "X-Method-Override", "X-HTTP-Method"} {
		if request.Header.Get(name) != "" {
			return true
		}
	}
	return false
}

func validGatewayAuthorization(header, capability string) bool {
	want := "Bearer " + capability
	return len(header) == len(want) && subtle.ConstantTimeCompare([]byte(header), []byte(want)) == 1
}

func gatewayUpstreamURL(base, incoming *url.URL) *url.URL {
	target := *base
	target.Path = strings.TrimRight(base.Path, "/") + incoming.Path
	target.RawPath = ""
	target.RawQuery = incoming.RawQuery
	return &target
}

func gatewayStatusClass(status int) string {
	if status < 100 || status > 999 {
		return "unknown"
	}
	return fmt.Sprintf("%dxx", status/100)
}

func randomGatewayCapability() (string, error) {
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		return "", fmt.Errorf("create live gateway capability: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
