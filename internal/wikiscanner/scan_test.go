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

func TestSharedWikiBlockPredicatesMatchParsers(t *testing.T) {
	tests := []struct {
		name      string
		predicate func(string) bool
		parse     func(string) bool
		samples   map[string]bool
	}{
		{
			name:      "heading",
			predicate: IsHeading,
			parse: func(line string) bool {
				_, _, ok := ParseHeading(line)
				return ok
			},
			samples: map[string]bool{
				"h3. Title":  true,
				"h0. Title":  false,
				"h3.Title":   false,
				" h3. Title": false,
			},
		},
		{
			name:      "code open",
			predicate: IsCodeOpen,
			parse: func(line string) bool {
				_, _, _, ok := ParseCodeOpen(line)
				return ok
			},
			samples: map[string]bool{
				"{code:go}body":  true,
				"{noformat}body": true,
				"{Code:go}body":  false,
				"{code:go":       false,
				"x{code}":        false,
			},
		},
		{
			name:      "quote open",
			predicate: IsQuoteOpen,
			parse: func(line string) bool {
				_, ok := ParseQuoteOpen(line)
				return ok
			},
			samples: map[string]bool{
				"{quote}body":     true,
				"{Quote}body":     false,
				"{quote:bad}body": false,
			},
		},
		{
			name:      "panel open",
			predicate: IsPanelOpen,
			parse: func(line string) bool {
				_, _, ok := ParsePanelOpen(line)
				return ok
			},
			samples: map[string]bool{
				"{panel:title=Note}body": true,
				"{Panel:title=Note}body": false,
				"{panel:title=Note":      false,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for line, want := range tt.samples {
				parsed := tt.parse(line)
				if parsed != want {
					t.Errorf("parser(%q) = %v, want %v", line, parsed, want)
				}
				if got := tt.predicate(line); got != parsed {
					t.Errorf("predicate(%q) = %v, parser = %v", line, got, parsed)
				}
			}
		})
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
	for _, line := range []string{"```", "````go", "   ```go", "    ```", "\t```go", "```go`bad", "```x``", "---", "---   ", "*****", "___"} {
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

func TestMarkdownFenceGrammar(t *testing.T) {
	openers := []struct {
		line string
		run  int
		info string
	}{
		{"```", 3, ""},
		{"````go", 4, "go"},
		{"   ````` go lang\r", 5, " go lang\r"},
	}
	for _, tt := range openers {
		run, info, ok := ParseMarkdownFenceOpen(tt.line)
		if !ok || run != tt.run || info != tt.info {
			t.Errorf("ParseMarkdownFenceOpen(%q) = %d, %q, %v", tt.line, run, info, ok)
		}
	}
	for _, line := range []string{"``", "    ```", "\t```", "```go`bad"} {
		if _, _, ok := ParseMarkdownFenceOpen(line); ok {
			t.Errorf("ParseMarkdownFenceOpen(%q) accepted invalid opener", line)
		}
	}

	for _, line := range []string{"````", "`````", "  ````` \t\r"} {
		if !IsMarkdownFenceClose(line, 4) {
			t.Errorf("IsMarkdownFenceClose(%q, 4) = false", line)
		}
	}
	for _, line := range []string{"```", "```` tail", "    ````", "\t````"} {
		if IsMarkdownFenceClose(line, 4) {
			t.Errorf("IsMarkdownFenceClose(%q, 4) = true", line)
		}
	}
}

func TestNormalizeMarkdownFenceInfo(t *testing.T) {
	for input, want := range map[string]string{"": "", " go ": "go", "C++": "C++"} {
		if got, ok := NormalizeMarkdownFenceInfo(input); !ok || got != want {
			t.Errorf("NormalizeMarkdownFenceInfo(%q) = %q, %v; want %q, true", input, got, ok, want)
		}
	}
	for _, input := range []string{"go`bad", "go\ntext", "go\rtext"} {
		if got, ok := NormalizeMarkdownFenceInfo(input); ok || got != "" {
			t.Errorf("NormalizeMarkdownFenceInfo(%q) = %q, %v; want empty, false", input, got, ok)
		}
	}
}

func TestMarkdownBlockCollisionEscapeIsReversible(t *testing.T) {
	for _, original := range []string{"```json", "````json", "   ```json", "\\```json", "\\\\```json", "---", "\\---", "\t***   "} {
		encoded := EscapeMarkdownBlockCollision(original)
		decoded, _, ok := UnescapeMarkdownBlockCollision(encoded)
		if !ok || decoded != original {
			t.Fatalf("roundtrip %q -> %q -> %q, ok=%v", original, encoded, decoded, ok)
		}
	}
	if _, _, ok := UnescapeMarkdownBlockCollision(`\----`); ok {
		t.Fatal("single-slash 4-dash line treated as a synthetic escape")
	}
	for _, line := range []string{"\\``", "\\text ```"} {
		if _, _, ok := UnescapeMarkdownBlockCollision(line); ok {
			t.Errorf("UnescapeMarkdownBlockCollision(%q) accepted invalid fence escape", line)
		}
	}
}
