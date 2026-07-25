package cli

import (
	"strings"
	"testing"
)

func TestConfPageOutlineAndSectionContracts(t *testing.T) {
	cs := newConfServer(t)
	cs.page = `{"id":"42","type":"page","title":"Example","space":{"key":"ENG"},"version":{"number":3},"ancestors":[],"body":{"storage":{"value":"<h1>Overview</h1><p>Intro</p><h2>Details</h2><p>First</p><h2>Details</h2><p>Second</p><h1>Appendix</h1>"}}}`

	outline, code := runCLI(t, confEnv(cs.srv), "conf", "page", "outline", "42")
	if code != exitOK {
		t.Fatalf("outline exit=%d output=%s", code, outline)
	}
	assertGolden(t, "conf_page_outline.json", []byte(outline))

	_, code = runCLI(t, confEnv(cs.srv), "conf", "page", "section", "42", "--heading", "Details")
	if code != exitCheckFailed {
		t.Fatalf("ambiguous section exit=%d", code)
	}
	section, code := runCLI(t, confEnv(cs.srv), "-o", "text", "conf", "page", "section", "42", "--heading", "Details", "--occurrence", "2")
	sectionCorrect := code == exitOK && strings.Contains(section, "## Details") && strings.Contains(section, "Second") && !strings.Contains(section, "Appendix")
	if !sectionCorrect {
		t.Fatalf("section exit=%d output=%q", code, section)
	}
	evaluateAgentWorkflow(t, "confluence-section-recovery.v1.json", deterministicObservation(
		"confluence.section-recovery", 3, int64(len(outline)+len(section)), cs.requests(),
		map[string]bool{
			"ambiguity_fail_closed":    true,
			"outline_present":          strings.Contains(outline, `"headings"`),
			"selected_section_correct": sectionCorrect,
		},
	))

	truncated, code := runCLI(t, confEnv(cs.srv), "conf", "page", "section", "42", "--heading", "Overview", "--max-bytes", "40")
	if code != exitOK || !strings.Contains(truncated, `"complete": false`) || !strings.Contains(truncated, `"truncated": true`) {
		t.Fatalf("truncated exit=%d output=%s", code, truncated)
	}
	// A partial section names its limiter in JSON so a client can tell the
	// recoverable byte bound from a terminal partial read without parsing prose.
	if !strings.Contains(truncated, `"partial_reason": "max_bytes"`) {
		t.Fatalf("truncated JSON must carry the machine-readable reason: %s", truncated)
	}
	// The complete read stays silent about partial_reason on both surfaces.
	if complete, code := runCLI(t, confEnv(cs.srv), "conf", "page", "section", "42", "--heading", "Overview"); code != exitOK ||
		!strings.Contains(complete, `"complete": true`) || strings.Contains(complete, "partial_reason") {
		t.Fatalf("complete section exit=%d output=%s", code, complete)
	}
	// Text output is unchanged: the rendered Markdown plus the existing marker.
	truncatedText, code := runCLI(t, confEnv(cs.srv), "-o", "text", "conf", "page", "section", "42", "--heading", "Overview", "--max-bytes", "40")
	if code != exitOK || !strings.Contains(truncatedText, "# Overview") ||
		!strings.Contains(truncatedText, "[... truncated by atl ...]") || strings.Contains(truncatedText, "partial_reason") {
		t.Fatalf("truncated text exit=%d output=%q", code, truncatedText)
	}
	outlinePartial, code := runCLI(t, confEnv(cs.srv), "conf", "page", "outline", "42")
	if code != exitOK || strings.Contains(outlinePartial, "partial_reason") {
		t.Fatalf("complete outline must omit partial_reason: exit=%d output=%s", code, outlinePartial)
	}
}

// TestConfPageSectionExpectedVersionGate pins the optional CLI gate: the flag is
// opt-in so every existing invocation keeps working, and when it is set the
// existing sentinel mapping supplies the exit codes without a new one.
func TestConfPageSectionExpectedVersionGate(t *testing.T) {
	cs := newConfServer(t)
	cs.page = `{"id":"42","type":"page","title":"Example","space":{"key":"ENG"},"version":{"number":3},"ancestors":[],"body":{"storage":{"value":"<h1>Overview</h1><p>Intro</p><h2>Details</h2><p>First</p>"}}}`

	ungated, code := runCLI(t, confEnv(cs.srv), "conf", "page", "section", "42", "--heading", "Overview")
	if code != exitOK || !strings.Contains(ungated, `"page_version_gated": false`) {
		t.Fatalf("ungated section exit=%d output=%s", code, ungated)
	}
	assertGolden(t, "conf_page_section_ungated.json", []byte(ungated))
	gated, code := runCLI(t, confEnv(cs.srv), "conf", "page", "section", "42", "--heading", "Overview", "--expected-version", "3")
	if code != exitOK || !strings.Contains(gated, `"page_version_gated": true`) ||
		!strings.Contains(gated, `"version": 3`) || !strings.Contains(gated, `"schema_version": 1`) {
		t.Fatalf("gated section exit=%d output=%s", code, gated)
	}
	assertGolden(t, "conf_page_section_gated.json", []byte(gated))
	if _, code := runCLI(t, confEnv(cs.srv), "conf", "page", "section", "42", "--heading", "Overview", "--expected-version", "4"); code != exitCheckFailed {
		t.Fatalf("stale gate exit=%d want %d", code, exitCheckFailed)
	}
	if _, code := runCLI(t, confEnv(cs.srv), "conf", "page", "section", "42", "--heading", "Overview", "--expected-version", "-1"); code != exitUsage {
		t.Fatalf("negative gate exit=%d want %d", code, exitUsage)
	}
}
