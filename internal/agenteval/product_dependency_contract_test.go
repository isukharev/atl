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

const (
	productInternalImportPrefix = "github.com/isukharev/atl/internal/"
	dependencyLedgerTestFile    = "product_dependency_contract_test.go"
)

type evaluatorDependencyLedger struct {
	Production           map[string][]string
	Tests                map[string][]string
	EntrypointProduction map[string][]string
	EntrypointTests      map[string][]string
}

// TestEvaluatorProductDependencyLedger is a reviewed, exact ownership ledger,
// not a minimum or maximum. Any added, removed, moved, aliased, dot, or blank
// product-private import changes a package/file entry and requires explicit
// review. The ledger test excludes itself so its AST machinery cannot conceal
// a dependency by making its own imports part of the expected boundary.
// The first reviewed baseline was 29/27/5 production declarations/files/targets
// and 66/33/10 for tests. The current values below record the evaluator-owned
// CLI error wire, capability catalog, skill catalog, synthetic backend, and
// selected-binary Jira reference oracle ownership reductions.
func TestEvaluatorProductDependencyLedger(t *testing.T) {
	want := evaluatorDependencyLedger{
		Production: map[string][]string{
			productInternalImportPrefix + "safepath": {
				"cli_route_qualification.go",
				"private_activation_calibration.go",
				"private_activation_evidence.go",
				"private_activation_recovery.go",
				"private_activation_store.go",
				"private_baseline.go",
				"private_checkpoint.go",
				"private_coverage_scorecard.go",
				"private_finding_acceptance.go",
				"private_finding_ledger_v2.go",
				"private_plan.go",
				"private_review.go",
				"private_review_panel.go",
				"private_review_provider.go",
				"private_review_runner.go",
				"private_sampling.go",
				"private_scorecard.go",
				"private_snapshot.go",
				"private_synthetic_sampling.go",
				"private_workspace.go",
				"private_workspace_migration.go",
				"provider_runtime.go",
				"runspec.go",
				"storage.go",
				"tool_availability.go",
			},
		},
		Tests: map[string][]string{
			productInternalImportPrefix + "app": {
				"confluence_csv_formula_safety_benchmark_test.go",
				"confluence_multi_section_benchmark_test.go",
				"confluence_page_evidence_benchmark_test.go",
				"confluence_paginated_search_benchmark_test.go",
				"confluence_selection_completeness_benchmark_test.go",
				"corpus_contract_test.go",
				"cross_service_discovery_benchmark_test.go",
				"jira_board_incomplete_benchmark_test.go",
				"jira_board_pagination_benchmark_test.go",
				"jira_history_benchmark_test.go",
				"jira_meeting_tasks_workflow_benchmark_test.go",
				"jira_paginated_search_benchmark_test.go",
				"jira_portfolio_discovery_benchmark_test.go",
				"jira_quarter_portfolio_benchmark_test.go",
				"jira_reporting_workflows_benchmark_test.go",
				"jira_search_zero_progress_benchmark_test.go",
				"jira_spec_to_backlog_workflow_benchmark_test.go",
				"jira_structure_folder_selection_recovery_benchmark_test.go",
				"jira_triage_issue_workflow_benchmark_test.go",
			},
			productInternalImportPrefix + "config": {
				"confluence_csv_formula_safety_benchmark_test.go",
				"confluence_multi_section_benchmark_test.go",
				"confluence_page_evidence_benchmark_test.go",
				"confluence_paginated_search_benchmark_test.go",
				"confluence_selection_completeness_benchmark_test.go",
				"cross_service_discovery_benchmark_test.go",
				"jira_board_incomplete_benchmark_test.go",
				"jira_board_pagination_benchmark_test.go",
				"jira_history_benchmark_test.go",
				"jira_meeting_tasks_workflow_benchmark_test.go",
				"jira_paginated_search_benchmark_test.go",
				"jira_portfolio_discovery_benchmark_test.go",
				"jira_quarter_portfolio_benchmark_test.go",
				"jira_reporting_workflows_benchmark_test.go",
				"jira_search_zero_progress_benchmark_test.go",
				"jira_spec_to_backlog_workflow_benchmark_test.go",
				"jira_structure_folder_selection_recovery_benchmark_test.go",
				"jira_triage_issue_workflow_benchmark_test.go",
			},
			productInternalImportPrefix + "domain": {
				"corpus_contract_test.go",
				"jira_meeting_tasks_workflow_benchmark_test.go",
				"jira_portfolio_discovery_benchmark_test.go",
				"jira_reporting_workflows_benchmark_test.go",
				"jira_spec_to_backlog_workflow_benchmark_test.go",
				"jira_structure_folder_selection_recovery_benchmark_test.go",
				"jira_triage_issue_workflow_benchmark_test.go",
			},
			productInternalImportPrefix + "mcpserver": {
				"corpus_contract_test.go",
				"jira_artifact_graph_development_mcp_benchmark_test.go",
				"jira_artifact_graph_mcp_benchmark_test.go",
			},
			productInternalImportPrefix + "mdwiki": {
				"jira_meeting_tasks_workflow_benchmark_test.go",
				"jira_spec_to_backlog_workflow_benchmark_test.go",
				"jira_triage_issue_workflow_benchmark_test.go",
			},
			productInternalImportPrefix + "safepath": {
				"private_baseline_test.go",
				"private_workspace_migration_test.go",
			},
		},
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
	if declarations, files, targets := dependencyLaneCounts(got.Production); declarations != 25 || files != 25 || targets != 1 {
		t.Fatalf("production dependency counts=%d declarations/%d files/%d targets, want 25/25/1", declarations, files, targets)
	}
	if declarations, files, targets := dependencyLaneCounts(got.Tests); declarations != 52 || files != 23 || targets != 6 {
		t.Fatalf("test dependency counts=%d declarations/%d files/%d targets, want 52/23/6", declarations, files, targets)
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
	if err := scanEvaluatorDependencyDirectory(libraryDirectory, dependencyLedgerTestFile, ledger.Production, ledger.Tests); err != nil {
		return evaluatorDependencyLedger{}, err
	}
	if err := scanEvaluatorDependencyDirectory(entrypointDirectory, "", ledger.EntrypointProduction, ledger.EntrypointTests); err != nil {
		return evaluatorDependencyLedger{}, err
	}
	for _, lane := range []map[string][]string{ledger.Production, ledger.Tests, ledger.EntrypointProduction, ledger.EntrypointTests} {
		for path := range lane {
			sort.Strings(lane[path])
		}
	}
	return ledger, nil
}

func scanEvaluatorDependencyDirectory(directory, excludedFile string, production, tests map[string][]string) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || name == excludedFile {
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
