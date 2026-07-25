package csf

import (
	"bytes"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// cloudRules projects the cloud-compat findings out of a diagnostic set, in
// order, so a test can assert the exact rules a body triggers.
func cloudRules(ps []Problem) []string {
	var out []string
	for _, p := range ps {
		if strings.HasPrefix(p.Rule, "cloud-compat/") {
			out = append(out, p.Rule)
		}
	}
	return out
}

// A body loaded with cloud-compat hits must stay invisible to the default
// validator: the pack is strictly opt-in, and Validate's output is a contract
// for the push gate and every existing caller.
func TestCloudCompatIsOptIn(t *testing.T) {
	body := []byte(`<ac:structured-macro ac:name="chart"/>` +
		`<ac:structured-macro ac:name="expand"><ac:rich-text-body>` +
		`<ac:structured-macro ac:name="code"><ac:plain-text-body><![CDATA[x]]></ac:plain-text-body></ac:structured-macro>` +
		`</ac:rich-text-body></ac:structured-macro>` +
		`<table><tbody><tr><td><table><tbody><tr><td>x</td></tr></tbody></table></td></tr></tbody></table>`)

	def := Validate(body)
	if got := cloudRules(def); len(got) != 0 {
		t.Fatalf("Validate emitted cloud-compat rules %v; the pack must be opt-in", got)
	}
	// The explicit zero-value option path must be identical to Validate.
	if zero := ValidateWithOptions(body, Options{}); !reflect.DeepEqual(zero, def) {
		t.Fatalf("ValidateWithOptions(_, Options{}) = %+v, want identical to Validate = %+v", zero, def)
	}
	if on := ValidateWithOptions(body, Options{CloudCompat: true}); len(cloudRules(on)) == 0 {
		t.Fatalf("ValidateWithOptions with CloudCompat produced no cloud-compat findings: %+v", on)
	}
}

// Enabling the pack must never change the default diagnostics: the cloud
// findings are appended, and the prefix stays byte-identical.
func TestCloudCompatOnlyAppends(t *testing.T) {
	// A body that also trips a default warning (drawio without diagramName) and
	// an invisible-character warning.
	body := []byte("<p>a\u00a0b</p>" +
		`<ac:structured-macro ac:name="drawio"><ac:parameter ac:name="revision">1</ac:parameter></ac:structured-macro>` +
		`<ac:structured-macro ac:name="chart"/>`)
	def := Validate(body)
	on := ValidateWithOptions(body, Options{CloudCompat: true})
	if len(on) < len(def) {
		t.Fatalf("cloud pack dropped diagnostics: %d < %d", len(on), len(def))
	}
	if !reflect.DeepEqual(on[:len(def)], def) {
		t.Fatalf("default diagnostics changed under CloudCompat:\n got %+v\nwant %+v", on[:len(def)], def)
	}
	for _, p := range on[len(def):] {
		if !strings.HasPrefix(p.Rule, "cloud-compat/") {
			t.Errorf("appended non-cloud problem %+v", p)
		}
	}
}

func TestCloudCompatMacroStatus(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		// Positive: one per documented category.
		{"not insertable", `<ac:structured-macro ac:name="info"><ac:rich-text-body><p>x</p></ac:rich-text-body></ac:structured-macro>`,
			[]string{RuleCloudMacroNotInsertable}},
		{"code block storage key", `<ac:structured-macro ac:name="code"><ac:plain-text-body><![CDATA[x]]></ac:plain-text-body></ac:structured-macro>`,
			[]string{RuleCloudMacroNotInsertable}},
		{"view only", `<ac:structured-macro ac:name="chart"/>`, []string{RuleCloudMacroViewOnly}},
		{"removed", `<ac:structured-macro ac:name="span"/>`, []string{RuleCloudMacroRemoved}},
		{"legacy ac:macro element", `<ac:macro ac:name="junitreport"/>`, []string{RuleCloudMacroRemoved}},

		// Explicit non-hits: supported built-ins, marketplace/user macros, and
		// keys that merely look like a listed one.
		{"supported builtin toc", `<ac:structured-macro ac:name="toc"/>`, nil},
		{"supported builtin expand", `<ac:structured-macro ac:name="expand"><ac:rich-text-body><p>x</p></ac:rich-text-body></ac:structured-macro>`, nil},
		{"marketplace macro", `<ac:structured-macro ac:name="com.example.plugin__roadmap"/>`, nil},
		{"user macro", `<ac:structured-macro ac:name="my-user-macro"/>`, nil},
		{"unnamed macro", `<ac:structured-macro/>`, nil},
		{"near miss of listed key", `<ac:structured-macro ac:name="info-panel"/>`, nil},
		{"Cloud table key is not the CSF storage key", `<ac:structured-macro ac:name="codeBlock"/>`, nil},
		{"plain markup", `<p>hello <strong>world</strong></p>`, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ps := ValidateWithOptions([]byte(c.body), Options{CloudCompat: true})
			if got := cloudRules(ps); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("rules = %v, want %v (problems %+v)", got, c.want, ps)
			}
		})
	}
}

