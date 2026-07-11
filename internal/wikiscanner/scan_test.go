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

func TestSharedWikiBlockRecognizers(t *testing.T) {
	if level, body, ok := ParseHeading("h3. Title"); !ok || level != 3 || body != "Title" {
		t.Fatalf("ParseHeading = %d, %q, %v", level, body, ok)
	}
	if macro, params, rest, ok := ParseCodeOpen("{code:go}fmt.Println()"); !ok || macro != "code" || params != "go" || rest != "fmt.Println()" {
		t.Fatalf("ParseCodeOpen = %q, %q, %q, %v", macro, params, rest, ok)
	}
	if rest, ok := ParseQuoteOpen("{quote}Text"); !ok || rest != "Text" {
		t.Fatalf("ParseQuoteOpen = %q, %v", rest, ok)
	}
	if params, rest, ok := ParsePanelOpen("{panel:title=Note}Text"); !ok || params != "title=Note" || rest != "Text" {
		t.Fatalf("ParsePanelOpen = %q, %q, %v", params, rest, ok)
	}
	if !IsHorizontalRule("----") || IsHorizontalRule("---") {
		t.Fatal("horizontal rule recognition drifted")
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
