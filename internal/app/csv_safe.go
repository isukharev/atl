package app

import "strings"

// spreadsheetCell neutralizes values that spreadsheet programs may interpret
// as formulas. CSV quoting does not disable formula evaluation, so the safe
// representation prefixes an apostrophe. raw is an explicit interoperability
// escape hatch for consumers that require byte-for-byte cell values.
func spreadsheetCell(value string, raw bool) string {
	if raw || value == "" {
		return value
	}
	first := value[0]
	if strings.ContainsRune("=+-@\t\r\n", rune(first)) {
		return "'" + value
	}
	return value
}

func spreadsheetRecord(record []string, raw bool) []string {
	out := make([]string, len(record))
	for i, value := range record {
		out[i] = spreadsheetCell(value, raw)
	}
	return out
}
