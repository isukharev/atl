package compose

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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
