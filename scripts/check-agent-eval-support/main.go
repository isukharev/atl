// Command check-agent-eval-support validates the maintainer-owned standalone
// agent-eval support contour without contacting a provider, backend, or
// release service.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

type policy struct {
	Schema        string `json:"schema"`
	SchemaVersion int    `json:"schema_version"`
	Status        string `json:"status"`
	SupportOwner  struct {
		Kind          string `json:"kind"`
		Repository    string `json:"repository"`
		SecurityRoute string `json:"security_route"`
	} `json:"support_owner"`
	ExternalConsumer struct {
		Kind          string `json:"kind"`
		Evidence      string `json:"evidence"`
		NamedConsumer bool   `json:"named_consumer"`
	} `json:"external_consumer"`
	Cadence struct {
		Stable     string `json:"stable"`
		PreRelease string `json:"pre_release"`
	} `json:"cadence"`
	Platforms []struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
		State        string `json:"state"`
		Surface      string `json:"surface"`
	} `json:"platforms"`
	ExcludedPlatforms []struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
		Reason       string `json:"reason"`
	} `json:"excluded_platforms"`
	Components []struct {
		ID             string `json:"id"`
		State          string `json:"state"`
		ProviderAccess string `json:"provider_access"`
		BackendAccess  string `json:"backend_access"`
		Network        string `json:"network"`
		Identity       string `json:"identity"`
		Route          string `json:"route"`
	} `json:"components"`
	Compatibility struct {
		SchemaPolicy     string `json:"schema_policy"`
		FutureGeneration string `json:"future_generation"`
		ContractWindow   string `json:"contract_window"`
		Rollback         string `json:"rollback"`
	} `json:"compatibility"`
	Deprecation struct {
		NoticeDays     int    `json:"notice_days"`
		NoticeReleases int    `json:"notice_releases"`
		RemovalMajor   bool   `json:"removal_requires_major"`
		ClockStarts    string `json:"clock_starts"`
	} `json:"deprecation"`
	Security struct {
		ResponseRoute string `json:"response_route"`
		Target        string `json:"target"`
		AutoUpdates   bool   `json:"automatic_updates"`
	} `json:"security"`
	Release struct {
		PublicationAuthority string   `json:"publication_authority"`
		StablePrerequisites  []string `json:"stable_prerequisites"`
		RepositoryExtraction string   `json:"repository_extraction"`
	} `json:"release"`
}

