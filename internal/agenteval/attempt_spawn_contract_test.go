package agenteval

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

type attemptSpawnCall struct {
	File, Function, Call string
}

// This inventory is intentionally exact. A process-entry call is admitted only
// after its durable owner and pre-entry classification have been reviewed.
var reviewedAttemptSpawnCalls = map[attemptSpawnCall]string{
	{"bounded_command.go", "executeBoundedCommand", "cmd.Start"}:                              "durable_parent_attempt",
	{"bounded_command.go", "executeBoundedCommand", "exec.CommandContext"}:                    "durable_parent_attempt",
	{"bounded_mcp_command.go", "startBoundedMCPCommand", "cmd.Start"}:                         "durable_parent_attempt",
	{"bounded_mcp_command.go", "startBoundedMCPCommand", "exec.Command"}:                      "durable_parent_attempt",
	{"calibration.go", "RunCodexCLICalibration", "exec.CommandContext"}:                       "durable_session",
	{"capability_process.go", "verifyATLCapabilityCatalogWithSession", "exec.CommandContext"}: "durable_session",
	{"cli_route_qualification.go", "QualifyCLIRoute", "exec.CommandContext"}:                  "durable_session",
	{"cmd/agent-eval/proxy.go", "runATLProxyWithWriteIntent", "cmd.Start"}:                    "durable_parent_attempt",
	{"cmd/agent-eval/proxy.go", "runATLProxyWithWriteIntent", "exec.Command"}:                 "durable_parent_attempt",
	{"extension_host_process.go", "startExtensionProcessWithSession", "cmd.Start"}:            "durable_session",
	{"extension_host_process.go", "startExtensionProcessWithSession", "exec.Command"}:         "durable_session",
	{"private_plan.go", "privateReadOnlyGitCommand", "exec.Command"}:                          "content_minimized_maintenance",
	{"private_plan.go", "privateRepositoryIdentity", "cmd.Output#1"}:                          "content_minimized_maintenance",
	{"private_plan.go", "privateRepositoryIdentity", "cmd.Output#2"}:                          "content_minimized_maintenance",
	{"private_review_provider.go", "runPrivateReviewProvider", "exec.CommandContext"}:         "durable_session",
	{"private_workspace.go", "commandHasOutput", "cmd.Start"}:                                 "content_minimized_maintenance",
	{"private_workspace.go", "gitPathIgnored", "cmd.Run#1"}:                                   "content_minimized_maintenance",
	{"private_workspace.go", "gitPathIgnored", "cmd.Run#2"}:                                   "content_minimized_maintenance",
	{"private_workspace.go", "gitPathIgnored", "exec.Command#1"}:                              "content_minimized_maintenance",
	{"private_workspace.go", "gitPathIgnored", "exec.Command#2"}:                              "content_minimized_maintenance",
	{"private_workspace.go", "privateWorkspaceGitBoundaryWithLimits", "cmd.Output"}:           "content_minimized_maintenance",
	{"private_workspace.go", "privateWorkspaceGitBoundaryWithLimits", "exec.Command#1"}:       "content_minimized_maintenance",
	{"private_workspace.go", "privateWorkspaceGitBoundaryWithLimits", "exec.Command#2"}:       "content_minimized_maintenance",
	{"provider_attempt.go", "executeProviderAttemptWithSession", "cmd.Start"}:                 "durable_session",
	{"provider_runtime.go", "runCodexPluginCommand", "cmd.Run"}:                               "durable_parent_attempt",
	{"provider_runtime.go", "runCodexPluginCommand", "exec.CommandContext"}:                   "durable_parent_attempt",
	{"runner.go", "atlRuntimeVersion", "cmd.Output"}:                                          "durable_parent_attempt",
	{"runner.go", "atlRuntimeVersion", "exec.CommandContext"}:                                 "durable_parent_attempt",
	{"runner.go", "commandVersionWithEnvironment", "cmd.Output"}:                              "durable_parent_attempt",
	{"runner.go", "commandVersionWithEnvironment", "exec.CommandContext"}:                     "durable_parent_attempt",
	{"runner.go", "runCodexConfinementPreflight", "cmd.Run"}:                                  "durable_parent_attempt",
	{"runner.go", "runCodexConfinementPreflight", "exec.CommandContext"}:                      "durable_parent_attempt",
	{"runner.go", "runHeadlessOnce", "exec.CommandContext"}:                                   "durable_session",
	{"runner.go", "runSyntheticMirrorBind", "cmd.Run"}:                                        "durable_parent_attempt",
	{"runner.go", "runSyntheticMirrorBind", "exec.CommandContext"}:                            "durable_parent_attempt",
	{"storage.go", "PreparePrivateOutputRoot", "cmd.Run"}:                                     "content_minimized_maintenance",
	{"storage.go", "PreparePrivateOutputRoot", "exec.Command"}:                                "content_minimized_maintenance",
	{"storage.go", "requirePrivateLiveInputsForWorkspace", "cmd.Run"}:                         "content_minimized_maintenance",
	{"storage.go", "requirePrivateLiveInputsForWorkspace", "exec.Command"}:                    "content_minimized_maintenance",
	{"tool_availability.go", "QualifyCodexCLIToolAvailability", "exec.CommandContext"}:        "durable_session",
}

