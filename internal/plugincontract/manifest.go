package plugincontract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
)

const (
	expectedManifestMCPServers = "./.mcp.json"
)

type pluginManifest struct {
	Version    string `json:"version"`
	MCPServers string `json:"mcpServers"`
}

type generatedMCPConfig struct {
	MCPServers map[string]generatedMCPServer `json:"mcpServers"`
}

type generatedMCPServer struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// GeneratedMCPConfigs derives each invocation from the manifest that consumes
// it. Manifest versions remain release-managed; no third product version is
// stored in a template or generator constant.
func GeneratedMCPConfigs(claudeManifest, codexManifest []byte) (claudeConfig, codexConfig []byte, err error) {
	claudeConfig, err = generatedMCPConfigForManifest(claudeManifest)
	if err != nil {
		return nil, nil, fmt.Errorf("claude plugin manifest: %w", err)
	}
	codexConfig, err = generatedMCPConfigForManifest(codexManifest)
	if err != nil {
		return nil, nil, fmt.Errorf("codex plugin manifest: %w", err)
	}
	return claudeConfig, codexConfig, nil
}

func generatedMCPConfigForManifest(data []byte) ([]byte, error) {
	if err := rejectDuplicateManifestKeys(data); err != nil {
		return nil, fmt.Errorf("decode manifest: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	var manifest pluginManifest
	if err := decoder.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("decode manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("manifest must contain exactly one JSON value")
	}
	if !ValidProductVersion(manifest.Version) {
		return nil, fmt.Errorf("manifest version must be a bounded product-version token")
	}
	if manifest.MCPServers != expectedManifestMCPServers {
		return nil, fmt.Errorf("manifest mcpServers must be %q", expectedManifestMCPServers)
	}
	config := generatedMCPConfig{MCPServers: map[string]generatedMCPServer{
		"atl": {Command: "atl", Args: []string{
			"mcp", "serve",
			"--" + InterfaceFlagName + "=" + strconv.Itoa(InterfaceVersion),
			"--" + ProductFlagName + "=" + manifest.Version,
		}},
	}}
	rendered, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(rendered, '\n'), nil
}

func rejectDuplicateManifestKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
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
					return fmt.Errorf("manifest contains a duplicate JSON key")
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
