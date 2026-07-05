package mdmerge

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mdcsf"
	"github.com/isukharev/atl/internal/mirror"
)

// Table merge: the block-level three-way discipline recursed one level down.
// A "complex" table (spans, styled/classed cells, wrapper divs — i.e. nearly
// every table the Confluence editor saves) cannot round-trip through the GFM
// table syntax, but its md view CAN be merged row by row: rows whose rendered
// text is unchanged keep their exact base bytes; a changed cell has the
// converted inline md spliced into its existing wrapper chain (so cell
// styles, classes and wrapper divs survive); a deleted row drops its byte
// range; an inserted row clones the byte structure of a neighboring row.
// Everything genuinely inexpressible stays fail-closed.

const (
	opKeep = iota
	opModify
	opDelete
)

type rowOp struct {
	op   int
	edit int // edited row index for opModify
}

// pickTableCandidate pairs an edited md table with the dropped complex-table
// base block sharing the most row lines (separator rows excluded); document
// order breaks ties. Returns false when no candidate shares a single line.
func pickTableCandidate(cands []int, reused map[int]bool, blocks []mirror.Block, txt string) (int, bool) {
	edit := map[string]int{}
	for _, l := range strings.Split(txt, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || mdcsf.IsTableSeparator(l) {
			continue
		}
		edit[l]++
	}
	best, bestScore := -1, 0
	for _, bi := range cands {
		if reused[bi] {
			continue
		}
		rem := make(map[string]int, len(edit))
		for k, v := range edit {
			rem[k] = v
		}
		score := 0
		for _, l := range strings.Split(blocks[bi].MD, "\n") {
			l = strings.TrimSpace(l)
			if rem[l] > 0 {
				rem[l]--
				score++
			}
		}
		if score > bestScore {
			best, bestScore = bi, score
		}
	}
	return best, best >= 0
}

