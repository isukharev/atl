//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris)

package corpus

import (
	"errors"
	"testing"
)

func TestDurableStoreIsExplicitlyUnsupported(t *testing.T) {
	if _, err := Initialize(t.TempDir(), Options{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("initialize error = %v", err)
	}
}
