package cli

import (
	"strings"
	"testing"
)

// TestBodyFromTTYRefused pins issue #75's fix: commands whose --from-file
// defaults to stdin must not hang forever when stdin is an interactive
// terminal — they exit 2 immediately with a message naming the remedy.
func TestBodyFromTTYRefused(t *testing.T) {
	prev := stdinIsTerminal
	stdinIsTerminal = func() bool { return true }
	t.Cleanup(func() { stdinIsTerminal = prev })

	cs := newConfServer(t)
	for _, args := range [][]string{
		{"conf", "page", "create", "--space", "K", "--title", "T"},
		{"conf", "comment", "add", "--id", "1"},
		{"jira", "issue", "comment", "add", "PROJ-1"},
	} {
		out, code := runCLI(t, confEnv(cs.srv), args...)
		if code != exitUsage {
			t.Errorf("%v on a TTY: exit %d, want %d (stdout=%q)", args, code, exitUsage, out)
		}
	}
	if reqs := cs.requests(); len(reqs) != 0 {
		t.Fatalf("expected zero requests, got %d: %+v", len(reqs), reqs)
	}
}

// TestBodyFromPipeStillWorks: the guard must not trip on piped stdin.
func TestBodyFromPipeStillWorks(t *testing.T) {
	prev := stdinIsTerminal
	stdinIsTerminal = func() bool { return false }
	t.Cleanup(func() { stdinIsTerminal = prev })

	cs := newConfServer(t)
	cs.writes = []cannedResp{{status: 200, body: pageJSON("55", "Piped", 1, "<p>x</p>")}}
	withStdin(t, "<p>x</p>", func() {
		out, code := runCLI(t, confEnv(cs.srv),
			"conf", "page", "create", "--space", "K", "--title", "Piped", "--from-file", "-")
		if code != exitOK {
			t.Fatalf("piped create: exit %d (stdout=%q)", code, out)
		}
		if !strings.Contains(out, `"id": "55"`) {
			t.Errorf("unexpected output %q", out)
		}
	})
}
