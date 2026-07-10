package app

import "testing"

func TestSpreadsheetCellNeutralizesFormulaPrefixes(t *testing.T) {
	for _, value := range []string{"=1+1", "+cmd", "-2", "@SUM(A1)", "\t=1", "\r=1", "\n=1"} {
		if got := spreadsheetCell(value, false); got != "'"+value {
			t.Errorf("spreadsheetCell(%q) = %q", value, got)
		}
		if got := spreadsheetCell(value, true); got != value {
			t.Errorf("raw spreadsheetCell(%q) = %q", value, got)
		}
	}
	for _, value := range []string{"", "plain", "  =not-leading", "'already-text"} {
		if got := spreadsheetCell(value, false); got != value {
			t.Errorf("spreadsheetCell(%q) = %q, want unchanged", value, got)
		}
	}
}
