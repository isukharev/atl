package agenteval

import (
	"fmt"
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

const productInternalImportPrefix = "github.com/isukharev/atl/internal/"

type evaluatorDependencyLedger struct {
	Production           map[string][]string
	Tests                map[string][]string
	EntrypointProduction map[string][]string
	EntrypointTests      map[string][]string
}

// TestEvaluatorProductDependencyLedger is a reviewed, exact ownership ledger,
// not a minimum or maximum. Any added, removed, moved, aliased, dot, or blank
// product-private import changes a package/file entry and requires explicit
// review. The ledger scans every evaluator source file, including this oracle,
// so its own imports cannot conceal a dependency outside the exact boundary.
// The first reviewed baseline was 29/27/5 production declarations/files/targets
// and 66/33/10 for tests. The evaluator library production and test lanes are
// now each intentionally 0/0/0. The entrypoint lanes remain 4/4/1 and 3/3/1.
// CLI error, capability, skill,
// synthetic backend, and selected-binary Jira and Confluence evidence workflows
// decode evaluator-owned released wires rather than constructing evidence from
// product app/config/domain/mdwiki owners.
func TestEvaluatorProductDependencyLedger(t *testing.T) {
	want := evaluatorDependencyLedger{
		Production: map[string][]string{},
		Tests:      map[string][]string{},
		EntrypointProduction: map[string][]string{
			"github.com/isukharev/atl/internal/agenteval": {
				"command_broker.go", "main.go", "private.go", "proxy.go",
			},
		},
		EntrypointTests: map[string][]string{
			"github.com/isukharev/atl/internal/agenteval": {
				"main_test.go", "private_test.go", "proxy_cli_error_contract_test.go",
			},
		},
	}
	got, err := scanEvaluatorDependencyLedger(".", filepath.Join("..", "..", "scripts", "agent-eval"))
	if err != nil {
		t.Fatal(err)
	}
	if err := compareEvaluatorDependencyLedgers(got, want); err != nil {
		t.Fatal(err)
	}
	if declarations, files, targets := dependencyLaneCounts(got.Production); declarations != 0 || files != 0 || targets != 0 {
		t.Fatalf("production dependency counts=%d declarations/%d files/%d targets, want 0/0/0", declarations, files, targets)
	}
	if declarations, files, targets := dependencyLaneCounts(got.Tests); declarations != 0 || files != 0 || targets != 0 {
		t.Fatalf("test dependency counts=%d declarations/%d files/%d targets, want 0/0/0", declarations, files, targets)
	}
	if declarations, files, targets := dependencyLaneCounts(got.EntrypointProduction); declarations != 4 || files != 4 || targets != 1 {
		t.Fatalf("entrypoint production dependency counts=%d declarations/%d files/%d targets, want 4/4/1", declarations, files, targets)
	}
	if declarations, files, targets := dependencyLaneCounts(got.EntrypointTests); declarations != 3 || files != 3 || targets != 1 {
		t.Fatalf("entrypoint test dependency counts=%d declarations/%d files/%d targets, want 3/3/1", declarations, files, targets)
	}
}

func TestEvaluatorProductDependencyLedgerDetectsAliasAndOwnershipDrift(t *testing.T) {
	parsed, err := parser.ParseFile(token.NewFileSet(), "synthetic.go", `package synthetic
import (
	alias "github.com/isukharev/atl/internal/app"
	. "github.com/isukharev/atl/internal/domain"
	_ "github.com/isukharev/atl/internal/httpx"
	"fmt"
)`, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	ledger := evaluatorDependencyLedger{Production: map[string][]string{}, Tests: map[string][]string{}}
	if err := addParsedProductImports(ledger.Production, "synthetic.go", parsed); err != nil {
		t.Fatal(err)
	}
	if declarations, files, targets := dependencyLaneCounts(ledger.Production); declarations != 3 || files != 1 || targets != 3 {
		t.Fatalf("aliased imports counted as %d declarations/%d files/%d targets", declarations, files, targets)
	}

	t.Run("oracle file remains inside scan", func(t *testing.T) {
		directory := t.TempDir()
		path := filepath.Join(directory, "product_dependency_contract_test.go")
		if err := os.WriteFile(path, []byte(`package synthetic
import _ "github.com/isukharev/atl/internal/httpx"
`), 0o600); err != nil {
			t.Fatal(err)
		}
		production, tests := map[string][]string{}, map[string][]string{}
		if err := scanEvaluatorDependencyDirectory(directory, production, tests); err != nil {
			t.Fatal(err)
		}
		want := map[string][]string{
			productInternalImportPrefix + "httpx": {"product_dependency_contract_test.go"},
		}
		if !reflect.DeepEqual(tests, want) {
			t.Fatalf("oracle-file dependencies = %v, want %v", tests, want)
		}
	})

	zeroLibraryLanes := evaluatorDependencyLedger{Production: map[string][]string{}, Tests: map[string][]string{}}
	for name, mutation := range map[string]evaluatorDependencyLedger{
		"production": {
			Production: map[string][]string{productInternalImportPrefix + "unexpected": {"unexpected.go"}},
			Tests:      cloneDependencyLane(zeroLibraryLanes.Tests),
		},
		"tests": {
			Production: cloneDependencyLane(zeroLibraryLanes.Production),
			Tests:      map[string][]string{productInternalImportPrefix + "unexpected": {"unexpected_test.go"}},
		},
	} {
		t.Run("zero library lane "+name, func(t *testing.T) {
			if compareEvaluatorDependencyLedgers(mutation, zeroLibraryLanes) == nil {
				t.Fatal("unexpected product-private dependency in zero library lane was not detected")
			}
		})
	}

	baseline := evaluatorDependencyLedger{
		Production: map[string][]string{productInternalImportPrefix + "capability": {"runspec.go"}},
		Tests:      map[string][]string{productInternalImportPrefix + "domain": {"contract_test.go"}},
	}
	mutations := map[string]evaluatorDependencyLedger{
		"added": {
			Production: map[string][]string{
				productInternalImportPrefix + "capability": {"runspec.go"},
				productInternalImportPrefix + "newowner":   {"new.go"},
			},
			Tests: cloneDependencyLane(baseline.Tests),
		},
		"removed": {Production: map[string][]string{}, Tests: cloneDependencyLane(baseline.Tests)},
		"reclassified": {
			Production: map[string][]string{},
			Tests: map[string][]string{
				productInternalImportPrefix + "capability": {"runspec_test.go"},
				productInternalImportPrefix + "domain":     {"contract_test.go"},
			},
		},
	}
	for name, mutation := range mutations {
		t.Run(name, func(t *testing.T) {
			if compareEvaluatorDependencyLedgers(mutation, baseline) == nil {
				t.Fatal("dependency drift was not detected")
			}
		})
	}
}

func scanEvaluatorDependencyLedger(libraryDirectory, entrypointDirectory string) (evaluatorDependencyLedger, error) {
	ledger := evaluatorDependencyLedger{
		Production: map[string][]string{}, Tests: map[string][]string{},
		EntrypointProduction: map[string][]string{}, EntrypointTests: map[string][]string{},
	}
	if err := scanEvaluatorDependencyDirectory(libraryDirectory, ledger.Production, ledger.Tests); err != nil {
		return evaluatorDependencyLedger{}, err
	}
	if err := scanEvaluatorDependencyDirectory(entrypointDirectory, ledger.EntrypointProduction, ledger.EntrypointTests); err != nil {
		return evaluatorDependencyLedger{}, err
	}
	for _, lane := range []map[string][]string{ledger.Production, ledger.Tests, ledger.EntrypointProduction, ledger.EntrypointTests} {
		for path := range lane {
			sort.Strings(lane[path])
		}
	}
	return ledger, nil
}

func scanEvaluatorDependencyDirectory(directory string, production, tests map[string][]string) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		path := filepath.Join(directory, name)
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return fmt.Errorf("parse evaluator dependency owner %s: %w", path, err)
		}
		lane := production
		if strings.HasSuffix(name, "_test.go") {
			lane = tests
		}
		if err := addParsedProductImports(lane, name, parsed); err != nil {
			return err
		}
	}
	return nil
}

