package app

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestAppProductionImportsStayTransportNeutral keeps credentials, transport,
// and concrete assembly in compose. Config is temporarily allowed only in the
// exact files that own render profiles, field views, or derived-view settings.
func TestAppProductionImportsStayTransportNeutral(t *testing.T) {
	renderConfigOwners := map[string]bool{
		"confluence.go": true, "confluence_complete.go": true, "confluence_jira_macros.go": true,
		"confluence_plan.go": true, "confluence_pull.go": true, "confluence_render.go": true,
		"confluence_view.go": true, "created_registration.go": true, "environment.go": true,
		"jira.go": true, "jira_agile.go": true, "jira_apply.go": true, "jira_board.go": true,
		"jira_fields.go": true, "jira_list_views.go": true, "jira_pull.go": true,
		"jira_related.go": true, "jira_render.go": true, "jira_structure.go": true,
		"jira_sync.go": true, "jira_view.go": true, "render.go": true, "wire.go": true,
	}
	const internalPrefix = "github.com/isukharev/atl/internal/"
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Errorf("parse %s: %v", name, err)
			continue
		}
		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				continue
			}
			switch {
			case path == internalPrefix+"adapter" || strings.HasPrefix(path, internalPrefix+"adapter/"):
				t.Errorf("%s imports concrete adapter %s; compose it in internal/compose", name, path)
			case path == internalPrefix+"auth" || strings.HasPrefix(path, internalPrefix+"auth/"),
				path == internalPrefix+"httpx" || strings.HasPrefix(path, internalPrefix+"httpx/"):
				t.Errorf("%s imports outer concern %s; project it through a neutral app/domain contract", name, path)
			case path == internalPrefix+"config" && !renderConfigOwners[name]:
				t.Errorf("%s imports config outside the exact render-owner allowlist", name)
			}
		}
	}
}
