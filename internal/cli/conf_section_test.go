package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/app"
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
	requests := cs.requests()
	if len(requests) != 3 || len(outline)+len(section) > 4096 {
		t.Fatalf("recovery budget requests=%d output_bytes=%d", len(requests), len(outline)+len(section))
	}
	for _, request := range requests {
		if request.method != "GET" {
			t.Fatalf("recovery used non-read method %q", request.method)
		}
	}
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

func TestConfPageSectionsPreservesOrderAndAggregateContract(t *testing.T) {
	cs := newConfServer(t)
	cs.page = `{"id":"42","type":"page","title":"Example","space":{"key":"ENG"},"version":{"number":3},"ancestors":[],"body":{"storage":{"value":"<h1>Alpha</h1><h2>Status</h2><p>First</p><h1>Beta</h1><h2>Status</h2><p>Second</p><h1>Tail</h1><p>Last</p>"}}}`

	out, code := runCLI(t, confEnv(cs.srv), "conf", "page", "sections", "42",
		"--heading", "Status", "--heading", "Tail", "--heading", "Status",
		"--occurrence", "2", "--occurrence", "0", "--occurrence", "1",
		"--expected-version", "3")
	if code != exitOK {
		t.Fatalf("sections exit=%d output=%s", code, out)
	}
	var result app.ConfluencePageSectionsResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if result.RequestedCount != 3 || result.ReturnedCount != 3 || !result.Reconciled ||
		!result.Complete || result.Truncated || !result.PageVersionGated || result.Version != 3 {
		t.Fatalf("result=%+v", result)
	}
	if len(result.Sections) != 3 || result.Sections[0].Occurrence != 2 || result.Sections[0].Path[0] != "Beta" ||
		result.Sections[1].Heading != "Tail" || result.Sections[2].Occurrence != 1 || result.EmittedBytes > result.MaxBytes {
		t.Fatalf("ordered sections=%+v", result.Sections)
	}
	assertGolden(t, "conf_page_sections_gated.json", []byte(out))

	text, code := runCLI(t, confEnv(cs.srv), "-o", "text", "conf", "page", "sections", "42",
		"--heading", "Status", "--heading", "Tail", "--heading", "Status",
		"--occurrence", "2", "--occurrence", "0", "--occurrence", "1")
	second, last, first := strings.Index(text, "Second"), strings.Index(text, "Last"), strings.LastIndex(text, "First")
	if code != exitOK || second < 0 || last < 0 || first < 0 || second >= last || last >= first {
		t.Fatalf("text order lost: exit=%d output=%q", code, text)
	}
}

func TestConfPageSectionsRejectsMismatchedSelectorsBeforeBackend(t *testing.T) {
	cs := newConfServer(t)
	for _, args := range [][]string{
		{"conf", "page", "sections", "42"},
		{"conf", "page", "sections", "42", "--heading", "A", "--heading", "B", "--occurrence", "1"},
		{"conf", "page", "sections", "42", "--heading", "A", "--occurrence", "not-a-number"},
		{"conf", "page", "sections", "42", "--heading", "A", "--occurrence", "-1"},
	} {
		before := len(cs.requests())
		if out, code := runCLI(t, confEnv(cs.srv), args...); code != exitUsage {
			t.Fatalf("args=%v exit=%d output=%s", args, code, out)
		}
		if got := len(cs.requests()); got != before {
			t.Fatalf("args=%v backend requests=%d want %d", args, got, before)
		}
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
