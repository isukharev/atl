//go:build !linux

package brokerjournal

// Darwin durability/locking qualification is required before supported Broker
// journal enablement. The storage foundation never silently weakens its gates.
func openDisk(_ string, _ bool) (disk, error) { return nil, errUnavailable }
