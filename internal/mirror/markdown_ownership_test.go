package mirror

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// markdownRendererSourceInventory is closed over every production markdown*.go
// file and every top-level declaration in those files. A new renderer path must
// therefore change this reviewed source-owner contract even when it uses a new
// declaration name instead of moving one of the established entry points.
var markdownRendererSourceInventory = map[string][]string{
	"markdown.go": {
		"const confluenceMarkdownCurrent",
		"const confluenceMarkdownV5",
		"func (*mdRenderer).ref",
		"func RenderMarkdown",
		"func RenderMarkdownOpts",
		"func RenderMarkdownOptsV5",
		"func RenderMarkdownViewParts",
		"func newMDRenderer",
		"func newMDRendererOffset",
		"func newMDRendererOffsetVersion",
		"func pageSectionValue",
		"func pageTableValue",
		"func renderMarkdownHeadingOffsetVersion",
		"func renderMarkdownViewPartsVersion",
		"func renderPageFields",
		"func safeMarkerID",
		"type confluenceMarkdownFormat",
		"type mdRenderer",
		"type MDViewOpts",
		"type PageField",
	},
	"markdown_block.go": {
		"func (*mdRenderer).block",
		"func (*mdRenderer).list",
		"func isHeading",
		"func markdownFence",
		"func safeMarkdownFenceInfo",
	},
	"markdown_comments.go": {
		"func commentCodeIsMultiline",
		"func commentCodeTable",
		"func exclusiveCommentCodeWrapper",
		"func renderCommentMarkdownWithRenderer",
		"func renderQualifiedCommentMarkdownVersion",
	},
	"markdown_inline.go": {
		"func (*mdRenderer).acImage",
		"func (*mdRenderer).acLink",
		"func (*mdRenderer).inline",
		"func (*mdRenderer).inlineNoBlock",
		"func (*mdRenderer).inlineNode",
		"func collapseWS",
		"func hasLeadingSpace",
		"func hasTrailingSpace",
		"func isFlowBreak",
		"func markdownLinkLabel",
		"func normalizeBlankLines",
		"func pageLinkIdentity",
		"func SafeCSSColor",
		"func squeezeSpaces",
		"func styleColor",
	},
	"markdown_macro.go": {
		"func (*mdRenderer).inlineTaskBody",
		"func (*mdRenderer).macro",
		"func (*mdRenderer).taskList",
		"func attachmentNameUnder",
		"func blockquote",
		"func includedPageTitle",
		"func IsBlockMacro",
		"func isBlockMacroName",
		"func macroParam",
		"func plainBody",
		"func richBody",
		"func soleBlockMacro",
	},
	"markdown_table.go": {
		"func (*mdRenderer).table",
		"func (*mdRenderer).tableGrid",
		"func colspanOfVersion",
		"func maxPendingCol",
		"func rowCells",
		"func rowspanOfVersion",
		"func TableGrid",
		"func tableRows",
		"type pendingCell",
		"type TableCell",
	},
}

func TestMarkdownRendererSourceInventory(t *testing.T) {
	sources := loadMarkdownRendererSources(t)
	if err := validateMarkdownRendererSources(sources); err != nil {
		t.Fatal(err)
	}
	functions, types, constants := 0, 0, 0
	for _, inventory := range markdownRendererSourceInventory {
		for _, declaration := range inventory {
			switch {
			case strings.HasPrefix(declaration, "func "):
				functions++
			case strings.HasPrefix(declaration, "type "):
				types++
			case strings.HasPrefix(declaration, "const "):
				constants++
			}
		}
	}
	if functions != 59 || types != 6 || constants != 2 {
		t.Fatalf("renderer inventory functions=%d types=%d constants=%d want 59/6/2", functions, types, constants)
	}
}

func TestMarkdownRendererSourceInventoryRejectsDrift(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*testing.T, map[string]string)
		wantErr string
	}{
		{
			name: "omitted declaration",
			mutate: func(t *testing.T, sources map[string]string) {
				removeRendererDeclaration(t, sources, "markdown.go", "func safeMarkerID")
			},
			wantErr: "missing declaration",
		},
		{
			name: "moved declaration",
			mutate: func(t *testing.T, sources map[string]string) {
				moveRendererDeclaration(t, sources, "markdown.go", "markdown_inline.go", "func safeMarkerID")
			},
			wantErr: "wrong owner",
		},
		{
			name: "duplicate declaration",
			mutate: func(t *testing.T, sources map[string]string) {
				duplicateRendererDeclaration(t, sources, "markdown.go", "func safeMarkerID")
			},
			wantErr: "duplicate declaration",
		},
		{
			name: "unexpected declaration",
			mutate: func(_ *testing.T, sources map[string]string) {
				sources["markdown_inline.go"] += "\nfunc differentlyNamedRenderer() {}\n"
			},
			wantErr: "unexpected declaration",
		},
		{
			name: "unexpected renderer source",
			mutate: func(_ *testing.T, sources map[string]string) {
				sources["markdown_duplicate.go"] = "package mirror\n\nfunc differentlyNamedRenderer() {}\n"
			},
			wantErr: "unexpected renderer source file",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sources := loadMarkdownRendererSources(t)
			test.mutate(t, sources)
			err := validateMarkdownRendererSources(sources)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error=%v want %q", err, test.wantErr)
			}
		})
	}
}

