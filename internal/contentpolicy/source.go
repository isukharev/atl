package contentpolicy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

const maxPolicyBytes int64 = 64 << 10

var ruleIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
var kindPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

type Environment struct {
	Inline           string
	File             string
	FileSHA256       string
	ExpectedOwnerUID *uint32
}

type policyJSON struct {
	SchemaVersion int          `json:"schema_version"`
	Rules         []ruleJSON   `json:"rules"`
	Backend       *backendJSON `json:"backend,omitempty"`
}

type backendJSON struct {
	JiraSHA256       string `json:"jira_sha256,omitempty"`
	ConfluenceSHA256 string `json:"confluence_sha256,omitempty"`
}

type ruleJSON struct {
	ID       string       `json:"id"`
	Effect   Effect       `json:"effect"`
	Verbs    []string     `json:"verbs"`
	Resource selectorJSON `json:"resource"`
}

type selectorJSON struct {
	Service stringList `json:"service"`
	Kind    stringList `json:"kind,omitempty"`
	Project stringList `json:"project,omitempty"`
	Key     stringList `json:"key,omitempty"`
	Space   stringList `json:"space,omitempty"`
	ID      stringList `json:"id,omitempty"`
	Under   stringList `json:"under,omitempty"`
}

type stringList []string

func (values *stringList) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return fmt.Errorf("selector must be a string or string array")
	}
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		*values = stringList{single}
		return nil
	}
	var many []string
	if err := json.Unmarshal(data, &many); err != nil {
		return fmt.Errorf("selector must be a string or string array")
	}
	*values = many
	return nil
}

func Load(configDir string, environment Environment) (*Resolved, error) {
	if environment.Inline != "" && environment.File != "" {
		return nil, fmt.Errorf("ATL_POLICY and ATL_POLICY_FILE are mutually exclusive")
	}
	if environment.FileSHA256 != "" && environment.File == "" {
		return nil, fmt.Errorf("ATL_POLICY_SHA256 is valid only with ATL_POLICY_FILE")
	}
	resolved := &Resolved{}
	// Managed policy is layer 1 and is evaluated before user policy (layer 2).
	if environment.Inline != "" {
		data := []byte(environment.Inline)
		if int64(len(data)) > maxPolicyBytes {
			return nil, fmt.Errorf("env_inline policy exceeds %d-byte limit", maxPolicyBytes)
		}
		if err := resolved.addLayer("env_inline", data); err != nil {
			return nil, err
		}
	}
	if environment.File != "" {
		data, present, err := readOptionalPolicyFile(environment.File, environment.ExpectedOwnerUID)
		if err != nil {
			return nil, fmt.Errorf("load env_file policy: %w", err)
		}
		if !present {
			return nil, fmt.Errorf("ATL_POLICY_FILE is missing")
		}
		if environment.FileSHA256 != "" {
			if !validDigest(environment.FileSHA256) {
				return nil, fmt.Errorf("ATL_POLICY_SHA256 is malformed")
			}
			if Digest(data) != environment.FileSHA256 {
				actual := Digest(data)
				return nil, &DenialError{
					Reason: ReasonPolicyDigestMismatch, Advice: AdviceNoRetry,
					Message: "local policy digest does not match ATL_POLICY_SHA256",
					Details: DenialDetails{
						SchemaVersion: 1, Phase: "resolved", Verbs: make(domain.WriteVerbSet, 0),
						Target: DenialTarget{}, DecidedBy: DenialDecision{Layer: "managed", Effect: "source_error"},
						Reason: ReasonPolicyDigestMismatch, AllowedVerbsHere: make(domain.WriteVerbSet, 0),
						Advice: AdviceNoRetry, PolicyDigest: DenialPolicyDigest{Managed: &actual},
						PolicySource: "env_file", RetrySafe: false,
					},
				}
			}
		}
		if err := resolved.addLayer("env_file", data); err != nil {
			return nil, err
		}
	}
	configPath := filepath.Join(configDir, "policy.json")
	if configDir != "" {
		data, present, err := readOptionalPolicyFile(configPath, environment.ExpectedOwnerUID)
		if err != nil {
			return nil, fmt.Errorf("load config_dir policy: %w", err)
		}
		if present {
			if err := resolved.addLayer("config_dir", data); err != nil {
				return nil, err
			}
		}
	}
	return resolved, nil
}

func (resolved *Resolved) addLayer(source string, data []byte) error {
	policy, warnings, err := parsePolicy(data)
	if err != nil {
		return fmt.Errorf("parse %s policy: %w", source, err)
	}
	resolved.Layers = append(resolved.Layers, Layer{Source: source, Digest: Digest(data), Policy: policy})
	for index := range warnings {
		warnings[index].Source = source
	}
	resolved.Warnings = append(resolved.Warnings, warnings...)
	return nil
}

