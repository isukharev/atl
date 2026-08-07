package config

import (
	"strings"
	"testing"
)

func TestTransportConfigSetClearAndEnvironmentPrecedence(t *testing.T) {
	cfg := &Config{}
	if err := SetTransportKey(cfg, "transport.jira.ca_bundle", "/config/jira.pem"); err != nil {
		t.Fatal(err)
	}
	if err := SetTransportKey(cfg, "transport.confluence.ca_bundle", "/config/conf.pem"); err != nil {
		t.Fatal(err)
	}
	if got := cfg.CABundle(TransportServiceJira); got != "/config/jira.pem" {
		t.Fatalf("Jira CA=%q", got)
	}
	t.Setenv("ATL_JIRA_CA_BUNDLE", "/env/private/jira.pem")
	overlayTransportEnvironment(cfg)
	if got := cfg.CABundle(TransportServiceJira); got != "/env/private/jira.pem" {
		t.Fatalf("environment Jira CA=%q", got)
	}
	projection := TransportProjection(cfg)
	if projection.Jira.CABundleSource != "environment" || !projection.Jira.CABundleConfigured {
		t.Fatalf("projection=%+v", projection)
	}
	if strings.Contains(projection.Jira.CABundleSource, "/env/") {
		t.Fatalf("projection leaked path: %+v", projection)
	}
	if err := SetTransportKey(cfg, "transport.jira.ca_bundle", ""); err != nil {
		t.Fatal(err)
	}
	if err := SetTransportKey(cfg, "transport.confluence.ca_bundle", ""); err != nil {
		t.Fatal(err)
	}
	if cfg.Transport != nil {
		t.Fatalf("empty transport was not pruned: %+v", cfg.Transport)
	}
}

func TestTransportConfigRejectsUnknownKey(t *testing.T) {
	if err := SetTransportKey(&Config{}, "transport.jira.private_key", "secret"); err == nil {
		t.Fatal("unknown secret-bearing transport key was accepted")
	}
}

func TestTransportConfigRoundTripsAndLoadAppliesEnvironmentOverride(t *testing.T) {
	t.Setenv("ATL_CONFIG_DIR", t.TempDir())
	t.Setenv("ATL_JIRA_CA_BUNDLE", "")
	t.Setenv("ATL_CONFLUENCE_CA_BUNDLE", "")
	cfg := &Config{}
	if err := SetTransportKey(cfg, "transport.jira.ca_bundle", "/config/jira.pem"); err != nil {
		t.Fatal(err)
	}
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load()
	if err != nil || loaded.CABundle(TransportServiceJira) != "/config/jira.pem" {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	t.Setenv("ATL_JIRA_CA_BUNDLE", "/environment/jira.pem")
	loaded, err = Load()
	if err != nil || loaded.CABundle(TransportServiceJira) != "/environment/jira.pem" {
		t.Fatalf("overridden=%+v err=%v", loaded, err)
	}
}
