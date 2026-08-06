package agenteval

import "testing"

func mustPinnedCapabilityCatalog(t testing.TB) CapabilityCatalog {
	t.Helper()
	catalog, err := PinnedCapabilityCatalog()
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}
