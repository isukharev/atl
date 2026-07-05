package main

import (
	"strings"
	"testing"
)

func TestRenderSubstitutesVars(t *testing.T) {
	got, err := render("run `{{atl.setup_cmd}}` and stop.\n", map[string]string{"setup_cmd": "/atl:setup"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "run `/atl:setup` and stop.\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRenderUnknownVarIsError(t *testing.T) {
	if _, err := render("hello {{atl.no_such_var}}\n", map[string]string{"setup_cmd": "x"}); err == nil {
		t.Fatal("expected an error for an unknown placeholder")
	}
}

func TestRenderDropsEmptyPlaceholderLineWithoutGap(t *testing.T) {
	src := "intro.\n\n{{atl.note}}\n\n## Next\n"
	got, err := render(src, map[string]string{"note": ""})
	if err != nil {
		t.Fatal(err)
	}
	if want := "intro.\n\n## Next\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRenderKeepsNonEmptyPlaceholderLine(t *testing.T) {
	src := "intro.\n\n{{atl.note}}\n\n## Next\n"
	got, err := render(src, map[string]string{"note": "Invocation: use `$setup`."})
	if err != nil {
		t.Fatal(err)
	}
	if want := "intro.\n\nInvocation: use `$setup`.\n\n## Next\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestWithHeaderRespectsFrontmatter(t *testing.T) {
	src := "---\nname: x\n---\nbody\n"
	got, err := withHeader(src, "x/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "---\n") {
		t.Fatalf("frontmatter must stay at byte 0, got %q", got[:20])
	}
	if !strings.Contains(got, "---\n<!-- Generated from skills-src/x/SKILL.md") {
		t.Fatalf("header not placed after frontmatter close: %q", got)
	}
	if !strings.HasSuffix(got, "-->\nbody\n") {
		t.Fatalf("body must follow the header: %q", got)
	}
}

func TestWithHeaderNoFrontmatterGoesOnTop(t *testing.T) {
	got, err := withHeader("body\n", "x/reference/y.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "<!-- Generated from skills-src/x/reference/y.md") {
		t.Fatalf("header must lead the file: %q", got)
	}
}

func TestPlatformVarSetsAreComplete(t *testing.T) {
	// Every platform must define the same variable names, or a source using a
	// var would render on one platform and error on the other.
	base := platforms[0].vars
	for _, pl := range platforms[1:] {
		for k := range base {
			if _, ok := pl.vars[k]; !ok {
				t.Errorf("platform %s is missing var %q", pl.name, k)
			}
		}
		for k := range pl.vars {
			if _, ok := base[k]; !ok {
				t.Errorf("platform %s defines extra var %q missing from %s", pl.name, k, platforms[0].name)
			}
		}
	}
}

func TestRenderStrayPlaceholderTyposAreErrors(t *testing.T) {
	vars := map[string]string{"setup_cmd": "/atl:setup"}
	for _, src := range []string{
		"run {{atl.Setup_cmd}} now\n",   // wrong case
		"run {{ atl.setup_cmd }} now\n", // inner spaces
		"run {{atl.setup_cmd }} now\n",  // trailing space
	} {
		if _, err := render(src, vars); err == nil {
			t.Errorf("typo %q must be a hard error, rendered fine", src)
		}
	}
}

func TestRenderNeverTouchesBlankLinesInContent(t *testing.T) {
	src := "```\nline1\n\n\nline2\n```\ntail\n\n\n"
	got, err := render(src, map[string]string{"setup_cmd": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if got != src {
		t.Fatalf("content without placeholders must pass through verbatim:\ngot  %q\nwant %q", got, src)
	}
}

func TestWithHeaderUnterminatedFrontmatterIsError(t *testing.T) {
	if _, err := withHeader("---\nname: x\nbody with no close\n", "x/SKILL.md"); err == nil {
		t.Fatal("unterminated frontmatter must be a hard error")
	}
}
