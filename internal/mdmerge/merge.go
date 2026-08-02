// Package mdmerge implements the block-level three-way merge behind
// `conf apply`: it maps an edited markdown view back onto the pristine base
// CSF body. Untouched blocks keep their exact base bytes; only changed or new
// blocks are converted (internal/mdcsf); blocks whose text reappears verbatim
// elsewhere reuse their base bytes (a move). The merge is fail-closed: any
// edited block it cannot convert faithfully aborts the whole merge with a
// *BlockError — there are no partial results.
package mdmerge

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/fragment"
	"github.com/isukharev/atl/internal/mdcsf"
	"github.com/isukharev/atl/internal/mirror"
	"github.com/isukharev/atl/internal/wikiscanner"
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
	// MergedTables counts complex tables merged row/cell-wise: untouched rows
	// kept their bytes, only edited cells were converted.
	MergedTables int `json:"merged_tables,omitempty"`

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
	baseMatch, editMatch, aligned := lcs(baseTexts, edited)
	if !aligned {
		return nil, nil, fmt.Errorf("%w: Markdown alignment exceeds the bounded safety budget; edit the native .csf directly", domain.ErrCheckFailed)
	}
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

	// A dropped complex table is not converted from md like other blocks — it
	// is merged row/cell-wise against its base bytes (tablemerge.go). Its
	// opaque fragments therefore stay out of the global marker pool: most of
	// the table's bytes survive in place, so treating its macros/mentions as
	// relocatable would duplicate them elsewhere on the page.
	var droppedComplexTables, droppedSimpleTables []int
	markerKept := append([]bool(nil), kept...)
	for i, n := range nodes {
		if kept[i] || blocks[i].Kind != "table" {
			continue
		}
		if hasComplexTable(n) {
			droppedComplexTables = append(droppedComplexTables, i)
			markerKept[i] = true
		} else {
			droppedSimpleTables = append(droppedSimpleTables, i)
		}
	}
	markers := collectMarkers(nodes, markerKept, base, refs)

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
		if strings.HasPrefix(txt, "|") && len(droppedComplexTables) > 0 {
			if bi, ok := pickTableCandidate(droppedComplexTables, reused, blocks, txt); ok {
				merged, err := mergeTable(base, nodes[bi], refs, txt)
				if err != nil {
					return nil, nil, &BlockError{Block: txt, Err: err}
				}
				reused[bi] = true
				removeFromPool(pool, units[firstUnit[bi]].text, bi)
				pending = append(pending, merged...)
				rep.MergedTables++
				continue
			}
			// An edit of a dropped *simple* table converts wholesale below; a
			// table sharing rows with nothing while a complex table was
			// dropped is that table rewritten beyond recognition — converting
			// it would silently strip the structure.
			if _, ok := pickTableCandidate(droppedSimpleTables, reused, blocks, txt); !ok {
				return nil, nil, &BlockError{Block: txt, Err: fmt.Errorf(
					"the edited table uses spans/styling/nested structure the md surface cannot express")}
			}
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
		if r.Kind == domain.RefPageLink {
			continue // counted below with space + target + label identity
		}
		have[string(r.Kind)+"\x00"+r.Key]++
	}
	for name, c := range macroCounts(resRoot) {
		have["macro\x00"+name] = c
	}
	for signature, item := range protectedInlineInventory(resRoot) {
		have["protected\x00"+signature] = item.count
	}
	var removed []domain.Ref
	for _, r := range fragment.Extract(baseRoot) {
		if r.Kind == domain.RefPageLink {
			continue // counted below with space + target + label identity
		}
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
	for signature, item := range protectedInlineInventory(baseRoot) {
		k := "protected\x00" + signature
		missing := item.count - have[k]
		for i := 0; i < missing; i++ {
			removed = append(removed, item.ref)
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

// hasComplexTable reports a table whose native shape the md→CSF table
// converter cannot reproduce. A wholesale conversion is safe only for the
// converter's canonical shape: an attribute-free table containing exactly one
// attribute-free tbody, an all-th first row, and all-td later rows. Everything
// else is routed through the byte-preserving row/cell merge.
func hasComplexTable(n *csf.Node) bool {
	return !hasReproducibleTableShape(n)
}

func hasReproducibleTableShape(table *csf.Node) bool {
	if table == nil || table.Type != csf.Element || table.Name.Space != "" || table.Name.Local != "table" || len(table.Attr) != 0 {
		return false
	}
	var tbody *csf.Node
	for _, child := range table.Children {
		if child.Type != csf.Element {
			continue
		}
		if child.Name.Space != "" || child.Name.Local != "tbody" || tbody != nil || len(child.Attr) != 0 {
			return false
		}
		tbody = child
	}
	if tbody == nil {
		return false
	}
	var rows []*csf.Node
	for _, child := range tbody.Children {
		if child.Type != csf.Element {
			continue
		}
		if child.Name.Space != "" || child.Name.Local != "tr" || len(child.Attr) != 0 {
			return false
		}
		rows = append(rows, child)
	}
	if len(rows) == 0 {
		return false
	}
	for ri, row := range rows {
		cells := 0
		for _, child := range row.Children {
			if child.Type != csf.Element {
				continue
			}
			if child.Name.Space != "" || (child.Name.Local != "td" && child.Name.Local != "th") || len(child.Attr) != 0 {
				return false
			}
			if ri == 0 && child.Name.Local != "th" || ri > 0 && child.Name.Local != "td" {
				return false
			}
			if nodeHasTable(child) {
				return false
			}
			if cellHasNonCanonicalTableContent(child) {
				return false
			}
			cells++
		}
		if cells == 0 {
			return false
		}
	}
	return true
}

// removeFromPool drops one specific block from a byte-reuse queue (it was
// consumed by a table merge and must not be emitted a second time as a move).
func removeFromPool(pool map[string][]int, key string, bi int) {
	q := pool[key]
	for i, v := range q {
		if v == bi {
			pool[key] = append(q[:i:i], q[i+1:]...)
			return
		}
	}
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

type protectedInlineItem struct {
	count int
	ref   domain.Ref
}

// protectedInlineInventory covers identity that fragment.Extract cannot
// represent precisely enough for a loss gate. The signature hashes the complete
// parsed subtree (element names, ordered attributes, text and descendants), so
// rich labels and same-color spans with different content cannot mask loss.
func protectedInlineInventory(root *csf.Node) map[string]protectedInlineItem {
	items := map[string]protectedInlineItem{}
	csf.Walk(root, func(n *csf.Node) bool {
		collectProtectedMacroMetadata(items, n)
		collectProtectedStructure(items, n)
		switch {
		case n.Name.Space == "ac" && n.Name.Local == "link":
			var title, space, label string
			csf.Walk(n, func(x *csf.Node) bool {
				switch {
				case x.Name.Space == "ri" && x.Name.Local == "page":
					title = x.Attrv("ri", "content-title")
					space = x.Attrv("ri", "space-key")
				case x.Name.Space == "ac" && (x.Name.Local == "link-body" || x.Name.Local == "plain-text-link-body"):
					label = csf.TextContent(x)
				}
				return true
			})
			if title != "" {
				if label == "" {
					label = title
				}
				key := title
				if space != "" {
					key = space + "/" + key
				}
				if label != title {
					key += " (" + label + ")"
				}
				signature := string(domain.RefPageLink) + "\x00" + protectedNodeHash(n)
				item := items[signature]
				item.count++
				item.ref = domain.Ref{Kind: domain.RefPageLink, Key: key, Display: label}
				items[signature] = item
			}
		case n.Name.Space == "" && n.Name.Local == "span":
			if color := protectedSpanColor(n); color != "" {
				signature := "color\x00" + protectedNodeHash(n)
				item := items[signature]
				item.count++
				item.ref = domain.Ref{Kind: "color", Key: color, Display: color}
				items[signature] = item
			}
		case n.Name.Space == "ac" && n.Name.Local == "inline-comment-marker":
			ref := n.Attrv("ac", "ref")
			signature := "inline-comment-marker\x00" + protectedNodeHash(n)
			item := items[signature]
			item.count++
			item.ref = domain.Ref{Kind: "inline-comment-marker", Key: ref, Display: csf.TextContent(n)}
			items[signature] = item
		}
		return true
	})
	return items
}

// collectProtectedMacroMetadata inventories only macro bytes that the
// Markdown representation cannot reproduce. The common structured code
// language parameter is representable when it is a safe fence info string;
// macro ids, other parameters, legacy macro tags, and unsafe language values
// remain explicitly loss-gated. Values are hashed and never exposed.
func collectProtectedMacroMetadata(items map[string]protectedInlineItem, n *csf.Node) {
	if n.Type != csf.Element || n.Name.Space != "ac" ||
		(n.Name.Local != "structured-macro" && n.Name.Local != "macro") {
		return
	}
	macroName := n.Attrv("ac", "name")
	parts := make([]string, 0, len(n.Attr)+4)
	if n.Name.Local != "structured-macro" {
		parts = append(parts, "element="+n.Name.String())
	}
	for _, attr := range n.Attr {
		if attr.Name.Space == "ac" && attr.Name.Local == "name" {
			continue
		}
		parts = append(parts, "attr:"+attr.Name.String()+"="+attr.Value)
	}
	for _, child := range n.Children {
		if child.Type != csf.Element || child.Name.Space != "ac" || child.Name.Local != "parameter" {
			continue
		}
		name := child.Attrv("ac", "name")
		value := csf.TextContent(child)
		if macroName == "code" && name == "language" && canonicalCodeLanguageParameter(child, value) {
			continue
		}
		parts = append(parts, "parameter:"+name+"="+value)
	}
	if len(parts) == 0 {
		return
	}
	sort.Strings(parts)
	shape := protectedHashParts(append([]string{macroName}, parts...)...)
	signature := "structure\x00macro-metadata\x00" + shape
	item := items[signature]
	item.count++
	item.ref = domain.Ref{Kind: "structure", Key: "macro-metadata:" + shape, Display: "macro metadata"}
	items[signature] = item
}

func canonicalCodeLanguageParameter(parameter *csf.Node, value string) bool {
	if len(parameter.Attr) != 1 || parameter.Attr[0].Name.Space != "ac" || parameter.Attr[0].Name.Local != "name" ||
		len(parameter.Children) != 1 || parameter.Children[0].Type != csf.Text {
		return false
	}
	normalized, ok := wikiscanner.NormalizeMarkdownFenceInfo(value)
	return ok && normalized == value && parameter.Children[0].Data == value
}

// collectProtectedStructure inventories structure that the Markdown surface
// cannot express. Signatures contain element names and attributes, never cell
// or caption prose, so an edit spliced into a preserved wrapper does not look
// like structural loss.
func collectProtectedStructure(items map[string]protectedInlineItem, n *csf.Node) {
	if n.Type != csf.Element || n.Name.Space != "" {
		return
	}
	add := func(kind, display, shape string) {
		signature := "structure\x00" + kind + "\x00" + shape
		item := items[signature]
		item.count++
		item.ref = domain.Ref{Kind: "structure", Key: kind + ":" + shape, Display: display}
		items[signature] = item
	}
	switch n.Name.Local {
	case "br":
		add("br", "<br>", protectedElementShapeHash(n))
	case "caption", "colgroup", "col", "thead", "tbody", "tfoot":
		add(n.Name.Local, "<"+n.Name.Local+">", protectedElementShapeHash(n))
	}
	// Attributes on ordinary HTML elements are native structure unless the
	// Markdown conversion reproduces them. Inventorying all of them keeps a
	// prose edit from silently dropping, for example, <p style>, <div class>,
	// or non-href link metadata. Opaque substitution and table splicing retain
	// the same signature, so only an actual loss reaches the gate.
	if len(n.Attr) > 0 {
		add(n.Name.Local+"-attributes", "<"+n.Name.Local+"> attributes", protectedElementShapeHash(n))
	}
	if n.Name.Local == "table" {
		for _, topology := range protectedTableTopologies(n) {
			add("table-topology", "table topology", protectedHashParts(topology))
		}
	}
}

// protectedElementShapeHash hashes only an element's qualified name and
// attributes. Attribute order is not structural, so attributes are sorted.
func protectedElementShapeHash(n *csf.Node) string {
	parts := make([]string, 0, len(n.Attr))
	for _, attr := range n.Attr {
		parts = append(parts, attr.Name.String()+"="+attr.Value)
	}
	sort.Strings(parts)
	return protectedHashParts(append([]string{n.Name.String()}, parts...)...)
}

// protectedTableTopologies returns only the noncanonical topology that a GFM
// table cannot encode. Ordinary data-row insertion/deletion is intentionally
// absent: it is visible in Markdown and is supported by the row merge.
func protectedTableTopologies(table *csf.Node) []string {
	var topologies []string
	for _, child := range table.Children {
		if child.Type != csf.Element || child.Name.Space != "" {
			continue
		}
		switch child.Name.Local {
		case "tr":
			if len(topologies) == 0 || topologies[len(topologies)-1] != "direct-rows" {
				topologies = append(topologies, "direct-rows")
			}
		case "caption", "colgroup", "thead", "tbody", "tfoot":
			// These have their own inventory entries.
		default:
			topologies = append(topologies, "table-child:"+child.Name.Local)
		}
	}
	var rows []*csf.Node
	csf.Walk(table, func(n *csf.Node) bool {
		if n != table && n.Name.Space == "" && n.Name.Local == "table" {
			return false
		}
		if n.Name.Space == "" && n.Name.Local == "tr" {
			rows = append(rows, n)
			return false
		}
		return true
	})
	for ri, row := range rows {
		var cellTypes []string
		allTH, hasTH := true, false
		for _, child := range row.Children {
			if child.Type != csf.Element || child.Name.Space != "" ||
				(child.Name.Local != "td" && child.Name.Local != "th") {
				continue
			}
			cellTypes = append(cellTypes, child.Name.Local)
			hasTH = hasTH || child.Name.Local == "th"
			allTH = allTH && child.Name.Local == "th"
		}
		shape := strings.Join(cellTypes, ",")
		if ri == 0 && (len(cellTypes) == 0 || !allTH) {
			topologies = append(topologies, "first-row:"+shape)
		}
		if ri > 0 && hasTH {
			topologies = append(topologies, "later-row:"+shape)
		}
		for _, child := range row.Children {
			if child.Type != csf.Element || child.Name.Space != "" ||
				(child.Name.Local != "td" && child.Name.Local != "th") {
				continue
			}
			for _, wrapper := range tableCellWrapperShapes(child) {
				topologies = append(topologies, "cell-wrapper:"+wrapper)
			}
		}
	}
	return topologies
}

func cellHasNonCanonicalTableContent(cell *csf.Node) bool {
	return len(tableCellWrapperShapes(cell)) != 0
}

// tableCellWrapperShapes records native element kinds the Markdown table
// converter cannot reproduce, never prose or attribute values. Each wrapper
// is an independent multiset item: adding or moving opaque content does not
// make a retained p/div look removed, while flattening any wrapper still does.
func tableCellWrapperShapes(cell *csf.Node) []string {
	var shapes []string
	csf.Walk(cell, func(n *csf.Node) bool {
		if n != cell && n.Type == csf.Element && !isCanonicalTableInlineElement(n) {
			shapes = append(shapes, n.Name.String())
		}
		return true
	})
	return shapes
}

func isCanonicalTableInlineElement(n *csf.Node) bool {
	if n.Name.Space != "" {
		return false
	}
	switch n.Name.Local {
	case "strong", "em", "s", "code":
		return len(n.Attr) == 0
	case "a":
		return len(n.Attr) == 1 && n.Attr[0].Name.Space == "" && n.Attr[0].Name.Local == "href"
	}
	return false
}

func protectedHashParts(parts ...string) string {
	var b strings.Builder
	for _, part := range parts {
		fmt.Fprintf(&b, "%d:", len(part))
		b.WriteString(part)
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(b.String())))
}

func protectedNodeHash(root *csf.Node) string {
	var b strings.Builder
	var writePart = func(s string) {
		fmt.Fprintf(&b, "%d:", len(s))
		b.WriteString(s)
	}
	var walk func(*csf.Node)
	walk = func(n *csf.Node) {
		fmt.Fprintf(&b, "%d;", n.Type)
		writePart(n.Name.Space)
		writePart(n.Name.Local)
		fmt.Fprintf(&b, "%d;", len(n.Attr))
		for _, attr := range n.Attr {
			writePart(attr.Name.Space)
			writePart(attr.Name.Local)
			writePart(attr.Value)
		}
		writePart(n.Data)
		fmt.Fprintf(&b, "%d;", len(n.Children))
		for _, child := range n.Children {
			walk(child)
		}
	}
	walk(root)
	return fmt.Sprintf("%x", sha256.Sum256([]byte(b.String())))
}

func protectedSpanColor(n *csf.Node) string {
	if color := strings.TrimSpace(n.Attrv("", "data-color")); color != "" {
		return color
	}
	for _, decl := range strings.Split(n.Attrv("", "style"), ";") {
		key, value, ok := strings.Cut(decl, ":")
		if ok && strings.EqualFold(strings.TrimSpace(key), "color") {
			if color := strings.TrimSpace(value); color != "" {
				return color
			}
		}
	}
	return ""
}

func clipBlock(s string) string {
	r := []rune(s)
	if len(r) > 120 {
		return string(r[:120]) + "…"
	}
	return s
}
