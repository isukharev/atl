package agenteval

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	productInternalImportPrefix         = "github.com/isukharev/atl/internal/"
	evaluatorModuleImportPath           = productInternalImportPrefix + "agenteval"
	evaluatorAnalysisImportPath         = evaluatorModuleImportPath + "/analysis"
	evaluatorAgentAdapterImportPath     = evaluatorModuleImportPath + "/agentadapter"
	evaluatorCoreImportPath             = evaluatorModuleImportPath + "/core"
	evaluatorExecutionBackendImportPath = evaluatorModuleImportPath + "/executionbackend"
	evaluatorExperimentImportPath       = evaluatorModuleImportPath + "/experiment"
	evaluatorExtensionImportPath        = evaluatorModuleImportPath + "/extension"
	evaluatorGradingImportPath          = evaluatorModuleImportPath + "/grading"
	evaluatorLifecycleImportPath        = evaluatorModuleImportPath + "/lifecycle"
	evaluatorSchedulerImportPath        = evaluatorModuleImportPath + "/scheduler"
	evaluatorAgentSkillsImportPath      = evaluatorModuleImportPath + "/interchange/agentskills"
	evaluatorATLImportPath              = evaluatorModuleImportPath + "/profile/atl"
	evaluatorSchemaImportPath           = evaluatorModuleImportPath + "/schemaregistry"
)

type evaluatorPackage string

const (
	evaluatorRootPackage             evaluatorPackage = "root"
	evaluatorAnalysisPackage         evaluatorPackage = "analysis"
	evaluatorAgentAdapterPackage     evaluatorPackage = "agentadapter"
	evaluatorCorePackage             evaluatorPackage = "core"
	evaluatorExecutionBackendPackage evaluatorPackage = "executionbackend"
	evaluatorExperimentPackage       evaluatorPackage = "experiment"
	evaluatorExtensionPackage        evaluatorPackage = "extension"
	evaluatorGradingPackage          evaluatorPackage = "grading"
	evaluatorLifecyclePackage        evaluatorPackage = "lifecycle"
	evaluatorSchedulerPackage        evaluatorPackage = "scheduler"
	evaluatorAgentSkillsPackage      evaluatorPackage = "interchange/agentskills"
	evaluatorATLPackage              evaluatorPackage = "profile/atl"
	evaluatorSchemaPackage           evaluatorPackage = "schemaregistry"
	evaluatorCommandOwner            evaluatorPackage = "cmd/agent-eval"
)

var evaluatorPackages = []evaluatorPackage{
	evaluatorRootPackage,
	evaluatorAnalysisPackage,
	evaluatorAgentAdapterPackage,
	evaluatorCorePackage,
	evaluatorExecutionBackendPackage,
	evaluatorExperimentPackage,
	evaluatorExtensionPackage,
	evaluatorGradingPackage,
	evaluatorLifecyclePackage,
	evaluatorSchedulerPackage,
	evaluatorAgentSkillsPackage,
	evaluatorATLPackage,
	evaluatorSchemaPackage,
	evaluatorCommandOwner,
}

type evaluatorDependencyLane struct {
	Package evaluatorPackage
	Tests   bool
}

type evaluatorDependencyImport struct {
	File  string
	Path  string
	Alias string
}

type evaluatorDependencyLedger map[evaluatorDependencyLane][]evaluatorDependencyImport

