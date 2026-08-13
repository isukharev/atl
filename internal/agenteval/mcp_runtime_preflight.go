package agenteval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
)

const (
	syntheticMCPCapabilitiesResourceURI = "atl://capabilities"
	syntheticMCPRuntimeResourceURI      = "atl://runtime"
	syntheticMCPResourceMIMEType        = "application/json"
)

type syntheticMCPResourceDescriptor struct {
	URI         string
	Name        string
	Title       string
	Description string
	MIMEType    string
}

func syntheticMCPExpectedResourceInventory() [2]syntheticMCPResourceDescriptor {
	return [2]syntheticMCPResourceDescriptor{
		{
			URI:         syntheticMCPCapabilitiesResourceURI,
			Name:        "atl-capabilities",
			Title:       "atl capability routes",
			Description: "Static content-free CLI and MCP capability routing metadata.",
			MIMEType:    syntheticMCPResourceMIMEType,
		},
		{
			URI:         syntheticMCPRuntimeResourceURI,
			Name:        "atl-runtime",
			Title:       "atl runtime safety projection",
			Description: "Immutable content-free startup safety and compatibility metadata for this atl MCP invocation.",
			MIMEType:    syntheticMCPResourceMIMEType,
		},
	}
}

// verifyRuntimeResourceContract attests the invocation-specific safety
// projection on every selected-binary MCP run. This is deliberately separate
// from the optional full tools/list inventory: the two small resource requests
// are a universal admission boundary, while high-volume cohorts may retain the
// reviewed performance choice to skip a complete tool descriptor transfer.
func (p *boundedMCPCommand) verifyRuntimeResourceContract(ctx context.Context, expectedService string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped || !syntheticMCPServiceProfile(expectedService) {
		return fmt.Errorf("ATL MCP runtime resource expectation is invalid")
	}

	listID := p.nextID
	p.nextID++
	listed, err := p.exchangeLocked(ctx, map[string]any{
		"jsonrpc": "2.0",
		"id":      listID,
		"method":  "resources/list",
		"params":  map[string]any{},
	}, listID)
	if err != nil {
		return fmt.Errorf("list ATL MCP resources: %w", err)
	}
	if listed.err != nil {
		return fmt.Errorf("list ATL MCP resources: protocol error")
	}
	if err := validateSyntheticMCPResourceInventory(listed.result); err != nil {
		return fmt.Errorf("list ATL MCP resources: %w", err)
	}

	readID := p.nextID
	p.nextID++
	read, err := p.exchangeLocked(ctx, map[string]any{
		"jsonrpc": "2.0",
		"id":      readID,
		"method":  "resources/read",
		"params": map[string]any{
			"uri": syntheticMCPRuntimeResourceURI,
		},
	}, readID)
	if err != nil {
		return fmt.Errorf("read ATL MCP runtime resource: %w", err)
	}
	if read.err != nil {
		return fmt.Errorf("read ATL MCP runtime resource: protocol error")
	}
	if err := validateSyntheticMCPRuntimeResource(read.result, expectedService); err != nil {
		return fmt.Errorf("read ATL MCP runtime resource: %w", err)
	}
	return nil
}

func validateSyntheticMCPResourceInventory(result json.RawMessage) error {
	resourcesRaw, err := syntheticMCPCachedPayload(result, "resource inventory", "resources", "public")
	if err != nil {
		return err
	}
	expectedInventory := syntheticMCPExpectedResourceInventory()
	resources, err := syntheticMCPRawArray(resourcesRaw)
	if err != nil || len(resources) != len(expectedInventory) {
		return fmt.Errorf("resource inventory is not the exact one-page ATL inventory")
	}
	for index, expected := range expectedInventory {
		if err := validateSyntheticMCPResourceDescriptor(resources[index], expected); err != nil {
			return fmt.Errorf("resource inventory entry %d: %w", index, err)
		}
	}
	return nil
}