func addParsedProductImports(lane map[string][]string, file string, parsed *ast.File) error {
	for _, specification := range parsed.Imports {
		path, err := strconv.Unquote(specification.Path.Value)
		if err != nil {
			return fmt.Errorf("decode evaluator import in %s: %w", file, err)
		}
		if !strings.HasPrefix(path, productInternalImportPrefix) {
			continue
		}
		lane[path] = append(lane[path], file)
	}
	return nil
}

func compareEvaluatorDependencyLedgers(got, want evaluatorDependencyLedger) error {
	if !reflect.DeepEqual(got.Production, want.Production) {
		return fmt.Errorf("production product-private dependency ledger changed: got %v, want %v", got.Production, want.Production)
	}
	if !reflect.DeepEqual(got.Tests, want.Tests) {
		return fmt.Errorf("test product-private dependency ledger changed: got %v, want %v", got.Tests, want.Tests)
	}
	if !reflect.DeepEqual(got.EntrypointProduction, want.EntrypointProduction) {
		return fmt.Errorf("entrypoint production dependency ledger changed: got %v, want %v", got.EntrypointProduction, want.EntrypointProduction)
	}
	if !reflect.DeepEqual(got.EntrypointTests, want.EntrypointTests) {
		return fmt.Errorf("entrypoint test dependency ledger changed: got %v, want %v", got.EntrypointTests, want.EntrypointTests)
	}
	return nil
}

func dependencyLaneCounts(lane map[string][]string) (declarations, files, targets int) {
	ownedFiles := map[string]struct{}{}
	for _, imports := range lane {
		declarations += len(imports)
		for _, file := range imports {
			ownedFiles[file] = struct{}{}
		}
	}
	return declarations, len(ownedFiles), len(lane)
}

func cloneDependencyLane(source map[string][]string) map[string][]string {
	clone := make(map[string][]string, len(source))
	for path, files := range source {
		clone[path] = append([]string(nil), files...)
	}
	return clone
}
