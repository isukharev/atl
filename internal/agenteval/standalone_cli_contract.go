package agenteval

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

const (
	StandaloneContractVersion                 = "0.1.0-pre-release"
	StandaloneProjectConfigSchema             = "agent-eval/project-config"
	StandaloneProjectConfigVersion            = 1
	StandaloneProjectConfigMaxBytes           = 64 << 10
	StandaloneProjectConfigIdentifierMaxBytes = 1024
)

var ErrStandaloneProjectConfig = errors.New("standalone_invalid_project_config")

// StandaloneProjectConfig is the closed, invocation-selected project
// configuration. It contains selection only; no member grants contact,
// credential, network, private-workspace, or write authority.
type StandaloneProjectConfig struct {
	Schema          string  `json:"schema"`
	SchemaVersion   int     `json:"schema_version"`
	ContractVersion string  `json:"contract_version"`
	Profile         *string `json:"profile,omitempty"`
	Model           *string `json:"model,omitempty"`
	Repetitions     *int    `json:"repetitions,omitempty"`
}

// DecodeStandaloneProjectConfig strictly decodes one bounded schema-v1
// document. The returned error is content-free; callers must not render the
// underlying JSON or selected path.
func DecodeStandaloneProjectConfig(reader io.Reader) (StandaloneProjectConfig, error) {
	if reader == nil {
		return StandaloneProjectConfig{}, ErrStandaloneProjectConfig
	}
	limited := &io.LimitedReader{R: reader, N: StandaloneProjectConfigMaxBytes + 1}
	data, err := io.ReadAll(limited)
	if err != nil || limited.N == 0 || len(data) == 0 || !utf8.Valid(data) || bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) ||
		validateJSONNoDuplicateKeys(data) != nil || validateStandaloneProjectConfigShape(data) != nil {
		return StandaloneProjectConfig{}, ErrStandaloneProjectConfig
	}
	var config StandaloneProjectConfig
	if decodeStrictJSONObject(data, &config) != nil || validateStandaloneProjectConfig(config) != nil {
		return StandaloneProjectConfig{}, ErrStandaloneProjectConfig
	}
	return config, nil
}

func validateStandaloneProjectConfigShape(data []byte) error {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil {
		return err
	}
	for _, name := range []string{"profile", "model", "repetitions"} {
		if value, present := members[name]; present && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return ErrStandaloneProjectConfig
		}
	}
	return nil
}

// EncodeStandaloneProjectConfig emits the canonical compact document with one
// terminal LF. It is used by compatibility goldens, not by ambient discovery.
func EncodeStandaloneProjectConfig(config StandaloneProjectConfig) ([]byte, error) {
	if validateStandaloneProjectConfig(config) != nil {
		return nil, ErrStandaloneProjectConfig
	}
	data, err := json.Marshal(config)
	if err != nil || len(data)+1 > StandaloneProjectConfigMaxBytes {
		return nil, ErrStandaloneProjectConfig
	}
	return append(data, '\n'), nil
}

func validateStandaloneProjectConfig(config StandaloneProjectConfig) error {
	if config.Schema != StandaloneProjectConfigSchema || config.SchemaVersion != StandaloneProjectConfigVersion ||
		config.ContractVersion != StandaloneContractVersion ||
		!validStandaloneConfigText(config.Profile, StandaloneProjectConfigIdentifierMaxBytes) ||
		!validStandaloneConfigText(config.Model, StandaloneProjectConfigIdentifierMaxBytes) ||
		(config.Repetitions != nil && (*config.Repetitions < 1 || *config.Repetitions > 1000)) {
		return ErrStandaloneProjectConfig
	}
	return nil
}

func validStandaloneConfigText(value *string, maximum int) bool {
	if value == nil {
		return true
	}
	return strings.TrimSpace(*value) != "" && len(*value) <= maximum && utf8.ValidString(*value) && !strings.ContainsRune(*value, 0)
}
