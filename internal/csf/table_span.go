package csf

import "strings"

// MaxTableSpan bounds expansion of server-controlled rowspan/colspan values.
// Exact consumers that materialize a rectangular table grid use TableSpan;
// legacy derived-view renderers share the cap while preserving their current
// document-format parsing semantics until an explicit marker migration.
const MaxTableSpan = 100

// TableSpan returns a normalized table-cell span in [1, MaxTableSpan]. Missing,
// malformed, zero, and negative values mean one, matching browser table
// semantics while keeping hostile attributes bounded before allocation.
func TableSpan(n *Node, name string) int {
	v, valid := tableSpanValue(n, name)
	if !valid {
		return 1
	}
	return min(v, MaxTableSpan)
}

// TableSpanExceedsMax reports a syntactically valid positive span that cannot
// be represented within MaxTableSpan. Exact structured projections use this to
// fail closed instead of claiming reconciled geometry for a clamped table;
// best-effort derived views may still render the bounded TableSpan.
func TableSpanExceedsMax(n *Node, name string) bool {
	v, valid := tableSpanValue(n, name)
	return valid && v > MaxTableSpan
}

func tableSpanValue(n *Node, name string) (int, bool) {
	if n == nil {
		return 0, false
	}
	raw := strings.TrimSpace(n.Attrv("", name))
	raw = strings.TrimPrefix(raw, "+")
	if raw == "" {
		return 0, false
	}
	value := 0
	for _, r := range raw {
		if r < '0' || r > '9' {
			return 0, false
		}
		if value <= MaxTableSpan {
			value = value*10 + int(r-'0')
			if value > MaxTableSpan {
				value = MaxTableSpan + 1
			}
		}
	}
	if value < 1 {
		return 0, false
	}
	return value, true
}