// TestEvaluatorProductDependencyLedger is a reviewed exact package, lane,
// import, alias, and file ledger. The scan is recursive and parses every Go
// source file regardless of build constraints. The evaluator retains zero
// root-product-private imports; module-self imports must also satisfy the
// independently enforced agentadapter + core + executionbackend + experiment + extension + lifecycle + scheduler -> none, analysis -> experiment,
// grading -> core + executionbackend, interchange/agentskills -> core, profile/atl -> core + grading, root -> analysis + agentadapter +
// core + executionbackend + experiment + extension + grading + interchange/agentskills + lifecycle + scheduler + profile/atl +
// schemaregistry, and cmd/agent-eval -> exact root
// DAG. The schema registry is a declaration-only leaf.
func TestEvaluatorProductDependencyLedger(t *testing.T) {
	want := evaluatorDependencyLedger{
		{Package: evaluatorRootPackage}: {
			{File: "agent_adapter_builtin.go", Path: evaluatorAgentAdapterImportPath},
			{File: "agent_adapter_contract.go", Path: evaluatorAgentAdapterImportPath},
			{File: "agent_adapter_observation.go", Path: evaluatorAgentAdapterImportPath},
			{File: "sequential_reference.go", Path: evaluatorAgentAdapterImportPath},
			{File: "sequential_reference_inspection.go", Path: evaluatorAgentAdapterImportPath},
			{File: "sequential_reference_profile.go", Path: evaluatorAgentAdapterImportPath},
			{File: "sequential_reference_publication.go", Path: evaluatorAgentAdapterImportPath},
			{File: "analysis_contract.go", Path: evaluatorAnalysisImportPath},
			{File: "junit_projection_facade.go", Path: evaluatorAnalysisImportPath},
			{File: "atl_core_profile.go", Path: evaluatorCoreImportPath},
			{File: "grading_contract.go", Path: evaluatorCoreImportPath},
			{File: "execution_backend_contract.go", Path: evaluatorExecutionBackendImportPath},
			{File: "execution_backend_local.go", Path: evaluatorExecutionBackendImportPath},
			{File: "extension_host_lifecycle.go", Path: evaluatorExecutionBackendImportPath},
			{File: "grading_contract.go", Path: evaluatorExecutionBackendImportPath},
			{File: "sequential_reference.go", Path: evaluatorExecutionBackendImportPath},
			{File: "sequential_reference_codec.go", Path: evaluatorExecutionBackendImportPath},
			{File: "sequential_reference_inspection.go", Path: evaluatorExecutionBackendImportPath},
			{File: "sequential_reference_profile.go", Path: evaluatorExecutionBackendImportPath},
			{File: "sequential_reference_publication.go", Path: evaluatorExecutionBackendImportPath},
			{File: "sequential_reference_scheduler.go", Path: evaluatorExecutionBackendImportPath},
			{File: "experiment_contract.go", Path: evaluatorExperimentImportPath},
			{File: "junit_projection_facade.go", Path: evaluatorExperimentImportPath},
			{File: "private_experiment_compat.go", Path: evaluatorExperimentImportPath},
			{File: "sequential_reference.go", Path: evaluatorExperimentImportPath},
			{File: "sequential_reference_codec.go", Path: evaluatorExperimentImportPath},
			{File: "sequential_reference_completion.go", Path: evaluatorExperimentImportPath},
			{File: "sequential_reference_inspection.go", Path: evaluatorExperimentImportPath},
			{File: "sequential_reference_profile.go", Path: evaluatorExperimentImportPath},
			{File: "sequential_reference_publication.go", Path: evaluatorExperimentImportPath},
			{File: "sequential_reference_scheduler.go", Path: evaluatorExperimentImportPath},
			{File: "atl_core_profile.go", Path: evaluatorExtensionImportPath},
			{File: "attempt_session.go", Path: evaluatorExtensionImportPath},
			{File: "extension_host.go", Path: evaluatorExtensionImportPath},
			{File: "extension_host_lifecycle.go", Path: evaluatorExtensionImportPath},
			{File: "extension_host_process.go", Path: evaluatorExtensionImportPath},
			{File: "atl_grading_adapter.go", Path: evaluatorGradingImportPath},
			{File: "attempt_session.go", Path: evaluatorGradingImportPath},
			{File: "extension_host_lifecycle.go", Path: evaluatorGradingImportPath},
			{File: "grading_contract.go", Path: evaluatorGradingImportPath},
			{File: "private_grading_compat.go", Path: evaluatorGradingImportPath},
			{File: "private_review_runner.go", Path: evaluatorGradingImportPath},
			{File: "sequential_reference.go", Path: evaluatorGradingImportPath},
			{File: "sequential_reference_codec.go", Path: evaluatorGradingImportPath},
			{File: "sequential_reference_inspection.go", Path: evaluatorGradingImportPath},
			{File: "sequential_reference_profile.go", Path: evaluatorGradingImportPath},
			{File: "sequential_reference_publication.go", Path: evaluatorGradingImportPath},
			{File: "agent_skills_interchange.go", Path: evaluatorAgentSkillsImportPath},
			{File: "experiment_contract.go", Path: evaluatorAgentSkillsImportPath},
			{File: "aggregate_root.go", Path: evaluatorLifecycleImportPath},
			{File: "attempt_ledger.go", Path: evaluatorLifecycleImportPath},
			{File: "attempt_ledger_report.go", Path: evaluatorLifecycleImportPath},
			{File: "attempt_run_binding.go", Path: evaluatorLifecycleImportPath},
			{File: "attempt_session.go", Path: evaluatorLifecycleImportPath},
			{File: "calibration.go", Path: evaluatorLifecycleImportPath},
			{File: "execution_backend_contract.go", Path: evaluatorLifecycleImportPath},
			{File: "experiment_contract.go", Path: evaluatorLifecycleImportPath},
			{File: "extension_host_lifecycle.go", Path: evaluatorLifecycleImportPath},
			{File: "extension_host_process.go", Path: evaluatorLifecycleImportPath},
			{File: "grading_contract.go", Path: evaluatorLifecycleImportPath},
			{File: "private_review_runner.go", Path: evaluatorLifecycleImportPath},
			{File: "provider_attempt.go", Path: evaluatorLifecycleImportPath},
			{File: "runner.go", Path: evaluatorLifecycleImportPath},
			{File: "sequential_reference.go", Path: evaluatorLifecycleImportPath},
			{File: "sequential_reference_completion.go", Path: evaluatorLifecycleImportPath},
			{File: "sequential_reference_inspection.go", Path: evaluatorLifecycleImportPath},
			{File: "sequential_reference_publication.go", Path: evaluatorLifecycleImportPath},
			{File: "sequential_reference_scheduler.go", Path: evaluatorLifecycleImportPath},
			{File: "synthetic_atl_process_lifecycle.go", Path: evaluatorLifecycleImportPath},
			{File: "atl_core_profile.go", Path: evaluatorATLImportPath, Alias: "profileatl"},
			{File: "atl_grading_compat.go", Path: evaluatorATLImportPath, Alias: "profileatl"},
			{File: "scheduler_contract.go", Path: evaluatorSchedulerImportPath},
			{File: "sequential_reference.go", Path: evaluatorSchedulerImportPath},
			{File: "sequential_reference_completion.go", Path: evaluatorSchedulerImportPath},
			{File: "sequential_reference_inspection.go", Path: evaluatorSchedulerImportPath},
			{File: "sequential_reference_publication.go", Path: evaluatorSchedulerImportPath},
			{File: "sequential_reference_scheduler.go", Path: evaluatorSchedulerImportPath},
			{File: "schema_registry.go", Path: evaluatorSchemaImportPath},
		},
		{Package: evaluatorRootPackage, Tests: true}: {
			{File: "agent_adapter_contract_test.go", Path: evaluatorAgentAdapterImportPath},
			{File: "agent_adapter_observation_test.go", Path: evaluatorAgentAdapterImportPath},
			{File: "sequential_reference_test.go", Path: evaluatorAgentAdapterImportPath},
			{File: "junit_projection_test.go", Path: evaluatorAnalysisImportPath},
			{File: "standalone_product_contract_golden_test.go", Path: evaluatorAnalysisImportPath},
			{File: "atl_core_profile_test.go", Path: evaluatorCoreImportPath},
			{File: "execution_backend_contract_test.go", Path: evaluatorExecutionBackendImportPath},
			{File: "execution_backend_process_test.go", Path: evaluatorExecutionBackendImportPath},
			{File: "sequential_reference_test.go", Path: evaluatorExecutionBackendImportPath},
			{File: "standalone_product_contract_golden_test.go", Path: evaluatorExecutionBackendImportPath},
			{File: "standalone_product_contract_test.go", Path: evaluatorExecutionBackendImportPath},
			{File: "analysis_contract_test.go", Path: evaluatorExperimentImportPath},
			{File: "experiment_contract_test.go", Path: evaluatorExperimentImportPath},
			{File: "junit_projection_test.go", Path: evaluatorExperimentImportPath},
			{File: "sequential_reference_test.go", Path: evaluatorExperimentImportPath},
			{File: "standalone_product_contract_golden_test.go", Path: evaluatorExperimentImportPath},
			{File: "standalone_product_contract_test.go", Path: evaluatorExperimentImportPath},
			{File: "agent_adapter_contract_test.go", Path: evaluatorExtensionImportPath},
			{File: "atl_core_profile_test.go", Path: evaluatorExtensionImportPath},
			{File: "attempt_ledger_windows_test.go", Path: evaluatorExtensionImportPath},
			{File: "execution_backend_process_test.go", Path: evaluatorExtensionImportPath},
			{File: "extension_host_process_test.go", Path: evaluatorExtensionImportPath},
			{File: "extension_host_test.go", Path: evaluatorExtensionImportPath},
			{File: "grading_contract_test.go", Path: evaluatorExtensionImportPath},
			{File: "standalone_product_contract_golden_test.go", Path: evaluatorExtensionImportPath},
			{File: "atl_grading_adapter_test.go", Path: evaluatorGradingImportPath},
			{File: "grading_contract_test.go", Path: evaluatorGradingImportPath},
			{File: "private_grading_compat_test.go", Path: evaluatorGradingImportPath},
			{File: "private_review_provider_test.go", Path: evaluatorGradingImportPath},
			{File: "sequential_reference_test.go", Path: evaluatorGradingImportPath},
			{File: "standalone_product_contract_golden_test.go", Path: evaluatorGradingImportPath},
			{File: "standalone_product_contract_test.go", Path: evaluatorGradingImportPath},
			{File: "aggregate_root_test.go", Path: evaluatorLifecycleImportPath},
			{File: "attempt_ledger_test.go", Path: evaluatorLifecycleImportPath},
			{File: "capability_process_test.go", Path: evaluatorLifecycleImportPath},
			{File: "execution_backend_contract_test.go", Path: evaluatorLifecycleImportPath},
			{File: "experiment_contract_test.go", Path: evaluatorLifecycleImportPath},
			{File: "grading_contract_test.go", Path: evaluatorLifecycleImportPath},
			{File: "private_review_provider_test.go", Path: evaluatorLifecycleImportPath},
			{File: "provider_attempt_test.go", Path: evaluatorLifecycleImportPath},
			{File: "sequential_reference_test.go", Path: evaluatorLifecycleImportPath},
			{File: "standalone_product_contract_golden_test.go", Path: evaluatorLifecycleImportPath},
			{File: "standalone_product_contract_test.go", Path: evaluatorLifecycleImportPath},
			{File: "atl_core_profile_test.go", Path: evaluatorATLImportPath, Alias: "profileatl"},
			{File: "atl_grading_adapter_test.go", Path: evaluatorATLImportPath, Alias: "profileatl"},
		},
		{Package: evaluatorAnalysisPackage}: {
			{File: "analysis/analyze.go", Path: evaluatorExperimentImportPath},
			{File: "analysis/binding.go", Path: evaluatorExperimentImportPath},
			{File: "analysis/codec.go", Path: evaluatorExperimentImportPath},
			{File: "analysis/contract.go", Path: evaluatorExperimentImportPath},
			{File: "analysis/identity.go", Path: evaluatorExperimentImportPath},
			{File: "analysis/projection.go", Path: evaluatorExperimentImportPath},
			{File: "analysis/summaries.go", Path: evaluatorExperimentImportPath},
			{File: "analysis/validate.go", Path: evaluatorExperimentImportPath},
		},
		{Package: evaluatorAnalysisPackage, Tests: true}: {
			{File: "analysis/analysis_test.go", Path: evaluatorExperimentImportPath},
			{File: "analysis/fixture_test.go", Path: evaluatorExperimentImportPath},
		},
		{Package: evaluatorAgentAdapterPackage}:              {},
		{Package: evaluatorAgentAdapterPackage, Tests: true}: {},
		{Package: evaluatorCorePackage}:                      {},
		{Package: evaluatorCorePackage, Tests: true}: {
			{File: "core/engine_test.go", Path: evaluatorCoreImportPath},
		},
		{Package: evaluatorExecutionBackendPackage}:              {},
		{Package: evaluatorExecutionBackendPackage, Tests: true}: {},
		{Package: evaluatorExperimentPackage}:                    {},
		{Package: evaluatorExperimentPackage, Tests: true}:       {},
		{Package: evaluatorExtensionPackage}:                     {},
		{Package: evaluatorExtensionPackage, Tests: true}:        {},
		{Package: evaluatorGradingPackage}: {
			{File: "grading/core.go", Path: evaluatorCoreImportPath},
			{File: "grading/script.go", Path: evaluatorExecutionBackendImportPath},
			{File: "grading/validate.go", Path: evaluatorExecutionBackendImportPath},
		},
		{Package: evaluatorGradingPackage, Tests: true}: {
			{File: "grading/grading_test.go", Path: evaluatorCoreImportPath},
			{File: "grading/receipt_internal_test.go", Path: evaluatorCoreImportPath},
			{File: "grading/grading_test.go", Path: evaluatorExecutionBackendImportPath},
			{File: "grading/grading_test.go", Path: evaluatorGradingImportPath},
		},
		{Package: evaluatorLifecyclePackage}:              {},
		{Package: evaluatorLifecyclePackage, Tests: true}: {},
		{Package: evaluatorSchedulerPackage}:              {},
		{Package: evaluatorSchedulerPackage, Tests: true}: {},
		{Package: evaluatorAgentSkillsPackage}: {
			{File: "interchange/agentskills/contract.go", Path: evaluatorCoreImportPath},
			{File: "interchange/agentskills/project.go", Path: evaluatorCoreImportPath},
		},
		{Package: evaluatorAgentSkillsPackage, Tests: true}: {
			{File: "interchange/agentskills/import_test.go", Path: evaluatorCoreImportPath},
		},
		{Package: evaluatorATLPackage}: {
			{File: "profile/atl/profile.go", Path: evaluatorCoreImportPath},
			{File: "profile/atl/grading.go", Path: evaluatorGradingImportPath},
		},
		{Package: evaluatorATLPackage, Tests: true}: {
			{File: "profile/atl/profile_test.go", Path: evaluatorCoreImportPath},
			{File: "profile/atl/grading_test.go", Path: evaluatorGradingImportPath},
			{File: "profile/atl/grading_test.go", Path: evaluatorATLImportPath, Alias: "profileatl"},
			{File: "profile/atl/profile_test.go", Path: evaluatorATLImportPath, Alias: "profileatl"},
		},
		{Package: evaluatorSchemaPackage}:              {},
		{Package: evaluatorSchemaPackage, Tests: true}: {},
		{Package: evaluatorCommandOwner}: {
			{File: "cmd/agent-eval/agent_adapter.go", Path: evaluatorModuleImportPath},
			{File: "cmd/agent-eval/command_broker.go", Path: evaluatorModuleImportPath},
			{File: "cmd/agent-eval/execution_backend.go", Path: evaluatorModuleImportPath},
			{File: "cmd/agent-eval/grader.go", Path: evaluatorModuleImportPath},
			{File: "cmd/agent-eval/main.go", Path: evaluatorModuleImportPath},
			{File: "cmd/agent-eval/private.go", Path: evaluatorModuleImportPath},
			{File: "cmd/agent-eval/proxy.go", Path: evaluatorModuleImportPath},
			{File: "cmd/agent-eval/standalone.go", Path: evaluatorModuleImportPath},
			{File: "cmd/agent-eval/standalone_agentskills.go", Path: evaluatorModuleImportPath},
			{File: "cmd/agent-eval/standalone_config.go", Path: evaluatorModuleImportPath},
			{File: "cmd/agent-eval/standalone_operations.go", Path: evaluatorModuleImportPath},
			{File: "cmd/agent-eval/standalone_reference.go", Path: evaluatorModuleImportPath},
			{File: "cmd/agent-eval/standalone_schema.go", Path: evaluatorModuleImportPath},
		},
		{Package: evaluatorCommandOwner, Tests: true}: {
			{File: "cmd/agent-eval/main_test.go", Path: evaluatorModuleImportPath},
			{File: "cmd/agent-eval/private_test.go", Path: evaluatorModuleImportPath},
			{File: "cmd/agent-eval/proxy_cli_error_contract_test.go", Path: evaluatorModuleImportPath},
			{File: "cmd/agent-eval/standalone_conformance_test.go", Path: evaluatorModuleImportPath},
			{File: "cmd/agent-eval/standalone_followup_test.go", Path: evaluatorModuleImportPath},
			{File: "cmd/agent-eval/standalone_review_test.go", Path: evaluatorModuleImportPath},
			{File: "cmd/agent-eval/standalone_schema_test.go", Path: evaluatorModuleImportPath},
			{File: "cmd/agent-eval/standalone_test.go", Path: evaluatorModuleImportPath},
		},
	}
	got, err := scanEvaluatorDependencyLedger(".")
	if err != nil {
		t.Fatal(err)
	}
	if err := compareEvaluatorDependencyLedgers(got, want); err != nil {
		t.Fatal(err)
	}
}

