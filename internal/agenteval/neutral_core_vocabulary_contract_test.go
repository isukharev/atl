package agenteval

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

// TestNeutralCoreVocabularyContract keeps the reusable production API free of
// product, transport, routing, and dynamic-registration vocabulary. Comments,
// local identifiers, test helpers, and implementation values are deliberately
// outside this declaration-and-JSON-tag contract.
func TestNeutralCoreVocabularyContract(t *testing.T) {
	for _, root := range []string{"analysis", "core", "executionbackend", "experiment", "extension"} {
		if err := validateNeutralCoreVocabulary(root); err != nil {
			t.Fatalf("%s: %v", root, err)
		}
	}
}

func TestNeutralCoreVocabularyContractRejectsMutations(t *testing.T) {
	valid := `package core

// ATL Jira Confluence Codex Claude MCP gateway HTTP route backend budget are
// allowed in comments because this oracle owns declarations, not prose.
type Engine struct {
	Adapter string ` + "`json:\"adapter,omitempty\"`" + `
	Backend string ` + "`json:\"backend,omitempty\"`" + `
	Budget  uint64 ` + "`json:\"budget,omitempty\"`" + `
}

func Run() {
	ATLRoute := "local implementation detail"
	_ = ATLRoute
}
`
	t.Run("valid declaration boundary", func(t *testing.T) {
		root := t.TempDir()
		writeNeutralCoreFixture(t, root, "core.go", valid)
		if err := validateNeutralCoreVocabulary(root); err != nil {
			t.Fatal(err)
		}
	})

	tests := []struct {
		name   string
		source string
		want   string
	}{
		{name: "ATL type", source: "package core\n\ntype ATLPlan struct{}\n", want: "ATLPlan"},
		{name: "Jira field", source: "package core\n\ntype Plan struct { JiraID string }\n", want: "JiraID"},
		{name: "Confluence method", source: "package core\n\ntype Plan struct{}\nfunc (Plan) ConfluencePage() {}\n", want: "ConfluencePage"},
		{name: "Codex constant", source: "package core\n\nconst CodexMode = 1\n", want: "CodexMode"},
		{name: "Claude variable", source: "package core\n\nvar ClaudeAdapter any\n", want: "ClaudeAdapter"},
		{name: "MCP function", source: "package core\n\nfunc MCPExecute() {}\n", want: "MCPExecute"},
		{name: "gateway field", source: "package core\n\ntype Plan struct { Gateway string }\n", want: "Gateway"},
		{name: "HTTP field", source: "package core\n\ntype Plan struct { HTTPClient string }\n", want: "HTTPClient"},
		{name: "route tag", source: "package core\n\ntype Plan struct { Value string `json:\"route\"` }\n", want: "route"},
		{name: "backend mode pair", source: "package core\n\ntype Plan struct { BackendMode string }\n", want: "BackendMode"},
		{name: "backend observation pair", source: "package core\n\ntype Plan struct { BackendObservation string }\n", want: "BackendObservation"},
		{name: "backend requests pair", source: "package core\n\ntype Plan struct { MaxBackendRequests uint64 }\n", want: "MaxBackendRequests"},
		{name: "backend budget pair", source: "package core\n\ntype Plan struct { BackendBudget uint64 }\n", want: "BackendBudget"},
		{name: "backend budget tag", source: "package core\n\ntype Plan struct { Value uint64 `json:\"backend_budget\"` }\n", want: "backend_budget"},
		{name: "malformed JSON tag", source: "package core\n\ntype Plan struct { Value string `json:\"value` }\n", want: "malformed struct tag"},
		{name: "init", source: "package core\n\nfunc init() {}\n", want: "init functions"},
		{name: "Register", source: "package core\n\nfunc RegisterProfile() {}\n", want: "RegisterProfile"},
		{name: "MustRegister", source: "package core\n\nfunc MustRegisterProfile() {}\n", want: "MustRegisterProfile"},
		{name: "Unregister", source: "package core\n\nfunc UnregisterProfile() {}\n", want: "UnregisterProfile"},
		{name: "stdlib plugin", source: "package core\n\nimport _ \"plugin\"\n", want: "stdlib plugin"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeNeutralCoreFixture(t, root, "core.go", test.source)
			err := validateNeutralCoreVocabulary(root)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v, want substring %q", err, test.want)
			}
		})
	}
}

