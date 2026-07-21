package agenteval

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

const (
	CLICommandPolicySchemaVersion = 1
	maxCLICommandPolicyBytes      = 1 << 20
)

var (
	cliCommandTokenRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	cliFlagNameRE     = regexp.MustCompile(`^--?[A-Za-z0-9][A-Za-z0-9-]{0,63}$`)
)

type CLICommandPolicy struct {
	SchemaVersion int              `json:"schema_version"`
	Rules         []CLICommandRule `json:"rules"`
}

type CLICommandRule struct {
	Name           string            `json:"name"`
	Command        []string          `json:"command"`
	Positionals    []CLIArgumentRule `json:"positionals,omitempty"`
	Flags          []CLIFlagRule     `json:"flags,omitempty"`
	MaxInvocations int               `json:"max_invocations"`
}

type CLIArgumentRule struct {
	Values []string `json:"values"`
}

type CLIFlagRule struct {
	Name        string   `json:"name"`
	Values      []string `json:"values,omitempty"`
	ValueFormat string   `json:"value_format,omitempty"`
	Required    bool     `json:"required,omitempty"`
}

type CLICommandMatch struct {
	Name           string
	MaxInvocations int
}

func (p CLICommandPolicy) Validate() error {
	if p.SchemaVersion != CLICommandPolicySchemaVersion {
		return fmt.Errorf("unsupported cli command policy schema_version %d", p.SchemaVersion)
	}
	if len(p.Rules) == 0 || len(p.Rules) > 64 {
		return fmt.Errorf("cli command policy requires 1..64 rules")
	}
	seenNames := map[string]struct{}{}
	for _, rule := range p.Rules {
		if !identifierRE.MatchString(rule.Name) {
			return fmt.Errorf("invalid cli command rule name")
		}
		if _, exists := seenNames[rule.Name]; exists {
			return fmt.Errorf("duplicate cli command rule name %q", rule.Name)
		}
		seenNames[rule.Name] = struct{}{}
		if len(rule.Command) == 0 || len(rule.Command) > 8 {
			return fmt.Errorf("cli command rule %q requires 1..8 command tokens", rule.Name)
		}
		for _, token := range rule.Command {
			if !cliCommandTokenRE.MatchString(token) {
				return fmt.Errorf("cli command rule %q has an invalid command token", rule.Name)
			}
		}
		if len(rule.Positionals) > 16 || len(rule.Flags) > 32 || rule.MaxInvocations < 1 || rule.MaxInvocations > 100 {
			return fmt.Errorf("cli command rule %q has invalid bounds", rule.Name)
		}
		for _, positional := range rule.Positionals {
			if err := validateCLIAllowedValues(positional.Values); err != nil {
				return fmt.Errorf("cli command rule %q positional: %w", rule.Name, err)
			}
		}
		seenFlags := map[string]struct{}{}
		for _, flag := range rule.Flags {
			if !cliFlagNameRE.MatchString(flag.Name) || flag.Name == "--" {
				return fmt.Errorf("cli command rule %q has an invalid flag name", rule.Name)
			}
			if _, exists := seenFlags[flag.Name]; exists {
				return fmt.Errorf("cli command rule %q has duplicate flag %q", rule.Name, flag.Name)
			}
			seenFlags[flag.Name] = struct{}{}
			if len(flag.Values) > 0 && flag.ValueFormat != "" {
				return fmt.Errorf("cli command rule %q flag %q cannot combine values and value_format", rule.Name, flag.Name)
			}
			if len(flag.Values) > 0 {
				if err := validateCLIAllowedValues(flag.Values); err != nil {
					return fmt.Errorf("cli command rule %q flag %q: %w", rule.Name, flag.Name, err)
				}
			}
			if flag.ValueFormat != "" && flag.ValueFormat != "sha256" {
				return fmt.Errorf("cli command rule %q flag %q has an invalid value_format", rule.Name, flag.Name)
			}
		}
	}
	return nil
}

func (p CLICommandPolicy) Match(args []string) (CLICommandMatch, error) {
	if err := p.Validate(); err != nil {
		return CLICommandMatch{}, err
	}
	if len(args) > 0 && args[0] == "--read-only" {
		args = args[1:]
	}
	var matches []CLICommandRule
	for _, rule := range p.Rules {
		if matchCLICommandRule(rule, args) {
			matches = append(matches, rule)
		}
	}
	if len(matches) == 0 {
		return CLICommandMatch{}, fmt.Errorf("cli command is outside the reviewed policy")
	}
	if len(matches) != 1 {
		return CLICommandMatch{}, fmt.Errorf("cli command policy is ambiguous")
	}
	return CLICommandMatch{Name: matches[0].Name, MaxInvocations: matches[0].MaxInvocations}, nil
}