func TestEvaluatorProductDependencyLedgerDetectsAliasAndOwnershipDrift(t *testing.T) {
	t.Run("exact ledger drift", func(t *testing.T) {
		for name, mutate := range map[string]func(*testing.T, string){
			"alias": func(t *testing.T, root string) {
				replaceEvaluatorDependencyFixture(t, root, "facade.go", `"`+evaluatorExtensionImportPath+`"`, `protocol "`+evaluatorExtensionImportPath+`"`)
			},
			"lane": func(t *testing.T, root string) {
				writeEvaluatorDependencyFile(t, root, "facade.go", "package agenteval\n")
				writeEvaluatorDependencyFile(t, root, "facade_test.go", "package agenteval\n\nimport \""+evaluatorCoreImportPath+"\"\n")
			},
			"edge": func(t *testing.T, root string) {
				writeEvaluatorDependencyFile(t, root, "profile_edge.go", "package agenteval\n\nimport \""+evaluatorATLImportPath+"\"\n")
			},
			"external extension test self import": func(t *testing.T, root string) {
				writeEvaluatorDependencyFile(t, root, "extension/external_test.go", "package extension_test\n\nimport \""+evaluatorExtensionImportPath+"\"\n")
			},
		} {
			t.Run(name, func(t *testing.T) {
				root := writeEvaluatorDependencyFixture(t)
				baseline, err := scanEvaluatorDependencyLedger(root)
				if err != nil {
					t.Fatal(err)
				}
				mutate(t, root)
				got, err := scanEvaluatorDependencyLedger(root)
				if err != nil {
					t.Fatal(err)
				}
				if compareEvaluatorDependencyLedgers(got, baseline) == nil {
					t.Fatal("exact evaluator dependency drift was not detected")
				}
			})
		}
	})

	t.Run("closed package DAG", func(t *testing.T) {
		tests := []struct {
			name   string
			mutate func(*testing.T, string)
			want   string
		}{
			{
				name: "build-tagged agent adapter reverse edge",
				mutate: func(t *testing.T, root string) {
					writeEvaluatorDependencyFile(t, root, "agentadapter/reverse_ignore.go", "//go:build ignore\n\npackage agentadapter\n\nimport \""+evaluatorModuleImportPath+"\"\n")
				},
				want: "package DAG forbids",
			},
			{
				name: "build-tagged reverse edge",
				mutate: func(t *testing.T, root string) {
					writeEvaluatorDependencyFile(t, root, "core/reverse_ignore.go", "//go:build ignore\n\npackage core\n\nimport \""+evaluatorModuleImportPath+"\"\n")
				},
				want: "package DAG forbids",
			},
			{
				name: "build-tagged extension reverse edge",
				mutate: func(t *testing.T, root string) {
					writeEvaluatorDependencyFile(t, root, "extension/reverse_ignore.go", "//go:build ignore\n\npackage extension\n\nimport \""+evaluatorModuleImportPath+"\"\n")
				},
				want: "package DAG forbids",
			},
			{
				name: "build-tagged Agent Skills reverse edge",
				mutate: func(t *testing.T, root string) {
					writeEvaluatorDependencyFile(t, root, "interchange/agentskills/reverse_ignore.go", "//go:build ignore\n\npackage agentskills\n\nimport \""+evaluatorModuleImportPath+"\"\n")
				},
				want: "package DAG forbids",
			},
			{
				name: "dot self import",
				mutate: func(t *testing.T, root string) {
					writeEvaluatorDependencyFile(t, root, "dot.go", "package agenteval\n\nimport . \""+evaluatorCoreImportPath+"\"\n")
				},
				want: "dot and blank module-self imports are forbidden",
			},
			{
				name: "blank self import",
				mutate: func(t *testing.T, root string) {
					writeEvaluatorDependencyFile(t, root, "blank.go", "package agenteval\n\nimport _ \""+evaluatorCoreImportPath+"\"\n")
				},
				want: "dot and blank module-self imports are forbidden",
			},
			{
				name: "unknown package directory",
				mutate: func(t *testing.T, root string) {
					writeEvaluatorDependencyFile(t, root, "reporting/report.go", "package reporting\n")
				},
				want: "unknown Go package directory",
			},
			{
				name: "analysis reverse edge",
				mutate: func(t *testing.T, root string) {
					writeEvaluatorDependencyFile(t, root, "analysis/reverse.go", "package analysis\n\nimport \""+evaluatorModuleImportPath+"\"\n")
				},
				want: "package DAG forbids",
			},
			{
				name: "experiment reverse edge",
				mutate: func(t *testing.T, root string) {
					writeEvaluatorDependencyFile(t, root, "experiment/reverse.go", "package experiment\n\nimport \""+evaluatorModuleImportPath+"\"\n")
				},
				want: "package DAG forbids",
			},
			{
				name: "profile to profile edge",
				mutate: func(t *testing.T, root string) {
					writeEvaluatorDependencyFile(t, root, "profile/atl/reverse.go", "package atl\n\nimport \""+evaluatorModuleImportPath+"/profile/other\"\n")
				},
				want: "unknown evaluator package",
			},
			{
				name: "extension to core edge",
				mutate: func(t *testing.T, root string) {
					writeEvaluatorDependencyFile(t, root, "extension/core.go", "package extension\n\nimport \""+evaluatorCoreImportPath+"\"\n")
				},
				want: "package DAG forbids",
			},
			{
				name: "extension external test to profile edge",
				mutate: func(t *testing.T, root string) {
					writeEvaluatorDependencyFile(t, root, "extension/profile_test.go", "package extension_test\n\nimport \""+evaluatorATLImportPath+"\"\n")
				},
				want: "package DAG forbids",
			},
			{
				name: "Agent Skills to profile edge",
				mutate: func(t *testing.T, root string) {
					writeEvaluatorDependencyFile(t, root, "interchange/agentskills/profile.go", "package agentskills\n\nimport \""+evaluatorATLImportPath+"\"\n")
				},
				want: "package DAG forbids",
			},
			{
				name: "schema registry reverse edge",
				mutate: func(t *testing.T, root string) {
					writeEvaluatorDependencyFile(t, root, "schemaregistry/reverse.go", "package schemaregistry\n\nimport \""+evaluatorModuleImportPath+"\"\n")
				},
				want: "package DAG forbids",
			},
			{
				name: "scheduler reverse edge",
				mutate: func(t *testing.T, root string) {
					writeEvaluatorDependencyFile(t, root, "scheduler/reverse.go", "package scheduler\n\nimport \""+evaluatorModuleImportPath+"\"\n")
				},
				want: "package DAG forbids",
			},
			{
				name: "extension internal test to root edge",
				mutate: func(t *testing.T, root string) {
					writeEvaluatorDependencyFile(t, root, "extension/root_test.go", "package extension\n\nimport \""+evaluatorModuleImportPath+"\"\n")
				},
				want: "package DAG forbids",
			},
			{
				name: "command subpackage edge",
				mutate: func(t *testing.T, root string) {
					writeEvaluatorDependencyFile(t, root, "cmd/agent-eval/subpackage.go", "package main\n\nimport \""+evaluatorCoreImportPath+"\"\n")
				},
				want: "package DAG forbids",
			},
			{
				name: "command extension edge",
				mutate: func(t *testing.T, root string) {
					writeEvaluatorDependencyFile(t, root, "cmd/agent-eval/extension.go", "package main\n\nimport \""+evaluatorExtensionImportPath+"\"\n")
				},
				want: "package DAG forbids",
			},
			{
				name: "command Agent Skills edge",
				mutate: func(t *testing.T, root string) {
					writeEvaluatorDependencyFile(t, root, "cmd/agent-eval/agentskills.go", "package main\n\nimport \""+evaluatorAgentSkillsImportPath+"\"\n")
				},
				want: "package DAG forbids",
			},
			{
				name: "command schema registry edge",
				mutate: func(t *testing.T, root string) {
					writeEvaluatorDependencyFile(t, root, "cmd/agent-eval/schema.go", "package main\n\nimport \""+evaluatorSchemaImportPath+"\"\n")
				},
				want: "package DAG forbids",
			},
			{
				name: "agent adapter product-private edge",
				mutate: func(t *testing.T, root string) {
					writeEvaluatorDependencyFile(t, root, "agentadapter/product.go", "package agentadapter\n\nimport \""+productInternalImportPrefix+"app\"\n")
				},
				want: "imports product-private package",
			},
			{
				name: "product-private edge",
				mutate: func(t *testing.T, root string) {
					writeEvaluatorDependencyFile(t, root, "core/product.go", "package core\n\nimport \""+productInternalImportPrefix+"app\"\n")
				},
				want: "imports product-private package",
			},
			{
				name: "extension product-private edge",
				mutate: func(t *testing.T, root string) {
					writeEvaluatorDependencyFile(t, root, "extension/product.go", "package extension\n\nimport \""+productInternalImportPrefix+"app\"\n")
				},
				want: "imports product-private package",
			},
			{
				name: "execution backend product-private edge",
				mutate: func(t *testing.T, root string) {
					writeEvaluatorDependencyFile(t, root, "executionbackend/product.go", "package executionbackend\n\nimport \""+productInternalImportPrefix+"app\"\n")
				},
				want: "imports product-private package",
			},
			{
				name: "scheduler product-private edge",
				mutate: func(t *testing.T, root string) {
					writeEvaluatorDependencyFile(t, root, "scheduler/product.go", "package scheduler\n\nimport \""+productInternalImportPrefix+"app\"\n")
				},
				want: "imports product-private package",
			},
			{
				name: "Agent Skills product-private edge",
				mutate: func(t *testing.T, root string) {
					writeEvaluatorDependencyFile(t, root, "interchange/agentskills/product.go", "package agentskills\n\nimport \""+productInternalImportPrefix+"app\"\n")
				},
				want: "imports product-private package",
			},
			{
				name: "malformed source",
				mutate: func(t *testing.T, root string) {
					writeEvaluatorDependencyFile(t, root, "core/malformed.go", "package core\n\nfunc malformed( {\n")
				},
				want: "parse evaluator dependency owner",
			},
			{
				name: "missing production package",
				mutate: func(t *testing.T, root string) {
					if err := os.Remove(filepath.Join(root, "profile", "atl", "atl.go")); err != nil {
						t.Fatal(err)
					}
				},
				want: "has no production Go source",
			},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				root := writeEvaluatorDependencyFixture(t)
				test.mutate(t, root)
				_, err := scanEvaluatorDependencyLedger(root)
				if err == nil || !strings.Contains(err.Error(), test.want) {
					t.Fatalf("error=%v, want substring %q", err, test.want)
				}
			})
		}
	})

	t.Run("symbolic link", func(t *testing.T) {
		root := writeEvaluatorDependencyFixture(t)
		if err := os.Symlink("core.go", filepath.Join(root, "core", "link.go")); err != nil {
			t.Fatal(err)
		}
		_, err := scanEvaluatorDependencyLedger(root)
		if err == nil || !strings.Contains(err.Error(), "symbolic links are not allowed") {
			t.Fatalf("error=%v, want symbolic-link rejection", err)
		}
	})

	t.Run("Go recursive package exclusions", func(t *testing.T) {
		root := writeEvaluatorDependencyFixture(t)
		baseline, err := scanEvaluatorDependencyLedger(root)
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{
			"testdata/fixture.go", "vendor/example.test/dependency/dependency.go",
			".scratch/reverse.go", "_scratch/reverse.go",
		} {
			writeEvaluatorDependencyFile(t, root, path, "package ignored\n\nimport \""+productInternalImportPrefix+"app\"\n")
		}
		got, err := scanEvaluatorDependencyLedger(root)
		if err != nil {
			t.Fatal(err)
		}
		if err := compareEvaluatorDependencyLedgers(got, baseline); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("excluded-name symlink", func(t *testing.T) {
		root := writeEvaluatorDependencyFixture(t)
		if err := os.Symlink("core", filepath.Join(root, "_ignored")); err != nil {
			t.Fatal(err)
		}
		_, err := scanEvaluatorDependencyLedger(root)
		if err == nil || !strings.Contains(err.Error(), "symbolic links are not allowed") {
			t.Fatalf("error=%v, want excluded-name symbolic-link rejection", err)
		}
	})
}

func scanEvaluatorDependencyLedger(root string) (evaluatorDependencyLedger, error) {
	ledger := make(evaluatorDependencyLedger, len(evaluatorPackages)*2)
	productionSources := make(map[evaluatorPackage]bool, len(evaluatorPackages))
	for _, owner := range evaluatorPackages {
		ledger[evaluatorDependencyLane{Package: owner}] = []evaluatorDependencyImport{}
		ledger[evaluatorDependencyLane{Package: owner, Tests: true}] = []evaluatorDependencyImport{}
	}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("resolve evaluator dependency owner %s: %w", path, err)
		}
		relative = filepath.ToSlash(relative)
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("inspect evaluator dependency owner %s: symbolic links are not allowed", relative)
		}
		if entry.IsDir() {
			if relative != "." && evaluatorDependencyDirectoryExcluded(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".go") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect evaluator dependency owner %s: %w", relative, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("inspect evaluator dependency owner %s: Go sources must be regular files", relative)
		}
		owner, ok := evaluatorPackageForDirectory(filepath.ToSlash(filepath.Dir(relative)))
		if !ok {
			return fmt.Errorf("evaluator dependency owner %s is in unknown Go package directory %q", relative, filepath.ToSlash(filepath.Dir(relative)))
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			return fmt.Errorf("parse evaluator dependency owner %s: %w", relative, err)
		}
		tests := strings.HasSuffix(entry.Name(), "_test.go")
		if err := validateEvaluatorPackageName(owner, tests, relative, parsed.Name.Name); err != nil {
			return err
		}
		if !tests {
			productionSources[owner] = true
		}
		lane := evaluatorDependencyLane{Package: owner, Tests: tests}
		imports, err := parsedEvaluatorImports(owner, tests, relative, parsed)
		if err != nil {
			return err
		}
		ledger[lane] = append(ledger[lane], imports...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, owner := range evaluatorPackages {
		if !productionSources[owner] {
			return nil, fmt.Errorf("evaluator package %q has no production Go source", owner)
		}
	}
	for lane := range ledger {
		sort.Slice(ledger[lane], func(i, j int) bool {
			left, right := ledger[lane][i], ledger[lane][j]
			if left.Path != right.Path {
				return left.Path < right.Path
			}
			if left.File != right.File {
				return left.File < right.File
			}
			return left.Alias < right.Alias
		})
	}
	return ledger, nil
}