// mergeTable merges an edited md table onto its base <table> node, returning
// the merged table bytes. Fail-closed: any edit the row/cell mapping cannot
// carry faithfully returns an error and the whole merge aborts.
func mergeTable(base []byte, tableNode *csf.Node, refs []domain.Ref, editedTxt string) ([]byte, error) {
	// Parse the edited md table with the converter's shape rules.
	lines := strings.Split(strings.TrimSpace(editedTxt), "\n")
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}
	if len(lines) < 2 {
		return nil, fmt.Errorf("table without separator row")
	}
	for _, l := range lines {
		if !strings.HasPrefix(l, "|") {
			return nil, fmt.Errorf("table with non-row line %q", l)
		}
	}
	if !mdcsf.IsTableSeparator(lines[1]) {
		return nil, fmt.Errorf("second table line is not a separator row")
	}
	width := len(mdcsf.SplitTableRow(lines[0]))
	if len(mdcsf.SplitTableRow(lines[1])) != width {
		return nil, fmt.Errorf("table separator width differs from header")
	}
	editRows := [][]string{mdcsf.SplitTableRow(lines[0])}
	for _, l := range lines[2:] {
		cells := mdcsf.SplitTableRow(l)
		if len(cells) > width {
			return nil, fmt.Errorf("table row wider than header: %q", l)
		}
		for len(cells) < width {
			cells = append(cells, "")
		}
		editRows = append(editRows, cells)
	}

	grid, trs, hdr := mirror.TableGrid(tableNode, refs)
	if len(grid) == 0 {
		return nil, fmt.Errorf("base table renders no rows")
	}
	if hdr > 0 {
		return nil, fmt.Errorf("base table's header is not its first row — edit the .csf directly")
	}
	if len(grid[0]) != width {
		return nil, fmt.Errorf("table column count changed (%d → %d) — add or remove columns in the .csf directly", len(grid[0]), width)
	}

	// A cell spanning several md rows (rowspan) cannot have rows inserted or
	// deleted through it, and only a cell's top-left origin slot is editable.
	multiRow := map[*csf.Node]bool{}
	seenRow := map[*csf.Node]int{}
	for ri, row := range grid {
		for _, c := range row {
			if c.Node == nil {
				continue
			}
			if prev, ok := seenRow[c.Node]; ok && prev != ri {
				multiRow[c.Node] = true
			}
			seenRow[c.Node] = ri
		}
	}

	// Row alignment: same LCS as the block merge, on the exact md row texts.
	baseKeys := make([]string, len(grid))
	for i, row := range grid {
		parts := make([]string, len(row))
		for j, c := range row {
			parts[j] = c.Text
		}
		baseKeys[i] = strings.Join(parts, "\x00")
	}
	editKeys := make([]string, len(editRows))
	for i, cells := range editRows {
		editKeys[i] = strings.Join(cells, "\x00")
	}
	baseMatch, _ := lcs(baseKeys, editKeys)

	// Turn the alignment into per-row operations. Inside a gap run, base and
	// edited rows pair positionally (a modification); leftover base rows are
	// deletions, leftover edited rows are insertions anchored before the next
	// matched base row.
	ops := make([]rowOp, len(trs))
	inserts := map[int][]int{} // base row index (len(trs) = end) → edited row indices
	pb, pe := 0, 0
	handleGap := func(bEnd, eEnd int) {
		nb, ne := bEnd-pb, eEnd-pe
		k := nb
		if ne < k {
			k = ne
		}
		for i := 0; i < k; i++ {
			ops[pb+i] = rowOp{op: opModify, edit: pe + i}
		}
		for i := k; i < nb; i++ {
			ops[pb+i] = rowOp{op: opDelete}
		}
		for i := k; i < ne; i++ {
			inserts[bEnd] = append(inserts[bEnd], pe+i)
		}
	}
	for b, e := range baseMatch {
		if e < 0 {
			continue
		}
		handleGap(b, e)
		ops[b] = rowOp{op: opKeep}
		pb, pe = b+1, e+1
	}
	handleGap(len(trs), len(editRows))

	// Guards, and the table-local marker pool: opaque fragments from replaced
	// cells and deleted rows only — kept rows keep their bytes, so collecting
	// from them would duplicate a macro that also survives in place.
	var pool []*marker
	for ri, op := range ops {
		row := grid[ri]
		switch op.op {
		case opDelete:
			for _, c := range row {
				if c.Node == nil {
					continue
				}
				if multiRow[c.Node] {
					return nil, fmt.Errorf("row %d is part of a rowspan — delete it in the .csf directly", ri+1)
				}
				if nodeHasTable(c.Node) {
					return nil, fmt.Errorf("row %d holds a nested table — delete it in the .csf directly", ri+1)
				}
				if c.Origin {
					collectOpaque(c.Node, base, refs, &pool)
				}
			}
		case opModify:
			e := editRows[op.edit]
			for col, c := range row {
				if c.Text == e[col] {
					continue
				}
				if c.Node == nil {
					return nil, fmt.Errorf("row %d gains content in column %d, which the base row does not have — edit the .csf directly", ri+1, col+1)
				}
				if !c.Origin {
					return nil, fmt.Errorf("the cell at row %d column %d continues a span — edit it in the .csf directly", ri+1, col+1)
				}
				if nodeHasTable(c.Node) {
					return nil, fmt.Errorf("the cell at row %d column %d holds a nested table — edit it in the .csf directly", ri+1, col+1)
				}
				collectOpaque(c.Node, base, refs, &pool)
			}
		}
	}
	// Content that keeps its bytes (kept rows, unchanged cells of modified
	// rows) contributes clone-safe markers, so a mention or link can be
	// copied into an edited cell without degrading to plain text. Appended
	// after the movable pool: at equal length the stable sort prefers
	// consuming a dropped fragment over cloning a kept one.
	for ri, op := range ops {
		switch op.op {
		case opKeep:
			collectCopyable(trs[ri], base, refs, &pool)
		case opModify:
			e := editRows[op.edit]
			for col, c := range grid[ri] {
				if c.Node != nil && c.Origin && c.Text == e[col] {
					collectCopyable(c.Node, base, refs, &pool)
				}
			}
		}
	}
	sort.SliceStable(pool, func(a, b int) bool { return len(pool[a].md) > len(pool[b].md) })

	hasMacro := func(n *csf.Node) bool {
		found := false
		csf.Walk(n, func(x *csf.Node) bool {
			if x.MacroName() != "" {
				found = true
			}
			return !found
		})
		return found
	}

	// template picks the row whose byte structure an insertion clones: the
	// nearest data row (preferring upwards) made of plain 1×1 cells.
	template := func(at int) int {
		qualify := func(ri int) bool {
			for _, c := range grid[ri] {
				if c.Node == nil || !c.Origin || multiRow[c.Node] || nodeHasTable(c.Node) {
					return false
				}
				if c.Node.Name.Local == "th" {
					return false
				}
			}
			// Colspans surface as non-origin slots, so a qualifying row has
			// exactly `width` distinct cells.
			return true
		}
		for ri := at - 1; ri >= 0; ri-- {
			if qualify(ri) {
				return ri
			}
		}
		for ri := at; ri < len(grid); ri++ {
			if qualify(ri) {
				return ri
			}
		}
		return -1
	}

	cloneRow := func(tpl int, texts []string) ([]byte, error) {
		tr := trs[tpl]
		var out []byte
		cur := tr.Start
		for col, c := range grid[tpl] {
			s, e, err := cellInnerSpan(c.Node, base)
			if err != nil {
				return nil, err
			}
			out = append(out, base[cur:s]...)
			if texts[col] == c.Text {
				// Identical text keeps the template cell's bytes — unless it
				// holds a macro, whose clone would duplicate its macro-id.
				if hasMacro(c.Node) {
					return nil, fmt.Errorf("an inserted row copies a cell holding a macro — build that row in the .csf directly")
				}
				out = append(out, base[s:e]...)
			} else {
				conv, err := convertCell(texts[col], pool)
				if err != nil {
					return nil, err
				}
				out = append(out, conv...)
			}
			cur = e
		}
		out = append(out, base[cur:tr.End]...)
		return out, nil
	}

	mergeRow := func(ri int, texts []string) ([]byte, error) {
		tr := trs[ri]
		var out []byte
		cur := tr.Start
		for col, c := range grid[ri] {
			if c.Node == nil || !c.Origin || c.Text == texts[col] {
				continue
			}
			s, e, err := cellInnerSpan(c.Node, base)
			if err != nil {
				return nil, err
			}
			if s < cur || e > tr.End {
				return nil, fmt.Errorf("cell offsets escape their row (this is a bug in the table merge)")
			}
			conv, err := convertCell(texts[col], pool)
			if err != nil {
				return nil, err
			}
			out = append(out, base[cur:s]...)
			out = append(out, conv...)
			cur = e
		}
		out = append(out, base[cur:tr.End]...)
		return out, nil
	}

	// Assemble: gaps between rows (tbody boundaries, whitespace) always come
	// from the base; insertions land before their anchor row.
	var out []byte
	cur := tableNode.Start
	emitInserts := func(at int) error {
		for _, ei := range inserts[at] {
			tpl := template(at)
			if tpl < 0 {
				return fmt.Errorf("no plain row to clone for an inserted row — build it in the .csf directly")
			}
			cloned, err := cloneRow(tpl, editRows[ei])
			if err != nil {
				return err
			}
			out = append(out, cloned...)
		}
		return nil
	}
	for ri, tr := range trs {
		out = append(out, base[cur:tr.Start]...)
		if err := emitInserts(ri); err != nil {
			return nil, err
		}
		switch ops[ri].op {
		case opKeep:
			out = append(out, base[tr.Start:tr.End]...)
		case opModify:
			merged, err := mergeRow(ri, editRows[ops[ri].edit])
			if err != nil {
				return nil, err
			}
			out = append(out, merged...)
		}
		cur = tr.End
	}
	if err := emitInserts(len(trs)); err != nil {
		return nil, err
	}
	out = append(out, base[cur:tableNode.End]...)
	return out, nil
}

