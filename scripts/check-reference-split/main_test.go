package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestMarkdownHeadingAnchors(t *testing.T) {
	contents := strings.Join([]string{
		"# API: [read](guide.md) + `write()`",
		"# Same",
		"# Same",
		"# Same-1",
		"# Same",
		"# <span>Wrapped</span>",
		"```md",
		"# Hidden",
		"```",
		"<a id=\"explicit-anchor\"></a>",
	}, "\n")
	document := parseMarkdown(contents)
	got := make([]string, 0, len(document.Headings))
	for _, item := range document.Headings {
		got = append(got, item.Anchor)
	}
	want := []string{
		"api-read--write",
		"same",
		"same-1",
		"same-1-1",
		"same-2",
		"wrapped",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("anchors = %q, want %q", got, want)
	}
	if !document.Anchors["explicit-anchor"] || document.Anchors["hidden"] {
		t.Fatalf("explicit/fenced anchors parsed incorrectly: %#v", document.Anchors)
	}
}

func TestMarkdownHeadingVisibleText(t *testing.T) {
	input := "[Read **this**](guide.md) &amp; `run()` <https://example.com> <em>now</em>"
	want := "Read **this** & run() https://example.com now"
	if got := markdownHeadingVisibleText(input); got != want {
		t.Fatalf("visible text = %q, want %q", got, want)
	}
	if got := githubHeadingSlug(input); got != "read-this--run-httpsexamplecom-now" {
		t.Fatalf("slug = %q", got)
	}
}

func TestLoadManifestRejectsMalformedJSON(t *testing.T) {
	tests := []struct {
		name      string
		contents  string
		wantError string
	}{
		{
			name:      "unknown field",
			contents:  `{"schema_version":1,"routes":[],"extra":true}`,
			wantError: "unknown field",
		},
		{
			name:      "unknown route field",
			contents:  `{"schema_version":1,"routes":[{"legacy_path":"docs/legacy.md","heading_text":"Old","heading_level":1,"source_order":1,"destination_path":"docs/new.md","destination_anchor":"new","extra":true}]}`,
			wantError: "unknown field",
		},
		{
			name:      "null routes",
			contents:  `{"schema_version":1,"routes":null}`,
			wantError: "non-null routes",
		},
		{
			name:      "empty routes",
			contents:  `{"schema_version":1,"routes":[]}`,
			wantError: "at least one route",
		},
		{
			name:      "multiple values",
			contents:  `{"schema_version":1,"routes":[]} {}`,
			wantError: "multiple JSON values",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "manifest.json")
			if err := os.WriteFile(path, []byte(test.contents), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := loadManifest(path)
			requireErrorContains(t, err, test.wantError)
		})
	}
}

func TestValidateManifestRejectsInvalidRoutes(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func([]route) []route
		wantError string
	}{
		{
			name: "source order gap",
			mutate: func(routes []route) []route {
				routes[1].SourceOrder = 3
				return routes
			},
			wantError: "want contiguous order 2",
		},
		{
			name: "unsorted legacy paths",
			mutate: func(routes []route) []route {
				routes[0].LegacyPath = "docs/z.md"
				routes[1].LegacyPath = "docs/a.md"
				routes[1].SourceOrder = 1
				return routes
			},
			wantError: "must be sorted",
		},
		{
			name: "invalid heading level",
			mutate: func(routes []route) []route {
				routes[0].HeadingLevel = 7
				return routes[:1]
			},
			wantError: "outside 1..6",
		},
		{
			name: "noncanonical destination",
			mutate: func(routes []route) []route {
				routes[0].DestinationPath = "docs/reference/../new.md"
				return routes[:1]
			},
			wantError: "not a canonical Markdown path",
		},
		{
			name: "self route",
			mutate: func(routes []route) []route {
				routes[0].DestinationPath = routes[0].LegacyPath
				return routes[:1]
			},
			wantError: "must differ",
		},
		{
			name: "heading does not round trip",
			mutate: func(routes []route) []route {
				routes[0].HeadingText = "Old section ###"
				return routes[:1]
			},
			wantError: "does not round-trip",
		},
		{
			name: "malformed destination anchor",
			mutate: func(routes []route) []route {
				routes[0].DestinationAnchor = "#new-section"
				return routes[:1]
			},
			wantError: "destination_anchor",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, routes := routeFixture(t)
			routes = test.mutate(routes)
			_, err := validateManifest(root, splitManifest{SchemaVersion: 1, Routes: routes})
			requireErrorContains(t, err, test.wantError)
		})
	}
}

