package wikiscanner

import "testing"

func TestParseListLine(t *testing.T) {
	markers, body, ok := ParseListLine("  *# nested item")
	if !ok || markers != "*#" || body != "nested item" {
		t.Fatalf("ParseListLine = %q, %q, %v", markers, body, ok)
	}
	if IsListLine("*missing-space") {
		t.Fatal("list without marker separator accepted")
	}
}

func TestTableRowEnd(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		want  int
	}{
		{name: "single", lines: []string{"| a |"}, want: 0},
		{name: "continued", lines: []string{"| a", "continued", "end |", "plain"}, want: 2},
		{name: "blank refuses", lines: []string{"| a", "", "end |"}, want: 0},
		{name: "new row refuses", lines: []string{"| a", "| b |"}, want: 0},
		{name: "unterminated refuses", lines: []string{"| a", "plain"}, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TableRowEnd(len(tt.lines), 0, func(i int) string { return tt.lines[i] })
			if got != tt.want {
				t.Fatalf("TableRowEnd = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestMarkdownBlockCollision(t *testing.T) {
	for _, line := range []string{"```", "   ```go", "    ```", "\t```go", "---", "---   ", "*****", "___"} {
		if !MarkdownBlockCollision(line) {
			t.Errorf("MarkdownBlockCollision(%q) = false", line)
		}
	}
	for _, line := range []string{"  --", "text ```", "----", "---- tail"} {
		if MarkdownBlockCollision(line) {
			t.Errorf("MarkdownBlockCollision(%q) = true", line)
		}
	}
}

func TestMarkdownBlockCollisionEscapeIsReversible(t *testing.T) {
	for _, original := range []string{"```json", "\\```json", "\\\\```json", "---", "\\---", "\t***   "} {
		encoded := EscapeMarkdownBlockCollision(original)
		decoded, _, ok := UnescapeMarkdownBlockCollision(encoded)
		if !ok || decoded != original {
			t.Fatalf("roundtrip %q -> %q -> %q, ok=%v", original, encoded, decoded, ok)
		}
	}
	if _, _, ok := UnescapeMarkdownBlockCollision(`\----`); ok {
		t.Fatal("single-slash 4-dash line treated as a synthetic escape")
	}
}
