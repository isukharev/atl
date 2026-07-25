package csf

import (
	"fmt"
	"sort"
)

// The Cloud-compatibility rule pack is an opt-in, advisory inventory of
// constructs that Atlassian documents as limited or unavailable in the
// Confluence Cloud editor. It is deliberately conservative: it reports only
// macro keys named on Atlassian's official compatibility list and two
// structural rules, it never claims that a migration will fail, and every
// finding is a warning, so it can never change HasErrors or a push gate.
//
// The pack is versioned because Atlassian's support pages change over time.
// CloudCompatSourceDate records when the pack was last reconciled against them.
const (
	// CloudCompatRulePack identifies the frozen taxonomy these rules implement.
	CloudCompatRulePack = "v1"
	// CloudCompatSourceDate is the date the pack was reconciled against the
	// official Atlassian Cloud editor / macro-removal documentation.
	CloudCompatSourceDate = "2026-07-25"
)

// Stable rule identifiers. These are part of the machine-readable output
// contract: the category of a macro finding is carried by the rule name, not by
// the prose message.
const (
	// RuleCloudMacroNotInsertable: the macro cannot be inserted in the Cloud
	// editor, but Atlassian documents how existing content migrates or converts.
	RuleCloudMacroNotInsertable = "cloud-compat/macro-not-insertable"
	// RuleCloudMacroViewOnly: the macro is removed from both Cloud editors —
	// existing instances remain visible but cannot be inserted or edited.
	RuleCloudMacroViewOnly = "cloud-compat/macro-view-only"
	// RuleCloudMacroRemoved: the macro is removed from Confluence Cloud.
	RuleCloudMacroRemoved = "cloud-compat/macro-removed"
	// RuleCloudNestedBodiedMacro: a macro with a body nested inside another
	// macro, which the Cloud editor does not natively support.
	RuleCloudNestedBodiedMacro = "cloud-compat/nested-bodied-macro"
	// RuleCloudNestedTable: a table inside another table, which the Cloud
	// editor does not support.
	RuleCloudNestedTable = "cloud-compat/nested-table"
)

// cloudMacroStatus maps an affected CSF macro key to its documented Cloud
// status. Only keys Atlassian names explicitly are listed — an unlisted
// marketplace app macro, user macro, or other unknown key is never guessed at.
var cloudMacroStatus = map[string]string{
	// Unavailable in the Cloud editor: cannot be inserted; existing content
	// migrates and stays visible.
	"align":                RuleCloudMacroNotInsertable,
	"bgcolor":              RuleCloudMacroNotInsertable,
	"center":               RuleCloudMacroNotInsertable,
	"cheese":               RuleCloudMacroNotInsertable,
	"code":                 RuleCloudMacroNotInsertable,
	"content-by-user":      RuleCloudMacroNotInsertable,
	"copyright":            RuleCloudMacroNotInsertable,
	"create-space-button":  RuleCloudMacroNotInsertable,
	"div":                  RuleCloudMacroNotInsertable,
	"fancy-bullets":        RuleCloudMacroNotInsertable,
	"favpages":             RuleCloudMacroNotInsertable,
	"global-reports":       RuleCloudMacroNotInsertable,
	"highlight":            RuleCloudMacroNotInsertable,
	"htmlcomment":          RuleCloudMacroNotInsertable,
	"info":                 RuleCloudMacroNotInsertable,
	"loremipsum":           RuleCloudMacroNotInsertable,
	"multimedia":           RuleCloudMacroNotInsertable,
	"navmap":               RuleCloudMacroNotInsertable,
	"noformat":             RuleCloudMacroNotInsertable,
	"note":                 RuleCloudMacroNotInsertable,
	"panel":                RuleCloudMacroNotInsertable,
	"privacy-mark":         RuleCloudMacroNotInsertable,
	"privacy-policy":       RuleCloudMacroNotInsertable,
	"recently-used-labels": RuleCloudMacroNotInsertable,
	"reg-tm":               RuleCloudMacroNotInsertable,
	"search":               RuleCloudMacroNotInsertable,
	"sm":                   RuleCloudMacroNotInsertable,
	"space-attachments":    RuleCloudMacroNotInsertable,
	"space-details":        RuleCloudMacroNotInsertable,
	"strike":               RuleCloudMacroNotInsertable,
	"style":                RuleCloudMacroNotInsertable,
	"tip":                  RuleCloudMacroNotInsertable,
	"tm":                   RuleCloudMacroNotInsertable,
	"warning":              RuleCloudMacroNotInsertable,

	// Removed from both editors: visible only.
	"chart":                      RuleCloudMacroViewOnly,
	"contributors-summary":       RuleCloudMacroViewOnly,
	"gallery":                    RuleCloudMacroViewOnly,
	"index":                      RuleCloudMacroViewOnly,
	"recently-updated-dashboard": RuleCloudMacroViewOnly,
	"related-labels":             RuleCloudMacroViewOnly,
	"spaces":                     RuleCloudMacroViewOnly,
	"viewdoc":                    RuleCloudMacroViewOnly,
	"viewppt":                    RuleCloudMacroViewOnly,

	// Removed from Confluence Cloud.
	"im":                  RuleCloudMacroRemoved,
	"junitreport":         RuleCloudMacroRemoved,
	"rolloverwithoudbody": RuleCloudMacroRemoved,
	"span":                RuleCloudMacroRemoved,
}