func validateSyntheticMCPResourceDescriptor(raw json.RawMessage, expected syntheticMCPResourceDescriptor) error {
	if validateJSONNoDuplicateKeys(raw) != nil {
		return fmt.Errorf("descriptor contains duplicate JSON members")
	}
	var document map[string]json.RawMessage
	if err := decodeStrictJSONObject(raw, &document); err != nil || document == nil {
		return fmt.Errorf("descriptor is not one object")
	}
	if err := requireExactJSONMembers(document, "resource descriptor", []string{
		"uri", "name", "title", "description", "mimeType",
	}); err != nil {
		return err
	}
	expectedValues := map[string]string{
		"uri": expected.URI, "name": expected.Name, "title": expected.Title,
		"description": expected.Description, "mimeType": expected.MIMEType,
	}
	for member, expectedValue := range expectedValues {
		var value string
		if json.Unmarshal(document[member], &value) != nil || value != expectedValue {
			return fmt.Errorf("descriptor %s drifted", member)
		}
	}
	return nil
}

func validateSyntheticMCPRuntimeResource(result json.RawMessage, expectedService string) error {
	contentsRaw, err := syntheticMCPCachedPayload(result, "runtime resource", "contents", "private")
	if err != nil {
		return err
	}
	contents, err := syntheticMCPRawArray(contentsRaw)
	if err != nil || len(contents) != 1 {
		return fmt.Errorf("runtime resource must contain exactly one content entry")
	}
	if validateJSONNoDuplicateKeys(contents[0]) != nil {
		return fmt.Errorf("runtime resource content contains duplicate JSON members")
	}
	var content map[string]json.RawMessage
	if err := decodeStrictJSONObject(contents[0], &content); err != nil || content == nil {
		return fmt.Errorf("runtime resource content is not one object")
	}
	if err := requireExactJSONMembers(content, "runtime resource content", []string{"uri", "mimeType", "text"}); err != nil {
		return err
	}
	var uri, mimeType, text string
	if json.Unmarshal(content["uri"], &uri) != nil || uri != syntheticMCPRuntimeResourceURI {
		return fmt.Errorf("runtime resource content URI drifted")
	}
	if json.Unmarshal(content["mimeType"], &mimeType) != nil || mimeType != syntheticMCPResourceMIMEType {
		return fmt.Errorf("runtime resource content MIME type drifted")
	}
	if json.Unmarshal(content["text"], &text) != nil || text == "" {
		return fmt.Errorf("runtime resource content text is invalid")
	}
	return validateSyntheticMCPRuntimeProjection([]byte(text), expectedService)
}

func validateSyntheticMCPRuntimeProjection(data []byte, expectedService string) error {
	if !syntheticMCPServiceProfile(expectedService) {
		return fmt.Errorf("runtime projection expectation is invalid")
	}
	if validateJSONNoDuplicateKeys(data) != nil {
		return fmt.Errorf("runtime projection contains duplicate JSON members")
	}
	var document map[string]json.RawMessage
	if err := decodeStrictJSONObject(data, &document); err != nil || document == nil {
		return fmt.Errorf("runtime projection is not one JSON object")
	}
	if err := requireExactJSONMembers(document, "runtime projection", []string{
		"schema_version", "access", "lifecycle", "change_activation", "service_profile",
		"global_read_only_policy", "plugin",
	}); err != nil {
		return err
	}
	if string(bytes.TrimSpace(document["schema_version"])) != "1" {
		return fmt.Errorf("runtime projection schema_version drifted")
	}
	for member, expected := range map[string]string{
		"access": "hard_read_only", "lifecycle": "startup_only", "change_activation": "restart_required",
	} {
		value, ok := syntheticMCPString(document[member])
		if !ok || value != expected {
			return fmt.Errorf("runtime projection %s drifted", member)
		}
	}
	service, ok := syntheticMCPString(document["service_profile"])
	if !ok || !syntheticMCPServiceProfile(service) {
		return fmt.Errorf("runtime projection service_profile is invalid")
	}
	if service != expectedService {
		return fmt.Errorf("runtime projection service_profile does not match the admitted MCP profile")
	}

	policy, err := syntheticMCPJSONObject(document["global_read_only_policy"], "global read-only policy")
	if err != nil {
		return err
	}
	if err := requireExactJSONMembers(policy, "global read-only policy", []string{
		"configured_read_only", "effective_read_only", "read_only_source",
	}); err != nil {
		return err
	}
	configured, configuredOK := syntheticMCPBool(policy["configured_read_only"])
	effective, effectiveOK := syntheticMCPBool(policy["effective_read_only"])
	source, sourceOK := syntheticMCPString(policy["read_only_source"])
	if !configuredOK || !effectiveOK || !sourceOK || !syntheticMCPReadOnlySource(source) {
		return fmt.Errorf("runtime projection global read-only policy is invalid")
	}
	if !syntheticMCPReadOnlyPolicyConsistent(configured, effective, source) {
		return fmt.Errorf("runtime projection global read-only policy is contradictory")
	}
	if configured || !effective || source != "environment" {
		return fmt.Errorf("runtime projection does not match the synthetic evaluator read-only policy")
	}

	plugin, err := syntheticMCPJSONObject(document["plugin"], "plugin projection")
	if err != nil {
		return err
	}
	if err := requireExactJSONMembers(plugin, "plugin projection", []string{"interface_contract", "product_version"}); err != nil {
		return err
	}
	interfaceContract, interfaceOK := syntheticMCPString(plugin["interface_contract"])
	productVersion, productOK := syntheticMCPString(plugin["product_version"])
	if !interfaceOK || !productOK ||
		(interfaceContract != "unverified" && interfaceContract != "compatible") ||
		(productVersion != "unverified" && productVersion != "match" && productVersion != "mismatch") {
		return fmt.Errorf("runtime projection plugin status is invalid")
	}
	if (interfaceContract == "unverified") != (productVersion == "unverified") {
		return fmt.Errorf("runtime projection plugin status is contradictory")
	}
	if interfaceContract != "unverified" || productVersion != "unverified" {
		return fmt.Errorf("runtime projection does not match the unmarked synthetic evaluator launch")
	}
	return nil
}

