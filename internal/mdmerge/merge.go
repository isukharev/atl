// Package mdmerge implements the block-level three-way merge behind
// `conf apply`: it maps an edited markdown view back onto the pristine base
// CSF body. Untouched blocks keep their exact base bytes; only changed or new
// blocks are converted (internal/mdcsf); blocks whose text reappears verbatim
// elsewhere reuse their base bytes (a move). The merge is fail-closed: any
// edited block it cannot convert faithfully aborts the whole merge with a
// *BlockError — there are no partial results.
package mdmerge

import (
	"fmt"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/fragment"
	"github.com/isukharev/atl/internal/mdcsf"
	"github.com/isukharev/atl/internal/mirror"
)

// Options tune a merge.
type Options struct {
	// AllowFragmentLoss downgrades the removed-fragment gate from an error to
	// a report entry. `conf push --dry-run` remains the final gate.
	AllowFragmentLoss bool
}

// Report summarizes what the merge did, in blocks.
type Report struct {
	Unchanged int `json:"unchanged"` // base blocks kept byte-identical
	Moved     int `json:"moved"`     // base blocks reused verbatim at a new position
	Converted int `json:"converted"` // edited/new markdown blocks converted to CSF
	Removed   int `json:"removed"`   // base blocks with no counterpart in the edited md

	// RemovedFragments lists opaque fragments present in the base but absent
	// from the result (macros, mentions, links, images the edit dropped).
	RemovedFragments []domain.Ref `json:"removed_fragments,omitempty"`
	// Problems carries the validation diagnostics of the merged body.
	Problems []csf.Problem `json:"problems,omitempty"`
}

// BlockError reports an edited markdown block the merge cannot convert; the
// remedy is to make that edit on the .csf directly.
type BlockError struct {
	Block string // the offending markdown block (clipped)
	Err   error
}

func (e *BlockError) Error() string {
	return fmt.Sprintf("cannot convert edited block %q: %v — make this edit in the .csf directly", clipBlock(e.Block), e.Err)
}
func (e *BlockError) Unwrap() error { return e.Err }

// LossError reports fragments the edit would drop (gate not overridden).
type LossError struct {
	Removed []domain.Ref
}

func (e *LossError) Error() string {
	names := make([]string, 0, len(e.Removed))
	for _, r := range e.Removed {
		names = append(names, string(r.Kind)+":"+r.Display)
	}
	return fmt.Sprintf("edit removes %d opaque fragment(s): %s — restore the marker(s) in the .md, or pass --allow-fragment-loss",
		len(e.Removed), strings.Join(names, ", "))
}

