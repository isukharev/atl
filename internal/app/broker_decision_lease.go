package app

import "github.com/isukharev/atl/internal/domain"

// brokerLocalDecisionDeadline retains the signed wall expiry without allowing
// clock skew or a delayed authority reply to extend the locally observed lease.
// callStartedMillis must be captured immediately before the authorization I/O.
func brokerLocalDecisionDeadline(core domain.BrokerDecisionCore, callStartedMillis int64) int64 {
	return min(core.ExpiresAtMillis, callStartedMillis+core.ExpiresAtMillis-core.IssuedAtMillis)
}
