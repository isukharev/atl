package agenteval

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	ExternalMCPProfileSchemaVersion = 1
	maxExternalMCPProfileBytes      = 1 << 20
	externalMCPServerName           = "external_ro"
)

var sha256HexRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// External MCP profiles deliberately use a narrower vocabulary than benchmark
// observations. A reviewed server annotation is useful evidence, but it must
// never turn a write-capable semantic family into a read operation merely
// because the remote tool has an innocuous name.
var externalMCPReadCapabilityFamilies = map[string]struct{}{
	"jira.fields": {}, "jira.issue.fields": {}, "jira.issue.field": {},
	"jira.issue.search": {}, "jira.epic.digest": {}, "jira.board.view": {},
	"confluence.search": {}, "confluence.page.resolve": {},
	"confluence.page.outline": {}, "confluence.page.section": {},
}

// ExternalMCPProfile is a private, parent-only policy. It contains no literal
// credentials: header values are resolved from the owner-only atl config only
// after dry-run validation has completed.
type ExternalMCPProfile struct {
	SchemaVersion           int                     `json:"schema_version"`
	UpstreamURL             string                  `json:"upstream_url"`
	ProtocolVersion         string                  `json:"protocol_version"`
	CatalogSHA256           string                  `json:"catalog_sha256"`
	CatalogSHA256Alternates []string                `json:"catalog_sha256_alternates,omitempty"`
	ReviewedRO              bool                    `json:"reviewed_ro"`
	Headers                 []ExternalMCPHeader     `json:"headers"`
	Tools                   []ExternalMCPToolPolicy `json:"tools"`
	MaxRequestBytes         int64                   `json:"max_request_bytes"`
	MaxResponseBytes        int64                   `json:"max_response_bytes"`
	MaxTotalResponseBytes   int64                   `json:"max_total_response_bytes"`
	MaxConcurrent           int                     `json:"max_concurrent"`
	TimeoutSeconds          int                     `json:"timeout_seconds"`
}

type ExternalMCPHeader struct {
	Name      string `json:"name"`
	ValueFrom string `json:"value_from"`
}

type ExternalMCPToolPolicy struct {
	Name                        string            `json:"name"`
	Capability                  string            `json:"capability"`
	InputSchemaSHA256           string            `json:"input_schema_sha256"`
	InputSchemaSHA256Alternates []string          `json:"input_schema_sha256_alternates,omitempty"`
	MaxInvocations              int               `json:"max_invocations"`
	AllowedArguments            []json.RawMessage `json:"allowed_arguments"`
}

