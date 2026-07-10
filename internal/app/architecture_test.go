package app

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestAppUseCasesDoNotImportAdapters keeps concrete transport assembly confined
// to the composition root and its compile-time verifier. Ordinary use cases
// must depend on domain/neutral packages instead.
func TestAppUseCasesDoNotImportAdapters(t *testing.T) {
	allowed := map[string]bool{"wire.go": true, "verify.go": true}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || allowed[name] {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Errorf("parse %s: %v", name, err)
			continue
		}
		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err == nil && (strings.HasSuffix(path, "/internal/adapter") || strings.Contains(path, "/internal/adapter/")) {
				t.Errorf("%s imports concrete adapter %s; move shared logic to a neutral package", name, path)
			}
		}
	}
}