func evaluatorDependencyDirectoryExcluded(name string) bool {
	return name == "testdata" || name == "vendor" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")
}

func evaluatorPackageForDirectory(directory string) (evaluatorPackage, bool) {
	switch directory {
	case ".":
		return evaluatorRootPackage, true
	case string(evaluatorAnalysisPackage):
		return evaluatorAnalysisPackage, true
	case string(evaluatorAgentAdapterPackage):
		return evaluatorAgentAdapterPackage, true
	case string(evaluatorCorePackage):
		return evaluatorCorePackage, true
	case string(evaluatorExecutionBackendPackage):
		return evaluatorExecutionBackendPackage, true
	case string(evaluatorExperimentPackage):
		return evaluatorExperimentPackage, true
	case string(evaluatorExtensionPackage):
		return evaluatorExtensionPackage, true
	case string(evaluatorGradingPackage):
		return evaluatorGradingPackage, true
	case string(evaluatorLifecyclePackage):
		return evaluatorLifecyclePackage, true
	case string(evaluatorSchedulerPackage):
		return evaluatorSchedulerPackage, true
	case string(evaluatorAgentSkillsPackage):
		return evaluatorAgentSkillsPackage, true
	case string(evaluatorATLPackage):
		return evaluatorATLPackage, true
	case string(evaluatorSchemaPackage):
		return evaluatorSchemaPackage, true
	case string(evaluatorCommandOwner):
		return evaluatorCommandOwner, true
	default:
		return "", false
	}
}