// convertCell converts one edited cell's inline markdown, substituting the
// table-local opaque markers the same way convertBlock does.
func convertCell(txt string, markers []*marker) ([]byte, error) {
	txt, subst, err := substituteMarkers(txt, markers)
	if err != nil {
		return nil, err
	}
	conv, err := mdcsf.ConvertInline(txt)
	if err != nil {
		return nil, err
	}
	out := []byte(conv)
	for token, raw := range subst {
		out = bytes.Replace(out, []byte(token), raw, 1)
	}
	if len(subst) > 0 {
		if err := checkSpliceNesting(out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// cellInnerSpan returns the byte span a changed cell's new content replaces:
// the inside of the innermost element of the cell's single-chain wrapper
// (td > div > p …), so editor wrappers and their attributes survive edits.
func cellInnerSpan(c *csf.Node, base []byte) (int, int, error) {
	cur := c
	for {
		var elems []*csf.Node
		pure := true
		for _, ch := range cur.Children {
			switch ch.Type {
			case csf.Element:
				elems = append(elems, ch)
			case csf.Text:
				if strings.TrimSpace(ch.Data) != "" {
					pure = false
				}
			default:
				pure = false
			}
		}
		if pure && len(elems) == 1 && elems[0].Name.Space == "" &&
			(elems[0].Name.Local == "p" || elems[0].Name.Local == "div") {
			cur = elems[0]
			continue
		}
		break
	}
	return elementInnerSpan(cur, base)
}

// elementInnerSpan is the byte range strictly between an element's open and
// close tags.
func elementInnerSpan(n *csf.Node, base []byte) (int, int, error) {
	if len(n.Children) > 0 {
		return n.Children[0].Start, n.Children[len(n.Children)-1].End, nil
	}
	if n.Start < 0 || n.End > len(base) || n.End <= n.Start {
		return 0, 0, fmt.Errorf("cell <%s> has no byte span (this is a bug in the table merge)", n.Name.Local)
	}
	seg := base[n.Start:n.End]
	if bytes.HasSuffix(seg, []byte("/>")) {
		return 0, 0, fmt.Errorf("cell <%s> is self-closing — edit the .csf directly", n.Name.Local)
	}
	open := openTagEnd(seg)
	close := bytes.LastIndexByte(seg, '<')
	if open < 0 || close <= open {
		return 0, 0, fmt.Errorf("cannot locate the content of cell <%s>", n.Name.Local)
	}
	return n.Start + open + 1, n.Start + close, nil
}

// openTagEnd finds the '>' closing an element's open tag, skipping quoted
// attribute values.
func openTagEnd(seg []byte) int {
	var q byte
	for i := 0; i < len(seg); i++ {
		c := seg[i]
		switch {
		case q != 0:
			if c == q {
				q = 0
			}
		case c == '"' || c == '\'':
			q = c
		case c == '>':
			return i
		}
	}
	return -1
}

// nodeHasTable reports a nested <table> anywhere under n.
func nodeHasTable(n *csf.Node) bool {
	found := false
	csf.Walk(n, func(x *csf.Node) bool {
		if x != n && x.Type == csf.Element && x.Name.Space == "" && x.Name.Local == "table" {
			found = true
		}
		return !found
	})
	return found
}
