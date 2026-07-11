package mdmerge

import (
	"errors"
	"strings"
	"testing"
)

// stylePage mimics what the Confluence editor saves: styled cells, a
// numbering column, content-wrapper divs. This is the shape hasComplexTable
// flags — and the table merge must handle.
const stylePage = `<h1>Registry</h1><table><colgroup><col/><col/><col/></colgroup><tbody>` +
	`<tr><th>Nr</th><th>Name</th><th>Kept?</th></tr>` +
	`<tr><td class="numberingColumn">1</td><td><div class="content-wrapper"><p>alpha</p></div></td><td style="text-align: center;">?</td></tr>` +
	`<tr><td class="numberingColumn">2</td><td><div class="content-wrapper"><p>beta</p></div></td><td style="text-align: center;">yes</td></tr>` +
	`</tbody></table>`

func TestTableMergeStyledCellEdit(t *testing.T) {
	md := renderOf(t, stylePage, nil)
	edited := strings.Replace(md, "| 1 | alpha | ? |", "| 1 | alpha | yes |", 1)
	out, rep := mustMerge(t, stylePage, edited, nil, Options{})
	want := strings.Replace(stylePage, `<td style="text-align: center;">?</td>`,
		`<td style="text-align: center;">yes</td>`, 1)
	if string(out) != want {
		t.Fatalf("merged bytes diverge:\n got %s\nwant %s", out, want)
	}
	if rep.MergedTables != 1 || rep.Removed != 0 {
		t.Errorf("report = %+v", rep)
	}
}

func TestTableMergeWrapperChainPreserved(t *testing.T) {
	md := renderOf(t, stylePage, nil)
	edited := strings.Replace(md, "| beta |", "| gamma |", 1)
	out, _ := mustMerge(t, stylePage, edited, nil, Options{})
	if !strings.Contains(string(out), `<td><div class="content-wrapper"><p>gamma</p></div></td>`) {
		t.Fatalf("wrapper chain not preserved: %s", out)
	}
	if strings.Contains(string(out), "beta") {
		t.Fatalf("old cell text still present: %s", out)
	}
}

func TestTableMergeRowInsertClonesTemplate(t *testing.T) {
	md := renderOf(t, stylePage, nil)
	edited := strings.TrimRight(md, "\n") + "\n| 3 | delta | no |\n"
	out, _ := mustMerge(t, stylePage, edited, nil, Options{})
	want := `<tr><td class="numberingColumn">3</td><td><div class="content-wrapper"><p>delta</p></div></td><td style="text-align: center;">no</td></tr>`
	if !strings.Contains(string(out), want) {
		t.Fatalf("inserted row did not clone the template structure:\n got %s\nwant fragment %s", out, want)
	}
	if !strings.Contains(string(out), want+`</tbody>`) {
		t.Fatalf("inserted row not at the end: %s", out)
	}
}

func TestTableMergeRowInsertInMiddle(t *testing.T) {
	md := renderOf(t, stylePage, nil)
	edited := strings.Replace(md, "| 2 | beta |", "| 9 | inserted | mid |\n| 2 | beta |", 1)
	out, _ := mustMerge(t, stylePage, edited, nil, Options{})
	s := string(out)
	ins := strings.Index(s, ">inserted<")
	beta := strings.Index(s, ">beta<")
	alpha := strings.Index(s, ">alpha<")
	if ins < 0 || alpha >= ins || ins >= beta {
		t.Fatalf("inserted row misplaced (alpha=%d ins=%d beta=%d): %s", alpha, ins, beta, s)
	}
}

