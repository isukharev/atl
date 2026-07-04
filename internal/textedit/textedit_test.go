package textedit

import (
	"errors"
	"strings"
	"testing"
)

func TestExactMatchWins(t *testing.T) {
	r, err := Replace("<p>hello world</p>", "hello", "hi", false)
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if r.Text != "<p>hi world</p>" || r.Pass != PassExact {
		t.Errorf("got %q pass %s", r.Text, r.Pass)
	}
}

// The core CSF trap: the file has U+00A0 where the caller types a space.
func TestNBSPRawMatchesPlainSpace(t *testing.T) {
	text := "<p>Запрос предназначен для получения микса</p>"
	r, err := Replace(text, "Запрос предназначен для получения", "Запрос возвращает", false)
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if r.Pass != PassInvisible {
		t.Errorf("pass = %s, want invisible", r.Pass)
	}
	if r.Text != "<p>Запрос возвращает микса</p>" {
		t.Errorf("text = %q", r.Text)
	}
}

func TestNBSPEntityMatchesPlainSpace(t *testing.T) {
	text := "<td>нет,&nbsp;переношу в&#160;sandbox</td>"
	r, err := Replace(text, "нет, переношу в sandbox", "да", false)
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if r.Text != "<td>да</td>" || r.Pass != PassInvisible {
		t.Errorf("got %q pass %s", r.Text, r.Pass)
	}
}

func TestZeroWidthIgnored(t *testing.T) {
	text := "<p>data\u200b-mart\ufeff cleanup</p>"
	r, err := Replace(text, "data-mart cleanup", "done", false)
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if r.Text != "<p>done</p>" {
		t.Errorf("text = %q", r.Text)
	}
}

// Surrounding invisible bytes must survive the splice untouched.
func TestSplicePreservesSurroundingBytes(t *testing.T) {
	text := "<p> префикс </p><p>target here</p><p> суффикс&nbsp;</p>"
	r, err := Replace(text, "target here", "replaced", false)
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	want := "<p> префикс </p><p>replaced</p><p> суффикс&nbsp;</p>"
	if r.Text != want {
		t.Errorf("surrounding bytes changed:\n got %q\nwant %q", r.Text, want)
	}
}

func TestWhitespaceRunCollapse(t *testing.T) {
	text := "<td>нет,   переношу\n\tв sandbox</td>"
	r, err := Replace(text, "нет, переношу в sandbox", "да", false)
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if r.Pass != PassWhitespace || r.Text != "<td>да</td>" {
		t.Errorf("got %q pass %s", r.Text, r.Pass)
	}
}

func TestAmbiguousRefused(t *testing.T) {
	_, err := Replace("<td>да</td><td>да</td>", "да", "нет", false)
	var amb *AmbiguousError
	if !errors.As(err, &amb) || len(amb.Matches) != 2 {
		t.Fatalf("err = %v, want AmbiguousError with 2 matches", err)
	}
}

func TestReplaceAll(t *testing.T) {
	r, err := Replace("<td>да</td><td>да</td>", "да", "нет", true)
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if r.Text != "<td>нет</td><td>нет</td>" || len(r.Matches) != 2 {
		t.Errorf("got %q (%d matches)", r.Text, len(r.Matches))
	}
}

// The exact pass being ambiguous must not silently fall through to a looser
// pass — ambiguity is a refusal at the first pass that produced matches.
func TestAmbiguityDoesNotFallThrough(t *testing.T) {
	_, err := Replace("aa aa", "aa", "b", false)
	var amb *AmbiguousError
	if !errors.As(err, &amb) || amb.Pass != PassExact {
		t.Fatalf("err = %v, want exact-pass AmbiguousError", err)
	}
}

func TestNoMatchDumpsHiddenBytes(t *testing.T) {
	text := "<p>Параметры запроса и ответа</p>"
	_, err := Replace(text, "Параметры запроса совсем другие", "x", false)
	var nm *NoMatchError
	if !errors.As(err, &nm) {
		t.Fatalf("err = %v, want NoMatchError", err)
	}
	if !strings.Contains(nm.Context, "Параметры") {
		t.Errorf("context should expose the region with hidden bytes, got %q", nm.Context)
	}
}

func TestMultibyteOffsetsAccurate(t *testing.T) {
	text := "<h1>Назначение</h1><p>Кириллица и офсеты</p><h1>Конец</h1>"
	r, err := Replace(text, "Кириллица и офсеты", "ок", false)
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if r.Text != "<h1>Назначение</h1><p>ок</p><h1>Конец</h1>" {
		t.Errorf("text = %q", r.Text)
	}
	m := r.Matches[0]
	if text[:m.Start] != "<h1>Назначение</h1><p>" {
		t.Errorf("start offset landed mid-rune: %q", text[:m.Start])
	}
}

func TestEmptyOldRejected(t *testing.T) {
	if _, err := Replace("text", "", "x", false); err == nil {
		t.Fatal("empty old must be rejected")
	}
}

func TestCDATAContentEditable(t *testing.T) {
	text := "<ac:plain-text-body><![CDATA[curl -X GET https://x/v1?a=1]]></ac:plain-text-body>"
	r, err := Replace(text, "v1?a=1", "v2?a=1", false)
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if !strings.Contains(r.Text, "<![CDATA[curl -X GET https://x/v2?a=1]]>") {
		t.Errorf("text = %q", r.Text)
	}
}

// New text is inserted literally — no normalization of the replacement.
func TestNewInsertedVerbatim(t *testing.T) {
	r, err := Replace("<p>a b</p>", "a b", "x y", false)
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if r.Text != "<p>x y</p>" {
		t.Errorf("replacement was altered: %q", r.Text)
	}
}
