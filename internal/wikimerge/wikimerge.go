// Package wikimerge implements the block-level three-way merge behind
// `jira apply`: it maps an edited markdown description view back onto the
// pristine base Jira wiki body. It is the Jira analog of internal/mdmerge (the
// Confluence `conf apply` merge), but simpler because a wiki body is flat and
// line-oriented rather than a CSF DOM.
//
// Untouched blocks keep their exact base bytes (so an unchanged view round-trips
// to the base byte-for-byte); changed or new markdown blocks are converted via
// the fail-closed internal/mdwiki; a base block whose rendered text reappears
// elsewhere reuses its base bytes (a move). The merge is fail-closed: any edited
// block mdwiki cannot convert aborts the whole merge with a *BlockError, and any
// wiki construct present in the base but absent from the result (`{panel}`,
// `{color}`, mentions, `!embeds!`, macros) is a *LossError unless AllowLoss.
package wikimerge

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/isukharev/atl/internal/mdwiki"
	"github.com/isukharev/atl/internal/wikimd"
)

// Options tune a merge.
type Options struct {
	// AllowLoss downgrades the removed-construct gate from an error to a report
	// entry (the `--allow-loss` flag). `jira push --dry-run` remains the final
	// consequence preview.
	AllowLoss bool
	// Images maps a Jira image-embed filename to its local relative path, exactly
	// as renderIssueMarkdown resolves `!name.png!` embeds. It must match the map
	// the pristine `.md` view was rendered with, or unchanged-block detection for
	// blocks containing an image embed degrades.
	Images map[string]string
}

// Construct is one notable wiki construct dropped by an edit (a panel, color
// span, mention, image embed, macro, …). Kind classifies it; Text is the
// offending token (clipped).
type Construct struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// Report summarizes what the merge did, in blocks.
type Report struct {
	Unchanged int `json:"unchanged"` // base blocks kept byte-identical
	Moved     int `json:"moved"`     // base blocks reused verbatim at a new position
	Converted int `json:"converted"` // edited/new markdown blocks converted to wiki
	Removed   int `json:"removed"`   // base blocks with no counterpart in the edited md
	// RemovedConstructs lists wiki constructs present in the base but absent from
	// the merged result (macros, mentions, embeds, colored spans an edit dropped).
	RemovedConstructs []Construct `json:"removed_constructs,omitempty"`
}

// BlockError reports an edited markdown block the merge cannot convert to wiki;
// the remedy is to make that edit on the `.wiki` directly.
type BlockError struct {
	Block string // the offending markdown block (clipped)
	Err   error
}

func (e *BlockError) Error() string {
	return fmt.Sprintf("cannot convert edited block %q: %v — make this edit in the .wiki directly", clip(e.Block), e.Err)
}
func (e *BlockError) Unwrap() error { return e.Err }

// LossError reports wiki constructs the edit would drop (gate not overridden).
type LossError struct {
	Removed []Construct
}

func (e *LossError) Error() string {
	names := make([]string, 0, len(e.Removed))
	for _, c := range e.Removed {
		names = append(names, c.Kind+":"+c.Text)
	}
	return fmt.Sprintf("edit removes %d wiki construct(s): %s — restore them in the .md, edit the .wiki directly, or pass --allow-loss",
		len(e.Removed), strings.Join(names, ", "))
}

