package app

import (
	"context"
	"errors"
	"testing"

	"github.com/isukharev/atl/internal/compatibility"
	"github.com/isukharev/atl/internal/domain"
)

type compatibilityMetadataReaderStub struct {
	metadata      domain.ServerMetadata
	err           error
	calls         int
	singleAttempt bool
	redactedTrace bool
}

func (s *compatibilityMetadataReaderStub) ExactServerMetadata(ctx context.Context) (domain.ServerMetadata, error) {
	s.calls++
	s.singleAttempt = domain.SingleAttempt(ctx)
	s.redactedTrace = domain.RedactedHTTPTrace(ctx)
	return s.metadata, s.err
}

func supportedCompatibilitySettings(t *testing.T) compatibility.Settings {
	t.Helper()
	version, err := compatibility.ParseVersion("9.5.2")
	if err != nil {
		t.Fatal(err)
	}
	build, err := compatibility.ParseBuildNumber("12345")
	if err != nil {
		t.Fatal(err)
	}
	return compatibility.Settings{SchemaVersion: compatibility.SettingsSchemaVersion, Confluence: &compatibility.Activation{
		ProviderID: "synthetic-provider", Version: version, BuildNumber: build,
	}}
}

func syntheticCompatibilitySelector(product compatibility.Product, providerID string) (compatibility.Descriptor, bool) {
	if product != compatibility.ProductConfluence || providerID != "synthetic-provider" {
		return compatibility.Descriptor{}, false
	}
	return compatibility.Descriptor{ID: "synthetic-provider", Product: product, Family: "synthetic-family"}, true
}

func TestCompatibilityStatusDisabledUsesNoFactory(t *testing.T) {
	factoryCalls := 0
	service := &CompatibilityService{
		settings: compatibility.DefaultSettings(),
		confluenceFactory: func() (domain.ExactServerMetadataReader, string) {
			factoryCalls++
			return nil, "unexpected"
		},
	}
	result := service.Status(context.Background(), true)
	if result.Status != CompatibilityStatusDisabled || result.Qualified || factoryCalls != 0 {
		t.Fatalf("result=%+v factoryCalls=%d", result, factoryCalls)
	}
}

func TestCompatibilityStatusOfflineDoesNotReadRemote(t *testing.T) {
	reader := &compatibilityMetadataReaderStub{}
	service := &CompatibilityService{
		settings:          supportedCompatibilitySettings(t),
		confluenceFactory: func() (domain.ExactServerMetadataReader, string) { return reader, "" },
		selectDescriptor:  syntheticCompatibilitySelector,
	}
	result := service.Status(context.Background(), false)
	if result.Status != CompatibilityStatusConfigured || result.ProviderID == "" || result.Qualified || reader.calls != 0 {
		t.Fatalf("result=%+v calls=%d", result, reader.calls)
	}
}

func TestCompatibilityStatusRemoteExactMatch(t *testing.T) {
	reader := &compatibilityMetadataReaderStub{metadata: domain.ServerMetadata{
		Product: domain.ServerProductConfluence, Version: "9.5.2", BuildNumber: "12345",
	}}
	service := &CompatibilityService{
		settings:          supportedCompatibilitySettings(t),
		confluenceFactory: func() (domain.ExactServerMetadataReader, string) { return reader, "" },
		selectDescriptor:  syntheticCompatibilitySelector,
	}
	result := service.Status(context.Background(), true)
	if result.Status != CompatibilityStatusMatched || !result.Qualified || result.Reason != "exact_match" || reader.calls != 1 {
		t.Fatalf("result=%+v calls=%d", result, reader.calls)
	}
	if !reader.singleAttempt || !reader.redactedTrace {
		t.Fatalf("probe context single_attempt=%t redacted_trace=%t", reader.singleAttempt, reader.redactedTrace)
	}
}

func TestCompatibilityStatusProductionProfileUsesOwnerExactPin(t *testing.T) {
	settings := supportedCompatibilitySettings(t)
	settings.Confluence.ProviderID = compatibility.ConfluenceInlineCommentsDCProfileID
	reader := &compatibilityMetadataReaderStub{metadata: domain.ServerMetadata{
		Product: domain.ServerProductConfluence, Version: "9.5.2", BuildNumber: "12345",
	}}
	service := &CompatibilityService{
		settings: settings,
		confluenceFactory: func() (domain.ExactServerMetadataReader, string) {
			return reader, ""
		},
	}
	result := service.Status(context.Background(), true)
	if result.Status != CompatibilityStatusMatched || !result.Qualified || result.ProviderID != compatibility.ConfluenceInlineCommentsDCProfileID {
		t.Fatalf("result=%+v", result)
	}

	reader.metadata.BuildNumber = "12344"
	result = service.Status(context.Background(), true)
	if result.Status != CompatibilityStatusMismatch || result.Reason != "build_mismatch" || result.Qualified {
		t.Fatalf("mismatch result=%+v", result)
	}
}

func TestCompatibilityStatusRemoteMismatchAndUnavailabilityAreClosed(t *testing.T) {
	for name, testCase := range map[string]struct {
		reader     *compatibilityMetadataReaderStub
		wantStatus string
		wantReason string
	}{
		"version":   {&compatibilityMetadataReaderStub{metadata: domain.ServerMetadata{Product: domain.ServerProductConfluence, Version: "9.5.1", BuildNumber: "12345"}}, CompatibilityStatusMismatch, "version_mismatch"},
		"build":     {&compatibilityMetadataReaderStub{metadata: domain.ServerMetadata{Product: domain.ServerProductConfluence, Version: "9.5.2", BuildNumber: "12344"}}, CompatibilityStatusMismatch, "build_mismatch"},
		"malformed": {&compatibilityMetadataReaderStub{metadata: domain.ServerMetadata{Product: domain.ServerProductConfluence, Version: "private-version", BuildNumber: "private-build"}}, CompatibilityStatusUnavailable, "identity_unqualified"},
		"auth":      {&compatibilityMetadataReaderStub{err: domain.ErrAuth}, CompatibilityStatusUnavailable, "authentication_failed"},
		"unknown":   {&compatibilityMetadataReaderStub{err: errors.New("private backend response canary")}, CompatibilityStatusUnavailable, "request_failed"},
	} {
		t.Run(name, func(t *testing.T) {
			service := &CompatibilityService{
				settings:          supportedCompatibilitySettings(t),
				confluenceFactory: func() (domain.ExactServerMetadataReader, string) { return testCase.reader, "" },
				selectDescriptor:  syntheticCompatibilitySelector,
			}
			result := service.Status(context.Background(), true)
			if result.Status != testCase.wantStatus || result.Reason != testCase.wantReason || result.Qualified {
				t.Fatalf("result=%+v want status=%s reason=%s", result, testCase.wantStatus, testCase.wantReason)
			}
		})
	}
}
