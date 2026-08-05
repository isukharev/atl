// Command check-module-boundary verifies ATL's reviewed two-module layout.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	rootModulePath      = "github.com/isukharev/atl"
	evaluatorModulePath = rootModulePath + "/internal/agenteval"
	evaluatorModuleDir  = "internal/agenteval"
	evaluatorCommandDir = evaluatorModuleDir + "/cmd/agent-eval"
)

var expectedModuleFiles = []string{"go.mod", evaluatorModuleDir + "/go.mod"}

type moduleFile struct {
	Path      string
	GoVersion string
	Requires  map[string]bool
	Replaces  map[string]bool
}

type report struct {
	Status             string   `json:"status"`
	RootModule         string   `json:"root_module"`
	EvaluatorModule    string   `json:"evaluator_module"`
	GoVersion          string   `json:"go_version"`
	TrackedModuleFiles []string `json:"tracked_module_files"`
}

func main() {
	flags := flag.NewFlagSet("check-module-boundary", flag.ExitOnError)
	root := flags.String("root", ".", "repository root")
	if err := flags.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "check-module-boundary: unexpected arguments")
		os.Exit(2)
	}
	if err := run(*root, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "check-module-boundary:", err)
		os.Exit(1)
	}
}

func run(root string, output io.Writer) error {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve repository root: %w", err)
	}
	tracked, err := trackedFiles(absRoot)
	if err != nil {
		return err
	}
	moduleFiles, err := validateTrackedModuleLayout(absRoot, tracked)
	if err != nil {
		return err
	}

	rootModule, err := parseModuleFile(filepath.Join(absRoot, "go.mod"))
	if err != nil {
		return fmt.Errorf("root module: %w", err)
	}
	evaluatorModule, err := parseModuleFile(filepath.Join(absRoot, filepath.FromSlash(evaluatorModuleDir), "go.mod"))
	if err != nil {
		return fmt.Errorf("evaluator module: %w", err)
	}
	if rootModule.Path != rootModulePath {
		return fmt.Errorf("root module path = %q, want %q", rootModule.Path, rootModulePath)
	}
	if evaluatorModule.Path != evaluatorModulePath {
		return fmt.Errorf("evaluator module path = %q, want %q", evaluatorModule.Path, evaluatorModulePath)
	}
	if rootModule.GoVersion != evaluatorModule.GoVersion {
		return fmt.Errorf("go patch drift: root go %q, evaluator go %q", rootModule.GoVersion, evaluatorModule.GoVersion)
	}
	if err := rejectModuleCoupling(rootModule, evaluatorModule, "root", "evaluator"); err != nil {
		return err
	}
	if err := rejectModuleCoupling(evaluatorModule, rootModule, "evaluator", "root"); err != nil {
		return err
	}
	if err := validateImports(absRoot, tracked); err != nil {
		return err
	}

	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report{
		Status:             "ok",
		RootModule:         rootModule.Path,
		EvaluatorModule:    evaluatorModule.Path,
		GoVersion:          rootModule.GoVersion,
		TrackedModuleFiles: moduleFiles,
	})
}