func TestEveryProductionProcessEntryHasAReviewedDurableOwner(t *testing.T) {
	actual, err := collectAttemptSpawnCalls(".")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(actual) != fmt.Sprint(sortedReviewedAttemptSpawnCalls()) {
		t.Fatalf("production process-entry inventory changed:\n got: %v\nwant: %v", actual, sortedReviewedAttemptSpawnCalls())
	}
	for call, owner := range reviewedAttemptSpawnCalls {
		if owner != "durable_session" && owner != "durable_parent_attempt" && owner != "legacy_private_durable" &&
			owner != "content_minimized_maintenance" {
			t.Fatalf("process-entry %v has invalid durable owner %q", call, owner)
		}
	}
}

func collectAttemptSpawnCalls(root string) ([]attemptSpawnCall, error) {
	var calls []attemptSpawnCall
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != root && (entry.Name() == "testdata" || strings.HasPrefix(entry.Name(), ".") || strings.HasPrefix(entry.Name(), "_")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		execAliases := map[string]bool{}
		for _, imported := range parsed.Imports {
			value, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				return err
			}
			if value == "os/exec" {
				name := "exec"
				if imported.Name != nil {
					name = imported.Name.Name
				}
				execAliases[name] = true
			}
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		for _, declaration := range parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			functionName := attemptSpawnFunctionName(function)
			ast.Inspect(function.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if identifier, ok := selector.X.(*ast.Ident); ok && execAliases[identifier.Name] &&
					(selector.Sel.Name == "Command" || selector.Sel.Name == "CommandContext") {
					calls = append(calls, attemptSpawnCall{relative, functionName, "exec." + selector.Sel.Name})
					return true
				}
				if len(execAliases) != 0 {
					switch selector.Sel.Name {
					case "Start", "Run", "Output", "CombinedOutput":
						calls = append(calls, attemptSpawnCall{relative, functionName, "cmd." + selector.Sel.Name})
					}
				}
				return true
			})
		}
		return nil
	})
	sort.Slice(calls, func(i, j int) bool {
		left, right := calls[i], calls[j]
		return left.File+"\x00"+left.Function+"\x00"+left.Call < right.File+"\x00"+right.Function+"\x00"+right.Call
	})
	for start := 0; start < len(calls); {
		end := start + 1
		for end < len(calls) && calls[end] == calls[start] {
			end++
		}
		if end-start > 1 {
			for index := start; index < end; index++ {
				calls[index].Call += fmt.Sprintf("#%d", index-start+1)
			}
		}
		start = end
	}
	return calls, err
}

func attemptSpawnFunctionName(function *ast.FuncDecl) string {
	if function.Recv == nil || len(function.Recv.List) != 1 {
		return function.Name.Name
	}
	receiver := function.Recv.List[0].Type
	if pointer, ok := receiver.(*ast.StarExpr); ok {
		receiver = pointer.X
	}
	identifier, ok := receiver.(*ast.Ident)
	if !ok {
		return function.Name.Name
	}
	return identifier.Name + "." + function.Name.Name
}

func sortedReviewedAttemptSpawnCalls() []attemptSpawnCall {
	result := make([]attemptSpawnCall, 0, len(reviewedAttemptSpawnCalls))
	for call := range reviewedAttemptSpawnCalls {
		result = append(result, call)
	}
	sort.Slice(result, func(i, j int) bool {
		left, right := result[i], result[j]
		return left.File+"\x00"+left.Function+"\x00"+left.Call < right.File+"\x00"+right.Function+"\x00"+right.Call
	})
	return result
}