func parsePolicy(data []byte) (Policy, []Warning, error) {
	if int64(len(data)) > maxPolicyBytes {
		return Policy{}, nil, fmt.Errorf("policy exceeds %d-byte limit", maxPolicyBytes)
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return Policy{}, nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var document policyJSON
	if err := decoder.Decode(&document); err != nil {
		return Policy{}, nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Policy{}, nil, fmt.Errorf("policy contains more than one JSON value")
		}
		return Policy{}, nil, fmt.Errorf("policy trailing data: %w", err)
	}
	return validatePolicy(document)
}

func validatePolicy(document policyJSON) (Policy, []Warning, error) {
	if document.SchemaVersion != SchemaVersion {
		return Policy{}, nil, fmt.Errorf("unsupported schema_version %d", document.SchemaVersion)
	}
	if len(document.Rules) == 0 || len(document.Rules) > 256 {
		return Policy{}, nil, fmt.Errorf("rules must contain 1..256 entries")
	}
	policy := Policy{SchemaVersion: SchemaVersion}
	if document.Backend != nil {
		bindings := []struct {
			name  string
			value string
		}{
			{name: "jira_sha256", value: document.Backend.JiraSHA256},
			{name: "confluence_sha256", value: document.Backend.ConfluenceSHA256},
		}
		for _, binding := range bindings {
			if binding.value != "" && !validDigest(binding.value) {
				return Policy{}, nil, fmt.Errorf("backend.%s is malformed", binding.name)
			}
		}
		policy.Backend = BackendBinding{JiraSHA256: document.Backend.JiraSHA256, ConfluenceSHA256: document.Backend.ConfluenceSHA256}
	}
	seenIDs := make(map[string]struct{}, len(document.Rules))
	var warnings []Warning
	for _, raw := range document.Rules {
		rule, ruleWarnings, err := validateRule(raw)
		if err != nil {
			return Policy{}, nil, fmt.Errorf("rule %q: %w", raw.ID, err)
		}
		if _, exists := seenIDs[rule.ID]; exists {
			return Policy{}, nil, fmt.Errorf("duplicate rule id %q", rule.ID)
		}
		seenIDs[rule.ID] = struct{}{}
		policy.Rules = append(policy.Rules, rule)
		warnings = append(warnings, ruleWarnings...)
	}
	return policy, warnings, nil
}

func validateRule(raw ruleJSON) (Rule, []Warning, error) {
	if !ruleIDPattern.MatchString(raw.ID) {
		return Rule{}, nil, fmt.Errorf("invalid id")
	}
	if raw.Effect != EffectAllow && raw.Effect != EffectDeny {
		return Rule{}, nil, fmt.Errorf("effect must be allow or deny")
	}
	verbs, err := expandVerbs(raw.Verbs)
	if err != nil {
		return Rule{}, nil, err
	}
	selector, err := validateSelector(raw.Resource)
	if err != nil {
		return Rule{}, nil, err
	}
	rule := Rule{ID: raw.ID, Effect: raw.Effect, Verbs: verbs, Resource: selector}
	var warnings []Warning
	if containsVerb(verbs, domain.WriteVerbCreate) && len(selector.IDs) > 0 {
		warnings = append(warnings, Warning{RuleID: raw.ID, Message: "create with id can never match"})
	}
	if containsVerb(verbs, domain.WriteVerbTransition) && containsString(selector.Services, "confluence") {
		warnings = append(warnings, Warning{RuleID: raw.ID, Message: "Confluence transition can never match"})
	}
	for _, kind := range selector.Kinds {
		if !kindProducedByServices(kind, selector.Services) {
			warnings = append(warnings, Warning{RuleID: raw.ID, Message: fmt.Sprintf("kind %q cannot be produced by the selected service", kind)})
		}
	}
	if (len(selector.Projects) > 0 || len(selector.Keys) > 0) && !containsString(selector.Services, "jira") {
		warnings = append(warnings, Warning{RuleID: raw.ID, Message: "Jira selectors cannot be produced by the selected service"})
	}
	if (len(selector.Spaces) > 0 || len(selector.Under) > 0) && !containsString(selector.Services, "confluence") {
		warnings = append(warnings, Warning{RuleID: raw.ID, Message: "Confluence selectors cannot be produced by the selected service"})
	}
	if len(selector.IDs) > 0 && !containsString(selector.Services, "confluence") &&
		(!containsString(selector.Services, "jira") || !containsString(selector.Kinds, "sprint")) {
		warnings = append(warnings, Warning{RuleID: raw.ID, Message: "id cannot be produced by the selected service and kind"})
	}
	return rule, warnings, nil
}

