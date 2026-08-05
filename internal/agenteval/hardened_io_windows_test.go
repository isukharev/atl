//go:build windows

package agenteval

import (
	"errors"
	"testing"
)

func TestHardenedSyncDirectoryWithinWindowsFailsClosedBeforePathResolution(t *testing.T) {
	if err := hardenedSyncDirectoryWithin("", ""); !errors.Is(err, errHardenedUnsafePrivatePath) {
		t.Fatalf("directory sync error=%v", err)
	}
}
