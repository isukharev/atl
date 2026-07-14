package scripts

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestLiveSmokeUsesCurrentGuardedAndRenderContracts(t *testing.T) {
	body, err := os.ReadFile("live-smoke.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(body)
	for _, want := range []string{
		"version,op,source,target,type,rationale,expected_updated",
		".fields.updated",
		"protected color span missing from markdown",
		`<span style="color:`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("live smoke is missing current contract %q", want)
		}
	}
	if regexp.MustCompile(`structure_export_args=.*--limit`).MatchString(script) {
		t.Fatal("Structure export still uses removed --limit flag")
	}
	if strings.Contains(script, `\u27e6color:`) {
		t.Fatal("live smoke still expects legacy color markers")
	}
}
