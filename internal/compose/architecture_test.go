package compose

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestInnerEntrypointsDoNotImportAdapters(t *testing.T) {
	for _, root := range []string{"../cli", "../mcpserver"} {
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			for _, spec := range file.Imports {
				importPath, err := strconv.Unquote(spec.Path.Value)
				if err == nil && strings.Contains(importPath, "/internal/adapter/") {
					t.Errorf("%s imports concrete adapter %s; use internal/compose", path, importPath)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("scan %s: %v", root, err)
		}
	}
}

func TestHTTPClientConstructionAndImmutablePolicyInventory(t *testing.T) {
	expectedConstructors := map[string]int{
		"adapter/confluence/confluence.go:NewWithScheduler:httpx.NewWithScheduler":       1,
		"adapter/confluence/confluence.go:NewWithSchedulerTLS:httpx.NewWithSchedulerTLS": 1,
		"adapter/jira/jira.go:NewWithScheduler:httpx.NewWithScheduler":                   1,
		"adapter/jira/jira.go:NewWithSchedulerTLS:httpx.NewWithSchedulerTLS":             1,
	}
	constructorCounts := map[string]int{}
	expectedPolicyCalls := map[string]map[string]int{
		"adapter/confluence/confluence.go": {"WithRequiredWriteClearance": 1, "WithTrace": 1},
		"adapter/jira/jira.go":             {"WithGenericConflict": 1, "WithRequiredWriteClearance": 1, "WithTrace": 1},
	}
	bannedMutablePolicy := map[string]bool{
		"SetTrace": true, "SetNoVersionGate": true, "RequireWriteClearance": true,
	}

	err := filepath.WalkDir("..", func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		relative := strings.TrimPrefix(filepath.ToSlash(path), "../")
		references, violations := scanHTTPConstructorReferences(file, relative)
		for _, violation := range violations {
			t.Error(violation)
		}
		for _, reference := range references {
			constructorCounts[reference.key]++
			if _, ok := expectedConstructors[reference.key]; !ok {
				t.Errorf("unreviewed raw HTTP client construction %s", reference.key)
				continue
			}
			validateReviewedConstructorCall(t, reference.key, reference.call)
		}
		httpxAliases := namedHTTPXAliases(file)
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			if bannedMutablePolicy[function.Name.Name] {
				t.Errorf("%s declares removed mutable HTTP policy %s", relative, function.Name.Name)
			}
			ast.Inspect(function.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if bannedMutablePolicy[selector.Sel.Name] {
					t.Errorf("%s:%s calls removed mutable HTTP policy %s", relative, function.Name.Name, selector.Sel.Name)
				}
				qualifier, ok := selector.X.(*ast.Ident)
				if !ok || !httpxAliases[qualifier.Name] {
					return true
				}
				if function.Name.Name == "transportOptions" {
					if expected := expectedPolicyCalls[relative]; expected != nil {
						expected[selector.Sel.Name]--
					}
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for constructor, expectedCount := range expectedConstructors {
		if constructorCounts[constructor] != expectedCount {
			t.Errorf("reviewed raw HTTP client construction %s count = %d, want %d", constructor, constructorCounts[constructor], expectedCount)
		}
	}
	for path, expected := range expectedPolicyCalls {
		for policy, remaining := range expected {
			if remaining != 0 {
				t.Errorf("%s immutable policy %s count drifted by %d", path, policy, -remaining)
			}
		}
	}
}

type constructorReference struct {
	key  string
	call *ast.CallExpr
}

func namedHTTPXAliases(file *ast.File) map[string]bool {
	aliases := map[string]bool{}
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil || importPath != "github.com/isukharev/atl/internal/httpx" {
			continue
		}
		name := "httpx"
		if spec.Name != nil {
			name = spec.Name.Name
		}
		if name != "." && name != "_" {
			aliases[name] = true
		}
	}
	return aliases
}

func scanHTTPConstructorReferences(file *ast.File, relative string) ([]constructorReference, []string) {
	constructors := map[string]bool{"New": true, "NewWithScheduler": true, "NewWithSchedulerTLS": true}
	aliases := namedHTTPXAliases(file)
	violations := []string{}
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			violations = append(violations, fmt.Sprintf("%s contains an invalid import path: %v", relative, err))
			continue
		}
		if importPath == "github.com/isukharev/atl/internal/httpx" && spec.Name != nil && (spec.Name.Name == "." || spec.Name.Name == "_") {
			violations = append(violations, fmt.Sprintf("%s imports internal/httpx as %q; use a named import so constructor ownership stays visible", relative, spec.Name.Name))
		}
	}
	directCalls := map[*ast.SelectorExpr]*ast.CallExpr{}
	ast.Inspect(file, func(node ast.Node) bool {
		if call, ok := node.(*ast.CallExpr); ok {
			if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
				directCalls[selector] = call
			}
		}
		return true
	})
	enclosingFunction := func(position token.Pos) string {
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if ok && function.Body != nil && function.Body.Pos() <= position && position <= function.Body.End() {
				return function.Name.Name
			}
		}
		return "<package>"
	}
	references := []constructorReference{}
	ast.Inspect(file, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok || !constructors[selector.Sel.Name] {
			return true
		}
		qualifier, ok := selector.X.(*ast.Ident)
		if !ok || !aliases[qualifier.Name] {
			return true
		}
		function := enclosingFunction(selector.Pos())
		key := relative + ":" + function + ":httpx." + selector.Sel.Name
		call := directCalls[selector]
		if call == nil {
			violations = append(violations, fmt.Sprintf("%s references raw HTTP constructor outside the exact direct call position", key))
			return true
		}
		references = append(references, constructorReference{key: key, call: call})
		return true
	})
	return references, violations
}

