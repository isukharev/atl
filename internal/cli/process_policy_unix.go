//go:build !windows

package cli

import (
	"math"
	"os"
)

func processPolicyOwnerUID() *uint32 {
	value := os.Geteuid()
	if value < 0 || uint64(value) > math.MaxUint32 {
		return nil
	}
	uid := uint32(value) // #nosec G115 -- bounded above before conversion.
	return &uid
}