func DecodeCLICommandPolicy(reader io.Reader) (CLICommandPolicy, error) {
	limited := &io.LimitedReader{R: reader, N: maxCLICommandPolicyBytes + 1}
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	var policy CLICommandPolicy
	if err := decoder.Decode(&policy); err != nil {
		return CLICommandPolicy{}, fmt.Errorf("decode cli command policy: %w", err)
	}
	if limited.N <= 0 {
		return CLICommandPolicy{}, fmt.Errorf("cli command policy exceeds %d bytes", maxCLICommandPolicyBytes)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return CLICommandPolicy{}, fmt.Errorf("cli command policy contains trailing JSON data")
	}
	if err := policy.Validate(); err != nil {
		return CLICommandPolicy{}, err
	}
	return policy, nil
}

func LoadCLICommandPolicy(path string) (CLICommandPolicy, error) {
	if err := requireOwnerOnly("cli command policy", path, false); err != nil {
		return CLICommandPolicy{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return CLICommandPolicy{}, err
	}
	policy, decodeErr := DecodeCLICommandPolicy(file)
	closeErr := file.Close()
	if decodeErr != nil {
		return CLICommandPolicy{}, decodeErr
	}
	if closeErr != nil {
		return CLICommandPolicy{}, closeErr
	}
	return policy, nil
}

func EncodeCLICommandPolicy(policy CLICommandPolicy) ([]byte, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(policy, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func validateCLIAllowedValues(values []string) error {
	if len(values) == 0 || len(values) > 32 {
		return fmt.Errorf("allowed values must contain 1..32 entries")
	}
	seen := map[string]struct{}{}
	for _, value := range values {
		if value == "" || len(value) > 2048 || strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("allowed value is invalid")
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("allowed values contain a duplicate")
		}
		seen[value] = struct{}{}
	}
	return nil
}

func matchCLICommandRule(rule CLICommandRule, args []string) bool {
	if len(args) < len(rule.Command) || !equalCLIStrings(args[:len(rule.Command)], rule.Command) {
		return false
	}
	flagRules := make(map[string]CLIFlagRule, len(rule.Flags))
	for _, flag := range rule.Flags {
		flagRules[flag.Name] = flag
	}
	seenFlags := map[string]struct{}{}
	var positionals []string
	rest := args[len(rule.Command):]
	for index := 0; index < len(rest); index++ {
		token := rest[index]
		if token == "--" || strings.Contains(token, "=") && strings.HasPrefix(token, "-") {
			return false
		}
		if strings.HasPrefix(token, "-") {
			flag, exists := flagRules[token]
			if !exists {
				return false
			}
			if _, duplicate := seenFlags[token]; duplicate {
				return false
			}
			seenFlags[token] = struct{}{}
			if len(flag.Values) > 0 || flag.ValueFormat != "" {
				index++
				if index >= len(rest) || !matchCLIFlagValue(flag, rest[index]) {
					return false
				}
			}
			continue
		}
		positionals = append(positionals, token)
	}
	if len(positionals) != len(rule.Positionals) {
		return false
	}
	for index, positional := range rule.Positionals {
		if !containsCLIString(positional.Values, positionals[index]) {
			return false
		}
	}
	for _, flag := range rule.Flags {
		_, present := seenFlags[flag.Name]
		if flag.Required && !present {
			return false
		}
	}
	return true
}

func matchCLIFlagValue(rule CLIFlagRule, candidate string) bool {
	if len(rule.Values) > 0 {
		return containsCLIString(rule.Values, candidate)
	}
	return rule.ValueFormat == "sha256" && validSHA256(candidate)
}

func equalCLIStrings(left, right []string) bool {
	return bytes.Equal([]byte(strings.Join(left, "\x00")), []byte(strings.Join(right, "\x00")))
}

func containsCLIString(values []string, candidate string) bool {
	for _, value := range values {
		if subtleCLIStringEqual(value, candidate) {
			return true
		}
	}
	return false
}

// Values are not secrets once the model has chosen them, but avoiding an
// early-exit comparison makes the policy behavior independent of shared
// prefixes and keeps this check suitable for private selectors.
func subtleCLIStringEqual(left, right string) bool {
	leftHash := sha256Bytes(left)
	rightHash := sha256Bytes(right)
	return bytes.Equal(leftHash[:], rightHash[:])
}

func sha256Bytes(value string) [32]byte {
	return sha256.Sum256([]byte(value))
}
