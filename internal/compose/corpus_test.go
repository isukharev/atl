package compose

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/corpus"
	"github.com/isukharev/atl/internal/domain"
)

func TestNewCorpusBuildLoadsOnlySelectedCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("composition unexpectedly contacted a backend")
	}))
	t.Cleanup(server.Close)
	t.Setenv("ATL_CONFIG_DIR", t.TempDir())
	t.Setenv("ATL_JIRA_PAT", "configured-fixture-token")
	t.Setenv("ATL_CONFLUENCE_PAT", "")

	service, err := NewCorpusBuild(&config.Config{JiraURL: server.URL, ConfluenceURL: server.URL}, CorpusBuildSelection{
		Jira: true, MaxInFlight: 2, RequestsPerSecond: 100,
		GeneratorVersion: "test-v1", BuildState: corpus.BuildStateClean,
	})
	if err != nil || service == nil {
		t.Fatalf("service=%#v err=%v", service, err)
	}
	if service, err := NewCorpusBuild(&config.Config{JiraURL: server.URL, ConfluenceURL: server.URL}, CorpusBuildSelection{
		Confluence: true, MaxInFlight: 2, RequestsPerSecond: 100,
		GeneratorVersion: "test-v1", BuildState: corpus.BuildStateClean,
	}); err == nil || service != nil || !errors.Is(err, domain.ErrConfig) {
		t.Fatalf("missing selected credential service=%#v err=%v", service, err)
	}
}

func TestCorpusBuildCompositionDenyAllAuthorizerAndScheduleBounds(t *testing.T) {
	authorizer := corpusDenyAllWrites{}
	if _, err := authorizer.Authorize(context.Background(), domain.WriteAuthorizationRequest{}); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("authorize error=%v", err)
	}
	if err := authorizer.Preflight(domain.WriteAuthorizationRequest{}); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("preflight error=%v", err)
	}
	for _, selection := range []CorpusBuildSelection{
		{},
		{Jira: true},
		{Jira: true, MaxInFlight: 1},
	} {
		if service, err := NewCorpusBuild(&config.Config{}, selection); err == nil || service != nil || !errors.Is(err, domain.ErrUsage) {
			t.Fatalf("selection=%#v service=%#v err=%v", selection, service, err)
		}
	}
}

func TestCorpusBuildCacheQualifiesConfiguredConfluenceTrustAndGenerator(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("composition unexpectedly contacted a backend")
	}))
	defer server.Close()
	bundle := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	path := filepath.Join(t.TempDir(), "configured-ca.pem")
	if err := os.WriteFile(path, bundle, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ATL_CONFIG_DIR", t.TempDir())
	t.Setenv("ATL_CONFLUENCE_PAT", "configured-fixture-token")
	configuration := &config.Config{
		ConfluenceURL: server.URL,
		Transport: &config.TransportConfig{Confluence: &config.BackendTransportConfig{
			CABundle: path,
		}},
	}
	service, err := NewCorpusBuild(configuration, CorpusBuildSelection{
		Confluence: true, QualifiedCacheTrust: true, MaxInFlight: 1, RequestsPerSecond: 10,
		GeneratorVersion: "test-v1", GeneratorCommit: "0123456789abcdef", BuildState: corpus.BuildStateClean,
	})
	if err != nil {
		t.Fatal(err)
	}
	value := reflect.ValueOf(service).Elem()
	sum := sha256.Sum256(bundle)
	if got := value.FieldByName("confluenceTrustDigest").String(); got != hex.EncodeToString(sum[:]) {
		t.Fatalf("trust digest=%q", got)
	}
	if got := value.FieldByName("generatorCommit").String(); got != "0123456789abcdef" {
		t.Fatalf("generator commit=%q", got)
	}
}

func TestCorpusBuildCacheKeepsAbsentAndMixedConfluenceTrustOrdinary(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("composition unexpectedly contacted a backend")
	}))
	defer server.Close()
	bundlePath := filepath.Join(t.TempDir(), "mixed-ca.pem")
	ordinaryBundle := append([]byte("# ordinary additive bundle\n"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})...)
	if err := os.WriteFile(bundlePath, ordinaryBundle, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ATL_CONFIG_DIR", t.TempDir())
	t.Setenv("ATL_CONFLUENCE_PAT", "configured-fixture-token")
	t.Setenv("ATL_JIRA_PAT", "configured-fixture-token")
	selections := []struct {
		name          string
		configuration *config.Config
		selection     CorpusBuildSelection
	}{
		{
			name:          "absent CA",
			configuration: &config.Config{ConfluenceURL: server.URL},
			selection:     CorpusBuildSelection{Confluence: true, QualifiedCacheTrust: true},
		},
		{
			name: "mixed services",
			configuration: &config.Config{
				ConfluenceURL: server.URL, JiraURL: server.URL,
				Transport: &config.TransportConfig{Confluence: &config.BackendTransportConfig{CABundle: bundlePath}},
			},
			selection: CorpusBuildSelection{Confluence: true, Jira: true},
		},
	}
	for _, test := range selections {
		t.Run(test.name, func(t *testing.T) {
			test.selection.MaxInFlight = 1
			test.selection.RequestsPerSecond = 10
			test.selection.GeneratorVersion = "test-v1"
			service, err := NewCorpusBuild(test.configuration, test.selection)
			if err != nil {
				t.Fatal(err)
			}
			if got := reflect.ValueOf(service).Elem().FieldByName("confluenceTrustDigest").String(); got != "" {
				t.Fatalf("ordinary transport received trust digest %q", got)
			}
		})
	}
}

func TestCorpusBuildCompositionRejectsQualifiedCacheTrustOutsideSoleConfluence(t *testing.T) {
	for _, selection := range []CorpusBuildSelection{
		{Jira: true, QualifiedCacheTrust: true, MaxInFlight: 1, RequestsPerSecond: 1},
		{Jira: true, Confluence: true, QualifiedCacheTrust: true, MaxInFlight: 1, RequestsPerSecond: 1},
	} {
		if _, err := NewCorpusBuild(&config.Config{}, selection); !errors.Is(err, domain.ErrUsage) {
			t.Fatalf("selection=%+v error=%v", selection, err)
		}
	}
}
