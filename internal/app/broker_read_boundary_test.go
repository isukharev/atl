package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"slices"
	"strconv"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestBrokerReadUsesOnlyNarrowPortsAndAdapterImports(t *testing.T) {
	assertBrokerReadPort(t, reflect.TypeFor[domain.BrokerJiraIssueReadPort](), []string{"BrokerOriginSHA256", "QualifyBrokerIssue", "ReadBrokerIssue"})
	assertBrokerReadPort(t, reflect.TypeFor[domain.BrokerConfluencePageReadPort](), []string{"BrokerOriginSHA256", "QualifyBrokerPage", "ReadBrokerPage"})

	appFiles := []string{"broker_read.go", "broker_read_qualification.go"}
	allowedCalls := map[string]map[string]bool{
		"authorizer": {"Admit": true, "AuthorizeQualification": true, "AuthorizeOperation": true},
		"jira":       {"QualifyBrokerIssue": true, "ReadBrokerIssue": true},
		"confluence": {"QualifyBrokerPage": true, "ReadBrokerPage": true},
	}
	for _, path := range appFiles {
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imported := range parsed.Imports {
			name, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if len(name) >= len("github.com/isukharev/atl/internal/") && name[:len("github.com/isukharev/atl/internal/")] == "github.com/isukharev/atl/internal/" &&
				name != "github.com/isukharev/atl/internal/domain" && name != "github.com/isukharev/atl/internal/brokercontract" {
				t.Errorf("%s imports broad ATL package %q", path, name)
			}
		}
		parsed, err = parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, violation := range brokerReadDependencyViolations(parsed, allowedCalls) {
			t.Errorf("%s: %s", path, violation)
		}
	}

	for _, path := range []string{"../adapter/jira/broker_read.go", "../adapter/confluence/broker_read.go"} {
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imported := range parsed.Imports {
			name, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if len(name) >= len("github.com/isukharev/atl/internal/") && name[:len("github.com/isukharev/atl/internal/")] == "github.com/isukharev/atl/internal/" &&
				name != "github.com/isukharev/atl/internal/backendid" && name != "github.com/isukharev/atl/internal/domain" && name != "github.com/isukharev/atl/internal/strictjson" {
				t.Errorf("%s imports broad ATL package %q", path, name)
			}
		}
	}
}

func TestBrokerReadDependencyOracleRejectsBroadDispatch(t *testing.T) {
	parsed, err := parser.ParseFile(token.NewFileSet(), "fixture.go", `package app
func unsafe(s *BrokerReadService) { _, _ = s.jira.GetIssue(nil, "EXAMPLE-1") }
`, 0)
	if err != nil {
		t.Fatal(err)
	}
	violations := brokerReadDependencyViolations(parsed, map[string]map[string]bool{"jira": {"ReadBrokerIssue": true}})
	if len(violations) != 1 {
		t.Fatalf("violations=%v", violations)
	}
}

func assertBrokerReadPort(t *testing.T, port reflect.Type, methods []string) {
	t.Helper()
	got := make([]string, port.NumMethod())
	for index := range port.NumMethod() {
		got[index] = port.Method(index).Name
	}
	slices.Sort(got)
	slices.Sort(methods)
	if !slices.Equal(got, methods) {
		t.Fatalf("%s methods=%v", port, got)
	}
}

func brokerReadDependencyViolations(file *ast.File, allowed map[string]map[string]bool) []string {
	var violations []string
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		method, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		dependency, ok := method.X.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		receiver, ok := dependency.X.(*ast.Ident)
		if !ok || receiver.Name != "s" {
			return true
		}
		methods, guarded := allowed[dependency.Sel.Name]
		if guarded && !methods[method.Sel.Name] {
			violations = append(violations, dependency.Sel.Name+"."+method.Sel.Name)
		}
		return true
	})
	return violations
}
