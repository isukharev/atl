package cli

import (
	"runtime"
	"testing"
)

func TestBrowserCmd(t *testing.T) {
	url := "https://example.com/page"
	cmd, args := browserCmd(url)
	switch runtime.GOOS {
	case "windows":
		if cmd != "rundll32" {
			t.Errorf("windows: want rundll32, got %q", cmd)
		}
		if len(args) != 2 || args[0] != "url.dll,FileProtocolHandler" || args[1] != url {
			t.Errorf("windows: wrong args %v", args)
		}
	case "darwin":
		if cmd != "open" || len(args) != 1 || args[0] != url {
			t.Errorf("darwin: want [open %s], got [%s %v]", url, cmd, args)
		}
	default:
		if cmd != "xdg-open" || len(args) != 1 || args[0] != url {
			t.Errorf("linux: want [xdg-open %s], got [%s %v]", url, cmd, args)
		}
	}
}
