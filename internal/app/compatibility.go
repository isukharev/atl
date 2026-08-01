package app

import (
	"context"
	"errors"

	"github.com/isukharev/atl/internal/compatibility"
	"github.com/isukharev/atl/internal/domain"
)

const compatibilityStatusSchemaVersion = 1

const (
	CompatibilityStatusDisabled    = "disabled"
	CompatibilityStatusConfigured  = "configured"
	CompatibilityStatusUnsupported = "unsupported"
	CompatibilityStatusUnavailable = "unavailable"
	CompatibilityStatusMismatch    = "mismatch"
	CompatibilityStatusMatched     = "matched"
)

type CompatibilityIdentity struct {
	Version     string `json:"version"`
	BuildNumber string `json:"build_number"`
}

type CompatibilityStatusResult struct {
	SchemaVersion   int                    `json:"schema_version"`
	Service         string                 `json:"service"`
	RemoteRequested bool                   `json:"remote_requested"`
	Status          string                 `json:"status"`
	Reason          string                 `json:"reason"`
	ProviderID      string                 `json:"provider_id,omitempty"`
	ProviderFamily  string                 `json:"provider_family,omitempty"`
	Configured      *CompatibilityIdentity `json:"configured,omitempty"`
	Observed        *CompatibilityIdentity `json:"observed,omitempty"`
	Qualified       bool                   `json:"qualified"`
}

// CompatibilityService qualifies one compile-time provider descriptor against
// owner-controlled settings and, optionally, one exact backend identity read.
// The ordinary product compatibility decision is intentionally independent.
type CompatibilityService struct {
	settings          compatibility.Settings
	confluenceFactory func() (domain.ExactServerMetadataReader, string)
	selectDescriptor  func(compatibility.Product, string) (compatibility.Descriptor, bool)
}

func (s *CompatibilityService) Status(ctx context.Context, remote bool) CompatibilityStatusResult {
	result := CompatibilityStatusResult{
		SchemaVersion: compatibilityStatusSchemaVersion,
		Service:       compatibility.ProductConfluence.String(), RemoteRequested: remote,
		Status: CompatibilityStatusDisabled, Reason: "not_configured",
	}
	if s == nil || s.settings.Confluence == nil {
		return result
	}
	activation := *s.settings.Confluence
	pin := activation.Pin()
	result.Configured = compatibilityIdentity(pin)
	selector := s.selectDescriptor
	if selector == nil {
		selector = compatibility.Select
	}
	descriptor, supported := selector(compatibility.ProductConfluence, activation.ProviderID)
	if !supported {
		result.Status = CompatibilityStatusUnsupported
		result.Reason = "unsupported_pin"
		return result
	}
	result.ProviderID = descriptor.ID
	result.ProviderFamily = descriptor.Family
	result.Status = CompatibilityStatusConfigured
	result.Reason = "remote_not_requested"
	if !remote {
		return result
	}
	if s.confluenceFactory == nil {
		result.Status = CompatibilityStatusUnavailable
		result.Reason = "not_configured"
		return result
	}
	reader, setup := s.confluenceFactory()
	if reader == nil {
		result.Status = CompatibilityStatusUnavailable
		result.Reason = setup
		if result.Reason == "" {
			result.Reason = "setup_unavailable"
		}
		return result
	}
	probeCtx := domain.WithRedactedHTTPTrace(domain.WithSingleAttempt(ctx))
	metadata, err := reader.ExactServerMetadata(probeCtx)
	if err != nil {
		result.Status = CompatibilityStatusUnavailable
		result.Reason = compatibilityReadFailure(err)
		return result
	}
	observedVersion, versionErr := compatibility.ParseVersion(metadata.Version)
	observedBuild, buildErr := compatibility.ParseBuildNumber(metadata.BuildNumber)
	if metadata.Product != domain.ServerProductConfluence || versionErr != nil || buildErr != nil {
		result.Status = CompatibilityStatusUnavailable
		result.Reason = "identity_unqualified"
		return result
	}
	observed := compatibility.Pin{Version: observedVersion, BuildNumber: observedBuild}
	result.Observed = compatibilityIdentity(observed)
	if observed.Version != pin.Version {
		result.Status = CompatibilityStatusMismatch
		result.Reason = "version_mismatch"
		return result
	}
	if observed.BuildNumber != pin.BuildNumber {
		result.Status = CompatibilityStatusMismatch
		result.Reason = "build_mismatch"
		return result
	}
	result.Status = CompatibilityStatusMatched
	result.Reason = "exact_match"
	result.Qualified = true
	return result
}

func compatibilityIdentity(pin compatibility.Pin) *CompatibilityIdentity {
	return &CompatibilityIdentity{Version: string(pin.Version), BuildNumber: string(pin.BuildNumber)}
}

func compatibilityReadFailure(err error) string {
	switch {
	case errors.Is(err, domain.ErrAuth):
		return "authentication_failed"
	case errors.Is(err, domain.ErrForbidden):
		return "forbidden"
	case errors.Is(err, domain.ErrNotFound):
		return "identity_endpoint_unavailable"
	case errors.Is(err, domain.ErrCheckFailed):
		return "identity_unqualified"
	default:
		return "request_failed"
	}
}

func CompatibilityStatusText(result CompatibilityStatusResult) string {
	text := "Confluence compatibility provider: " + result.Status + " (" + result.Reason + ")"
	if result.ProviderID != "" {
		text += "; provider: " + result.ProviderID
	}
	if result.Qualified {
		text += "; exact identity matched"
	}
	return text + "."
}
