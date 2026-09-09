package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestBrokerJiraCommentPortAndDispatchStayNarrow(t *testing.T) {
	port := reflect.TypeFor[domain.BrokerJiraGuardedCommentPort]()
	want := map[string]bool{
		"BrokerOriginSHA256": true, "QualifyBrokerIssue": true,
		"ReadGuardedCommentIssue": true, "ReadGuardedCommentActor": true,
		"ListJiraCommentsQualified": true, "WriteGuardedComment": true,
	}
	if port.NumMethod() != len(want) {
		t.Fatalf("port methods=%d", port.NumMethod())
	}
	for index := range port.NumMethod() {
		if !want[port.Method(index).Name] {
			t.Fatalf("unexpected port method %s", port.Method(index).Name)
		}
	}

	parsed, err := parser.ParseFile(token.NewFileSet(), "jira_comments_guarded.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	writes := 0
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != "dispatchGuardedComment" {
			continue
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
			switch selector.Sel.Name {
			case "WriteGuardedComment":
				writes++
			case "SendJSON", "Do", "DoWithBodyLimit":
				t.Fatalf("dispatch uses transport method %s", selector.Sel.Name)
			}
			return true
		})
	}
	if writes != 1 {
		t.Fatalf("dispatch write calls=%d", writes)
	}
}