func LoadExternalMCPProfile(path, repositoryRoot string) (ExternalMCPProfile, error) {
	if err := requireOwnerOnly("external MCP profile directory", filepath.Dir(path), true); err != nil {
		return ExternalMCPProfile{}, err
	}
	if err := requireOwnerOnly("external MCP profile", path, false); err != nil {
		return ExternalMCPProfile{}, err
	}
	before, err := os.Lstat(path)
	if err != nil {
		return ExternalMCPProfile{}, err
	}
	profilePath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return ExternalMCPProfile{}, err
	}
	repositoryRoot, err = filepath.EvalSymlinks(repositoryRoot)
	if err != nil {
		return ExternalMCPProfile{}, err
	}
	inside, err := pathWithin(repositoryRoot, profilePath)
	if err != nil {
		return ExternalMCPProfile{}, err
	}
	if inside {
		return ExternalMCPProfile{}, fmt.Errorf("external MCP profile must be outside the repository")
	}
	file, err := os.Open(profilePath)
	if err != nil {
		return ExternalMCPProfile{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return ExternalMCPProfile{}, err
	}
	if !info.Mode().IsRegular() || !os.SameFile(before, info) || (runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0) {
		return ExternalMCPProfile{}, fmt.Errorf("external MCP profile changed during validation")
	}
	limited := &io.LimitedReader{R: file, N: maxExternalMCPProfileBytes + 1}
	data, err := io.ReadAll(limited)
	if err != nil {
		return ExternalMCPProfile{}, err
	}
	if len(data) > maxExternalMCPProfileBytes {
		return ExternalMCPProfile{}, fmt.Errorf("external MCP profile is oversized")
	}
	if err := validateJSONNoDuplicateKeys(data); err != nil {
		return ExternalMCPProfile{}, fmt.Errorf("decode external MCP profile: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var profile ExternalMCPProfile
	if err := decoder.Decode(&profile); err != nil {
		return ExternalMCPProfile{}, fmt.Errorf("decode external MCP profile: %w", err)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return ExternalMCPProfile{}, fmt.Errorf("external MCP profile contains trailing data")
	}
	if err := profile.Validate(); err != nil {
		return ExternalMCPProfile{}, err
	}
	return profile, nil
}

func validateExternalMCPProfileForRun(profile ExternalMCPProfile, spec RunSpec, scenario Scenario) error {
	if !sameExternalMCPTools(spec.AllowedMCPTools, profile.Tools) {
		return fmt.Errorf("external MCP run tool allowlist does not match its private profile")
	}
	capabilities := make([]string, 0, len(profile.Tools))
	seenCapabilities := map[string]struct{}{}
	for _, tool := range profile.Tools {
		dataCapability, ok := neutralDataCapability[tool.Capability]
		if !ok {
			return fmt.Errorf("external MCP profile exposes an unclassified data route")
		}
		seenCapabilities[dataCapability] = struct{}{}
	}
	for capability := range seenCapabilities {
		capabilities = append(capabilities, capability)
	}
	sort.Strings(capabilities)
	if !equalStrings(capabilities, spec.DataCapabilities) {
		return fmt.Errorf("external MCP profile data capabilities do not match the reviewed run")
	}
	var calls int
	for _, tool := range profile.Tools {
		calls += tool.MaxInvocations
	}
	if scenario.Budgets.MaxInterfaceInvocations < 1 || calls > scenario.Budgets.MaxInterfaceInvocations {
		return fmt.Errorf("external MCP profile call caps exceed the scenario interface budget")
	}
	if scenario.Budgets.MaxOutputBytes > 0 && profile.MaxTotalResponseBytes > scenario.Budgets.MaxOutputBytes {
		return fmt.Errorf("external MCP profile response cap exceeds the scenario output budget")
	}
	return nil
}

func (p ExternalMCPProfile) Validate() error {
	if p.SchemaVersion != ExternalMCPProfileSchemaVersion || !p.ReviewedRO {
		return fmt.Errorf("external MCP profile must be schema v1 and reviewed_ro")
	}
	upstream, err := url.Parse(p.UpstreamURL)
	if err != nil || upstream.Scheme != "https" || upstream.Host == "" || upstream.User != nil || upstream.RawQuery != "" || upstream.Fragment != "" || upstream.RawPath != "" {
		return fmt.Errorf("external MCP upstream URL must be a pinned HTTPS origin/path")
	}
	if p.ProtocolVersion == "" || len(p.ProtocolVersion) > 64 || strings.ContainsAny(p.ProtocolVersion, "\r\n\x00") || !sha256HexRE.MatchString(p.CatalogSHA256) {
		return fmt.Errorf("external MCP protocol or catalog digest is invalid")
	}
	if len(p.CatalogSHA256Alternates) > 7 {
		return fmt.Errorf("external MCP catalog digest variants are oversized")
	}
	seenCatalogDigests := map[string]bool{p.CatalogSHA256: true}
	for _, digest := range p.CatalogSHA256Alternates {
		if !sha256HexRE.MatchString(digest) || seenCatalogDigests[digest] {
			return fmt.Errorf("external MCP catalog digest variants are invalid")
		}
		seenCatalogDigests[digest] = true
	}
	if len(p.Headers) < 1 || len(p.Headers) > 8 || len(p.Tools) < 1 || len(p.Tools) > 128 {
		return fmt.Errorf("external MCP header/tool policy is empty or oversized")
	}
	allowedBindings := map[string]bool{
		"jira.credential": true, "jira.base_url": true,
		"confluence.credential": true, "confluence.base_url": true,
	}
	seenHeaders := map[string]bool{}
	forbiddenHeaders := map[string]bool{
		"Host": true, "Content-Length": true, "Origin": true, "Mcp-Session-Id": true,
		"Connection": true, "Proxy-Connection": true, "Transfer-Encoding": true,
		"Trailer": true, "Upgrade": true, "Te": true, "Accept": true,
		"Content-Type": true, "Mcp-Protocol-Version": true,
	}
	for _, header := range p.Headers {
		canonical := http.CanonicalHeaderKey(header.Name)
		if canonical == "" || canonical != header.Name || seenHeaders[canonical] || !allowedBindings[header.ValueFrom] || forbiddenHeaders[canonical] {
			return fmt.Errorf("external MCP header binding is invalid")
		}
		seenHeaders[canonical] = true
	}
	seenTools := map[string]bool{}
	for _, tool := range p.Tools {
		if !mcpToolNameRE.MatchString(tool.Name) || seenTools[tool.Name] || !sha256HexRE.MatchString(tool.InputSchemaSHA256) || tool.MaxInvocations < 1 || tool.MaxInvocations > 100 || len(tool.AllowedArguments) < 1 || len(tool.AllowedArguments) > 64 {
			return fmt.Errorf("external MCP tool policy is invalid")
		}
		if _, ok := externalMCPReadCapabilityFamilies[tool.Capability]; !ok || looksMutatingMCPTool(tool.Name) {
			return fmt.Errorf("external MCP tool policy is not a reviewed read capability")
		}
		if len(tool.InputSchemaSHA256Alternates) > 7 {
			return fmt.Errorf("external MCP tool schema digest variants are oversized")
		}
		seenSchemaDigests := map[string]bool{tool.InputSchemaSHA256: true}
		for _, digest := range tool.InputSchemaSHA256Alternates {
			if !sha256HexRE.MatchString(digest) || seenSchemaDigests[digest] {
				return fmt.Errorf("external MCP tool schema digest variants are invalid")
			}
			seenSchemaDigests[digest] = true
		}
		seenTools[tool.Name] = true
		seenArguments := map[string]bool{}
		for _, raw := range tool.AllowedArguments {
			canonical, err := canonicalJSONObject(raw)
			if err != nil || seenArguments[string(canonical)] {
				return fmt.Errorf("external MCP allowed arguments are invalid")
			}
			seenArguments[string(canonical)] = true
		}
	}
	if p.MaxRequestBytes < 1024 || p.MaxRequestBytes > 4<<20 || p.MaxResponseBytes < 1024 || p.MaxResponseBytes > 32<<20 || p.MaxTotalResponseBytes < p.MaxResponseBytes || p.MaxTotalResponseBytes > 128<<20 || p.MaxConcurrent != 1 || p.TimeoutSeconds < 1 || p.TimeoutSeconds > 120 {
		return fmt.Errorf("external MCP budgets are invalid")
	}
	return nil
}

func resolveExternalMCPHeaders(profile ExternalMCPProfile, liveConfigDir string) (map[string]string, []string, error) {
	inputs, err := loadLiveGatewayInputs(liveConfigDir)
	if err != nil {
		return nil, nil, err
	}
	values := map[string]string{
		"jira.credential": inputs.credentials["jira"], "confluence.credential": inputs.credentials["confluence"],
	}
	for _, service := range []string{"jira", "confluence"} {
		var value string
		if err := json.Unmarshal(inputs.config[service+"_url"], &value); err == nil {
			values[service+".base_url"] = value
		}
	}
	headers := map[string]string{}
	canaries := []string{profile.UpstreamURL}
	for _, binding := range profile.Headers {
		value := values[binding.ValueFrom]
		if !safeExternalMCPHeaderValue(value) {
			return nil, nil, fmt.Errorf("external MCP binding %q is unavailable", binding.ValueFrom)
		}
		headers[binding.Name] = value
		canaries = append(canaries, value)
	}
	return headers, canaries, nil
}

func safeExternalMCPHeaderValue(value string) bool {
	if value == "" || len(value) > 16<<10 {
		return false
	}
	for _, b := range []byte(value) {
		if b < 0x20 || b == 0x7f {
			return false
		}
	}
	return true
}

func canonicalJSONObject(raw json.RawMessage) ([]byte, error) {
	if err := validateJSONNoDuplicateKeys(raw); err != nil {
		return nil, err
	}
	var object map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&object); err != nil || object == nil || decoder.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("expected one JSON object")
	}
	return json.Marshal(object)
}