func trackedFiles(root string) ([]string, error) {
	command := exec.Command("git", "-C", root, "ls-files", "-z")
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			return nil, fmt.Errorf("list tracked files: %w", err)
		}
		return nil, fmt.Errorf("list tracked files: %w: %s", err, detail)
	}
	paths := strings.Split(strings.TrimSuffix(string(output), "\x00"), "\x00")
	if len(paths) == 1 && paths[0] == "" {
		return nil, errors.New("repository has no tracked files")
	}
	for _, path := range paths {
		if !isCleanRepositoryPath(path) {
			return nil, fmt.Errorf("tracked path %q is not a clean repository-relative path", path)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func isCleanRepositoryPath(path string) bool {
	if path == "" || filepath.IsAbs(path) || strings.Contains(path, "\\") {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
	return clean == path && clean != "." && !strings.HasPrefix(clean, "../")
}

func validateTrackedModuleLayout(root string, tracked []string) ([]string, error) {
	var modules []string
	for _, path := range tracked {
		base := filepath.Base(filepath.FromSlash(path))
		if base == "go.work" || base == "go.work.sum" {
			return nil, fmt.Errorf("tracked workspace file %q is forbidden; use GOWORK=off", path)
		}
		if base == "go.mod" {
			modules = append(modules, path)
		}
	}
	sort.Strings(modules)
	for _, path := range modules {
		if !contains(expectedModuleFiles, path) {
			return nil, fmt.Errorf("tracked go.mod layout contains unexpected module file %q", path)
		}
	}
	for _, path := range expectedModuleFiles {
		if !contains(modules, path) {
			return nil, fmt.Errorf("required module file %q is not tracked", path)
		}
		info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			return nil, fmt.Errorf("required module file %q is missing: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("required module file %q is not a regular file", path)
		}
	}
	return append([]string(nil), expectedModuleFiles...), nil
}

func parseModuleFile(path string) (moduleFile, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return moduleFile{}, fmt.Errorf("read go.mod: %w", err)
	}
	parsed := moduleFile{Requires: map[string]bool{}, Replaces: map[string]bool{}}
	section := ""
	for lineNumber, raw := range strings.Split(string(contents), "\n") {
		line := strings.TrimSpace(stripGoModComment(raw))
		if line == "" {
			continue
		}
		if line == ")" {
			if section == "" {
				return moduleFile{}, fmt.Errorf("line %d closes no directive block", lineNumber+1)
			}
			section = ""
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 2 && (fields[0] == "require" || fields[0] == "replace") && fields[1] == "(" {
			if section != "" {
				return moduleFile{}, fmt.Errorf("line %d nests directive blocks", lineNumber+1)
			}
			section = fields[0]
			continue
		}
		kind := section
		if kind == "" {
			if len(fields) == 0 {
				continue
			}
			kind = fields[0]
			fields = fields[1:]
		}
		switch kind {
		case "module":
			if len(fields) != 1 || parsed.Path != "" {
				return moduleFile{}, fmt.Errorf("line %d has an invalid module directive", lineNumber+1)
			}
			parsed.Path = fields[0]
		case "go":
			if len(fields) != 1 || parsed.GoVersion != "" {
				return moduleFile{}, fmt.Errorf("line %d has an invalid go directive", lineNumber+1)
			}
			parsed.GoVersion = fields[0]
		case "require":
			if len(fields) < 1 {
				return moduleFile{}, fmt.Errorf("line %d has an invalid require directive", lineNumber+1)
			}
			parsed.Requires[fields[0]] = true
		case "replace":
			if len(fields) < 3 || !contains(fields, "=>") {
				return moduleFile{}, fmt.Errorf("line %d has an invalid replace directive", lineNumber+1)
			}
			for _, field := range fields {
				if field != "=>" {
					parsed.Replaces[field] = true
				}
			}
		default:
			if section != "" {
				return moduleFile{}, fmt.Errorf("line %d has an invalid %s block entry", lineNumber+1, section)
			}
		}
	}
	if section != "" {
		return moduleFile{}, errors.New("unterminated go.mod directive block")
	}
	if parsed.Path == "" || parsed.GoVersion == "" {
		return moduleFile{}, errors.New("go.mod must contain exactly one module and go directive")
	}
	return parsed, nil
}

func stripGoModComment(line string) string {
	if index := strings.Index(line, "//"); index >= 0 {
		return line[:index]
	}
	return line
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func rejectModuleCoupling(owner, other moduleFile, ownerName, otherName string) error {
	if owner.Requires[other.Path] {
		return fmt.Errorf("%s module must not require the %s module", ownerName, otherName)
	}
	if owner.Replaces[other.Path] {
		return fmt.Errorf("%s module must not replace the %s module", ownerName, otherName)
	}
	return nil
}

func validateImports(root string, tracked []string) error {
	commandHasSource := false
	commandHasModuleSelfImport := false
	for _, path := range tracked {
		if !strings.HasSuffix(path, ".go") {
			continue
		}
		contents, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, contents, parser.ImportsOnly)
		if err != nil {
			return fmt.Errorf("parse imports in %s: %w", path, err)
		}
		imports, err := parsedImports(parsed)
		if err != nil {
			return fmt.Errorf("decode imports in %s: %w", path, err)
		}
		if strings.HasPrefix(path, evaluatorModuleDir+"/") {
			inCommand := strings.HasPrefix(path, evaluatorCommandDir+"/")
			if inCommand && !strings.HasSuffix(path, "_test.go") {
				commandHasSource = true
			}
			for _, imported := range imports {
				if !strings.HasPrefix(imported, rootModulePath+"/internal/") {
					continue
				}
				if inCommand && imported == evaluatorModulePath {
					if !strings.HasSuffix(path, "_test.go") {
						commandHasModuleSelfImport = true
					}
					continue
				}
				if inCommand {
					return fmt.Errorf("evaluator command %s imports product-private package %q outside its module self-boundary", path, imported)
				}
				return fmt.Errorf("evaluator library %s imports product-private package %q", path, imported)
			}
			continue
		}
		for _, imported := range imports {
			if imported == evaluatorModulePath || strings.HasPrefix(imported, evaluatorModulePath+"/") {
				return fmt.Errorf("product source %s imports evaluator module %q", path, imported)
			}
		}
	}
	if !commandHasSource {
		return fmt.Errorf("evaluator command %q has no production Go source", evaluatorCommandDir)
	}
	if !commandHasModuleSelfImport {
		return fmt.Errorf("evaluator command %q is missing its reviewed module self-boundary import", evaluatorCommandDir)
	}
	return nil
}

func parsedImports(file *ast.File) ([]string, error) {
	imports := make([]string, 0, len(file.Imports))
	for _, specification := range file.Imports {
		path, err := strconv.Unquote(specification.Path.Value)
		if err != nil {
			return nil, err
		}
		imports = append(imports, path)
	}
	return imports, nil
}
