package app

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

const appConfigImport = "github.com/isukharev/atl/internal/config"

var allowedAppConfigSelectors = map[string]bool{
	"JiraListViews": true,
	"Render":        true,
}

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

// TestAppConfigSelectorsStayRenderOnly prevents an allowlisted render owner
// from using the whole config object to recover backend, policy, or TLS state.
func TestAppConfigSelectorsStayRenderOnly(t *testing.T) {
	sources := loadAppProductionSources(t)
	configFields := loadConfigFields(t)
	if errs := validateAppConfigSelectors(sources, configFields); len(errs) != 0 {
		for _, err := range errs {
			t.Error(err)
		}
	}
}

func TestAppConfigSelectorOracleRejectsEveryNonRenderField(t *testing.T) {
	configFields := loadConfigFields(t)
	for field := range configFields {
		if allowedAppConfigSelectors[field] {
			continue
		}
		t.Run(field, func(t *testing.T) {
			source := fmt.Sprintf(`package app
import settings %q
type renderOwner struct { cfg *settings.Config }
func leak(owner *renderOwner) { alias := owner.cfg; _ = alias.%s }
`, appConfigImport, field)
			errs := validateAppConfigSelectors(map[string]string{"render.go": source}, configFields)
			if len(errs) != 1 || !strings.Contains(errs[0].Error(), "config.Config."+field) {
				t.Fatalf("errors=%v, want forbidden selector %s", errs, field)
			}
		})
	}
}

func loadAppProductionSources(t *testing.T) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	sources := map[string]string{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		sources[name] = string(body)
	}
	return sources
}

func loadConfigFields(t *testing.T) map[string]bool {
	t.Helper()
	path := filepath.Join("..", "config", "config.go")
	parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string]bool{}
	for _, declaration := range parsed.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok || general.Tok != token.TYPE {
			continue
		}
		for _, spec := range general.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok || typeSpec.Name.Name != "Config" {
				continue
			}
			structure, ok := typeSpec.Type.(*ast.StructType)
			if !ok {
				t.Fatal("config.Config is not a struct")
			}
			for _, field := range structure.Fields.List {
				for _, name := range field.Names {
					fields[name.Name] = true
				}
			}
		}
	}
	for selector := range allowedAppConfigSelectors {
		if !fields[selector] {
			t.Fatalf("allowed render selector %q is not a config.Config field", selector)
		}
	}
	if len(fields) == 0 {
		t.Fatal("config.Config fields not found")
	}
	return fields
}

type appConfigSource struct {
	filename string
	files    *token.FileSet
	parsed   *ast.File
	aliases  map[string]bool
}

