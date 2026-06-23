package cli

import "strings"

// cqlEscape escapes a CQL string value (backslash and double-quote).
func cqlEscape(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	return `"` + v + `"`
}

// buildSearchCQL builds a CQL query from convenience flags.
// Returns "" when all inputs are empty.
func buildSearchCQL(space, title, label, ctype string) string {
	var clauses []string
	if space != "" {
		clauses = append(clauses, `space = `+cqlEscape(space))
	}
	if title != "" {
		clauses = append(clauses, `title ~ `+cqlEscape(title))
	}
	if label != "" {
		clauses = append(clauses, `label = `+cqlEscape(label))
	}
	if ctype != "" {
		clauses = append(clauses, `type = `+cqlEscape(ctype))
	}
	return strings.Join(clauses, " AND ")
}
