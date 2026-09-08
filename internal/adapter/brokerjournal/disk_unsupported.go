//go:build !linux && !darwin

package brokerjournal

// Unqualified platforms never receive a generic filesystem fallback. The
// storage foundation does not silently weaken its allocation or sync gates.
func openDisk(_ string, _ bool) (disk, error) { return nil, errUnavailable }
