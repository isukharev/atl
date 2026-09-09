package app

import (
	"context"

	"github.com/isukharev/atl/internal/domain"
)

const maxRemoteLinkDiagnosticErrorNodes = 32

type remoteLinkHTTPStatus interface{ HTTPStatus() int }

type remoteLinkTransportFailure interface{ DiagnosticTransportFailure() bool }

type remoteLinkErrorUnwrapOne interface{ Unwrap() error }

type remoteLinkErrorUnwrapMany interface{ Unwrap() []error }

type remoteLinkErrorEvidence struct {
	status              int
	statusCount         int
	transportCount      int
	invalidTransport    bool
	authenticationCount int
	permissionCount     int
	notFoundCount       int
	hardExcluded        bool
	knownNonRequest     bool
}

// remoteLinkFailureDiagnostic inspects only structural error evidence. The
// walk is bounded and never calls Error or unwraps httpx.TransportError's
// private cause. Ambiguous or contradictory evidence yields no diagnostic.
func remoteLinkFailureDiagnostic(err error) *domain.ArtifactGraphSourceFailure {
	evidence, ok := collectRemoteLinkErrorEvidence(err)
	if !ok || evidence.hardExcluded || evidence.statusCount > 1 || evidence.transportCount > 1 || evidence.invalidTransport {
		return nil
	}
	semanticCount := evidence.authenticationCount + evidence.permissionCount + evidence.notFoundCount
	if evidence.authenticationCount > 1 || evidence.permissionCount > 1 || evidence.notFoundCount > 1 || semanticCount > 1 {
		return nil
	}
	if evidence.statusCount == 1 {
		if evidence.transportCount != 0 || evidence.status < 300 || evidence.status > 599 {
			return nil
		}
		status := evidence.status
		switch status {
		case 401:
			if evidence.permissionCount != 0 || evidence.notFoundCount != 0 || evidence.knownNonRequest {
				return nil
			}
			return remoteLinkFailure(domain.ArtifactSourceFailureAuthentication, status)
		case 403:
			if evidence.authenticationCount != 0 || evidence.notFoundCount != 0 || evidence.knownNonRequest {
				return nil
			}
			return remoteLinkFailure(domain.ArtifactSourceFailurePermission, status)
		case 404:
			if evidence.authenticationCount != 0 || evidence.permissionCount != 0 || evidence.knownNonRequest {
				return nil
			}
			return remoteLinkFailure(domain.ArtifactSourceFailureNotFound, status)
		default:
			if semanticCount != 0 {
				return nil
			}
			return remoteLinkFailure(domain.ArtifactSourceFailureHTTP, status)
		}
	}
	if evidence.transportCount == 1 {
		if semanticCount != 0 || evidence.knownNonRequest {
			return nil
		}
		return remoteLinkFailure(domain.ArtifactSourceFailureTransport, 0)
	}
	if evidence.knownNonRequest {
		return nil
	}
	switch {
	case evidence.authenticationCount == 1:
		return remoteLinkFailure(domain.ArtifactSourceFailureAuthentication, 0)
	case evidence.permissionCount == 1:
		return remoteLinkFailure(domain.ArtifactSourceFailurePermission, 0)
	case evidence.notFoundCount == 1:
		return remoteLinkFailure(domain.ArtifactSourceFailureNotFound, 0)
	default:
		return remoteLinkFailure(domain.ArtifactSourceFailureRequest, 0)
	}
}

