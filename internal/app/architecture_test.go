package app

import (
	"fmt"
	"go/ast"
	"go/build"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
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
		"jira_pull_types.go": true, "jira_related.go": true, "jira_render.go": true, "jira_structure.go": true,
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
	violations, err := validateAppConfigSelectors(sources)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		for _, violation := range violations {
			t.Error(violation)
		}
	}
}

func TestAppConfigSelectorOracleRejectsTypedLeakage(t *testing.T) {
	tests := []struct {
		name, field, declaration string
	}{
		{
			name: "direct selector read", field: "JiraURL",
			declaration: `func leak(value *settings.Config) { _ = value.JiraURL }`,
		},
		{
			name: "local alias write", field: "ReadOnly",
			declaration: `func leak(value *settings.Config) { alias := value; alias.ReadOnly = true }`,
		},
		{
			name: "promoted anonymous embedding", field: "ConfluenceURL",
			declaration: `type embedded struct { *settings.Config }
func leak(value embedded) { _ = value.ConfluenceURL }`,
		},
		{
			name: "type alias", field: "UpdateBaseURL",
			declaration: `type configAlias = settings.Config
func leak(value *configAlias) { _ = value.UpdateBaseURL }`,
		},
		{
			name: "keyed composite literal", field: "Transport",
			declaration: `func leak() { _ = settings.Config{Transport: nil} }`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := fmt.Sprintf("package app\nimport settings %q\n%s\n", appConfigImport, test.declaration)
			violations, err := validateAppConfigSelectors(map[string]string{"render.go": source})
			if err != nil {
				t.Fatal(err)
			}
			if len(violations) != 1 || !strings.Contains(violations[0].Error(), "config.Config."+test.field) {
				t.Fatalf("violations=%v, want typed config.Config.%s", violations, test.field)
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

func validateAppConfigSelectors(sources map[string]string) ([]error, error) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		return nil, err
	}
	files := token.NewFileSet()
	parsed := make([]*ast.File, 0, len(sources))
	filenames := make([]string, 0, len(sources))
	for filename := range sources {
		filenames = append(filenames, filename)
	}
	sort.Strings(filenames)
	for _, filename := range filenames {
		file, err := parser.ParseFile(files, filename, sources[filename], 0)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", filename, err)
		}
		parsed = append(parsed, file)
	}

	imports := newAppSourceImporter(root)
	configPackage, err := imports.Import(appConfigImport)
	if err != nil {
		return nil, fmt.Errorf("load config package: %w", err)
	}
	configFields, err := configFieldObjects(configPackage)
	if err != nil {
		return nil, err
	}
	info := &types.Info{
		Selections: map[*ast.SelectorExpr]*types.Selection{},
		Uses:       map[*ast.Ident]types.Object{},
	}
	checker := types.Config{Importer: imports}
	if _, err := checker.Check("github.com/isukharev/atl/internal/app", files, parsed, info); err != nil {
		return nil, fmt.Errorf("type-check app sources: %w", err)
	}

	var violations []error
	for selector, selection := range info.Selections {
		field, ok := selection.Obj().(*types.Var)
		if !ok || !configFields[field] || allowedAppConfigSelectors[field.Name()] {
			continue
		}
		violations = append(violations, configSelectorViolation(files, selector.Pos(), field))
	}
	for _, file := range parsed {
		ast.Inspect(file, func(node ast.Node) bool {
			keyed, ok := node.(*ast.KeyValueExpr)
			if !ok {
				return true
			}
			name, ok := keyed.Key.(*ast.Ident)
			if !ok {
				return true
			}
			field, ok := info.Uses[name].(*types.Var)
			if !ok || !configFields[field] || allowedAppConfigSelectors[field.Name()] {
				return true
			}
			violations = append(violations, configSelectorViolation(files, name.Pos(), field))
			return true
		})
	}
	sort.Slice(violations, func(i, j int) bool { return violations[i].Error() < violations[j].Error() })
	return violations, nil
}

func configFieldObjects(pkg *types.Package) (map[*types.Var]bool, error) {
	object, ok := pkg.Scope().Lookup("Config").(*types.TypeName)
	if !ok {
		return nil, fmt.Errorf("%s.Config type not found", appConfigImport)
	}
	named, ok := types.Unalias(object.Type()).(*types.Named)
	if !ok {
		return nil, fmt.Errorf("%s.Config is not named", appConfigImport)
	}
	structure, ok := named.Underlying().(*types.Struct)
	if !ok {
		return nil, fmt.Errorf("%s.Config is not a struct", appConfigImport)
	}
	fields := map[*types.Var]bool{}
	for index := range structure.NumFields() {
		field := structure.Field(index)
		fields[field] = true
	}
	for selector := range allowedAppConfigSelectors {
		found := false
		for field := range fields {
			found = found || field.Name() == selector
		}
		if !found {
			return nil, fmt.Errorf("allowed render selector %q is not a config.Config field", selector)
		}
	}
	return fields, nil
}

func configSelectorViolation(files *token.FileSet, position token.Pos, field *types.Var) error {
	return fmt.Errorf("%s selects config.Config.%s (typed field %s.%s)",
		files.Position(position), field.Name(), field.Pkg().Path(), field.Name())
}

type appSourceImporter struct {
	root           string
	stdlib         types.Importer
	moduleVersions map[string]string
	cache          map[string]*types.Package
	loading        map[string]bool
}

func newAppSourceImporter(root string) *appSourceImporter {
	return &appSourceImporter{
		root: root, stdlib: importer.Default(), moduleVersions: readModuleVersions(root),
		cache: map[string]*types.Package{}, loading: map[string]bool{},
	}
}

func (i *appSourceImporter) Import(importPath string) (*types.Package, error) {
	const modulePath = "github.com/isukharev/atl"
	var directory string
	if strings.HasPrefix(importPath, modulePath+"/") {
		directory = filepath.Join(i.root, filepath.FromSlash(strings.TrimPrefix(importPath, modulePath+"/")))
	} else {
		pkg, standardErr := i.stdlib.Import(importPath)
		if standardErr == nil {
			return pkg, nil
		}
		var ok bool
		directory, ok = i.modulePackageDirectory(importPath)
		if !ok {
			return nil, standardErr
		}
	}
	if pkg := i.cache[importPath]; pkg != nil {
		return pkg, nil
	}
	if i.loading[importPath] {
		return nil, fmt.Errorf("unexpected source import cycle at %s", importPath)
	}
	i.loading[importPath] = true
	defer delete(i.loading, importPath)

	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	files := token.NewFileSet()
	var parsed []*ast.File
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		matched, err := build.Default.MatchFile(directory, name)
		if err != nil {
			return nil, err
		}
		if !matched {
			continue
		}
		file, err := parser.ParseFile(files, filepath.Join(directory, name), nil, 0)
		if err != nil {
			return nil, err
		}
		parsed = append(parsed, file)
	}
	if len(parsed) == 0 {
		return nil, fmt.Errorf("no production Go sources for %s", importPath)
	}
	checker := types.Config{Importer: i}
	pkg, err := checker.Check(importPath, files, parsed, nil)
	if err != nil {
		return nil, err
	}
	i.cache[importPath] = pkg
	return pkg, nil
}