// Every listed key must map to a category that has a message template, and the
// pack must classify each listed key exactly once.
func TestCloudCompatMacroTableIsClosed(t *testing.T) {
	wantKeys := strings.Fields(
		"align bgcolor center chart cheese code content-by-user contributors-summary copyright create-space-button " +
			"div fancy-bullets favpages gallery global-reports highlight htmlcomment im index info junitreport " +
			"loremipsum multimedia navmap noformat note panel privacy-mark privacy-policy recently-updated-dashboard " +
			"recently-used-labels reg-tm related-labels rolloverwithoudbody search sm space-attachments space-details " +
			"spaces span strike style tip tm viewdoc viewppt warning")
	gotKeys := make([]string, 0, len(cloudMacroStatus))
	for key := range cloudMacroStatus {
		gotKeys = append(gotKeys, key)
	}
	sort.Strings(gotKeys)
	if !reflect.DeepEqual(gotKeys, wantKeys) {
		t.Fatalf("macro key set changed without a rule-pack/source review:\n got %v\nwant %v", gotKeys, wantKeys)
	}

	for key, rule := range cloudMacroStatus {
		if _, ok := cloudMacroMessage[rule]; !ok {
			t.Fatalf("macro %q maps to rule %q with no message template", key, rule)
		}
		body := `<ac:structured-macro ac:name="` + key + `"/>`
		ps := ValidateWithOptions([]byte(body), Options{CloudCompat: true})
		if got := cloudRules(ps); !reflect.DeepEqual(got, []string{rule}) {
			t.Errorf("macro %q rules = %v, want [%s]", key, got, rule)
		}
		if HasErrors(ps) {
			t.Errorf("macro %q produced a blocking problem: %+v", key, ps)
		}
	}
}

func TestCloudCompatNestedBodiedMacro(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{"bodied macro inside a bodied macro",
			`<ac:structured-macro ac:name="expand"><ac:rich-text-body>` +
				`<ac:structured-macro ac:name="expand"><ac:rich-text-body><p>x</p></ac:rich-text-body></ac:structured-macro>` +
				`</ac:rich-text-body></ac:structured-macro>`,
			[]string{RuleCloudNestedBodiedMacro}},
		{"three levels report each nested bodied macro",
			`<ac:structured-macro ac:name="expand"><ac:rich-text-body>` +
				`<ac:structured-macro ac:name="section"><ac:rich-text-body>` +
				`<ac:structured-macro ac:name="column"><ac:rich-text-body><p>x</p></ac:rich-text-body></ac:structured-macro>` +
				`</ac:rich-text-body></ac:structured-macro>` +
				`</ac:rich-text-body></ac:structured-macro>`,
			[]string{RuleCloudNestedBodiedMacro, RuleCloudNestedBodiedMacro}},

		// Non-hits.
		{"single bodied macro",
			`<ac:structured-macro ac:name="expand"><ac:rich-text-body><p>x</p></ac:rich-text-body></ac:structured-macro>`, nil},
		{"sibling bodied macros",
			`<ac:structured-macro ac:name="expand"><ac:rich-text-body><p>x</p></ac:rich-text-body></ac:structured-macro>` +
				`<ac:structured-macro ac:name="expand"><ac:rich-text-body><p>y</p></ac:rich-text-body></ac:structured-macro>`, nil},
		{"bodyless macro nested inside a bodied macro",
			`<ac:structured-macro ac:name="expand"><ac:rich-text-body>` +
				`<ac:structured-macro ac:name="toc"><ac:parameter ac:name="maxLevel">2</ac:parameter></ac:structured-macro>` +
				`</ac:rich-text-body></ac:structured-macro>`, nil},
		{"parameters alone are not a body",
			`<ac:structured-macro ac:name="expand"><ac:rich-text-body>` +
				`<ac:structured-macro ac:name="unsupported-app"><ac:parameter ac:name="k">v</ac:parameter></ac:structured-macro>` +
				`</ac:rich-text-body></ac:structured-macro>`, nil},
		{"bodied macro inside a top-level parameter",
			`<ac:structured-macro ac:name="outer"><ac:parameter ac:name="k">` +
				`<ac:structured-macro ac:name="expand"><ac:rich-text-body><p>x</p></ac:rich-text-body></ac:structured-macro>` +
				`</ac:parameter></ac:structured-macro>`, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ps := ValidateWithOptions([]byte(c.body), Options{CloudCompat: true})
			if got := cloudRules(ps); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("rules = %v, want %v (problems %+v)", got, c.want, ps)
			}
		})
	}
}

