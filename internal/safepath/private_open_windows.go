//go:build windows

package safepath

import "os"

func openPrivateFile(root *os.Root, name string) (*os.File, error) {
	return root.Open(name)
}