func evaluatorPackageForImport(path string) (evaluatorPackage, bool) {
	switch path {
	case evaluatorModuleImportPath:
		return evaluatorRootPackage, true
	case evaluatorAnalysisImportPath:
		return evaluatorAnalysisPackage, true
	case evaluatorAgentAdapterImportPath:
		return evaluatorAgentAdapterPackage, true
	case evaluatorCoreImportPath:
		return evaluatorCorePackage, true
	case evaluatorExecutionBackendImportPath:
		return evaluatorExecutionBackendPackage, true
	case evaluatorExperimentImportPath:
		return evaluatorExperimentPackage, true
	case evaluatorExtensionImportPath:
		return evaluatorExtensionPackage, true
	case evaluatorGradingImportPath:
		return evaluatorGradingPackage, true
	case evaluatorLifecycleImportPath:
		return evaluatorLifecyclePackage, true
	case evaluatorSchedulerImportPath:
		return evaluatorSchedulerPackage, true
	case evaluatorAgentSkillsImportPath:
		return evaluatorAgentSkillsPackage, true
	case evaluatorATLImportPath:
		return evaluatorATLPackage, true
	case evaluatorSchemaImportPath:
		return evaluatorSchemaPackage, true
	case evaluatorModuleImportPath + "/cmd/agent-eval":
		return evaluatorCommandOwner, true
	default:
		return "", false
	}
}

