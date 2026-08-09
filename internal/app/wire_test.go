package app

import (
	"testing"

	"github.com/isukharev/atl/internal/compatibility"
	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
)

func TestNewConfluenceServiceProjectsPureDependencies(t *testing.T) {
	cfg := &config.Config{JiraURL: "https://render-config-must-not-supply-jira.example.com"}
	activation := compatibility.Activation{ProviderID: "provider"}
	service := NewConfluenceService(ConfluenceDependencies{
		BaseURL: "https://confluence.example.com", Config: cfg, JiraBaseURL: "https://jira.example.com",
		RequestMaxInFlight: 4, RequestsPerSecond: 10,
		CommentMutationActivation: &activation,
	})
	activation.ProviderID = "changed"
	if service.baseURL != "https://confluence.example.com" || service.cfg != cfg {
		t.Fatalf("constructor lost base/config: %+v", service)
	}
	if service.jiraBaseURL != "https://jira.example.com" {
		t.Fatalf("Jira sibling base URL=%q", service.jiraBaseURL)
	}
	if service.requestMaxInFlight != 4 || service.requestsPerSecond != 10 {
		t.Fatalf("schedule = %d/%d", service.requestMaxInFlight, service.requestsPerSecond)
	}
	if service.commentMutationActivation == nil || service.commentMutationActivation.ProviderID != "provider" {
		t.Fatalf("activation was not defensively copied: %+v", service.commentMutationActivation)
	}
}

func TestNewJiraServiceProjectsPureDependencies(t *testing.T) {
	cfg := &config.Config{ConfluenceURL: "https://render-config-must-not-supply-confluence.example.com"}
	service := NewJiraService(JiraDependencies{
		BaseURL: "https://jira.example.com", Config: cfg, ConfluenceBaseURL: "https://confluence.example.com",
	})
	if service.baseURL != "https://jira.example.com" || service.cfg != cfg {
		t.Fatalf("constructor lost base/config: %+v", service)
	}
	if got := jiraGraphConfluenceBase(service); got != "https://confluence.example.com" {
		t.Fatalf("Confluence sibling base URL=%q", got)
	}
	if _, reason := service.confluenceGraphMetadataReader(); reason != string(DependencyNotConfigured) {
		t.Fatalf("optional Confluence reason=%q", reason)
	}
}

func TestNewJiraServiceKeepsConfluenceGraphFactoryLazyAndAtMostOnce(t *testing.T) {
	calls := 0
	service := NewJiraService(JiraDependencies{
		ConfluenceGraphFactory: func() (domain.ConfluenceGraphPageMetadataReader, string) {
			calls++
			return nil, string(DependencyCredentialsMissing)
		},
	})
	if calls != 0 {
		t.Fatalf("factory called during construction %d times", calls)
	}
	for range 2 {
		if reader, reason := service.confluenceGraphMetadataReader(); reader != nil || reason != string(DependencyCredentialsMissing) {
			t.Fatalf("reader=%v reason=%q", reader, reason)
		}
	}
	if calls != 1 {
		t.Fatalf("factory called %d times, want exactly once", calls)
	}
}

func TestNewEnvironmentServiceKeepsClosedSetupStatuses(t *testing.T) {
	service := NewEnvironmentService(&config.Config{}, EnvironmentDependencies{
		JiraSetup: DependencyCredentialsMissing, ConfluenceSetup: DependencyInvalidConfiguration,
	})
	if service.jiraSetup != "credentials_missing" || service.confluenceSetup != "invalid_configuration" {
		t.Fatalf("setup=%q/%q", service.jiraSetup, service.confluenceSetup)
	}
}

func TestNewCompatibilityServiceKeepsLazySetupStatus(t *testing.T) {
	called := 0
	service := NewCompatibilityService(compatibility.Settings{}, func() (domain.ExactServerMetadataReader, DependencySetupStatus) {
		called++
		return nil, DependencyCredentialsMissing
	})
	_ = service
	if called != 0 {
		t.Fatalf("factory called eagerly %d times", called)
	}
}