func validateNeutralCoreVocabulary(root string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("resolve neutral core path %s: %w", path, err)
		}
		relative = filepath.ToSlash(relative)
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("inspect neutral core %s: symbolic links are not allowed", relative)
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect neutral core %s: %w", relative, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("inspect neutral core %s: production Go sources must be regular files", relative)
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			return fmt.Errorf("parse neutral core %s: %w", relative, err)
		}
		return inspectNeutralCoreFile(relative, parsed)
	})
}

func inspectNeutralCoreFile(file string, parsed *ast.File) error {
	for _, specification := range parsed.Imports {
		path, err := strconv.Unquote(specification.Path.Value)
		if err != nil {
			return fmt.Errorf("decode neutral core import in %s: %w", file, err)
		}
		if path == "plugin" {
			return fmt.Errorf("neutral core %s imports the forbidden stdlib plugin package", file)
		}
	}
	for _, declaration := range parsed.Decls {
		switch declaration := declaration.(type) {
		case *ast.FuncDecl:
			if declaration.Name.Name == "init" {
				return fmt.Errorf("neutral core %s must not declare init functions", file)
			}
			if forbiddenRegistrationName(declaration.Name.Name) {
				return fmt.Errorf("neutral core %s declares forbidden registration function %q", file, declaration.Name.Name)
			}
			if ast.IsExported(declaration.Name.Name) {
				if err := validateNeutralVocabularyName(file, "function or method", declaration.Name.Name); err != nil {
					return err
				}
			}
		case *ast.GenDecl:
			for _, specification := range declaration.Specs {
				if err := inspectNeutralCoreSpecification(file, specification); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func inspectNeutralCoreSpecification(file string, specification ast.Spec) error {
	switch specification := specification.(type) {
	case *ast.TypeSpec:
		if ast.IsExported(specification.Name.Name) {
			if err := validateNeutralVocabularyName(file, "type", specification.Name.Name); err != nil {
				return err
			}
		}
		return inspectNeutralCoreType(file, specification.Type)
	case *ast.ValueSpec:
		for _, name := range specification.Names {
			if ast.IsExported(name.Name) {
				if err := validateNeutralVocabularyName(file, "value", name.Name); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func inspectNeutralCoreType(file string, expression ast.Expr) error {
	var inspectionErr error
	ast.Inspect(expression, func(node ast.Node) bool {
		if inspectionErr != nil {
			return false
		}
		field, ok := node.(*ast.Field)
		if !ok {
			return true
		}
		for _, name := range field.Names {
			if ast.IsExported(name.Name) {
				inspectionErr = validateNeutralVocabularyName(file, "field", name.Name)
				if inspectionErr != nil {
					return false
				}
			}
		}
		if len(field.Names) == 0 {
			if name := embeddedFieldName(field.Type); ast.IsExported(name) {
				inspectionErr = validateNeutralVocabularyName(file, "embedded field", name)
				if inspectionErr != nil {
					return false
				}
			}
		}
		if field.Tag != nil {
			inspectionErr = validateNeutralJSONTag(file, field.Tag.Value)
		}
		return inspectionErr == nil
	})
	return inspectionErr
}

func embeddedFieldName(expression ast.Expr) string {
	switch expression := expression.(type) {
	case *ast.Ident:
		return expression.Name
	case *ast.SelectorExpr:
		return expression.Sel.Name
	case *ast.StarExpr:
		return embeddedFieldName(expression.X)
	default:
		return ""
	}
}

func validateNeutralJSONTag(file, literal string) error {
	decoded, err := strconv.Unquote(literal)
	if err != nil {
		return fmt.Errorf("decode neutral core struct tag in %s: %w", file, err)
	}
	value, ok, err := lookupNeutralStructTag(decoded, "json")
	if err != nil {
		return fmt.Errorf("neutral core %s has malformed struct tag: %w", file, err)
	}
	if !ok {
		return nil
	}
	name, _, _ := strings.Cut(value, ",")
	if name == "" || name == "-" {
		return nil
	}
	return validateNeutralVocabularyName(file, "JSON tag", name)
}

func lookupNeutralStructTag(tag, want string) (string, bool, error) {
	values := map[string]string{}
	for tag != "" {
		if tag[0] == ' ' {
			tag = strings.TrimLeft(tag, " ")
			continue
		}
		keyEnd := 0
		for keyEnd < len(tag) && tag[keyEnd] > ' ' && tag[keyEnd] != ':' && tag[keyEnd] != '"' && tag[keyEnd] != 0x7f {
			keyEnd++
		}
		if keyEnd == 0 || keyEnd+1 >= len(tag) || tag[keyEnd] != ':' || tag[keyEnd+1] != '"' {
			return "", false, fmt.Errorf("invalid key/value boundary")
		}
		key := tag[:keyEnd]
		quoted := tag[keyEnd+1:]
		valueEnd := 1
		for valueEnd < len(quoted) {
			switch quoted[valueEnd] {
			case '\\':
				valueEnd += 2
				continue
			case '"':
				valueEnd++
				goto decodedValue
			}
			valueEnd++
		}
		return "", false, fmt.Errorf("unterminated quoted value for %q", key)

	decodedValue:
		value, err := strconv.Unquote(quoted[:valueEnd])
		if err != nil {
			return "", false, fmt.Errorf("decode value for %q: %w", key, err)
		}
		if _, duplicate := values[key]; duplicate {
			return "", false, fmt.Errorf("duplicate key %q", key)
		}
		values[key] = value
		tag = quoted[valueEnd:]
	}
	value, ok := values[want]
	return value, ok, nil
}

func forbiddenRegistrationName(name string) bool {
	return strings.HasPrefix(name, "Register") ||
		strings.HasPrefix(name, "MustRegister") ||
		strings.HasPrefix(name, "Unregister")
}

func validateNeutralVocabularyName(file, kind, name string) error {
	words := neutralVocabularyWords(name)
	for _, forbidden := range []string{"atl", "jira", "confluence", "codex", "claude", "mcp", "gateway", "http", "route"} {
		if words[forbidden] {
			return fmt.Errorf("neutral core %s %s %q contains forbidden vocabulary %q", file, kind, name, forbidden)
		}
	}
	if words["backend"] {
		for _, paired := range []string{"budget", "mode", "observation", "request", "requests", "route", "routes", "url", "host", "method", "methods"} {
			if words[paired] {
				return fmt.Errorf("neutral core %s %s %q contains forbidden paired backend/%s vocabulary", file, kind, name, paired)
			}
		}
	}
	return nil
}

func neutralVocabularyWords(value string) map[string]bool {
	words := map[string]bool{}
	runes := []rune(value)
	start := -1
	flush := func(end int) {
		if start >= 0 && start < end {
			words[strings.ToLower(string(runes[start:end]))] = true
		}
		start = -1
	}
	for index, current := range runes {
		if !unicode.IsLetter(current) && !unicode.IsDigit(current) {
			flush(index)
			continue
		}
		if start < 0 {
			start = index
			continue
		}
		previous := runes[index-1]
		nextLower := index+1 < len(runes) && unicode.IsLower(runes[index+1])
		if unicode.IsUpper(current) && unicode.IsLower(previous) ||
			unicode.IsUpper(current) && unicode.IsUpper(previous) && nextLower ||
			unicode.IsDigit(current) != unicode.IsDigit(previous) {
			flush(index)
			start = index
		}
	}
	flush(len(runes))
	return words
}

func writeNeutralCoreFixture(t *testing.T, root, name, contents string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