func TestCloudCompatNestedTable(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{"table inside a table cell",
			`<table><tbody><tr><td><table><tbody><tr><td>x</td></tr></tbody></table></td></tr></tbody></table>`,
			[]string{RuleCloudNestedTable}},
		{"three levels report each inner table",
			`<table><tbody><tr><td><table><tbody><tr><td><table><tbody><tr><td>x</td></tr></tbody></table></td></tr></tbody></table></td></tr></tbody></table>`,
			[]string{RuleCloudNestedTable, RuleCloudNestedTable}},
		{"table nested through a macro body still counts",
			`<table><tbody><tr><td>` +
				`<ac:structured-macro ac:name="expand"><ac:rich-text-body>` +
				`<table><tbody><tr><td>x</td></tr></tbody></table>` +
				`</ac:rich-text-body></ac:structured-macro>` +
				`</td></tr></tbody></table>`,
			[]string{RuleCloudNestedTable}},

		// Non-hits.
		{"single table", `<table><tbody><tr><td>x</td></tr></tbody></table>`, nil},
		{"sibling tables",
			`<table><tbody><tr><td>x</td></tr></tbody></table><table><tbody><tr><td>y</td></tr></tbody></table>`, nil},
		{"table inside a macro body only", `<ac:structured-macro ac:name="expand"><ac:rich-text-body>` +
			`<table><tbody><tr><td>x</td></tr></tbody></table></ac:rich-text-body></ac:structured-macro>`, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ps := ValidateWithOptions([]byte(c.body), Options{CloudCompat: true})
			if got := cloudRules(ps); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("rules = %v, want %v (problems %+v)", got, c.want, ps)
			}
		})
	}
}

// The whole point of the pack is that it is advisory: findings must be warnings
// and must never flip the push gate.
func TestCloudCompatNeverBlocks(t *testing.T) {
	body := []byte(`<ac:structured-macro ac:name="chart"/>` +
		`<ac:structured-macro ac:name="span"/>` +
		`<ac:structured-macro ac:name="info"><ac:rich-text-body>` +
		`<ac:structured-macro ac:name="code"><ac:plain-text-body><![CDATA[x]]></ac:plain-text-body></ac:structured-macro>` +
		`</ac:rich-text-body></ac:structured-macro>` +
		`<table><tbody><tr><td><table><tbody><tr><td>x</td></tr></tbody></table></td></tr></tbody></table>`)
	ps := ValidateWithOptions(body, Options{CloudCompat: true})
	if len(cloudRules(ps)) < 5 {
		t.Fatalf("expected findings from every v1 rule, got %+v", ps)
	}
	for _, p := range ps {
		if strings.HasPrefix(p.Rule, "cloud-compat/") && p.Severity != "warning" {
			t.Errorf("cloud finding %+v is not a warning", p)
		}
	}
	if HasErrors(ps) {
		t.Fatal("cloud-compat findings must never make HasErrors true")
	}
}