func collectRemoteLinkErrorEvidence(err error) (remoteLinkErrorEvidence, bool) {
	if err == nil {
		return remoteLinkErrorEvidence{}, false
	}
	evidence := remoteLinkErrorEvidence{}
	pending := []error{err}
	visited := 0
	for len(pending) != 0 {
		if visited >= maxRemoteLinkDiagnosticErrorNodes {
			return remoteLinkErrorEvidence{}, false
		}
		current := pending[0]
		pending = pending[1:]
		if current == nil {
			continue
		}
		visited++

		if status, ok := current.(remoteLinkHTTPStatus); ok {
			evidence.statusCount++
			evidence.status = status.HTTPStatus()
		}
		if marker, ok := current.(remoteLinkTransportFailure); ok {
			if marker.DiagnosticTransportFailure() {
				evidence.transportCount++
			} else {
				evidence.invalidTransport = true
			}
		}
		switch current {
		case domain.ErrAuth:
			evidence.authenticationCount++
		case domain.ErrForbidden:
			evidence.permissionCount++
		case domain.ErrNotFound:
			evidence.notFoundCount++
		case context.Canceled, context.DeadlineExceeded, domain.ErrOutputLimit, domain.ErrConfig,
			domain.ErrReadAttemptBudgetExhausted, domain.ErrReadResponseBudgetExhausted:
			evidence.hardExcluded = true
		case domain.ErrVersionConflict, domain.ErrUsage, domain.ErrCheckFailed:
			evidence.knownNonRequest = true
		}

		one, hasOne := current.(remoteLinkErrorUnwrapOne)
		many, hasMany := current.(remoteLinkErrorUnwrapMany)
		if hasOne && hasMany {
			return remoteLinkErrorEvidence{}, false
		}
		if hasMany {
			children := many.Unwrap()
			if len(children) > maxRemoteLinkDiagnosticErrorNodes-visited-len(pending) {
				return remoteLinkErrorEvidence{}, false
			}
			pending = append(pending, children...)
		} else if hasOne {
			if child := one.Unwrap(); child != nil {
				if len(pending) >= maxRemoteLinkDiagnosticErrorNodes-visited {
					return remoteLinkErrorEvidence{}, false
				}
				pending = append(pending, child)
			}
		}
	}
	return evidence, true
}

func remoteLinkFailure(class domain.ArtifactGraphSourceFailureClass, status int) *domain.ArtifactGraphSourceFailure {
	failure := &domain.ArtifactGraphSourceFailure{Class: class}
	if status != 0 {
		failure.HTTPStatus = &status
	}
	return failure
}

func validJiraGraphRemoteLinkFailure(source domain.ArtifactGraphSource) bool {
	failure := source.Failure
	if failure == nil {
		return true
	}
	if source.Kind != "remote_links" || !source.Requested || source.Complete || source.Count != 0 || source.Truncated {
		return false
	}
	switch failure.Class {
	case domain.ArtifactSourceFailureAuthentication:
		return source.Status == domain.ArtifactSourceForbidden && source.PartialReason == "" &&
			(failure.HTTPStatus == nil || *failure.HTTPStatus == 401)
	case domain.ArtifactSourceFailurePermission:
		return source.Status == domain.ArtifactSourceForbidden && source.PartialReason == "" &&
			(failure.HTTPStatus == nil || *failure.HTTPStatus == 403)
	case domain.ArtifactSourceFailureNotFound:
		return source.Status == domain.ArtifactSourceUnsupported && source.PartialReason == "" &&
			(failure.HTTPStatus == nil || *failure.HTTPStatus == 404)
	case domain.ArtifactSourceFailureHTTP:
		return failure.HTTPStatus != nil && *failure.HTTPStatus >= 300 && *failure.HTTPStatus <= 599 &&
			*failure.HTTPStatus != 401 && *failure.HTTPStatus != 403 && *failure.HTTPStatus != 404 &&
			source.Status == domain.ArtifactSourcePartial &&
			(source.PartialReason == domain.ArtifactPartialRequestFailed || source.PartialReason == domain.ArtifactPartialMalformed)
	case domain.ArtifactSourceFailureTransport, domain.ArtifactSourceFailureRequest:
		return failure.HTTPStatus == nil && source.Status == domain.ArtifactSourcePartial && source.PartialReason == domain.ArtifactPartialRequestFailed
	default:
		return false
	}
}
