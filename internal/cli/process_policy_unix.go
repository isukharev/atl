//go:build !windows

package cli

import "os"

func processPolicyOwnerUID() *uint32 {
	uid := uint32(os.Geteuid())
	return &uid
}