func validateEvaluatorPackageName(owner evaluatorPackage, tests bool, file, got string) error {
	want := map[evaluatorPackage]string{
		evaluatorRootPackage: "agenteval", evaluatorAnalysisPackage: "analysis", evaluatorAgentAdapterPackage: "agentadapter", evaluatorCorePackage: "core",
		evaluatorExecutionBackendPackage: "executionbackend",
		evaluatorExperimentPackage:       "experiment",
		evaluatorExtensionPackage:        "extension",
		evaluatorGradingPackage:          "grading",
		evaluatorLifecyclePackage:        "lifecycle",
		evaluatorSchedulerPackage:        "scheduler",
		evaluatorAgentSkillsPackage:      "agentskills",
		evaluatorATLPackage:              "atl",
		evaluatorSchemaPackage:           "schemaregistry", evaluatorCommandOwner: "main",
	}[owner]
	if got == want || tests && got == want+"_test" {
		return nil
	}
	return fmt.Errorf("evaluator dependency owner %s declares package %q, want %q", file, got, want)
}

func parsedEvaluatorImports(owner evaluatorPackage, tests bool, file string, parsed *ast.File) ([]evaluatorDependencyImport, error) {
	var imports []evaluatorDependencyImport
	for _, specification := range parsed.Imports {
		path, err := strconv.Unquote(specification.Path.Value)
		if err != nil {
			return nil, fmt.Errorf("decode evaluator import in %s: %w", file, err)
		}
		if !strings.HasPrefix(path, productInternalImportPrefix) {
			continue
		}
		if path != evaluatorModuleImportPath && !strings.HasPrefix(path, evaluatorModuleImportPath+"/") {
			return nil, fmt.Errorf("evaluator package %q source %s imports product-private package %q", owner, file, path)
		}
		target, known := evaluatorPackageForImport(path)
		if !known {
			return nil, fmt.Errorf("evaluator package %q source %s imports unknown evaluator package %q", owner, file, path)
		}
		alias := ""
		if specification.Name != nil {
			alias = specification.Name.Name
		}
		if alias == "." || alias == "_" {
			return nil, fmt.Errorf("evaluator package %q source %s: dot and blank module-self imports are forbidden", owner, file)
		}
		if target == owner {
			wantExternalPackage := strings.TrimSuffix(parsed.Name.Name, "_test") + "_test"
			if !tests || parsed.Name.Name != wantExternalPackage {
				return nil, fmt.Errorf("evaluator package DAG forbids %q -> %q in %s", owner, target, file)
			}
		} else if !evaluatorPackageEdgeAllowed(owner, target) {
			return nil, fmt.Errorf("evaluator package DAG forbids %q -> %q in %s", owner, target, file)
		}
		imports = append(imports, evaluatorDependencyImport{File: file, Path: path, Alias: alias})
	}
	return imports, nil
}

