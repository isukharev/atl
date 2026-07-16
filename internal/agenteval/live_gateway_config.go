package agenteval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"time"
)

type liveGatewayInputs struct {
	config      map[string]json.RawMessage
	credentials map[string]string
}

func startPrivateCLIGateway(sourceDir, childDir, auditPath string, spec RunSpec, scenario Scenario) (*LiveGateway, error) {
	inputs, err := loadLiveGatewayInputs(sourceDir)
	if err != nil {
		return nil, err
	}
	services := make(map[string]LiveGatewayServiceConfig, len(spec.AllowedGatewayRoutes))
	for service, routes := range spec.AllowedGatewayRoutes {
		urlKey := service + "_url"
		var baseURL string
		if err := json.Unmarshal(inputs.config[urlKey], &baseURL); err != nil || baseURL == "" {
			return nil, fmt.Errorf("private-live %s URL is missing or invalid", service)
		}
		token := inputs.credentials[service]
		if token == "" {
			return nil, fmt.Errorf("private-live %s credential is missing", service)
		}
		services[service] = LiveGatewayServiceConfig{BaseURL: baseURL, Token: token, Routes: append([]LiveGatewayRoute(nil), routes...)}
	}
	timeout := time.Duration(spec.TimeoutSeconds) * time.Second
	if timeout > 2*time.Minute {
		timeout = 2 * time.Minute
	}
	gateway, err := StartLiveGateway(LiveGatewayConfig{
		AuditPath: auditPath, Services: services,
		MaxRequests: scenario.Budgets.MaxBackendRequests, MaxConcurrent: 1,
		MaxResponseBytes: spec.GatewayMaxResponseBytes, MaxTotalResponseBytes: spec.GatewayMaxTotalBytes,
		RequestTimeout: timeout,
	})
	if err != nil {
		return nil, err
	}
	if err := writeLiveGatewayChildConfig(childDir, inputs.config, gateway.Endpoints()); err != nil {
		_ = gateway.Close(context.Background())
		return nil, err
	}
	return gateway, nil
}

func loadLiveGatewayInputs(sourceDir string) (liveGatewayInputs, error) {
	configData, err := readBoundedFile(filepath.Join(sourceDir, "config.json"), 4<<20)
	if err != nil {
		return liveGatewayInputs{}, fmt.Errorf("read private-live config: %w", err)
	}
	credentialsData, err := readBoundedFile(filepath.Join(sourceDir, "credentials.json"), 4<<20)
	if err != nil {
		return liveGatewayInputs{}, fmt.Errorf("read private-live credentials: %w", err)
	}
	config := map[string]json.RawMessage{}
	credentials := map[string]string{}
	if err := decodeSingleJSONObject(configData, &config); err != nil {
		return liveGatewayInputs{}, fmt.Errorf("private-live config.json: %w", err)
	}
	if err := decodeSingleJSONObject(credentialsData, &credentials); err != nil {
		return liveGatewayInputs{}, fmt.Errorf("private-live credentials.json: %w", err)
	}
	return liveGatewayInputs{config: config, credentials: credentials}, nil
}

func decodeSingleJSONObject(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("expected one JSON object")
	}
	return nil
}

func writeLiveGatewayChildConfig(directory string, source map[string]json.RawMessage, endpoints map[string]LiveGatewayEndpoint) error {
	if err := mkdirPrivate(directory); err != nil {
		return err
	}
	child := make(map[string]json.RawMessage, len(source)+1)
	for key, value := range source {
		child[key] = append(json.RawMessage(nil), value...)
	}
	delete(child, "update_base_url")
	for _, service := range []string{"jira", "confluence"} {
		delete(child, service+"_url")
	}
	child["read_only"] = json.RawMessage("true")
	credentials := map[string]string{}
	for service, endpoint := range endpoints {
		encoded, err := json.Marshal(endpoint.BaseURL)
		if err != nil {
			return err
		}
		child[service+"_url"] = encoded
		credentials[service] = endpoint.Token
	}
	configData, err := json.MarshalIndent(child, "", "  ")
	if err != nil {
		return err
	}
	credentialData, err := json.MarshalIndent(credentials, "", "  ")
	if err != nil {
		return err
	}
	if err := writePrivateFile(filepath.Join(directory, "config.json"), append(configData, '\n')); err != nil {
		return err
	}
	return writePrivateFile(filepath.Join(directory, "credentials.json"), append(credentialData, '\n'))
}
