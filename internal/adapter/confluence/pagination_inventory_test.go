package confluence

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestConfluencePaginationOwnerInventoryIsClosed(t *testing.T) {
	want := map[string][]string{
		"comments_qualified.go:ListConfluenceComments": {"advance", "requestStart", "startAt", "startAt"},
		"confluence.go:HistoryQualified":               {"advance", "requestStart", "startAt"},
		"extras.go:ListAttachmentsQualified":           {"advance", "requestStart", "startAt"},
		"extras.go:ListComments":                       {"advance", "requestStart", "startAt"},
		"labels.go:ListContentLabels":                  {"advance", "requestStart", "startAt"},
		"pagination.go:advance":                        {"checkedEnd"},
		"search.go:SearchComplete":                     {"advance", "checkedEnd", "requestStart", "startAt"},
		"search.go:Tree":                               {"advance", "requestStart", "startAt"},
	}
	tracked := map[string]bool{"advance": true, "checkedEnd": true, "startAt": true}
	got := map[string][]string{}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), entry.Name(), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", entry.Name(), err)
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			key := entry.Name() + ":" + function.Name.Name
			ast.Inspect(function.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if ok && tracked[selector.Sel.Name] {
					got[key] = append(got[key], selector.Sel.Name)
				}
				if ok && selector.Sel.Name == "Set" && len(call.Args) > 0 {
					if literal, literalOK := call.Args[0].(*ast.BasicLit); literalOK {
						value, unquoteErr := strconv.Unquote(literal.Value)
						if unquoteErr == nil && value == "start" {
							got[key] = append(got[key], "requestStart")
						}
					}
				}
				return true
			})
		}
	}
	for key := range got {
		sort.Strings(got[key])
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Confluence checked-pagination owners = %#v, want exactly %#v", got, want)
	}
}