// Merge maps editedMD onto the base CSF body. refs are the page's resolved
// fragments (from .meta.json) — they must be the ones the .md was rendered
// with, or unchanged-block detection degrades.
func Merge(base []byte, refs []domain.Ref, editedMD string, opts Options) ([]byte, *Report, error) {
	root, err := csf.Parse(base)
	if err != nil {
		return nil, nil, fmt.Errorf("base CSF does not parse: %w", err)
	}
	blocks, nodes := mirror.RenderBlockNodes(root, refs)

	// Base units: each block split into its markdown pieces (an unknown
	// wrapper element can render several paragraphs from one block).
	type baseUnit struct {
		text    string
		block   int
		piece   int
		matched int // edited index, -1 if none
	}
	var units []baseUnit
	pieces := make([]int, len(blocks))
	for i, b := range blocks {
		ps := mdcsf.SplitBlocks(b.MD)
		pieces[i] = len(ps)
		for j, p := range ps {
			units = append(units, baseUnit{text: strings.TrimSpace(p), block: i, piece: j, matched: -1})
		}
	}
	edited := mdcsf.SplitBlocks(editedMD)
	for i := range edited {
		edited[i] = strings.TrimSpace(edited[i])
	}

	baseTexts := make([]string, len(units))
	for i, u := range units {
		baseTexts[i] = u.text
	}
	baseMatch, editMatch := lcs(baseTexts, edited)
	for i := range units {
		units[i].matched = baseMatch[i]
	}

	// A block is kept only when all its pieces matched, in consecutive edited
	// positions (no insertions inside the block's span).
	kept := make([]bool, len(blocks))
	firstUnit := make([]int, len(blocks))
	{
		u := 0
		for i := range blocks {
			firstUnit[i] = u
			ok := true
			prev := -2
			for j := 0; j < pieces[i]; j++ {
				m := units[u+j].matched
				if m < 0 || (prev >= 0 && m != prev+1) {
					ok = false
				}
				prev = m
			}
			kept[i] = ok
			u += pieces[i]
		}
	}
	// A partially-matched multi-piece block means the agent edited inside an
	// unrecognized wrapper — refuse rather than dismantle the wrapper.
	for i := range blocks {
		if kept[i] || pieces[i] <= 1 {
			continue
		}
		for j := 0; j < pieces[i]; j++ {
			if units[firstUnit[i]+j].matched >= 0 {
				return nil, nil, &BlockError{
					Block: blocks[i].MD,
					Err:   fmt.Errorf("edit touches content inside an unrecognized wrapper element"),
				}
			}
		}
	}
	// Pieces of non-kept blocks become plain additions on the edited side.
	editKept := make([]int, len(edited)) // base unit index when the piece belongs to a kept block, else -1
	for e := range edited {
		editKept[e] = -1
		if b := editMatch[e]; b >= 0 && kept[units[b].block] {
			editKept[e] = b
		}
	}

	// Byte-reuse pool: single-piece base blocks that were not kept, keyed by
	// their exact markdown text (duplicates queue in document order).
	pool := map[string][]int{}
	for i := range blocks {
		if !kept[i] && pieces[i] == 1 {
			t := units[firstUnit[i]].text
			pool[t] = append(pool[t], i)
		}
	}
	reused := make(map[int]bool)

	markers := collectMarkers(nodes, kept, base, refs)

	// The md table syntax cannot express spans, cell styling, or wrapper
	// structure. If the edit drops a complex table (i.e. the agent edited
	// one), converting its replacement as a plain GFM table would silently
	// strip that structure — refuse table conversions in that merge.
	complexTableDropped := false
	for i, n := range nodes {
		if !kept[i] && blocks[i].Kind == "table" && hasComplexTable(n) {
			complexTableDropped = true
			break
		}
	}

	// Assemble the output: walk edited pieces in order, splicing base bytes
	// for kept blocks and buffering generated/reused bytes so insertions land
	// directly before the next kept block (inside the same container).
	rep := &Report{}
	var out []byte
	var pending []byte
	gapStart := 0
	nextBlock := 0
	// flushRun emits everything up to (not including) kept block j — the gaps
	// of dropped blocks and the buffered replacement bytes. Replacements take
	// the slot of the first dropped block so they stay inside its container
	// (layout cell); pure insertions land just before the next kept block.
	flushRun := func(j int) {
		flushed := false
		for k := nextBlock; k < j; k++ {
			out = append(out, base[gapStart:blocks[k].CSFStart]...)
			if !flushed {
				out = append(out, pending...)
				pending = nil
				flushed = true
			}
			gapStart = blocks[k].CSFEnd
		}
		if j < len(blocks) {
			out = append(out, base[gapStart:blocks[j].CSFStart]...)
		}
		if !flushed {
			out = append(out, pending...)
			pending = nil
		}
		nextBlock = j
	}
	for e := 0; e < len(edited); e++ {
		if u := editKept[e]; u >= 0 {
			bi := units[u].block
			if units[u].piece != 0 {
				continue // later piece of an already-emitted kept block
			}
			flushRun(bi)
			out = append(out, base[blocks[bi].CSFStart:blocks[bi].CSFEnd]...)
			gapStart = blocks[bi].CSFEnd
			nextBlock = bi + 1
			rep.Unchanged++
			continue
		}
		txt := edited[e]
		if q := pool[txt]; len(q) > 0 { // verbatim text seen in a dropped block: move, reuse bytes
			bi := q[0]
			pool[txt] = q[1:]
			reused[bi] = true
			pending = append(pending, base[blocks[bi].CSFStart:blocks[bi].CSFEnd]...)
			rep.Moved++
			continue
		}
		if complexTableDropped && strings.HasPrefix(txt, "|") {
			return nil, nil, &BlockError{Block: txt, Err: fmt.Errorf(
				"the edited table uses spans/styling/nested structure the md surface cannot express")}
		}
		conv, err := convertBlock(txt, markers)
		if err != nil {
			return nil, nil, &BlockError{Block: txt, Err: err}
		}
		pending = append(pending, conv...)
		rep.Converted++
	}
	flushRun(len(blocks))
	out = append(out, base[gapStart:]...)

	for i := range blocks {
		if !kept[i] && !reused[i] {
			rep.Removed++
		}
	}

	// Validity gate: a merge must never produce a body that cannot be pushed.
	rep.Problems = csf.Validate(out)
	if csf.HasErrors(rep.Problems) {
		return nil, rep, fmt.Errorf("merged body is not well-formed CSF (this is a bug in the merge): %s", rep.Problems[0].Message)
	}

	// Loss gate: fragments present in base but gone from the result.
	rep.RemovedFragments = removedFragments(root, out)
	if len(rep.RemovedFragments) > 0 && !opts.AllowFragmentLoss {
		return nil, rep, &LossError{Removed: rep.RemovedFragments}
	}
	return out, rep, nil
}