func syntheticMCPCachedPayload(result json.RawMessage, owner, payloadMember, cacheScope string) (json.RawMessage, error) {
	if validateJSONNoDuplicateKeys(result) != nil {
		return nil, fmt.Errorf("%s contains duplicate JSON members", owner)
	}
	var document map[string]json.RawMessage
	if err := decodeStrictJSONObject(result, &document); err != nil || document == nil {
		return nil, fmt.Errorf("%s is not one object", owner)
	}
	if err := requireExactJSONMembers(document, owner, []string{payloadMember, "ttlMs", "cacheScope"}); err != nil {
		return nil, err
	}
	if string(bytes.TrimSpace(document["ttlMs"])) != "0" {
		return nil, fmt.Errorf("%s has an invalid ATL cache TTL", owner)
	}
	scope, ok := syntheticMCPString(document["cacheScope"])
	if !ok || scope != cacheScope {
		return nil, fmt.Errorf("%s has an invalid ATL cache scope", owner)
	}
	return document[payloadMember], nil
}

func syntheticMCPRawArray(raw json.RawMessage) ([]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var values []json.RawMessage
	if err := decoder.Decode(&values); err != nil || values == nil || decoder.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("expected one non-null JSON array")
	}
	return values, nil
}

func syntheticMCPJSONObject(raw json.RawMessage, owner string) (map[string]json.RawMessage, error) {
	var document map[string]json.RawMessage
	if err := decodeStrictJSONObject(raw, &document); err != nil || document == nil {
		return nil, fmt.Errorf("runtime projection %s is not one object", owner)
	}
	return document, nil
}

func syntheticMCPString(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", false
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}

func syntheticMCPBool(raw json.RawMessage) (bool, bool) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false, false
	}
	var value bool
	if json.Unmarshal(raw, &value) != nil {
		return false, false
	}
	return value, true
}

func syntheticMCPServiceProfile(value string) bool {
	return value == "default" || value == "jira" || value == "confluence" || value == "offline"
}

func syntheticMCPReadOnlySource(value string) bool {
	return value == "flag" || value == "environment" || value == "configuration" || value == "none"
}

func syntheticMCPReadOnlyPolicyConsistent(configured, effective bool, source string) bool {
	switch source {
	case "none":
		return !configured && !effective
	case "configuration":
		return configured && effective
	case "flag", "environment":
		return effective
	default:
		return false
	}
}
