package backendid

import (
	"strings"
	"testing"
)

func TestOriginSHA256CanonicalEquivalence(t *testing.T) {
	forms := []string{
		" https://EXAMPLE.test:443/context/ ",
		"https://example.test/context",
		"HTTPS://example.test/context///",
		"https://example.test/%63ontext",
	}
	want, err := OriginSHA256(forms[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, form := range forms[1:] {
		got, err := OriginSHA256(form)
		if err != nil || got != want {
			t.Fatalf("OriginSHA256(%q) = %q, %v; want %q", form, got, err, want)
		}
	}
	if !strings.HasPrefix(want, Prefix) || len(want) != len(Prefix)+64 {
		t.Fatalf("digest = %q", want)
	}
}

func TestOriginSHA256CanonicalizesURIAndDNSAliases(t *testing.T) {
	for _, forms := range [][]string{
		{"https://example.test/wiki%2fpart", "https://example.test/wiki%2Fpart"},
		{"https://example.test/%7eteam", "https://example.test/~team"},
		{"https://example.test./wiki", "https://example.test/wiki"},
		{"https://bücher.example/wiki", "https://xn--bcher-kva.example/wiki"},
	} {
		first, err := OriginSHA256(forms[0])
		if err != nil {
			t.Fatalf("OriginSHA256(%q): %v", forms[0], err)
		}
		second, err := OriginSHA256(forms[1])
		if err != nil || second != first {
			t.Fatalf("aliases %q and %q = %q/%q, %v", forms[0], forms[1], first, second, err)
		}
	}

	encodedSeparator, err := OriginSHA256("https://example.test/wiki%2Fpart")
	if err != nil {
		t.Fatal(err)
	}
	pathSeparator, err := OriginSHA256("https://example.test/wiki/part")
	if err != nil {
		t.Fatal(err)
	}
	if encodedSeparator == pathSeparator {
		t.Fatal("reserved encoded separator was collapsed into a path separator")
	}
}

func TestOriginSHA256PreservesMeaningfulDifferences(t *testing.T) {
	base, err := OriginSHA256("https://example.test/context")
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		"https://example.test/other",
		"https://example.test:8443/context",
		"http://example.test/context",
		"https://other.example.test/context",
	} {
		got, err := OriginSHA256(raw)
		if err != nil {
			t.Fatalf("OriginSHA256(%q): %v", raw, err)
		}
		if got == base {
			t.Fatalf("meaningfully different origin %q collided", raw)
		}
	}
}

func TestOriginSHA256RejectsAmbiguousOrSensitiveURLs(t *testing.T) {
	for _, raw := range []string{
		"", "example.test", "ftp://example.test", "https://user@example.test",
		"https://example.test?q=x", "https://example.test/#x",
		"https://example.test/a//b", "https://example.test/a/../b",
		"https://example.test:99999", "https://example.test/\npath",
	} {
		if got, err := OriginSHA256(raw); err == nil || got != "" {
			t.Fatalf("OriginSHA256(%q) = %q, %v; want rejection", raw, got, err)
		}
	}
}
