package csf

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
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

func TestValidateRejectsProlog(t *testing.T) {
	// A leading <?xml ...?> or <!DOCTYPE ...> is accepted by encoding/xml once
	// the body is wrapped in <root> (it lands out of prolog position), but the
	// server rejects these in storage-format body content. Validate must too.
	cases := []struct {
		name string
		body string
	}{
		{"xml decl", `<?xml version="1.0"?><p>hi</p>`},
		{"doctype", `<!DOCTYPE html><p>hi</p>`},
		{"mid-body processing instruction", `<p>ok</p><?target data?>`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ps := Validate([]byte(c.body))
			if !HasErrors(ps) {
				t.Fatalf("Validate(%q) HasErrors=false, want true (%+v)", c.body, ps)
			}
		})
	}
	// A normal body without a prolog still passes.
	if ps := Validate([]byte("<p>hi</p>")); HasErrors(ps) {
		t.Fatalf("plain body should pass, got %+v", ps)
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

func TestParseNoWrapperLayer(t *testing.T) {
	// The returned root's Children must be the actual top-level CSF nodes; the
	// synthetic <root> wrapper must not appear as an extra layer.
	root, err := Parse([]byte(`<p>a</p><p>b</p>`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(root.Children) != 2 {
		t.Fatalf("root has %d children, want 2 (%+v)", len(root.Children), root.Children)
	}
	for i, c := range root.Children {
		if c.Type != Element || c.Name.Local != "p" {
			t.Errorf("child %d = %+v, want element <p>", i, c)
		}
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

func TestNestingDepthBoundary(t *testing.T) {
	t.Parallel()

	for _, depth := range []int{MaxNestingDepth - 1, MaxNestingDepth} {
		t.Run(fmt.Sprintf("accepts_%d", depth), func(t *testing.T) {
			raw := nestedCSF(depth)
			original := append([]byte(nil), raw...)

			root, err := Parse(raw)
			if err != nil {
				t.Fatalf("Parse depth %d: %v", depth, err)
			}
			if root == nil {
				t.Fatalf("Parse depth %d returned nil root", depth)
			}
			if !bytes.Equal(raw, original) {
				t.Fatalf("Parse depth %d mutated its input", depth)
			}
			if problems := Validate(raw); HasErrors(problems) {
				t.Fatalf("Validate depth %d returned blocking problems: %+v", depth, problems)
			}
		})
	}

	t.Run("rejects_limit_plus_one", func(t *testing.T) {
		raw := nestedCSF(MaxNestingDepth + 1)
		original := append([]byte(nil), raw...)

		root, err := Parse(raw)
		if root != nil {
			t.Fatal("Parse returned a partial root for over-depth input")
		}
		if !errors.Is(err, ErrMaxNestingDepth) {
			t.Fatalf("Parse error = %v, want ErrMaxNestingDepth", err)
		}
		var depthErr *MaxNestingDepthError
		if !errors.As(err, &depthErr) {
			t.Fatalf("Parse error type = %T, want *MaxNestingDepthError", err)
		}
		if depthErr.Depth != MaxNestingDepth+1 || depthErr.Limit != MaxNestingDepth {
			t.Fatalf("depth error = %+v", depthErr)
		}
		if !bytes.Equal(raw, original) {
			t.Fatal("Parse mutated over-depth input")
		}

		problems := Validate(raw)
		if len(problems) != 1 {
			t.Fatalf("Validate returned %d problems, want 1: %+v", len(problems), problems)
		}
		got := problems[0]
		if got.Severity != "error" || got.Rule != "max-depth" {
			t.Fatalf("Validate problem = %+v, want error/max-depth", got)
		}
		if got.Message != err.Error() {
			t.Fatalf("Validate message = %q, want %q", got.Message, err.Error())
		}
	})
}

func nestedCSF(depth int) []byte {
	return []byte(strings.Repeat("<x>", depth) + strings.Repeat("</x>", depth))
}
