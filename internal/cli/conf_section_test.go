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
}
