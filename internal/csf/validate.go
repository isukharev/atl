package csf

import (
	"encoding/xml"
	"fmt"
	"io"
)

// Problem is a machine-readable diagnostic. Severity "error" blocks a push
// (malformed CSF would corrupt the page); "warning" is advisory only.
type Problem struct {
	Severity string `json:"severity"` // error | warning
	Line     int    `json:"line,omitempty"`
	Col      int    `json:"col,omitempty"`
	Rule     string `json:"rule"`
	Message  string `json:"message"`
}

// HasErrors reports whether any problem is blocking.
func HasErrors(ps []Problem) bool {
	for _, p := range ps {
		if p.Severity == "error" {
			return true
		}
	}
	return false
}

// Validate checks a CSF body. It always runs well-formedness (errors); when the
// body parses it also runs non-blocking sanity checks (warnings). Diagnostics
// carry 1-based line/col into the original bytes.
func Validate(raw []byte) []Problem {
	if p, ok := wellFormed(raw); !ok {
		return []Problem{p}
	}
	root, err := Parse(raw)
	if err != nil {
		// Should not happen if wellFormed passed, but be safe.
		return []Problem{{Severity: "error", Rule: "well-formedness", Message: err.Error()}}
	}
	return sanity(root)
}

// wellFormed streams tokens and, on failure, reports an accurate line/col.
func wellFormed(raw []byte) (Problem, bool) {
	d := decoder(raw)
	for {
		tok, err := d.Token()
		if err == io.EOF {
			return Problem{}, true
		}
		if err != nil {
			line, col := 0, 0
			if se, ok := err.(*xml.SyntaxError); ok {
				line = se.Line // wrapper adds no newlines → same line as original
			}
			// InputOffset points just past the failure in the wrapped stream;
			// subtract the 6-byte "<root>" prefix to map back to the original.
			off := int(d.InputOffset()) - len("<root>")
			if off >= 0 && off <= len(raw) {
				line, col = lineCol(raw, off)
			}
			return Problem{
				Severity: "error",
				Line:     line,
				Col:      col,
				Rule:     "well-formedness",
				Message:  cleanXMLErr(err.Error()),
			}, false
		}
		// Wrapping the body in <root> moves any <?xml ...?>, processing
		// instruction or <!DOCTYPE ...> out of prolog position, so the decoder
		// accepts it as a ProcInst/Directive token instead of erroring. The server
		// rejects these in storage-format body content (in any position), so flag
		// them here regardless of where they appear.
		switch tok.(type) {
		case xml.ProcInst, xml.Directive:
			line, col := 0, 0
			off := int(d.InputOffset()) - len("<root>")
			if off >= 0 && off <= len(raw) {
				line, col = lineCol(raw, off)
			}
			return Problem{
				Severity: "error",
				Line:     line,
				Col:      col,
				Rule:     "well-formedness",
				Message:  "xml declaration, processing instruction, or doctype not allowed in storage-format body",
			}, false
		}
	}
}

// sanity walks the DOM for advisory issues that commonly indicate a broken edit.
func sanity(root *Node) []Problem {
	var ps []Problem
	Walk(root, func(n *Node) bool {
		if n.Type != Element {
			return true
		}
		if n.Name.Space == "ac" && (n.Name.Local == "structured-macro" || n.Name.Local == "macro") {
			name := n.Attrv("ac", "name")
			if name == "" {
				ps = append(ps, Problem{Severity: "warning", Rule: "macro-name",
					Message: "structured-macro without ac:name"})
			}
			if name == "drawio" {
				params := macroParams(n)
				if params["diagramName"] == "" {
					ps = append(ps, Problem{Severity: "warning", Rule: "drawio-params",
						Message: "drawio macro missing diagramName parameter"})
				}
			}
		}
		// Inline-image / attachment refs with an empty filename are dangling.
		if n.Name.Space == "ri" && n.Name.Local == "attachment" {
			if n.Attrv("ri", "filename") == "" {
				ps = append(ps, Problem{Severity: "warning", Rule: "dangling-ref",
					Message: "ri:attachment without ri:filename"})
			}
		}
		return true
	})
	return ps
}

// macroParams collects the ac:parameter children of a structured-macro.
func macroParams(macro *Node) map[string]string {
	out := map[string]string{}
	for _, c := range macro.Children {
		if c.Type == Element && c.Name.Space == "ac" && c.Name.Local == "parameter" {
			out[c.Attrv("ac", "name")] = TextContent(c)
		}
	}
	return out
}

func lineCol(raw []byte, off int) (int, int) {
	line, col := 1, 1
	for i := 0; i < off && i < len(raw); i++ {
		if raw[i] == '\n' {
			line++
			col = 1
		} else {
			col++
		}
	}
	return line, col
}

func cleanXMLErr(s string) string {
	return fmt.Sprintf("malformed CSF: %s", s)
}