func evaluatorPackageEdgeAllowed(owner, target evaluatorPackage) bool {
	switch owner {
	case evaluatorRootPackage:
		return target == evaluatorAnalysisPackage || target == evaluatorAgentAdapterPackage || target == evaluatorCorePackage || target == evaluatorExecutionBackendPackage || target == evaluatorExperimentPackage || target == evaluatorExtensionPackage || target == evaluatorGradingPackage || target == evaluatorLifecyclePackage || target == evaluatorSchedulerPackage || target == evaluatorAgentSkillsPackage || target == evaluatorATLPackage || target == evaluatorSchemaPackage
	case evaluatorAnalysisPackage:
		return target == evaluatorExperimentPackage
	case evaluatorAgentAdapterPackage:
		return false
	case evaluatorCorePackage:
		return false
	case evaluatorExecutionBackendPackage:
		return false
	case evaluatorExperimentPackage:
		return false
	case evaluatorExtensionPackage:
		return false
	case evaluatorGradingPackage:
		return target == evaluatorCorePackage || target == evaluatorExecutionBackendPackage
	case evaluatorLifecyclePackage:
		return false
	case evaluatorSchedulerPackage:
		return false
	case evaluatorAgentSkillsPackage:
		return target == evaluatorCorePackage
	case evaluatorATLPackage:
		return target == evaluatorCorePackage || target == evaluatorGradingPackage
	case evaluatorSchemaPackage:
		return false
	case evaluatorCommandOwner:
		return target == evaluatorRootPackage
	default:
		return false
	}
}