// cloudMacroMessage holds one deterministic message template per macro
// category. The message describes the documented editor behavior and never
// asserts that migration will fail.
var cloudMacroMessage = map[string]string{
	RuleCloudMacroNotInsertable: "macro %q cannot be inserted in the Confluence Cloud editor; Atlassian documents a Cloud editor migration or conversion path for existing content",
	RuleCloudMacroViewOnly:      "macro %q is removed from both Confluence Cloud editors; existing instances stay visible but cannot be inserted or edited",
	RuleCloudMacroRemoved:       "macro %q is removed from Confluence Cloud",
}

// cloudCompat walks the read-only DOM in document order and reports advisory
// Cloud-compatibility findings. raw is used only to map a node's start offset to
// a 1-based line/col; the DOM and the bytes are never mutated.
func cloudCompat(raw []byte, root *Node) []Problem {
	var ps []Problem
	positions := newSourcePositions(raw)
	var rec func(n *Node, bodyMacroAncestor *Node, tableDepth int)
	rec = func(n *Node, bodyMacroAncestor *Node, tableDepth int) {
		nextTableDepth := tableDepth
		if n.Type == Element {
			if isMacroElement(n) {
				name := n.Attrv("ac", "name")
				// An empty ac:name is already reported by the default sanity
				// pass; classifying it here would be a guess.
				if rule, listed := cloudMacroStatus[name]; listed {
					ps = append(ps, cloudProblem(positions, n, rule, fmt.Sprintf(cloudMacroMessage[rule], name)))
				}
				if bodyMacroAncestor != nil && hasMacroBody(n) {
					ps = append(ps, cloudProblem(positions, n, RuleCloudNestedBodiedMacro, fmt.Sprintf(
						"macro %q has a body and is nested inside macro %q; the Confluence Cloud editor does not support nested bodied macros",
						macroLabel(name), macroLabel(bodyMacroAncestor.Attrv("ac", "name")))))
				}
			}
			if n.Name.Space == "" && n.Name.Local == "table" {
				if tableDepth > 0 {
					ps = append(ps, cloudProblem(positions, n, RuleCloudNestedTable,
						"table nested inside another table; the Confluence Cloud editor does not support nested tables"))
				}
				nextTableDepth = tableDepth + 1
			}
		}
		for _, c := range n.Children {
			nextBodyMacroAncestor := bodyMacroAncestor
			if isMacroElement(n) && isMacroBodyElement(c) {
				nextBodyMacroAncestor = n
			}
			rec(c, nextBodyMacroAncestor, nextTableDepth)
		}
	}
	// root is the synthetic wrapper; start from the real top-level nodes.
	for _, c := range root.Children {
		rec(c, nil, 0)
	}
	return ps
}

// isMacroElement reports whether n is a CSF macro element.
func isMacroElement(n *Node) bool {
	return n.Type == Element && n.Name.Space == "ac" &&
		(n.Name.Local == "structured-macro" || n.Name.Local == "macro")
}

// hasMacroBody reports whether a macro carries a body, i.e. the shape Atlassian
// documents as unsupported when nested. Parameters are not a body.
func hasMacroBody(macro *Node) bool {
	for _, c := range macro.Children {
		if isMacroBodyElement(c) {
			return true
		}
	}
	return false
}

func isMacroBodyElement(n *Node) bool {
	return n.Type == Element && n.Name.Space == "ac" &&
		(n.Name.Local == "rich-text-body" || n.Name.Local == "plain-text-body")
}

// macroLabel keeps messages deterministic for a macro with no ac:name.
func macroLabel(name string) string {
	if name == "" {
		return "(unnamed)"
	}
	return name
}

// sourcePositions maps many byte offsets without rescanning from byte zero for
// every finding. The newline index costs O(len(raw)) to build and O(log lines)
// per lookup, avoiding quadratic validation on large pages with many findings.
type sourcePositions struct {
	rawLen   int
	newlines []int
}

func newSourcePositions(raw []byte) sourcePositions {
	p := sourcePositions{rawLen: len(raw)}
	for i, b := range raw {
		if b == '\n' {
			p.newlines = append(p.newlines, i)
		}
	}
	return p
}

func (p sourcePositions) lineCol(off int) (line, col int, ok bool) {
	if off < 0 || off > p.rawLen {
		return 0, 0, false
	}
	lineIndex := sort.SearchInts(p.newlines, off)
	lineStart := 0
	if lineIndex > 0 {
		lineStart = p.newlines[lineIndex-1] + 1
	}
	return lineIndex + 1, off - lineStart + 1, true
}

// cloudProblem builds a warning anchored at the node's source position. When
// the DOM offset cannot address the raw bytes, Line/Col stay zero (omitted from
// JSON) rather than being guessed.
func cloudProblem(positions sourcePositions, n *Node, rule, msg string) Problem {
	p := Problem{Severity: "warning", Rule: rule, Message: msg}
	if line, col, ok := positions.lineCol(n.Start); ok {
		p.Line, p.Col = line, col
	}
	return p
}
