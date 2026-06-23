package cli

import (
	"context"
	"os/exec"
	"runtime"
)

// browserOpener opens a URL in the system browser. Swappable for tests.
type browserOpener func(ctx context.Context, url string) error

var defaultBrowserOpener browserOpener = openInBrowser

func openInBrowser(ctx context.Context, url string) error {
	cmd, args := browserCmd(url)
	return exec.CommandContext(ctx, cmd, args...).Start()
}

// browserCmd returns the OS-specific command + args to open url.
func browserCmd(url string) (string, []string) {
	switch runtime.GOOS {
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", url}
	case "darwin":
		return "open", []string{url}
	default: // linux and others
		return "xdg-open", []string{url}
	}
}
