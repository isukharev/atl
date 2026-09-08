package domain

import "context"

// BrokerDiscoveryReader reads current advisory operation access facts.
// A projection never authorizes a later invocation.
type BrokerDiscoveryReader interface {
	Discover(context.Context, string) (BrokerDiscoveryProjectionV2, error)
}
