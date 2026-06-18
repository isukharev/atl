package csf

import (
	"os"
	"testing"
)

func TestSmokeSamplePage(t *testing.T) {
	raw, err := os.ReadFile("testdata/sample.csf")
	if err != nil {
		t.Fatal(err)
	}
	ps := Validate(raw)
	if HasErrors(ps) {
		t.Errorf("sample.csf: unexpected errors: %+v", ps)
	}
	root, err := Parse(raw)
	if err != nil {
		t.Fatalf("sample.csf parse: %v", err)
	}
	macros := map[string]int{}
	Walk(root, func(n *Node) bool {
		if m := n.MacroName(); m != "" {
			macros[m]++
		}
		return true
	})
	for _, want := range []string{"info", "code", "status", "drawio"} {
		if macros[want] == 0 {
			t.Errorf("expected macro %q in sample, macros=%v", want, macros)
		}
	}
}

func TestMalformed(t *testing.T) {
	bad := []byte("<p>open <b>unclosed</p>")
	ps := Validate(bad)
	if !HasErrors(ps) {
		t.Fatalf("expected error for malformed, got %+v", ps)
	}
	t.Logf("malformed → %+v", ps[0])
}

func TestEntitiesAndCDATA(t *testing.T) {
	body := []byte(`<p>a&nbsp;b &mdash; c</p><ac:structured-macro ac:name="code"><ac:plain-text-body><![CDATA[if a < b && c > d {}]]></ac:plain-text-body></ac:structured-macro>`)
	ps := Validate(body)
	if HasErrors(ps) {
		t.Fatalf("entities/CDATA should be well-formed: %+v", ps)
	}
}
