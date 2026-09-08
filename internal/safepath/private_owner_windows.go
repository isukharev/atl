//go:build windows

package safepath

import "os"

// Windows file modes do not prove owner-only ACLs, so the private Broker host
// remains unavailable there while the ordinary ATL CLI continues to build.
func ownedByCurrentUser(os.FileInfo) bool { return false }