// removedFragments diffs opaque content base→result: the registry fragments
// (drawio/user/attachment/page-link/image, by kind+key) plus every macro by
// name — fragment extraction does not cover generic macros, but dropping one
// (toc, jira, status, include…) is exactly the loss this gate exists for.
func removedFragments(baseRoot *csf.Node, result []byte) []domain.Ref {
	resRoot, err := csf.Parse(result)
	if err != nil {
		return nil
	}
	have := map[string]int{}
	for _, r := range fragment.Extract(resRoot) {
		have[string(r.Kind)+"\x00"+r.Key]++
	}
	for name, c := range macroCounts(resRoot) {
		have["macro\x00"+name] = c
	}
	var removed []domain.Ref
	for _, r := range fragment.Extract(baseRoot) {
		k := string(r.Kind) + "\x00" + r.Key
		if have[k] > 0 {
			have[k]--
			continue
		}
		removed = append(removed, r)
	}
	for name, c := range macroCounts(baseRoot) {
		k := "macro\x00" + name
		missing := c - have[k]
		for i := 0; i < missing; i++ {
			removed = append(removed, domain.Ref{Kind: "macro", Key: name, Display: name})
		}
	}
	sort.Slice(removed, func(i, j int) bool {
		if removed[i].Kind != removed[j].Kind {
			return removed[i].Kind < removed[j].Kind
		}
		return removed[i].Key < removed[j].Key
	})
	return removed
}

// hasComplexTable reports a table whose structure the md surface cannot
// carry: row/col spans, styled or classed cells, or nested tables.
func hasComplexTable(n *csf.Node) bool {
	complexTable := false
	tables := 0
	csf.Walk(n, func(x *csf.Node) bool {
		if complexTable {
			return false
		}
		if x.Name.Space != "" {
			return true
		}
		switch x.Name.Local {
		case "table":
			tables++
			if tables > 1 {
				complexTable = true
				return false
			}
		case "td", "th":
			for _, a := range []string{"rowspan", "colspan", "class", "style"} {
				if v := x.Attrv("", a); v != "" && v != "1" {
					complexTable = true
					return false
				}
			}
		}
		return true
	})
	return complexTable
}

func macroCounts(root *csf.Node) map[string]int {
	counts := map[string]int{}
	csf.Walk(root, func(n *csf.Node) bool {
		if name := n.MacroName(); name != "" {
			counts[name]++
		}
		return true
	})
	return counts
}

func clipBlock(s string) string {
	r := []rune(s)
	if len(r) > 120 {
		return string(r[:120]) + "…"
	}
	return s
}
