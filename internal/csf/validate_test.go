package csf

import (
	"bytes"
	"testing"
)

func TestValidateWellFormed(t *testing.T) {
	cases := []struct {
		name string
		body string
		ok   bool
	}{
		{"plain", "<p>hi</p>", true},
		{"nested macros", `<ac:structured-macro ac:name="info"><ac:rich-text-body><p>x</p></ac:rich-text-body></ac:structured-macro>`, true},
		{"cdata with angle brackets", `<ac:structured-macro ac:name="code"><ac:plain-text-body><![CDATA[a<b && c>d]]></ac:plain-text-body></ac:structured-macro>`, true},
		{"html entities", `<p>a&nbsp;b&mdash;c</p>`, true},
		{"unclosed tag", `<p>hi`, false},
		{"crossed tags", `<b><i>x</b></i>`, false},
		{"bare ampersand", `<p>a & b</p>`, false}, // not a valid XML entity
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ps := Validate([]byte(c.body))
			if HasErrors(ps) == c.ok {
				t.Fatalf("Validate(%q) HasErrors=%v, want ok=%v (%+v)", c.body, HasErrors(ps), c.ok, ps)
			}
		})
	}
}

func TestValidateLineCol(t *testing.T) {
	body := "<p>line one</p>\n<p>bad <b>x</p>"
	ps := Validate([]byte(body))
	if !HasErrors(ps) {
		t.Fatal("expected error")
	}
	if ps[0].Line != 2 {
		t.Errorf("error line = %d, want 2", ps[0].Line)
	}
}

func TestSanityWarnings(t *testing.T) {
	// drawio without diagramName → warning, but not an error.
	body := `<ac:structured-macro ac:name="drawio"><ac:parameter ac:name="revision">1</ac:parameter></ac:structured-macro>`
	ps := Validate([]byte(body))
	if HasErrors(ps) {
		t.Fatal("should be well-formed (warning only)")
	}
	found := false
	for _, p := range ps {
		if p.Rule == "drawio-params" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected drawio-params warning, got %+v", ps)
	}
}

func TestParseByteStableSource(t *testing.T) {
	// The parser is read-only: it must not mutate the caller's bytes, because
	// the mirror persists the raw body verbatim and pushes it back unchanged.
	raw := []byte(`<p>x</p><ac:structured-macro ac:name="status"><ac:parameter ac:name="title">OK</ac:parameter></ac:structured-macro>`)
	orig := append([]byte(nil), raw...) // snapshot before parsing
	if _, err := Parse(raw); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !bytes.Equal(raw, orig) {
		t.Fatal("Parse mutated its input; the mirror relies on a byte-stable source")
	}
}