func loadMarkdownRendererSources(t *testing.T) map[string]string {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test path")
	}
	entries, err := os.ReadDir(filepath.Dir(current))
	if err != nil {
		t.Fatal(err)
	}
	sources := map[string]string{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "markdown") ||
			!strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(filepath.Dir(current), entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		sources[entry.Name()] = string(data)
	}
	return sources
}

func validateMarkdownRendererSources(sources map[string]string) error {
	wantFiles := sortedRendererKeys(markdownRendererSourceInventory)
	gotFiles := sortedRendererKeys(sources)
	if strings.Join(gotFiles, "\x00") != strings.Join(wantFiles, "\x00") {
		for _, file := range gotFiles {
			if _, ok := markdownRendererSourceInventory[file]; !ok {
				return fmt.Errorf("unexpected renderer source file %q", file)
			}
		}
		for _, file := range wantFiles {
			if _, ok := sources[file]; !ok {
				return fmt.Errorf("missing renderer source file %q", file)
			}
		}
		return fmt.Errorf("renderer source files=%v want %v", gotFiles, wantFiles)
	}

	expected := map[string]string{}
	for file, declarations := range markdownRendererSourceInventory {
		for _, declaration := range declarations {
			if previous, duplicate := expected[declaration]; duplicate {
				return fmt.Errorf("duplicate expected declaration %q in %q and %q", declaration, previous, file)
			}
			expected[declaration] = file
		}
	}
	actual := map[string][]string{}
	for _, file := range gotFiles {
		declarations, err := rendererDeclarations(file, sources[file])
		if err != nil {
			return err
		}
		for _, declaration := range declarations {
			actual[declaration] = append(actual[declaration], file)
		}
	}

	declarations := sortedRendererKeys(expected)
	for _, declaration := range declarations {
		files := actual[declaration]
		switch {
		case len(files) == 0:
			return fmt.Errorf("missing declaration %q from %q", declaration, expected[declaration])
		case len(files) > 1:
			return fmt.Errorf("duplicate declaration %q in %v", declaration, files)
		case files[0] != expected[declaration]:
			return fmt.Errorf("wrong owner for declaration %q: got %q want %q", declaration, files[0], expected[declaration])
		}
	}
	actualDeclarations := sortedRendererKeys(actual)
	for _, declaration := range actualDeclarations {
		if _, ok := expected[declaration]; !ok {
			return fmt.Errorf("unexpected declaration %q in %v", declaration, actual[declaration])
		}
	}
	return nil
}

func rendererDeclarations(filename, source string) ([]string, error) {
	files := token.NewFileSet()
	parsed, err := parser.ParseFile(files, filename, source, 0)
	if err != nil {
		return nil, fmt.Errorf("parse renderer source %q: %w", filename, err)
	}
	var declarations []string
	for _, declaration := range parsed.Decls {
		switch declaration := declaration.(type) {
		case *ast.FuncDecl:
			key, err := rendererFunctionKey(files, declaration)
			if err != nil {
				return nil, fmt.Errorf("renderer declaration in %q: %w", filename, err)
			}
			declarations = append(declarations, key)
		case *ast.GenDecl:
			if declaration.Tok == token.IMPORT {
				continue
			}
			for _, spec := range declaration.Specs {
				switch spec := spec.(type) {
				case *ast.TypeSpec:
					declarations = append(declarations, "type "+spec.Name.Name)
				case *ast.ValueSpec:
					for _, name := range spec.Names {
						declarations = append(declarations, declaration.Tok.String()+" "+name.Name)
					}
				default:
					return nil, fmt.Errorf("unsupported renderer declaration %T in %q", spec, filename)
				}
			}
		}
	}
	return declarations, nil
}

func rendererFunctionKey(files *token.FileSet, function *ast.FuncDecl) (string, error) {
	if function.Recv == nil {
		return "func " + function.Name.Name, nil
	}
	if len(function.Recv.List) != 1 {
		return "", fmt.Errorf("function %q has %d receivers", function.Name.Name, len(function.Recv.List))
	}
	var receiver bytes.Buffer
	if err := format.Node(&receiver, files, function.Recv.List[0].Type); err != nil {
		return "", fmt.Errorf("format receiver for %q: %w", function.Name.Name, err)
	}
	return "func (" + receiver.String() + ")." + function.Name.Name, nil
}

func rendererDeclarationRange(t *testing.T, filename, source, key string) (int, int) {
	t.Helper()
	files := token.NewFileSet()
	parsed, err := parser.ParseFile(files, filename, source, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok {
			continue
		}
		got, err := rendererFunctionKey(files, function)
		if err != nil {
			t.Fatal(err)
		}
		if got == key {
			return files.Position(declaration.Pos()).Offset, files.Position(declaration.End()).Offset
		}
	}
	t.Fatalf("declaration %q not found in %q", key, filename)
	return 0, 0
}

func removeRendererDeclaration(t *testing.T, sources map[string]string, filename, key string) string {
	t.Helper()
	start, end := rendererDeclarationRange(t, filename, sources[filename], key)
	declaration := sources[filename][start:end]
	sources[filename] = sources[filename][:start] + sources[filename][end:]
	return declaration
}

func moveRendererDeclaration(t *testing.T, sources map[string]string, from, to, key string) {
	t.Helper()
	declaration := removeRendererDeclaration(t, sources, from, key)
	sources[to] += "\n" + declaration + "\n"
}

func duplicateRendererDeclaration(t *testing.T, sources map[string]string, filename, key string) {
	t.Helper()
	start, end := rendererDeclarationRange(t, filename, sources[filename], key)
	sources[filename] += "\n" + sources[filename][start:end] + "\n"
}

func sortedRendererKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
