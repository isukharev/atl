package mirror

import (
	"strconv"
	"strings"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
)

func (r *mdRenderer) table(b *strings.Builder, n *csf.Node) {
	grid, _, header := r.tableGrid(n)
	if len(grid) == 0 {
		return
	}
	width := len(grid[0])
	for ri, row := range grid {
		cells := make([]string, len(row))
		for i, c := range row {
			cells[i] = strings.ReplaceAll(c.Text, "|", "\\|")
		}
		b.WriteString("| " + strings.Join(cells, " | ") + " |\n")
		if (r.format == confluenceMarkdownCurrent && ri == 0) ||
			(r.format == confluenceMarkdownV5 && (ri == header || (header < 0 && ri == 0))) {
			seps := make([]string, width)
			for i := range seps {
				seps[i] = "---"
			}
			b.WriteString("| " + strings.Join(seps, " | ") + " |\n")
		}
	}
	b.WriteString("\n")
}

// TableCell is one slot of a table's md view: the owning td/th node (nil for
// width padding), whether the slot is the cell's top-left origin (span
// continuations and padding are not), and the exact text the .md table view
// renders there (before pipe escaping).
type TableCell struct {
	Node   *csf.Node
	Origin bool
	Text   string
}

// TableGrid exposes the md-view grid of a <table> node — one slice per md
// table row (all padded to uniform width), the parallel <tr> source nodes,
// and the header row index (-1 when no row holds a <th>). The md→CSF table
// merge aligns against this grid, so it must stay the single source of what
// the .md view shows (the renderer itself draws from it).
func TableGrid(table *csf.Node, refs []domain.Ref) ([][]TableCell, []*csf.Node, int) {
	r := newMDRenderer(refs)
	return r.tableGrid(table)
}

func (r *mdRenderer) tableGrid(n *csf.Node) ([][]TableCell, []*csf.Node, int) {
	var grid [][]TableCell
	var trs []*csf.Node
	header := -1

	pending := map[int]pendingCell{}
	for _, tr := range tableRows(n) {
		var cells []TableCell
		isHeader := false
		col := 0
		for _, c := range rowCells(tr) {
			for {
				if p, ok := pending[col]; ok {
					cells = append(cells, TableCell{Node: p.node, Text: p.text})
					p.rows--
					if p.rows <= 0 {
						delete(pending, col)
					} else {
						pending[col] = p
					}
					col++
					continue
				}
				break
			}
			if c.Name.Local == "th" {
				isHeader = true
			}
			text := strings.TrimSpace(r.inline(c))
			colspan := colspanOfVersion(c, r.format)
			rowspan := rowspanOfVersion(c, r.format)
			for spanCol := 0; spanCol < colspan; spanCol++ {
				cellText := text
				if spanCol > 0 {
					cellText = ""
				}
				cells = append(cells, TableCell{Node: c, Origin: spanCol == 0, Text: cellText})
				if rowspan > 1 {
					pending[col] = pendingCell{node: c, text: cellText, rows: rowspan - 1}
				}
				col++
			}
		}
		for col <= maxPendingCol(pending) {
			if p, ok := pending[col]; ok {
				cells = append(cells, TableCell{Node: p.node, Text: p.text})
				p.rows--
				if p.rows <= 0 {
					delete(pending, col)
				} else {
					pending[col] = p
				}
			} else {
				cells = append(cells, TableCell{})
			}
			col++
		}
		if isHeader && header < 0 {
			header = len(grid)
		}
		grid = append(grid, cells)
		trs = append(trs, tr)
	}
	width := 0
	for _, row := range grid {
		if len(row) > width {
			width = len(row)
		}
	}
	for i, row := range grid {
		for len(row) < width {
			row = append(row, TableCell{})
		}
		grid[i] = row
	}
	return grid, trs, header
}

type pendingCell struct {
	node *csf.Node
	text string
	rows int
}

func tableRows(table *csf.Node) []*csf.Node {
	var out []*csf.Node
	csf.Walk(table, func(x *csf.Node) bool {
		if x != table && x.Name.Local == "table" && x.Name.Space == "" {
			return false
		}
		if x.Name.Local == "tr" && x.Name.Space == "" {
			out = append(out, x)
			return false
		}
		return true
	})
	return out
}

func rowCells(row *csf.Node) []*csf.Node {
	var out []*csf.Node
	for _, c := range row.Children {
		if c.Type == csf.Element && c.Name.Space == "" && (c.Name.Local == "td" || c.Name.Local == "th") {
			out = append(out, c)
		}
	}
	return out
}

func maxPendingCol(pending map[int]pendingCell) int {
	max := -1
	for col := range pending {
		if col > max {
			max = col
		}
	}
	return max
}

func colspanOfVersion(cell *csf.Node, format confluenceMarkdownFormat) int {
	if format == confluenceMarkdownCurrent {
		return csf.TableSpan(cell, "colspan")
	}
	if n, err := strconv.Atoi(cell.Attrv("", "colspan")); err == nil && n > 1 {
		return min(n, csf.MaxTableSpan)
	}
	return 1
}

func rowspanOfVersion(cell *csf.Node, format confluenceMarkdownFormat) int {
	if format == confluenceMarkdownCurrent {
		return csf.TableSpan(cell, "rowspan")
	}
	if n, err := strconv.Atoi(cell.Attrv("", "rowspan")); err == nil && n > 1 {
		return min(n, csf.MaxTableSpan)
	}
	return 1
}