func TestTableMergeRowDeleteAndLossGate(t *testing.T) {
	page := strings.Replace(stylePage, "<p>beta</p>",
		`<p>beta <ac:structured-macro ac:name="status" ac:macro-id="m-1"><ac:parameter ac:name="title">OK</ac:parameter></ac:structured-macro></p>`, 1)
	md := renderOf(t, page, nil)
	lines := strings.Split(md, "\n")
	var kept []string
	for _, l := range lines {
		if strings.Contains(l, "beta") {
			continue
		}
		kept = append(kept, l)
	}
	edited := strings.Join(kept, "\n")
	_, _, err := Merge([]byte(page), nil, edited, Options{})
	var le *LossError
	if !errors.As(err, &le) {
		t.Fatalf("deleting a macro-bearing row: want LossError, got %v", err)
	}
	out, rep := mustMerge(t, page, edited, nil, Options{AllowFragmentLoss: true})
	if strings.Contains(string(out), "beta") || strings.Contains(string(out), "status") {
		t.Fatalf("deleted row content survived: %s", out)
	}
	if len(rep.RemovedFragments) == 0 {
		t.Errorf("report misses removed fragments: %+v", rep)
	}
	// Deleting a plain row needs no override.
	edited2 := strings.Replace(renderOf(t, stylePage, nil), "| 2 | beta | yes |\n", "", 1)
	out2, _ := mustMerge(t, stylePage, edited2, nil, Options{})
	if strings.Contains(string(out2), "beta") {
		t.Fatalf("plain row not deleted: %s", out2)
	}
}

func TestTableMergeMacroCellTextEditKeepsMacroBytes(t *testing.T) {
	page := strings.Replace(stylePage, "<p>beta</p>",
		`<p>beta <ac:structured-macro ac:name="jira" ac:macro-id="keep-me"><ac:parameter ac:name="key">AB-7</ac:parameter></ac:structured-macro></p>`, 1)
	md := renderOf(t, page, nil)
	if !strings.Contains(md, "[AB-7](jira:AB-7)") {
		t.Fatalf("fixture md misses the jira marker: %s", md)
	}
	edited := strings.Replace(md, "beta [AB-7](jira:AB-7)", "renamed [AB-7](jira:AB-7)", 1)
	out, _ := mustMerge(t, page, edited, nil, Options{})
	if !strings.Contains(string(out), `ac:macro-id="keep-me"`) {
		t.Fatalf("macro identity lost: %s", out)
	}
	if !strings.Contains(string(out), "renamed") {
		t.Fatalf("cell text edit not applied: %s", out)
	}
}

func TestTableMergeMacroMovesBetweenCells(t *testing.T) {
	page := strings.Replace(stylePage, "<p>beta</p>",
		`<p><ac:structured-macro ac:name="jira" ac:macro-id="mv-1"><ac:parameter ac:name="key">AB-9</ac:parameter></ac:structured-macro></p>`, 1)
	md := renderOf(t, page, nil)
	edited := strings.Replace(md, "| 2 | [AB-9](jira:AB-9) | yes |", "| 2 | plain | [AB-9](jira:AB-9) |", 1)
	out, _ := mustMerge(t, page, edited, nil, Options{})
	if c := strings.Count(string(out), `ac:macro-id="mv-1"`); c != 1 {
		t.Fatalf("macro must move exactly once, found %d: %s", c, out)
	}
	if !strings.Contains(string(out), "plain") {
		t.Fatalf("source cell not rewritten: %s", out)
	}
}

// Copying a mention from an untouched row into an edited cell must clone the
// user-link bytes, not degrade them to plain display text.
func TestTableMergeMentionCopiedFromKeptRow(t *testing.T) {
	page := strings.Replace(stylePage, "<p>alpha</p>",
		`<p><ac:link><ri:user ri:userkey="cafe01"/></ac:link></p>`, 1)
	md := renderOf(t, page, nil)
	// Row 1 (the mention's row) stays untouched; row 2's Name cell receives a
	// copy of its marker text.
	mention := "@cafe01"
	if !strings.Contains(md, mention) {
		t.Fatalf("fixture md misses the mention marker: %s", md)
	}
	edited := strings.Replace(md, "| 2 | beta | yes |", "| 2 | "+mention+" | yes |", 1)
	out, _ := mustMerge(t, page, edited, nil, Options{})
	if c := strings.Count(string(out), `ri:userkey="cafe01"`); c != 2 {
		t.Fatalf("mention must be cloned (want 2 occurrences, got %d): %s", c, out)
	}
	if strings.Contains(string(out), ">@cafe01<") || strings.Contains(string(out), "<p>@cafe01</p>") {
		t.Fatalf("mention degraded to plain text: %s", out)
	}
}

