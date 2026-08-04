package mcpserver

import (
	"fmt"

	"github.com/isukharev/atl/internal/domain"
)

// ServiceProfile names one of the fixed MCP capability surfaces. It is not an
// arbitrary allowlist: server construction accepts only these four values.
type ServiceProfile string

const (
	ServiceDefault    ServiceProfile = "default"
	ServiceJira       ServiceProfile = "jira"
	ServiceConfluence ServiceProfile = "confluence"
	ServiceOffline    ServiceProfile = "offline"
)

// ParseServiceProfile validates an explicitly supplied CLI service value. The
// empty string is invalid: only omitting --service selects the default profile.
func ParseServiceProfile(value string) (ServiceProfile, error) {
	profile := ServiceProfile(value)
	switch profile {
	case ServiceJira, ServiceConfluence, ServiceOffline:
		return profile, nil
	default:
		return "", fmt.Errorf("%w: invalid MCP service %q (want jira|confluence|offline)", domain.ErrUsage, value)
	}
}

func (profile ServiceProfile) valid() bool {
	switch profile {
	case ServiceDefault, ServiceJira, ServiceConfluence, ServiceOffline:
		return true
	default:
		return false
	}
}
