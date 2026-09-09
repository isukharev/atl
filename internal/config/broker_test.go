package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestLoadSelectsExplicitBrokerModeWithoutCredentialBytes(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("ATL_CONFIG_DIR", directory)
	configBody := []byte(`{"connection_mode":"direct","jira_list_views":{}}`)
	if err := os.WriteFile(filepath.Join(directory, "config.json"), configBody, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ATL_CONNECTION_MODE", "broker")
	t.Setenv("ATL_BROKER_URL", "https://broker.example.test")
	t.Setenv("ATL_BROKER_ID", "broker-1")
	t.Setenv("ATL_BROKER_AUDIENCE", "atl-broker")
	t.Setenv("ATL_BROKER_JIRA_SESSION_FILE", "/private/runtime/jira.json")
	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if EffectiveConnectionMode(loaded.ConnectionMode) != ConnectionModeBroker || loaded.Broker == nil || loaded.Broker.JiraSessionFile != "/private/runtime/jira.json" {
		t.Fatalf("loaded=%+v", loaded)
	}
}

func TestBrokerConfigurationFailsClosed(t *testing.T) {
	for _, test := range []Config{
		{ConnectionMode: "automatic"},
		{ConnectionMode: ConnectionModeBroker},
		{ConnectionMode: ConnectionModeBroker, Broker: &BrokerClientConfig{BaseURL: "http://broker.example.test", BrokerID: "broker-1", Audience: "atl-broker", JiraSessionFile: "session.json"}},
		{ConnectionMode: ConnectionModeBroker, Broker: &BrokerClientConfig{BaseURL: "https://broker.example.test/path", BrokerID: "broker-1", Audience: "atl-broker", JiraSessionFile: "session.json"}},
		{ConnectionMode: ConnectionModeBroker, Broker: &BrokerClientConfig{BaseURL: "https://broker.example.test", BrokerID: "broker-1", Audience: "atl-broker"}},
	} {
		t.Setenv("ATL_CONFIG_DIR", t.TempDir())
		if err := Save(&test); !errors.Is(err, domain.ErrConfig) {
			t.Fatalf("config=%+v err=%v", test, err)
		}
	}
}

func TestBrokerObservationSessionIsIndependentOrdinaryConfiguration(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("ATL_CONFIG_DIR", directory)
	configBody := []byte(`{"connection_mode":"broker","broker":{"base_url":"https://broker.example.test","broker_id":"broker-1","audience":"atl-broker","jira_session_file":"/private/runtime/writer.json","jira_observation_session_file":"/private/runtime/observer.json"},"jira_list_views":{}}`)
	if err := os.WriteFile(filepath.Join(directory, "config.json"), configBody, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Broker.JiraSessionFile != "/private/runtime/writer.json" || loaded.Broker.JiraObservationSessionFile != "/private/runtime/observer.json" {
		t.Fatalf("broker=%+v", loaded.Broker)
	}
	projection := BrokerProjection(loaded)
	if !projection.JiraSessionConfigured || !projection.JiraObservationSessionConfigured {
		t.Fatalf("projection=%+v", projection)
	}
}

func TestBrokerObservationOnlyConfigurationIsValidButWhitespaceIsRejected(t *testing.T) {
	valid := &BrokerClientConfig{
		BaseURL: "https://broker.example.test", BrokerID: "broker-1", Audience: "atl-broker",
		JiraObservationSessionFile: "/private/runtime/observer.json",
	}
	if err := ValidateBrokerClientConfig(ConnectionModeBroker, valid); err != nil {
		t.Fatalf("valid observation-only config: %v", err)
	}
	changed := *valid
	changed.JiraObservationSessionFile += " "
	if err := ValidateBrokerClientConfig(ConnectionModeBroker, &changed); err == nil {
		t.Fatal("whitespace observation session accepted")
	}
}