func validateReviewedConstructorCall(t *testing.T, key string, call *ast.CallExpr) {
	t.Helper()
	if call.Ellipsis == token.NoPos || len(call.Args) == 0 {
		t.Errorf("%s does not spread resolved immutable policy options", key)
		return
	}
	resolved, ok := call.Args[len(call.Args)-1].(*ast.CallExpr)
	if !ok {
		t.Errorf("%s last argument is not transportOptions(resolved)", key)
		return
	}
	name, ok := resolved.Fun.(*ast.Ident)
	if !ok || name.Name != "transportOptions" {
		t.Errorf("%s last argument is not transportOptions(resolved)", key)
		return
	}
	if len(resolved.Args) != 1 {
		t.Errorf("%s transportOptions argument count = %d", key, len(resolved.Args))
		return
	}
	argument, ok := resolved.Args[0].(*ast.Ident)
	if !ok || argument.Name != "resolved" {
		t.Errorf("%s does not pass the resolved adapter options", key)
	}
}

func TestHTTPClientConstructionOracleRejectsMutations(t *testing.T) {
	const importPath = `"github.com/isukharev/atl/internal/httpx"`
	approved := map[string]int{"adapter/confluence/confluence.go:NewWithScheduler:httpx.NewWithScheduler": 1}
	tests := []struct {
		name     string
		source   string
		expected map[string]int
	}{
		{
			name: "duplicate approved call",
			source: `package fixture
import httpx ` + importPath + `
func NewWithScheduler() {
	var resolved any
	_ = httpx.NewWithScheduler("", "", "", nil, transportOptions(resolved)...)
	_ = httpx.NewWithScheduler("", "", "", nil, transportOptions(resolved)...)
}`,
			expected: approved,
		},
		{
			name: "package direct initializer",
			source: `package fixture
import httpx ` + importPath + `
var client = httpx.New("", "", "")`,
			expected: map[string]int{},
		},
		{
			name: "package constructor alias",
			source: `package fixture
import httpx ` + importPath + `
var constructor = httpx.New`,
			expected: map[string]int{},
		},
		{
			name: "local constructor alias",
			source: `package fixture
import httpx ` + importPath + `
func build() { constructor := httpx.New; _ = constructor }`,
			expected: map[string]int{},
		},
		{
			name: "dot import",
			source: `package fixture
import . ` + importPath + `
var client = New("", "", "")`,
			expected: map[string]int{},
		},
		{
			name:     "parse shape drift",
			source:   `package fixture; func broken(`,
			expected: map[string]int{},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := auditHTTPConstructorSource(test.source, "adapter/confluence/confluence.go", test.expected); err == nil {
				t.Fatal("mutated constructor source passed the closed oracle")
			}
		})
	}
}

func auditHTTPConstructorSource(source, relative string, expected map[string]int) error {
	file, err := parser.ParseFile(token.NewFileSet(), relative, source, 0)
	if err != nil {
		return fmt.Errorf("parse fixture: %w", err)
	}
	references, violations := scanHTTPConstructorReferences(file, relative)
	if len(violations) > 0 {
		return fmt.Errorf("constructor reference violations: %s", strings.Join(violations, "; "))
	}
	counts := map[string]int{}
	for _, reference := range references {
		if _, ok := expected[reference.key]; !ok {
			return fmt.Errorf("unreviewed raw HTTP client construction %s", reference.key)
		}
		counts[reference.key]++
	}
	for key, expectedCount := range expected {
		if counts[key] != expectedCount {
			return fmt.Errorf("reviewed raw HTTP client construction %s count = %d, want %d", key, counts[key], expectedCount)
		}
	}
	return nil
}