func TestValidateManifestRejectsCaseMismatchAndSymlinks(t *testing.T) {
	t.Run("case mismatch", func(t *testing.T) {
		root, routes := routeFixture(t)
		routes[0].DestinationPath = "docs/New.md"
		_, err := validateManifest(root, splitManifest{SchemaVersion: 1, Routes: routes[:1]})
		requireErrorContains(t, err, "path case does not match")
	})

	t.Run("symlink", func(t *testing.T) {
		root, routes := routeFixture(t)
		if err := os.Symlink("new.md", filepath.Join(root, "docs", "linked.md")); err != nil {
			t.Fatal(err)
		}
		routes[0].DestinationPath = "docs/linked.md"
		_, err := validateManifest(root, splitManifest{SchemaVersion: 1, Routes: routes[:1]})
		requireErrorContains(t, err, "symlink")
	})
}

func TestValidateDestinations(t *testing.T) {
	root, routes := routeFixture(t)
	if err := validateDestinations(root, routes); err != nil {
		t.Fatalf("valid destinations: %v", err)
	}

	missing := append([]route(nil), routes...)
	missing[0].DestinationAnchor = "New-Section"
	requireErrorContains(t, validateDestinations(root, missing), "does not exist case-exactly")

	duplicate := append([]route(nil), routes...)
	duplicate[0].DestinationAnchor = "repeat-1"
	if err := validateDestinations(root, duplicate[:1]); err != nil {
		t.Fatalf("duplicate GitHub suffix destination: %v", err)
	}

	explicit := append([]route(nil), routes...)
	explicit[0].DestinationAnchor = "stable-explicit"
	if err := validateDestinations(root, explicit[:1]); err != nil {
		t.Fatalf("explicit destination anchor: %v", err)
	}
}

func TestRenderCompatibilityIndexIsDeterministic(t *testing.T) {
	routes := []route{
		{
			LegacyPath: "docs/legacy.md", HeadingText: "Old & `stable()`", HeadingLevel: 1, SourceOrder: 1,
			DestinationPath: "docs/reference/new file.md", DestinationAnchor: "new-section",
		},
		{
			LegacyPath: "docs/legacy.md", HeadingText: "Repeat", HeadingLevel: 2, SourceOrder: 2,
			DestinationPath: "docs/reference/new file.md", DestinationAnchor: "repeat",
		},
		{
			LegacyPath: "docs/legacy.md", HeadingText: "Repeat", HeadingLevel: 3, SourceOrder: 3,
			DestinationPath: "docs/reference/new file.md", DestinationAnchor: "repeat-1",
		},
	}
	first, err := renderCompatibilityIndex("docs/legacy.md", routes)
	if err != nil {
		t.Fatal(err)
	}
	second, err := renderCompatibilityIndex("docs/legacy.md", routes)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("rendering is not deterministic")
	}
	want := generatedPreamble + strings.Join([]string{
		"# Old & `stable()`",
		"",
		"[Read the canonical section](reference/new%20file.md#new-section).",
		"",
		"## Repeat",
		"",
		"[Read the canonical section](reference/new%20file.md#repeat).",
		"",
		"### Repeat",
		"",
		"[Read the canonical section](reference/new%20file.md#repeat-1).",
		"",
	}, "\n")
	if string(first) != want {
		t.Fatalf("rendered bytes differ\n--- got ---\n%s--- want ---\n%s", first, want)
	}
	parsed := parseMarkdown(string(first))
	if got := []string{parsed.Headings[0].Anchor, parsed.Headings[1].Anchor, parsed.Headings[2].Anchor}; !reflect.DeepEqual(got, []string{"old--stable", "repeat", "repeat-1"}) {
		t.Fatalf("legacy anchors = %q", got)
	}
}

