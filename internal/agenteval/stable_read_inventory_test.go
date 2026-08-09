package agenteval

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestStableReadProductionInventory(t *testing.T) {
	repository := filepath.Join("..", "..")
	const capabilityReferenceChecker = "scripts/check-skill-routing/capability_references.go"
	want := map[string]int{
		"internal/agenteval/aggregate_root.go":              3,
		"internal/agenteval/external_mcp_profile.go":        1,
		"internal/agenteval/extension_host.go":              2,
		"internal/agenteval/extension_host_acl_unix.go":     8,
		"internal/agenteval/extension_host_process.go":      2,
		"internal/agenteval/live_gateway.go":                1,
		"internal/agenteval/private_coverage_scorecard.go":  3,
		"internal/agenteval/private_finding_acceptance.go":  7,
		"internal/agenteval/private_finding_ledger_v2.go":   7,
		"internal/agenteval/private_plan.go":                6,
		"internal/agenteval/private_sampling.go":            7,
		"internal/agenteval/private_synthetic_sampling.go":  3,
		"internal/agenteval/private_workspace_migration.go": 7,
		"internal/agenteval/plugin_skill_catalog.go":        5,
		"internal/agenteval/provider_runtime.go":            7,
		"internal/agenteval/runner.go":                      5,
		"internal/agenteval/stable_root_read.go":            2,
		"internal/agenteval/storage.go":                     1,
		"internal/agenteval/synthetic_receipt.go":           2,
		"internal/cli/profile_revalidation.go":              1,
		"internal/contentpolicy/source_windows.go":          1,
		"internal/corpus/publication.go":                    1,
		"internal/corpus/scan.go":                           9,
		"internal/corpus/store.go":                          3,
		"internal/skillmeta/catalog.go":                     3,
		"internal/skillrouting/contract.go":                 1,
		capabilityReferenceChecker:                          1,
		"scripts/gen-plugins/main.go":                       17,
	}

	got := map[string]int{}
	files := token.NewFileSet()
	for _, root := range []string{"cmd", "internal", "scripts"} {
		err := filepath.WalkDir(filepath.Join(repository, root), func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			parsed, err := parser.ParseFile(files, path, nil, 0)
			if err != nil {
				return err
			}
			count := countOSSameFileCalls(parsed)
			if count > 0 {
				relative, err := filepath.Rel(repository, path)
				if err != nil {
					return err
				}
				got[filepath.ToSlash(relative)] = count
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stable-read production inventory drifted:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestCountOSSameFileCalls(t *testing.T) {
	for _, test := range []struct {
		name   string
		source string
		want   int
	}{
		{name: "ordinary import", source: `package fixture; import "os"; func f() { os.SameFile(nil, nil) }`, want: 1},
		{name: "alias import", source: `package fixture; import stdos "os"; func f() { stdos.SameFile(nil, nil) }`, want: 1},
		{name: "raw alias import", source: "package fixture; import stdos `os`; func f() { stdos.SameFile(nil, nil) }", want: 1},
		{name: "dot import", source: `package fixture; import . "os"; func f() { SameFile(nil, nil) }`, want: 1},
		{name: "unrelated selector", source: `package fixture; type service struct{}; func (service) SameFile() {}; func f() { var other service; other.SameFile() }`},
	} {
		t.Run(test.name, func(t *testing.T) {
			parsed, err := parser.ParseFile(token.NewFileSet(), "fixture.go", test.source, 0)
			if err != nil {
				t.Fatal(err)
			}
			if got := countOSSameFileCalls(parsed); got != test.want {
				t.Fatalf("SameFile calls=%d, want %d", got, test.want)
			}
		})
	}
}

func countOSSameFileCalls(parsed *ast.File) int {
	osAliases := map[string]bool{}
	dotOS := false
	for _, imported := range parsed.Imports {
		path, err := strconv.Unquote(imported.Path.Value)
		if err != nil || path != "os" {
			continue
		}
		switch {
		case imported.Name == nil:
			osAliases["os"] = true
		case imported.Name.Name == ".":
			dotOS = true
		case imported.Name.Name != "_":
			osAliases[imported.Name.Name] = true
		}
	}
	count := 0
	ast.Inspect(parsed, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if selector, ok := call.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == "SameFile" {
			owner, ok := selector.X.(*ast.Ident)
			if ok && osAliases[owner.Name] {
				count++
			}
		} else if function, ok := call.Fun.(*ast.Ident); ok && dotOS && function.Name == "SameFile" {
			count++
		}
		return true
	})
	return count
}