func kindProducedByServices(kind string, services []string) bool {
	jiraKinds := []string{"issue", "sprint", "link", "attachment", "worklog", "watcher"}
	confluenceKinds := []string{"page", "blogpost", "attachment", "comment"}
	return containsString(services, "jira") && containsString(jiraKinds, kind) ||
		containsString(services, "confluence") && containsString(confluenceKinds, kind)
}

func expandVerbs(raw []string) (domain.WriteVerbSet, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("verbs must be non-empty")
	}
	seen := make(map[domain.WriteVerb]struct{})
	var verbs domain.WriteVerbSet
	for _, name := range raw {
		var expanded domain.WriteVerbSet
		switch name {
		case "write":
			expanded = domain.WriteVerbSet{domain.WriteVerbCreate, domain.WriteVerbUpdate, domain.WriteVerbComment}
		case "delete":
			expanded = domain.WriteVerbSet{domain.WriteVerbDelete}
		default:
			verb := domain.WriteVerb(name)
			if !domain.ValidWriteVerb(verb) {
				return nil, fmt.Errorf("unknown verb or class %q", name)
			}
			expanded = domain.WriteVerbSet{verb}
		}
		for _, verb := range expanded {
			if _, duplicate := seen[verb]; duplicate {
				return nil, fmt.Errorf("verbs contain duplicate or overlapping value %q", verb)
			}
			seen[verb] = struct{}{}
			verbs = append(verbs, verb)
		}
	}
	return verbs, nil
}

func validateSelector(raw selectorJSON) (Selector, error) {
	services, err := normalizedValues("service", raw.Service, false)
	if err != nil || len(services) == 0 {
		return Selector{}, fmt.Errorf("resource.service is required and must be non-empty")
	}
	for _, service := range services {
		if service != "jira" && service != "confluence" {
			return Selector{}, fmt.Errorf("unknown service %q", service)
		}
	}
	kinds, err := normalizedValues("kind", raw.Kind, false)
	if err != nil {
		return Selector{}, err
	}
	for _, kind := range kinds {
		if !kindPattern.MatchString(kind) {
			return Selector{}, fmt.Errorf("resource.kind contains invalid value %q", kind)
		}
	}
	projects, err := normalizedValues("project", raw.Project, true)
	if err != nil {
		return Selector{}, err
	}
	for _, project := range projects {
		if !domain.ValidJiraIssueKey(project + "-1") {
			return Selector{}, fmt.Errorf("invalid Jira project key %q", project)
		}
	}
	keys, err := normalizedValues("key", raw.Key, true)
	if err != nil {
		return Selector{}, err
	}
	for _, key := range keys {
		if !domain.ValidJiraIssueKey(key) {
			return Selector{}, fmt.Errorf("invalid Jira issue key %q", key)
		}
	}
	spaces, err := normalizedValues("space", raw.Space, true)
	if err != nil {
		return Selector{}, err
	}
	ids, err := normalizedValues("id", raw.ID, false)
	if err != nil {
		return Selector{}, err
	}
	under, err := normalizedValues("under", raw.Under, false)
	if err != nil {
		return Selector{}, err
	}
	if len(ids) > 0 && len(kinds) == 0 {
		return Selector{}, fmt.Errorf("resource.kind is required with id")
	}
	for _, id := range append(append([]string(nil), ids...), under...) {
		if !domain.ValidConfluenceContentID(id) {
			return Selector{}, fmt.Errorf("invalid content id %q", id)
		}
	}
	return Selector{Services: services, Kinds: kinds, Projects: projects, Keys: keys, Spaces: spaces, IDs: ids, Under: under}, nil
}

func normalizedValues(name string, values []string, uppercase bool) ([]string, error) {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if uppercase {
			value = strings.ToUpper(value)
		}
		if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, "*?") {
			return nil, fmt.Errorf("resource.%s contains an invalid exact value", name)
		}
		if _, duplicate := seen[value]; duplicate {
			return nil, fmt.Errorf("resource.%s contains duplicate value %q", name, value)
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

func readOptionalPolicyFile(path string, expectedOwnerUID *uint32) ([]byte, bool, error) {
	data, err := readPolicyFile(path, maxPolicyBytes, expectedOwnerUID)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, err
	}
	return data, true, nil
}

func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, composite := token.(json.Delim)
		if !composite {
			return nil
		}
		switch delimiter {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return fmt.Errorf("object key is not a string")
				}
				if _, duplicate := seen[key]; duplicate {
					return fmt.Errorf("duplicate JSON key %q", key)
				}
				seen[key] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return fmt.Errorf("unexpected JSON delimiter")
		}
	}
	return walk()
}
