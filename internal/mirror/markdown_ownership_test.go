package mirror

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMarkdownRendererSourceOwnersStaySeparated(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test path")
	}
	want := map[string]string{
		"confluenceMarkdownFormat":              "markdown.go",
		"confluenceMarkdownCurrent":             "markdown.go",
		"confluenceMarkdownV5":                  "markdown.go",
		"RenderMarkdown":                        "markdown.go",
		"RenderMarkdownOpts":                    "markdown.go",
		"RenderMarkdownOptsV5":                  "markdown.go",
		"RenderMarkdownViewParts":               "markdown.go",
		"renderMarkdownViewPartsVersion":        "markdown.go",
		"block":                                 "markdown_block.go",
		"markdownFence":                         "markdown_block.go",
		"renderQualifiedCommentMarkdownVersion": "markdown_comments.go",
		"renderCommentMarkdownWithRenderer":     "markdown_comments.go",
		"inlineNode":                            "markdown_inline.go",
		"SafeCSSColor":                          "markdown_inline.go",
		"macro":                                 "markdown_macro.go",
		"IsBlockMacro":                          "markdown_macro.go",
		"tableGrid":                             "markdown_table.go",
		"TableGrid":                             "markdown_table.go",
	}
	got := map[string]string{}
	err := filepath.WalkDir(filepath.Dir(current), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		for _, declaration := range parsed.Decls {
			switch declaration := declaration.(type) {
			case *ast.FuncDecl:
				if _, tracked := want[declaration.Name.Name]; tracked {
					got[declaration.Name.Name] = filepath.Base(path)
				}
			case *ast.GenDecl:
				for _, spec := range declaration.Specs {
					switch spec := spec.(type) {
					case *ast.TypeSpec:
						if _, tracked := want[spec.Name.Name]; tracked {
							got[spec.Name.Name] = filepath.Base(path)
						}
					case *ast.ValueSpec:
						for _, name := range spec.Names {
							if _, tracked := want[name.Name]; tracked {
								got[name.Name] = filepath.Base(path)
							}
						}
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, file := range want {
		if got[name] != file {
			t.Errorf("%s owner=%q want %q", name, got[name], file)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("tracked declarations=%v want %v", got, want)
	}
}
