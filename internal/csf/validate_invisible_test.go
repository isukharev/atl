package csf

import (
	"strings"
	"testing"
)

func problemsByRule(ps []Problem, rule string) []Problem {
	var out []Problem
	for _, p := range ps {
		if p.Rule == rule {
			out = append(out, p)
		}
	}
	return out
}

func TestValidateWarnsOnInvisibleChars(t *testing.T) {
	// Two NBSPs (first at line 1 col 8), one ZWSP; all inside valid XML.
	raw := []byte("<p>slug one and​two</p>")
	ps := Validate(raw)
	if HasErrors(ps) {
		t.Fatalf("unexpected errors: %+v", ps)
	}
	inv := problemsByRule(ps, "invisible-chars")
	if len(inv) != 2 {
		t.Fatalf("want 2 invisible-chars warnings (NBSP class + zero-width class), got %+v", inv)
	}
	nbsp := inv[0]
	if !strings.Contains(nbsp.Message, "2×") || !strings.Contains(nbsp.Message, "U+00A0") {
		t.Errorf("nbsp warning = %+v", nbsp)
	}
	if nbsp.Line != 1 || nbsp.Col != 8 {
		t.Errorf("first NBSP position = %d:%d, want 1:8", nbsp.Line, nbsp.Col)
	}
	if nbsp.Severity != "warning" {
		t.Errorf("severity = %s", nbsp.Severity)
	}
	if !strings.Contains(inv[1].Message, "zero-width") {
		t.Errorf("second warning = %+v", inv[1])
	}
}

func TestValidateCleanFileHasNoInvisibleWarnings(t *testing.T) {
	ps := Validate([]byte("<p>plain ascii and кириллица</p>"))
	if inv := problemsByRule(ps, "invisible-chars"); len(inv) != 0 {
		t.Fatalf("unexpected warnings: %+v", inv)
	}
}

// Multiline: position counting must track newlines.
func TestInvisibleWarningPositionMultiline(t *testing.T) {
	raw := []byte("<p>line one</p>\n<p>x y</p>")
	inv := problemsByRule(Validate(raw), "invisible-chars")
	if len(inv) != 1 || inv[0].Line != 2 || inv[0].Col != 5 {
		t.Fatalf("position = %+v, want line 2 col 5", inv)
	}
}
