package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestBrokerOperationObservationUsesOnlyAuthorizedNarrowPorts(t *testing.T) {
	port := reflect.TypeFor[domain.BrokerJournalLookup]()
	if port.NumMethod() != 1 || port.Method(0).Name != "Lookup" {
		t.Fatalf("journal lookup port has %d methods", port.NumMethod())
	}
	parsed, err := parser.ParseFile(token.NewFileSet(), "broker_operation_observation.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]map[string]bool{
		"authorizer": {"Admit": true, "AuthorizeQualification": true, "AuthorizeOperation": true},
		"journal":    {"Lookup": true},
	}
	if violations := brokerOperationObservationDependencyViolations(parsed, allowed); len(violations) != 0 {
		t.Fatalf("broad operation-observation dependencies: %v", violations)
	}
}

func TestBrokerOperationObservationDependencyOracleRejectsArtifactRead(t *testing.T) {
	parsed, err := parser.ParseFile(token.NewFileSet(), "fixture.go", `package app
func unsafe(s *BrokerOperationObservationService) { _, _ = s.journal.ReadArtifact(nil, owner, "ticket") }
`, 0)
	if err != nil {
		t.Fatal(err)
	}
	violations := brokerOperationObservationDependencyViolations(parsed, map[string]map[string]bool{"journal": {"Lookup": true}})
	if len(violations) != 1 || violations[0] != "journal.ReadArtifact" {
		t.Fatalf("violations=%v", violations)
	}
}

func brokerOperationObservationDependencyViolations(file *ast.File, allowed map[string]map[string]bool) []string {
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