func TestTableMergeColspanOriginEdit(t *testing.T) {
	page := `<table><tbody><tr><th>A</th><th>B</th></tr>` +
		`<tr><td colspan="2" style="text-align: left;">wide</td></tr>` +
		`<tr><td>x</td><td>y</td></tr></tbody></table>`
	md := renderOf(t, page, nil)
	edited := strings.Replace(md, "| wide |", "| wider |", 1)
	out, _ := mustMerge(t, page, edited, nil, Options{})
	if !strings.Contains(string(out), `<td colspan="2" style="text-align: left;">wider</td>`) {
		t.Fatalf("colspan origin edit lost attributes: %s", out)
	}
}

func TestTableMergeEmptyCellFillAndClear(t *testing.T) {
	page := `<table><tbody><tr><th>K</th><th>V</th></tr>` +
		`<tr><td style="color: red;"></td><td>keep</td></tr></tbody></table>` +
		`<p>tail</p>`
	md := renderOf(t, page, nil)
	edited := strings.Replace(md, "|  | keep |", "| filled | keep |", 1)
	out, _ := mustMerge(t, page, edited, nil, Options{})
	if !strings.Contains(string(out), `<td style="color: red;">filled</td>`) {
		t.Fatalf("empty styled cell not filled in place: %s", out)
	}
	// And the reverse: clearing a cell empties its wrapper.
	md2 := renderOf(t, string(out), nil)
	edited2 := strings.Replace(md2, "| filled | keep |", "|  | keep |", 1)
	out2, _ := mustMerge(t, string(out), edited2, nil, Options{})
	if !strings.Contains(string(out2), `<td style="color: red;"></td>`) {
		t.Fatalf("cell not cleared in place: %s", out2)
	}
}

func TestTableMergePipeEscaping(t *testing.T) {
	page := strings.Replace(stylePage, "<p>beta</p>", "<p>a|b</p>", 1)
	md := renderOf(t, page, nil)
	if !strings.Contains(md, `a\|b`) {
		t.Fatalf("fixture md misses escaped pipe: %s", md)
	}
	edited := strings.Replace(md, `| 2 | a\|b | yes |`, `| 2 | c\|d | yes |`, 1)
	out, _ := mustMerge(t, page, edited, nil, Options{})
	if !strings.Contains(string(out), "<p>c|d</p>") {
		t.Fatalf("piped cell edit not applied: %s", out)
	}
}

func TestTableMergeInsertRefusesCopiedMacroCell(t *testing.T) {
	page := strings.Replace(stylePage, "<p>beta</p>",
		`<p><ac:structured-macro ac:name="status" ac:macro-id="s-1"/></p>`, 1)
	md := renderOf(t, page, nil)
	// The inserted row repeats the macro cell's marker text — cloning would
	// duplicate the macro's identity.
	edited := strings.TrimRight(md, "\n") + "\n| 3 | ⟦status⟧ | no |\n"
	_, _, err := Merge([]byte(page), nil, edited, Options{})
	var be *BlockError
	if !errors.As(err, &be) {
		t.Fatalf("want BlockError for copied macro cell, got %v", err)
	}
}

