package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

// TestWriteErrorJSON locks the machine-readable error contract: with JSON output
// (the default) a failed command prints a single {"error","code"} object so a
// script can parse stderr the same way it parses stdout.
func TestWriteErrorJSON(t *testing.T) {
	var buf bytes.Buffer
	err := fmt.Errorf("%w: Confluence URL not set", domain.ErrConfig)
	writeError(&buf, "json", err, exitConfig)

	var got struct {
		Error string `json:"error"`
		Code  int    `json:"code"`
	}
	if e := json.Unmarshal(buf.Bytes(), &got); e != nil {
		t.Fatalf("stderr is not valid JSON: %v (raw=%q)", e, buf.String())
	}
	if got.Code != exitConfig {
		t.Errorf("code = %d, want %d", got.Code, exitConfig)
	}
	if !strings.Contains(got.Error, "Confluence URL not set") {
		t.Errorf("error = %q, want it to contain the message", got.Error)
	}
}

// TestWriteErrorText keeps the human-friendly `error: <msg>` line under -o text.
func TestWriteErrorText(t *testing.T) {
	var buf bytes.Buffer
	writeError(&buf, "text", errors.New("boom"), exitGeneric)
	if got := buf.String(); got != "error: boom\n" {
		t.Errorf("text error = %q, want %q", got, "error: boom\n")
	}
}

// TestMirrorRootDefault verifies ATL_MIRROR_ROOT overrides the per-command
// fallback, and the fallback is used when the env var is unset/empty.
func TestMirrorRootDefault(t *testing.T) {
	t.Setenv("ATL_MIRROR_ROOT", "")
	if got := mirrorRootDefault("mirror"); got != "mirror" {
		t.Errorf("unset: got %q, want fallback %q", got, "mirror")
	}
	if got := mirrorRootDefault("mirror-jira"); got != "mirror-jira" {
		t.Errorf("unset jira: got %q, want fallback %q", got, "mirror-jira")
	}

	t.Setenv("ATL_MIRROR_ROOT", "/home/me/.atl/payments")
	if got := mirrorRootDefault("mirror"); got != "/home/me/.atl/payments" {
		t.Errorf("set: got %q, want the env value", got)
	}
	if got := mirrorRootDefault("mirror-jira"); got != "/home/me/.atl/payments" {
		t.Errorf("set jira: got %q, want the env value (one root for the workspace)", got)
	}
}