// Merge maps editedDescMD onto the base Jira wiki body, block by block. The
// return is (mergedWiki, report, error): on a fail-closed refusal it returns a
// nil body with (for a LossError) the populated report so the caller can show
// what would be dropped, mirroring internal/mdmerge.
func Merge(baseWiki []byte, editedDescMD string, opts Options) ([]byte, *Report, error) {
	base := string(baseWiki)
	blocks := scanWikiBlocks(base)

	// Base units: each base block rendered to its markdown piece(s) via wikimd
	// (the same renderer the read view uses). A wiki block almost always renders
	// to exactly one markdown block; the multi-piece handling mirrors mdmerge and
	// keeps an exotic renderer that splits one block into several honest.
	type unit struct {
		text  string
		block int
		piece int
	}
	var units []unit
	pieces := make([]int, len(blocks))
	firstUnit := make([]int, len(blocks))
	for bi, blk := range blocks {
		firstUnit[bi] = len(units)
		md := wikimd.Render(base[blk.start:blk.end], wikimd.Options{Images: opts.Images})
		ps := splitMDBlocks(md)
		if len(ps) == 0 {
			// A block that renders to nothing still needs a unit so an unchanged
			// view can keep it; fall back to its raw wiki text.
			ps = []string{strings.TrimSpace(base[blk.start:blk.end])}
		}
		pieces[bi] = len(ps)
		for pj, p := range ps {
			units = append(units, unit{text: p, block: bi, piece: pj})
		}
	}
	edited := splitMDBlocks(editedDescMD)

	baseTexts := make([]string, len(units))
	for i, u := range units {
		baseTexts[i] = u.text
	}
	baseMatch, editMatch := lcs(baseTexts, edited)

	// A block is kept only when all its pieces matched, in consecutive edited
	// positions (no insertion inside the block's span).
	kept := make([]bool, len(blocks))
	for bi := range blocks {
		ok := pieces[bi] > 0
		prev := -2
		for pj := 0; pj < pieces[bi]; pj++ {
			m := baseMatch[firstUnit[bi]+pj]
			if m < 0 || (prev >= 0 && m != prev+1) {
				ok = false
			}
			prev = m
		}
		kept[bi] = ok
	}
	// A partially-matched multi-piece block means the edit reached inside a
	// construct that renders to several markdown blocks — refuse rather than
	// dismantle it (mdmerge parity).
	for bi := range blocks {
		if kept[bi] || pieces[bi] <= 1 {
			continue
		}
		for pj := 0; pj < pieces[bi]; pj++ {
			if baseMatch[firstUnit[bi]+pj] >= 0 {
				return nil, nil, &BlockError{
					Block: units[firstUnit[bi]].text,
					Err:   fmt.Errorf("edit touches content inside a multi-block wiki construct"),
				}
			}
		}
	}

	// Byte-reuse pool: single-piece base blocks that were not kept, keyed by their
	// exact rendered markdown (duplicates queue in document order) — an edited
	// block whose text matches one is a move that reuses the base bytes.
	pool := map[string][]int{}
	for bi := range blocks {
		if !kept[bi] && pieces[bi] == 1 {
			pool[units[firstUnit[bi]].text] = append(pool[units[firstUnit[bi]].text], bi)
		}
	}
	reused := make(map[int]bool)

	rep := &Report{}
	var plan []planItem
	for e := 0; e < len(edited); e++ {
		if u := editMatch[e]; u >= 0 && kept[units[u].block] {
			bi := units[u].block
			if units[u].piece != 0 {
				continue // later piece of an already-planned kept block
			}
			plan = append(plan, planItem{raw: true, bi: bi})
			rep.Unchanged++
			continue
		}
		txt := edited[e]
		if q := pool[txt]; len(q) > 0 { // rendered text seen in a dropped block: reuse bytes
			bi := q[0]
			pool[txt] = q[1:]
			reused[bi] = true
			plan = append(plan, planItem{raw: true, bi: bi})
			rep.Moved++
			continue
		}
		conv, err := mdwiki.ConvertBlock(txt)
		if err != nil {
			return nil, nil, &BlockError{Block: txt, Err: err}
		}
		plan = append(plan, planItem{conv: []byte(conv)})
		rep.Converted++
	}
	for bi := range blocks {
		if !kept[bi] && !reused[bi] {
			rep.Removed++
		}
	}

	out := assemble(base, blocks, plan)

	// Loss gate: constructs present in the base but gone from the result. Diffing
	// base→result globally (rather than per block) naturally covers dropped blocks,
	// edited-out constructs, and moves (a moved block keeps its bytes, so its
	// constructs survive in the result and are not flagged) — mdmerge parity.
	rep.RemovedConstructs = removedConstructs(base, out)
	if len(rep.RemovedConstructs) > 0 && !opts.AllowLoss {
		return nil, rep, &LossError{Removed: rep.RemovedConstructs}
	}
	return []byte(out), rep, nil
}

// planItem is one emitted unit: either a base block (kept or moved) reproduced
// from its exact bytes, or a converted markdown block.
type planItem struct {
	raw  bool
	bi   int    // base block index when raw
	conv []byte // converted wiki bytes when !raw
}

// assemble stitches the plan back into a wiki body. Maximal runs of raw base
// blocks whose indices are consecutive are emitted as a single original byte
// range so their inter-block whitespace is preserved verbatim; leading/trailing
// whitespace outside the first/last block is preserved when the corresponding
// edge block is emitted at the corresponding edge. This is what makes an
// all-unchanged plan reproduce baseWiki byte-for-byte. Every other boundary
// (raw↔raw non-consecutive, raw↔conv, conv↔conv) is joined by a single blank
// line, and converted blocks are emitted trimmed of trailing newlines.
func assemble(base string, blocks []block, plan []planItem) string {
	if len(plan) == 0 {
		// Nothing to emit. With no content blocks the base is pure whitespace and
		// is preserved verbatim (a vacuous round-trip); otherwise every base block
		// was dropped, i.e. the description was emptied.
		if len(blocks) == 0 {
			return base
		}
		return ""
	}
	var out []byte
	emitted := false
	i := 0
	for i < len(plan) {
		if plan[i].raw {
			j := i
			for j+1 < len(plan) && plan[j+1].raw && plan[j+1].bi == plan[j].bi+1 {
				j++
			}
			biStart, biEnd := plan[i].bi, plan[j].bi
			switch {
			case !emitted && biStart == 0:
				out = append(out, base[:blocks[0].start]...) // leading gap
			case emitted:
				out = append(out, "\n\n"...)
			}
			out = append(out, base[blocks[biStart].start:blocks[biEnd].end]...)
			if j == len(plan)-1 && biEnd == len(blocks)-1 {
				out = append(out, base[blocks[biEnd].end:]...) // trailing gap
			}
			emitted = true
			i = j + 1
			continue
		}
		if emitted {
			out = append(out, "\n\n"...)
		}
		out = append(out, bytes.TrimRight(plan[i].conv, "\n")...)
		emitted = true
		i++
	}
	return string(out)
}

func clip(s string) string {
	r := []rune(s)
	if len(r) > 120 {
		return string(r[:120]) + "…"
	}
	return s
}