func readModuleVersions(root string) map[string]string {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return nil
	}
	versions := map[string]string{}
	inRequire := false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(strings.SplitN(line, "//", 2)[0])
		switch {
		case line == "require (":
			inRequire = true
			continue
		case inRequire && line == ")":
			inRequire = false
			continue
		}
		fields := strings.Fields(line)
		if inRequire && len(fields) == 2 {
			versions[fields[0]] = fields[1]
		} else if len(fields) == 3 && fields[0] == "require" {
			versions[fields[1]] = fields[2]
		}
	}
	return versions
}

func (i *appSourceImporter) modulePackageDirectory(importPath string) (string, bool) {
	modulePath, version := "", ""
	for candidate, candidateVersion := range i.moduleVersions {
		if (importPath == candidate || strings.HasPrefix(importPath, candidate+"/")) && len(candidate) > len(modulePath) {
			modulePath, version = candidate, candidateVersion
		}
	}
	if modulePath == "" {
		return "", false
	}
	relative := strings.TrimPrefix(importPath, modulePath)
	relative = strings.TrimPrefix(relative, "/")
	for _, workspace := range filepath.SplitList(build.Default.GOPATH) {
		directory := filepath.Join(workspace, "pkg", "mod", filepath.FromSlash(escapeModuleCache(modulePath)+"@"+escapeModuleCache(version)), filepath.FromSlash(relative))
		if info, err := os.Stat(directory); err == nil && info.IsDir() {
			return directory, true
		}
	}
	return "", false
}

func escapeModuleCache(value string) string {
	var escaped strings.Builder
	for _, character := range value {
		if character >= 'A' && character <= 'Z' {
			escaped.WriteByte('!')
			character += 'a' - 'A'
		}
		escaped.WriteRune(character)
	}
	return escaped.String()
}