func validateAppConfigSelectors(sources map[string]string, configFields map[string]bool) []error {
	parsedSources := make([]appConfigSource, 0, len(sources))
	configStructFields := map[string]map[string]bool{}
	for filename, source := range sources {
		files := token.NewFileSet()
		parsed, err := parser.ParseFile(files, filename, source, 0)
		if err != nil {
			return []error{fmt.Errorf("parse %s: %w", filename, err)}
		}
		aliases := configAliases(parsed)
		parsedSources = append(parsedSources, appConfigSource{filename: filename, files: files, parsed: parsed, aliases: aliases})
		for _, declaration := range parsed.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || general.Tok != token.TYPE {
				continue
			}
			for _, spec := range general.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				structure, ok := typeSpec.Type.(*ast.StructType)
				if !ok {
					continue
				}
				for _, field := range structure.Fields.List {
					if !isConfigType(field.Type, aliases) {
						continue
					}
					if configStructFields[typeSpec.Name.Name] == nil {
						configStructFields[typeSpec.Name.Name] = map[string]bool{}
					}
					for _, name := range field.Names {
						configStructFields[typeSpec.Name.Name][name.Name] = true
					}
				}
			}
		}
	}

	var errs []error
	for _, source := range parsedSources {
		for _, declaration := range source.parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			environment := appFunctionTypes(function, source.aliases)
			for changed := true; changed; {
				changed = false
				ast.Inspect(function.Body, func(node ast.Node) bool {
					switch node := node.(type) {
					case *ast.AssignStmt:
						for index, left := range node.Lhs {
							if index >= len(node.Rhs) {
								break
							}
							name, ok := left.(*ast.Ident)
							kind := appExpressionType(node.Rhs[index], environment, configStructFields, source.aliases)
							if ok && kind != "" && environment[name.Name] != kind {
								environment[name.Name] = kind
								changed = true
							}
						}
					case *ast.DeclStmt:
						general, ok := node.Decl.(*ast.GenDecl)
						if !ok {
							break
						}
						for _, spec := range general.Specs {
							value, ok := spec.(*ast.ValueSpec)
							if !ok {
								continue
							}
							kind := appTypeName(value.Type, source.aliases)
							for index, name := range value.Names {
								if kind == "" && index < len(value.Values) {
									kind = appExpressionType(value.Values[index], environment, configStructFields, source.aliases)
								}
								if kind != "" && environment[name.Name] != kind {
									environment[name.Name] = kind
									changed = true
								}
							}
						}
					}
					return true
				})
			}
			ast.Inspect(function.Body, func(node ast.Node) bool {
				selector, ok := node.(*ast.SelectorExpr)
				if !ok || appExpressionType(selector.X, environment, configStructFields, source.aliases) != "config.Config" {
					return true
				}
				if allowedAppConfigSelectors[selector.Sel.Name] {
					return true
				}
				kind := "non-render selector"
				if configFields[selector.Sel.Name] {
					kind = "non-render field"
				}
				errs = append(errs, fmt.Errorf("%s:%d selects config.Config.%s (%s)",
					source.filename, source.files.Position(selector.Pos()).Line, selector.Sel.Name, kind))
				return true
			})
		}
	}
	return errs
}

func configAliases(file *ast.File) map[string]bool {
	aliases := map[string]bool{}
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil || path != appConfigImport {
			continue
		}
		name := "config"
		if spec.Name != nil {
			name = spec.Name.Name
		}
		aliases[name] = true
	}
	return aliases
}

func appFunctionTypes(function *ast.FuncDecl, aliases map[string]bool) map[string]string {
	environment := map[string]string{}
	for _, fields := range []*ast.FieldList{function.Recv, function.Type.Params} {
		if fields == nil {
			continue
		}
		for _, field := range fields.List {
			kind := appTypeName(field.Type, aliases)
			for _, name := range field.Names {
				environment[name.Name] = kind
			}
		}
	}
	return environment
}

func appTypeName(expression ast.Expr, aliases map[string]bool) string {
	for {
		switch typed := expression.(type) {
		case *ast.StarExpr:
			expression = typed.X
		case *ast.ParenExpr:
			expression = typed.X
		case *ast.Ident:
			return typed.Name
		case *ast.SelectorExpr:
			base, ok := typed.X.(*ast.Ident)
			if ok && aliases[base.Name] && typed.Sel.Name == "Config" {
				return "config.Config"
			}
			return ""
		default:
			return ""
		}
	}
}

func isConfigType(expression ast.Expr, aliases map[string]bool) bool {
	return appTypeName(expression, aliases) == "config.Config"
}

func appExpressionType(expression ast.Expr, environment map[string]string, configStructFields map[string]map[string]bool, aliases map[string]bool) string {
	switch expression := expression.(type) {
	case *ast.Ident:
		return environment[expression.Name]
	case *ast.ParenExpr:
		return appExpressionType(expression.X, environment, configStructFields, aliases)
	case *ast.UnaryExpr:
		return appExpressionType(expression.X, environment, configStructFields, aliases)
	case *ast.CompositeLit:
		return appTypeName(expression.Type, aliases)
	case *ast.SelectorExpr:
		owner := appExpressionType(expression.X, environment, configStructFields, aliases)
		if configStructFields[owner][expression.Sel.Name] {
			return "config.Config"
		}
	}
	return ""
}