func main() {
	data, err := os.ReadFile("docs/maintainers/agent-eval-support.v1.json")
	if err != nil {
		fail(err)
	}
	if err := validatePolicyJSONShape(data); err != nil {
		fail(fmt.Errorf("support policy JSON shape is invalid: %w", err))
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var value policy
	if err := decoder.Decode(&value); err != nil {
		fail(fmt.Errorf("support policy is not valid JSON: %w", err))
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		fail(errors.New("support policy has trailing JSON"))
	}
	if err := validate(value); err != nil {
		fail(err)
	}
	docs, err := os.ReadFile("docs/maintainers/agent-eval-support.md")
	if err != nil {
		fail(err)
	}
	projection := policyProjection(value)
	for _, marker := range []string{
		"agent-eval-support.v1.json",
		"The standalone evaluator is `pre_release`",
		"Linux/amd64",
		"Windows persistence",
		"Future schema generations refuse",
		"Rollback is a release prerequisite",
		"There are no automatic updates",
		"180 days and two later stable minor releases",
		"separately approved release",
	} {
		if !bytes.Contains(docs, []byte(marker)) {
			fail(fmt.Errorf("support policy documentation is missing required marker %q", marker))
		}
	}
	projectionMarker := "<!-- agent-eval-support-policy-sha256: " + projection + " -->"
	if bytes.Count(docs, []byte(projectionMarker)) != 1 {
		fail(errors.New("support policy documentation does not contain the unique machine-policy projection"))
	}
	fmt.Println("agent-eval support policy: pre-release contour is closed and machine-bound")
}

func validate(value policy) error {
	if value.Schema != "agent-eval/support-policy" || value.SchemaVersion != 1 || value.Status != "pre_release" {
		return errors.New("support policy schema or status is invalid")
	}
	if value.SupportOwner.Kind != "repository_maintainer" || value.SupportOwner.Repository != "github.com/isukharev/atl" || value.SupportOwner.SecurityRoute != "SECURITY.md" {
		return errors.New("support owner route is invalid")
	}
	if value.ExternalConsumer.Kind != "out_of_tree_provider_free_conformance_runner" || value.ExternalConsumer.Evidence != "required_before_first_signed_release" || value.ExternalConsumer.NamedConsumer {
		return errors.New("support policy must keep external-consumer evidence pending")
	}
	if value.Cadence.Stable != "not_declared" || value.Cadence.PreRelease != "reviewed_source_only" {
		return errors.New("support cadence is invalid")
	}
	if len(value.Platforms) != 1 || value.Platforms[0].OS != "linux" || value.Platforms[0].Architecture != "amd64" || value.Platforms[0].State != "candidate" || value.Platforms[0].Surface != "provider_free_process" {
		return errors.New("candidate platform contour is invalid")
	}
	wantExcluded := []struct {
		os, architecture, reason string
	}{
		{"windows", "", "owner_only_directory_persistence_not_proven"},
		{"darwin", "", "signed_distribution_matrix_not_proven"},
		{"linux", "arm64", "signed_distribution_matrix_not_proven"},
	}
	if len(value.ExcludedPlatforms) != len(wantExcluded) {
		return errors.New("excluded platform contour is incomplete")
	}
	for i, got := range value.ExcludedPlatforms {
		want := wantExcluded[i]
		if got.OS != want.os || got.Architecture != want.architecture || got.Reason != want.reason {
			return errors.New("excluded platform contour is invalid")
		}
	}
	wantComponents := []struct {
		id, state, providerAccess, backendAccess, network, identity, route string
	}{
		{"standalone_cli", "pre_release", "none", "none", "none", "", ""},
		{"compatibility_bundle", "pre_release", "", "", "", "content_addressed", ""},
		{"container", "not_declared", "", "", "", "", "1389"},
		{"github_action", "not_declared", "", "", "", "", "1389"},
	}
	if len(value.Components) != len(wantComponents) {
		return errors.New("support component contour is incomplete")
	}
	for i, got := range value.Components {
		want := wantComponents[i]
		if got.ID != want.id || got.State != want.state || got.ProviderAccess != want.providerAccess || got.BackendAccess != want.backendAccess || got.Network != want.network || got.Identity != want.identity || got.Route != want.route {
			return errors.New("support component contour is invalid")
		}
	}
	if value.Compatibility.SchemaPolicy != "current_and_prior_supported_generation_only" || value.Compatibility.FutureGeneration != "refuse" || value.Compatibility.ContractWindow != "pre_release_no_stable_clock" || value.Compatibility.Rollback != "required_before_stable_release" {
		return errors.New("compatibility policy is invalid")
	}
	if value.Security.ResponseRoute != "SECURITY.md" || value.Security.Target != "best_effort_until_stable_policy_is_approved" || value.Security.AutoUpdates {
		return errors.New("security policy is invalid")
	}
	if value.Deprecation.NoticeDays != 180 || value.Deprecation.NoticeReleases != 2 || !value.Deprecation.RemovalMajor || value.Deprecation.ClockStarts != "first_conforming_signed_standalone_release" {
		return errors.New("deprecation policy is invalid")
	}
	wantPrerequisites := []string{
		"named_external_consumer_evidence",
		"complete_supported_platform_matrix",
		"signed_reproducible_artifacts",
		"full_evaluator_gate",
		"provider_free_conformance",
		"security_and_privacy_review",
	}
	if value.Release.PublicationAuthority != "separate_explicit_approval" || value.Release.RepositoryExtraction != "not_authorized" || !equalStrings(value.Release.StablePrerequisites, wantPrerequisites) {
		return errors.New("release prerequisites are incomplete")
	}
	return nil
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func policyProjection(value policy) string {
	data, err := json.Marshal(value)
	if err != nil {
		return "invalid"
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func validatePolicyJSONShape(data []byte) error {
	root, err := jsonObject(data)
	if err != nil {
		return err
	}
	if err := requireJSONMembers(root, "policy", []string{"schema", "schema_version", "status", "support_owner", "external_consumer", "cadence", "platforms", "excluded_platforms", "components", "compatibility", "deprecation", "security", "release"}); err != nil {
		return err
	}
	if value, err := jsonObject(root["support_owner"]); err != nil {
		return fmt.Errorf("support_owner: %w", err)
	} else if err := requireJSONMembers(value, "support_owner", []string{"kind", "repository", "security_route"}); err != nil {
		return err
	}
	if value, err := jsonObject(root["external_consumer"]); err != nil {
		return fmt.Errorf("external_consumer: %w", err)
	} else if err := requireJSONMembers(value, "external_consumer", []string{"kind", "evidence", "named_consumer"}); err != nil {
		return err
	}
	if value, err := jsonObject(root["cadence"]); err != nil {
		return fmt.Errorf("cadence: %w", err)
	} else if err := requireJSONMembers(value, "cadence", []string{"stable", "pre_release"}); err != nil {
		return err
	}
	if values, err := jsonArray(root["platforms"]); err != nil {
		return fmt.Errorf("platforms: %w", err)
	} else {
		for index, raw := range values {
			value, err := jsonObject(raw)
			if err != nil {
				return fmt.Errorf("platforms[%d]: %w", index, err)
			}
			if err := requireJSONMembers(value, fmt.Sprintf("platforms[%d]", index), []string{"os", "architecture", "state", "surface"}); err != nil {
				return err
			}
		}
	}
	if values, err := jsonArray(root["excluded_platforms"]); err != nil {
		return fmt.Errorf("excluded_platforms: %w", err)
	} else {
		for index, raw := range values {
			value, err := jsonObject(raw)
			if err != nil {
				return fmt.Errorf("excluded_platforms[%d]: %w", index, err)
			}
			if err := requireJSONMembersOptional(value, fmt.Sprintf("excluded_platforms[%d]", index), []string{"os", "reason"}, []string{"architecture"}); err != nil {
				return err
			}
		}
	}
	if values, err := jsonArray(root["components"]); err != nil {
		return fmt.Errorf("components: %w", err)
	} else {
		for index, raw := range values {
			value, err := jsonObject(raw)
			if err != nil {
				return fmt.Errorf("components[%d]: %w", index, err)
			}
			id, err := jsonString(value["id"])
			if err != nil {
				return fmt.Errorf("components[%d].id: %w", index, err)
			}
			required := []string{"id", "state"}
			switch id {
			case "standalone_cli":
				required = append(required, "provider_access", "backend_access", "network")
			case "compatibility_bundle":
				required = append(required, "identity")
			case "container", "github_action":
				required = append(required, "route")
			default:
				return fmt.Errorf("components[%d].id %q is not closed", index, id)
			}
			if err := requireJSONMembers(value, fmt.Sprintf("components[%d]", index), required); err != nil {
				return err
			}
		}
	}
	if value, err := jsonObject(root["compatibility"]); err != nil {
		return fmt.Errorf("compatibility: %w", err)
	} else if err := requireJSONMembers(value, "compatibility", []string{"schema_policy", "future_generation", "contract_window", "rollback"}); err != nil {
		return err
	}
	if value, err := jsonObject(root["deprecation"]); err != nil {
		return fmt.Errorf("deprecation: %w", err)
	} else if err := requireJSONMembers(value, "deprecation", []string{"notice_days", "notice_releases", "removal_requires_major", "clock_starts"}); err != nil {
		return err
	}
	if value, err := jsonObject(root["security"]); err != nil {
		return fmt.Errorf("security: %w", err)
	} else if err := requireJSONMembers(value, "security", []string{"response_route", "target", "automatic_updates"}); err != nil {
		return err
	}
	if value, err := jsonObject(root["release"]); err != nil {
		return fmt.Errorf("release: %w", err)
	} else if err := requireJSONMembers(value, "release", []string{"publication_authority", "stable_prerequisites", "repository_extraction"}); err != nil {
		return err
	} else if _, err := jsonArray(value["stable_prerequisites"]); err != nil {
		return fmt.Errorf("release.stable_prerequisites: %w", err)
	}
	return nil
}

func jsonObject(data []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return nil, errors.New("expected JSON object")
	}
	result := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		name, ok := token.(string)
		if !ok {
			return nil, errors.New("object member name is not a string")
		}
		if _, exists := result[name]; exists {
			return nil, fmt.Errorf("duplicate member %q", name)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		result[name] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing JSON value")
	}
	return result, nil
}

func jsonArray(data []byte) ([]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '[' {
		return nil, errors.New("expected JSON array")
	}
	values := make([]json.RawMessage, 0)
	for decoder.More() {
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing JSON value")
	}
	return values, nil
}

func requireJSONMembers(value map[string]json.RawMessage, owner string, required []string) error {
	return requireJSONMembersOptional(value, owner, required, nil)
}

func requireJSONMembersOptional(value map[string]json.RawMessage, owner string, required, optional []string) error {
	allowed := make(map[string]bool, len(required)+len(optional))
	for _, name := range required {
		allowed[name] = true
		if _, ok := value[name]; !ok {
			return fmt.Errorf("%s is missing required member %q", owner, name)
		}
	}
	for _, name := range optional {
		allowed[name] = true
	}
	for name := range value {
		if !allowed[name] {
			return fmt.Errorf("%s contains unknown member %q", owner, name)
		}
	}
	return nil
}

func jsonString(raw json.RawMessage) (string, error) {
	var value string
	if len(raw) == 0 {
		return "", errors.New("missing string")
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", errors.New("expected string")
	}
	return value, nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