// A malformed body still reports exactly the well-formedness error: the rules
// need a DOM and must not invent findings without one.
func TestCloudCompatMalformedBodyUnchanged(t *testing.T) {
	body := []byte(`<ac:structured-macro ac:name="chart"><p>oops`)
	def := Validate(body)
	on := ValidateWithOptions(body, Options{CloudCompat: true})
	if !reflect.DeepEqual(def, on) {
		t.Fatalf("malformed body diagnostics differ:\n got %+v\nwant %+v", on, def)
	}
	if !HasErrors(on) {
		t.Fatalf("expected a well-formedness error, got %+v", on)
	}
}

// Positions come from real DOM offsets, never from a guess.
func TestCloudCompatPositions(t *testing.T) {
	body := []byte("<p>one</p>\n<p>two</p>\n  <ac:structured-macro ac:name=\"chart\"/>")
	ps := ValidateWithOptions(body, Options{CloudCompat: true})
	var found *Problem
	for i := range ps {
		if ps[i].Rule == RuleCloudMacroViewOnly {
			found = &ps[i]
		}
	}
	if found == nil {
		t.Fatalf("no view-only finding in %+v", ps)
	}
	if found.Line != 3 || found.Col != 3 {
		t.Errorf("position = %d:%d, want 3:3", found.Line, found.Col)
	}
	// The offset must address the macro's own start tag.
	root, err := Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	var macro *Node
	Walk(root, func(n *Node) bool {
		if n.MacroName() == "chart" {
			macro = n
		}
		return true
	})
	if macro == nil || !bytes.HasPrefix(body[macro.Start:], []byte("<ac:structured-macro")) {
		t.Fatalf("macro start offset does not point at the start tag")
	}
}

func TestSourcePositionsMatchLineCol(t *testing.T) {
	raw := []byte("alpha\nβeta\r\nlast")
	positions := newSourcePositions(raw)
	for off := range raw {
		line, col, ok := positions.lineCol(off)
		if !ok {
			t.Fatalf("offset %d unexpectedly rejected", off)
		}
		wantLine, wantCol := lineCol(raw, off)
		if line != wantLine || col != wantCol {
			t.Errorf("offset %d = %d:%d, want %d:%d", off, line, col, wantLine, wantCol)
		}
	}
	line, col, ok := positions.lineCol(len(raw))
	wantLine, wantCol := lineCol(raw, len(raw))
	if !ok || line != wantLine || col != wantCol {
		t.Errorf("end offset = %d:%d (ok=%v), want %d:%d", line, col, ok, wantLine, wantCol)
	}
	for _, off := range []int{-1, len(raw) + 1} {
		if _, _, ok := positions.lineCol(off); ok {
			t.Errorf("out-of-range offset %d accepted", off)
		}
	}
}

// The pack is read-only over both the DOM and the caller's bytes, and its
// output is stable across runs.
func TestCloudCompatByteStableAndDeterministic(t *testing.T) {
	raw := []byte(`<ac:structured-macro ac:name="info"><ac:rich-text-body>` +
		`<table><tbody><tr><td><table><tbody><tr><td>x</td></tr></tbody></table></td></tr></tbody></table>` +
		`</ac:rich-text-body></ac:structured-macro>`)
	orig := append([]byte(nil), raw...)
	first := ValidateWithOptions(raw, Options{CloudCompat: true})
	second := ValidateWithOptions(raw, Options{CloudCompat: true})
	if !bytes.Equal(raw, orig) {
		t.Fatal("ValidateWithOptions mutated its input; the mirror relies on byte-stable bytes")
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("non-deterministic output:\n%+v\n%+v", first, second)
	}
}

// The rule pack is versioned so a finding can be interpreted against the
// documentation snapshot it came from.
func TestCloudCompatRulePackIdentity(t *testing.T) {
	if CloudCompatRulePack != "v1" {
		t.Errorf("rule pack = %q, want v1", CloudCompatRulePack)
	}
	if CloudCompatSourceDate == "" {
		t.Error("rule pack source date must be set")
	}
}
