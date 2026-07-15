package main

import (
	"strings"
	"testing"
)

func TestRunRejectsMissingAndUnknownCommands(t *testing.T) {
	for _, args := range [][]string{nil, {"unknown"}, {"evaluate"}, {"aggregate"}} {
		if err := run(args); err == nil {
			t.Fatalf("run(%v) succeeded", args)
		}
	}
	if err := run([]string{"validate", "does-not-exist.json"}); err == nil || !strings.Contains(err.Error(), "does-not-exist") {
		t.Fatalf("err=%v", err)
	}
}
