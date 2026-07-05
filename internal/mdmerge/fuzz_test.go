package mdmerge

import (
	"testing"

	"github.com/isukharev/atl/internal/csf"
)

// FuzzMerge asserts the merge never panics on arbitrary base/edited pairs and
// that any successful merge yields well-formed CSF (the same invariant the
// converter holds, extended through splicing and marker substitution).
func FuzzMerge(f *testing.F) {
	mds := []string{
		"",
		"# Intro\n\nplain\n",
		"# Intro\n\nStatus of [AB-1](jira:AB-1) is green.\n\n⟦table of contents⟧\n",
		"| K | V |\n| --- | --- |\n| a | 1 |\n",
		"- [ ] task\n\n> INFO\n> \n> body\n",
		"```go\ncode\n```\n\ntail **bold**\n",
	}
	bases := []string{
		samplePage,
		`<p>one</p>`,
		`<ac:layout><ac:layout-section><ac:layout-cell><p>x</p></ac:layout-cell></ac:layout-section></ac:layout>`,
		`<p>a<ri:user ri:userkey="k"/>b</p><hr/>`,
		``,
		`<p>broken`,
	}
	for _, b := range bases {
		for _, m := range mds {
			f.Add([]byte(b), m)
		}
	}
	f.Fuzz(func(t *testing.T, base []byte, md string) {
		out, rep, err := Merge(base, nil, md, Options{AllowFragmentLoss: true})
		if err != nil {
			return
		}
		if rep == nil {
			t.Fatal("nil report on success")
		}
		if ps := csf.Validate(out); csf.HasErrors(ps) {
			t.Fatalf("Merge produced invalid CSF from base=%q md=%q: %q (%+v)", base, md, out, ps)
		}
	})
}
