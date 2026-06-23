package cli

import (
	"testing"
)

func TestBuildSearchCQL(t *testing.T) {
	cases := []struct {
		name                    string
		space, title, label, ct string
		want                    string
	}{
		{name: "empty", want: ""},
		{name: "space only", space: "ENG", want: `space = "ENG"`},
		{name: "title only", title: "Foo Bar", want: `title ~ "Foo Bar"`},
		{name: "label only", label: "release", want: `label = "release"`},
		{name: "type only", ct: "page", want: `type = "page"`},
		{name: "space+title", space: "ENG", title: "My Doc", want: `space = "ENG" AND title ~ "My Doc"`},
		{name: "all four", space: "ENG", title: "T", label: "l", ct: "page",
			want: `space = "ENG" AND title ~ "T" AND label = "l" AND type = "page"`},
		{name: "quote injection", space: `foo"bar`, want: `space = "foo\"bar"`},
		{name: "backslash injection", title: `a\b`, want: `title ~ "a\\b"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildSearchCQL(tc.space, tc.title, tc.label, tc.ct)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPageListCQL(t *testing.T) {
	space := "ENG"
	base := buildSearchCQL(space, "", "", "") + ` AND type = page`
	want := `space = "ENG" AND type = page`
	if base != want {
		t.Errorf("got %q, want %q", base, want)
	}

	withStatus := base + ` AND status = ` + cqlEscape("archived")
	wantWS := `space = "ENG" AND type = page AND status = "archived"`
	if withStatus != wantWS {
		t.Errorf("got %q, want %q", withStatus, wantWS)
	}
}