func compareEvaluatorDependencyLedgers(got, want evaluatorDependencyLedger) error {
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("evaluator dependency ledger changed:\n got: %v\nwant: %v", got, want)
	}
	return nil
}

func writeEvaluatorDependencyFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"facade.go":                              "package agenteval\n\nimport (\n\t\"" + evaluatorAnalysisImportPath + "\"\n\t\"" + evaluatorAgentAdapterImportPath + "\"\n\t\"" + evaluatorCoreImportPath + "\"\n\t\"" + evaluatorExecutionBackendImportPath + "\"\n\t\"" + evaluatorExperimentImportPath + "\"\n\t\"" + evaluatorExtensionImportPath + "\"\n\t\"" + evaluatorGradingImportPath + "\"\n\t\"" + evaluatorLifecycleImportPath + "\"\n\t\"" + evaluatorSchedulerImportPath + "\"\n\t\"" + evaluatorAgentSkillsImportPath + "\"\n\t\"" + evaluatorSchemaImportPath + "\"\n)\n",
		"analysis/analysis.go":                   "package analysis\n\nimport \"" + evaluatorExperimentImportPath + "\"\n",
		"agentadapter/contract.go":               "package agentadapter\n",
		"core/core.go":                           "package core\n",
		"executionbackend/contract.go":           "package executionbackend\n",
		"experiment/contract.go":                 "package experiment\n",
		"core/core_test.go":                      "package core_test\n\nimport \"" + evaluatorCoreImportPath + "\"\n",
		"extension/extension.go":                 "package extension\n",
		"extension/extension_test.go":            "package extension\n",
		"grading/grading.go":                     "package grading\n\nimport (\n\t\"" + evaluatorCoreImportPath + "\"\n\t\"" + evaluatorExecutionBackendImportPath + "\"\n)\n",
		"lifecycle/lifecycle.go":                 "package lifecycle\n",
		"scheduler/contract.go":                  "package scheduler\n",
		"interchange/agentskills/agentskills.go": "package agentskills\n\nimport \"" + evaluatorCoreImportPath + "\"\n",
		"profile/atl/atl.go":                     "package atl\n\nimport (\n\t\"" + evaluatorCoreImportPath + "\"\n\t\"" + evaluatorGradingImportPath + "\"\n)\n",
		"schemaregistry/registry.go":             "package schemaregistry\n",
		"cmd/agent-eval/main.go":                 "package main\n\nimport \"" + evaluatorModuleImportPath + "\"\n",
	}
	for path, contents := range files {
		writeEvaluatorDependencyFile(t, root, path, contents)
	}
	return root
}

func writeEvaluatorDependencyFile(t *testing.T, root, name, contents string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func replaceEvaluatorDependencyFixture(t *testing.T, root, name, old, replacement string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(contents), old, replacement, 1)
	if updated == string(contents) {
		t.Fatalf("fixture %s does not contain %q", name, old)
	}
	if err := os.WriteFile(path, []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}
}