func canonicalJSONSHA(raw json.RawMessage) (string, error) {
	canonical, err := canonicalJSON(raw)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

func canonicalJSON(raw json.RawMessage) ([]byte, error) {
	if err := validateJSONNoDuplicateKeys(raw); err != nil {
		return nil, err
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("invalid JSON")
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return canonical, nil
}

func validateJSONNoDuplicateKeys(raw []byte) error {
	if !utf8.Valid(raw) {
		return fmt.Errorf("JSON is not valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := validateJSONValue(decoder, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("JSON contains trailing data")
		}
		return err
	}
	return nil
}

func validateJSONValue(decoder *json.Decoder, depth int) error {
	if depth > 128 {
		return fmt.Errorf("JSON nesting exceeds 128 levels")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("JSON object key is not a string")
			}
			foldedKey := strings.ToLower(key)
			if _, duplicate := seen[foldedKey]; duplicate {
				return fmt.Errorf("JSON object contains duplicate key")
			}
			seen[foldedKey] = struct{}{}
			if err := validateJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return fmt.Errorf("JSON object is not terminated")
		}
	case '[':
		for decoder.More() {
			if err := validateJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return fmt.Errorf("JSON array is not terminated")
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter")
	}
	return nil
}

func looksMutatingMCPTool(name string) bool {
	mutators := map[string]bool{"create": true, "update": true, "delete": true, "remove": true, "edit": true, "move": true, "set": true, "add": true, "transition": true, "upload": true, "write": true, "apply": true, "push": true}
	for _, token := range strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return r == '_' || r == '-' || r == '.'
	}) {
		if mutators[token] {
			return true
		}
	}
	return false
}

func sameExternalMCPTools(allowed []string, policies []ExternalMCPToolPolicy) bool {
	if len(allowed) != len(policies) {
		return false
	}
	seen := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		seen[name] = true
	}
	for _, policy := range policies {
		if !seen[policy.Name] {
			return false
		}
	}
	return true
}
