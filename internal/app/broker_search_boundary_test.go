package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestBrokerProjectPageUsesOnlyNarrowPortsAndImports(t *testing.T) {
	assertBrokerSearchPort(t, reflect.TypeFor[domain.BrokerJiraProjectPageReadPort](), []string{
		"BrokerOriginSHA256", "QualifyBrokerProject", "QualifyBrokerProjectIssuePage", "ReadBrokerProjectIssuePage",
	})
	assertBrokerSearchPort(t, reflect.TypeFor[domain.BrokerProjectPageAuthorizerV2](), []string{
		"AdmitProjectPage", "AuthorizeProjectPage", "AuthorizeProjectPageQualification",
	})
	for _, path := range []string{"broker_search.go", "../adapter/jira/broker_search.go"} {
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imported := range parsed.Imports {
			name, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(name, "github.com/isukharev/atl/internal/") {
				continue
			}
			allowed := name == "github.com/isukharev/atl/internal/domain" || name == "github.com/isukharev/atl/internal/brokercontract" || name == "github.com/isukharev/atl/internal/strictjson"
			if !allowed {
				t.Errorf("%s imports broad ATL package %q", path, name)
			}
		}
	}

	parsed, err := parser.ParseFile(token.NewFileSet(), "broker_search.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	allowedReader := map[string]bool{"BrokerOriginSHA256": true, "QualifyBrokerProject": true, "QualifyBrokerProjectIssuePage": true, "ReadBrokerProjectIssuePage": true}
	allowedAuthorizer := map[string]bool{"AdmitProjectPage": true, "AuthorizeProjectPageQualification": true, "AuthorizeProjectPage": true}
	ast.Inspect(parsed, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		method, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if isBrokerSearchDependency(method.X, "authorizer") && !allowedAuthorizer[method.Sel.Name] {
			t.Errorf("broker search calls broad authorizer method %s", method.Sel.Name)
		}
		if isBrokerSearchReader(method.X) && !allowedReader[method.Sel.Name] {
			t.Errorf("broker search calls broad Jira method %s", method.Sel.Name)
		}
		return true
	})
}

func assertBrokerSearchPort(t *testing.T, port reflect.Type, methods []string) {
	t.Helper()
	got := make([]string, port.NumMethod())
	for index := range port.NumMethod() {
		got[index] = port.Method(index).Name
	}
	slices.Sort(got)
	slices.Sort(methods)
	if !slices.Equal(got, methods) {
		t.Fatalf("%s methods=%v want=%v", port, got, methods)
	}
}

func isBrokerSearchDependency(expression ast.Expr, field string) bool {
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != field {
		return false
	}
	receiver, ok := selector.X.(*ast.Ident)
	return ok && receiver.Name == "s"
}

func isBrokerSearchReader(expression ast.Expr) bool {
	reader, ok := expression.(*ast.SelectorExpr)
	if !ok || reader.Sel.Name != "Reader" {
		return false
	}
	return isBrokerSearchDependency(reader.X, "jira")
}
