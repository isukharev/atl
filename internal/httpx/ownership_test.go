package httpx

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTransportResponsibilityOwnersStayClosed(t *testing.T) {
	expected := map[string]string{
		"Option": "options.go", "WithTrace": "options.go", "WithGenericConflict": "options.go", "WithRequiredWriteClearance": "options.go",
		"TLSOptions": "tls.go", "ValidateCABundle": "tls.go", "transportWithCABundle": "tls.go",
		"attemptResult": "attempt.go", "classifyAttempt": "attempt.go", "classifyResult": "attempt.go",
		"replaySafe": "retry.go", "backoff": "retry.go", "retryAfter": "retry.go", "sleep": "retry.go",
		"ReadCapped": "body.go", "idleReader": "body.go", "readResponseBody": "body.go",
		"APIError": "errors.go", "TransportError": "errors.go", "classify": "errors.go", "transportErrorCategory": "errors.go",
		"readBudgetTransport": "transport.go", "resolveURL": "transport.go", "newRequestReader": "transport.go",
	}
	found := make(map[string]string, len(expected))
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
			t.Fatal(err)
		}
		for _, declaration := range file.Decls {
			switch value := declaration.(type) {
			case *ast.FuncDecl:
				checkResponsibilityOwner(t, expected, found, value.Name.Name, entry.Name())
			case *ast.GenDecl:
				for _, spec := range value.Specs {
					if typeSpec, ok := spec.(*ast.TypeSpec); ok {
						checkResponsibilityOwner(t, expected, found, typeSpec.Name.Name, entry.Name())
					}
				}
			}
		}
	}
	for name, owner := range expected {
		if found[name] != owner {
			t.Errorf("responsibility %s owner = %q, want %q", name, found[name], owner)
		}
	}
}

func checkResponsibilityOwner(t *testing.T, expected, found map[string]string, name, file string) {
	t.Helper()
	owner, tracked := expected[name]
	if !tracked {
		return
	}
	if prior := found[name]; prior != "" {
		t.Errorf("responsibility %s declared in both %s and %s", name, prior, file)
		return
	}
	found[name] = file
	if file != owner {
		t.Errorf("responsibility %s declared in %s, want %s", name, file, owner)
	}
}