func TestCheckReferenceSplitWriteAndValidate(t *testing.T) {
	root, routes := routeFixture(t)
	inventory := historicalInventoryForRoutes(routes)
	manifestPath := filepath.Join(root, "split-map.v1.json")
	writeManifest(t, manifestPath, splitManifest{SchemaVersion: 1, Routes: routes})

	_, err := checkReferenceSplitWithInventory(root, manifestPath, false, inventory)
	requireErrorContains(t, err, "is stale")

	report, err := checkReferenceSplitWithInventory(root, manifestPath, true, inventory)
	if err != nil {
		t.Fatal(err)
	}
	if report.Indexes != 1 || report.Routes != 2 || report.Written != 1 {
		t.Fatalf("write report = %+v", report)
	}
	report, err = checkReferenceSplitWithInventory(root, manifestPath, false, inventory)
	if err != nil {
		t.Fatal(err)
	}
	if report.Written != 0 {
		t.Fatalf("validation report = %+v", report)
	}

	legacyPath := filepath.Join(root, "docs", "legacy.md")
	contents, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	contents = append(contents, []byte("Canonical prose accidentally copied here.\n")...)
	if err := os.WriteFile(legacyPath, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = checkReferenceSplitWithInventory(root, manifestPath, false, inventory)
	requireErrorContains(t, err, "is stale")
}

func TestCheckReferenceSplitDetectsMissingAndStaleRoutes(t *testing.T) {
	root, routes := routeFixture(t)
	inventory := historicalInventoryForRoutes(routes)
	manifestPath := filepath.Join(root, "split-map.v1.json")
	writeManifest(t, manifestPath, splitManifest{SchemaVersion: 1, Routes: routes})
	if _, err := checkReferenceSplitWithInventory(root, manifestPath, true, inventory); err != nil {
		t.Fatal(err)
	}

	writeManifest(t, manifestPath, splitManifest{SchemaVersion: 1, Routes: routes[:1]})
	_, err := checkReferenceSplitWithInventory(root, manifestPath, true, inventory)
	requireErrorContains(t, err, "historical route inventory changed")

	routes[0].HeadingText = "Renamed legacy title"
	writeManifest(t, manifestPath, splitManifest{SchemaVersion: 1, Routes: routes})
	_, err = checkReferenceSplitWithInventory(root, manifestPath, true, inventory)
	requireErrorContains(t, err, "historical route identity changed")

	routes[0].HeadingText = "Legacy title"
	routes[0].DestinationAnchor = "removed-anchor"
	writeManifest(t, manifestPath, splitManifest{SchemaVersion: 1, Routes: routes})
	_, err = checkReferenceSplitWithInventory(root, manifestPath, false, inventory)
	requireErrorContains(t, err, "does not exist case-exactly")
}

func TestPermanentHistoricalInventory(t *testing.T) {
	value, err := loadManifest(filepath.Join("..", "..", "docs", "reference", "split-map.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateHistoricalInventory(value.Routes, historicalInventory{
		Routes: permanentHistoricalRouteCount, SHA256: permanentHistoricalRouteInventory,
	}); err != nil {
		t.Fatal(err)
	}
}

func routeFixture(t *testing.T) (string, []route) {
	t.Helper()
	root := t.TempDir()
	writeFile(t, root, "docs/legacy.md", "# Legacy placeholder\n")
	writeFile(t, root, "docs/new.md", strings.Join([]string{
		"# New Section",
		"## Repeat",
		"## Repeat",
		"<a id=\"stable-explicit\"></a>",
	}, "\n"))
	routes := []route{
		{
			LegacyPath: "docs/legacy.md", HeadingText: "Legacy title", HeadingLevel: 1, SourceOrder: 1,
			DestinationPath: "docs/new.md", DestinationAnchor: "new-section",
		},
		{
			LegacyPath: "docs/legacy.md", HeadingText: "Legacy detail", HeadingLevel: 2, SourceOrder: 2,
			DestinationPath: "docs/new.md", DestinationAnchor: "repeat",
		},
	}
	return root, routes
}

func writeManifest(t *testing.T, path string, value splitManifest) {
	t.Helper()
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	body = append(body, '\n')
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, root, relative, contents string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func requireErrorContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want substring %q", err, want)
	}
}