// Review finding (P0): a colored span wrapping a macro must never enter the
// copyable pool — cloning its bytes would duplicate the macro-id. Copying its
// marker text fails closed instead.
func TestTableMergeSpanWrappedMacroNotCloneable(t *testing.T) {
	page := strings.Replace(stylePage, "<p>alpha</p>",
		`<p><span style="color: rgb(255,0,0);"><ac:structured-macro ac:name="status" ac:macro-id="dup"><ac:parameter ac:name="title">OK</ac:parameter></ac:structured-macro></span></p>`, 1)
	md := renderOf(t, page, nil)
	// Row 1 (the span row) stays untouched; row 2's Name cell tries to copy
	// the span's marker text.
	start := strings.Index(md, "<span style=\"color:")
	end := -1
	if start >= 0 {
		end = strings.Index(md[start:], "</span>")
	}
	if start < 0 || end < 0 {
		t.Fatalf("fixture md misses the color marker: %s", md)
	}
	end += start
	markerTxt := md[start : end+len("</span>")]
	edited := strings.Replace(md, "| 2 | beta | yes |", "| 2 | "+markerTxt+" | yes |", 1)
	out, _, err := Merge([]byte(page), nil, edited, Options{})
	var be *BlockError
	if !errors.As(err, &be) {
		c := strings.Count(string(out), `ac:macro-id="dup"`)
		t.Fatalf("want BlockError (macro-id would duplicate), got err=%v, %d macro copies", err, c)
	}
}

// Review finding (P2): editing a plain table must not be refused just because
// a complex table elsewhere on the page was edited too.
func TestTableMergeSimpleTableEditBesideComplexEdit(t *testing.T) {
	simple := `<table><tbody><tr><th>X</th><th>Y</th></tr><tr><td>p</td><td>q</td></tr></tbody></table>`
	page := stylePage + simple
	md := renderOf(t, page, nil)
	edited := strings.Replace(md, "| ? |", "| yes |", 1)            // complex table edit
	edited = strings.Replace(edited, "| p | q |", "| p2 | q2 |", 1) // plain table edit
	out, rep := mustMerge(t, page, edited, nil, Options{})
	s := string(out)
	if !strings.Contains(s, `style="text-align: center;">yes</td>`) {
		t.Fatalf("complex table edit missing: %s", s)
	}
	if !strings.Contains(s, "<td>p2</td><td>q2</td>") {
		t.Fatalf("plain table edit refused or lost: %s", s)
	}
	if rep.MergedTables != 1 || rep.Converted == 0 {
		t.Errorf("report = %+v", rep)
	}
}

func TestTableMergeTwoTablesPairCorrectly(t *testing.T) {
	second := strings.NewReplacer("alpha", "one", "beta", "two", "Registry", "Other").Replace(stylePage)
	page := stylePage + second
	md := renderOf(t, page, nil)
	edited := strings.Replace(md, "| 1 | one | ? |", "| 1 | one | no |", 1)
	out, rep := mustMerge(t, page, edited, nil, Options{})
	s := string(out)
	if !strings.Contains(s, ">alpha<") || strings.Count(s, "<table>") != 2 {
		t.Fatalf("first table damaged: %s", s)
	}
	if !strings.Contains(strings.Split(s, "Other")[1], `style="text-align: center;">no</td>`) {
		t.Fatalf("edit landed in the wrong table: %s", s)
	}
	if rep.MergedTables != 1 {
		t.Errorf("MergedTables = %d", rep.MergedTables)
	}
}

// The merged output of a table merge must satisfy the same validity gate as
// every other merge product.
func TestTableMergeOutputValidates(t *testing.T) {
	md := renderOf(t, stylePage, nil)
	edited := strings.Replace(md, "| beta |", "| **bold** _em_ `code` |", 1)
	out, rep := mustMerge(t, stylePage, edited, nil, Options{})
	if len(rep.Problems) > 0 {
		for _, p := range rep.Problems {
			if p.Severity == "error" {
				t.Fatalf("merged body has validation errors: %+v", rep.Problems)
			}
		}
	}
	if !strings.Contains(string(out), "<strong>bold</strong>") {
		t.Fatalf("inline conversion missing: %s", out)
	}
}
