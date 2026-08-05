// Command list-go-packages classifies repository packages for maintainer gates.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const (
	classRootCore = "root-core"
)

var declaredPackagePatterns = []string{"./cmd/...", "./internal/...", "./scripts/..."}

type options struct {
	class  string
	format string
	scope  string
}

type packageSets struct {
	RootCore []string
}

func main() {
	if err := run(".", os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "list-go-packages:", err)
		os.Exit(1)
	}
}

func run(root string, arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("list-go-packages", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var selected options
	flags.StringVar(&selected.class, "class", "", "package class: root-core")
	flags.StringVar(&selected.format, "format", "lines", "output format: lines or csv")
	flags.StringVar(&selected.scope, "scope", "all", "package scope: all or internal")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || selected.class != classRootCore {
		return errors.New("--class must be exactly root-core")
	}
	if selected.format != "lines" && selected.format != "csv" {
		return errors.New("--format must be exactly lines or csv")
	}
	if selected.scope != "all" && selected.scope != "internal" {
		return errors.New("--scope must be exactly all or internal")
	}

	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve repository root: %w", err)
	}
	module, err := goOutput(absoluteRoot, "list", "-m", "-f", "{{.Path}}")
	if err != nil {
		return err
	}
	module = strings.TrimSpace(module)
	if module == "" || strings.ContainsAny(module, " \t\r\n") {
		return errors.New("go list returned an invalid module path")
	}
	listArguments := []string{"list", "-f", "{{.ImportPath}}"}
	listArguments = append(listArguments, declaredPackagePatterns...)
	listed, err := goOutput(absoluteRoot, listArguments...)
	if err != nil {
		return err
	}
	listedPackages := strings.Fields(listed)
	if err := verifyDeclaredPackageRoots(module, listedPackages); err != nil {
		return err
	}
	sets, err := classifyPackages(module, listedPackages)
	if err != nil {
		return err
	}
	if err := verifyPackageBoundaries(absoluteRoot, module, sets); err != nil {
		return err
	}

	packages := sets.RootCore
	if selected.scope == "internal" {
		packages = filterPackagePrefix(packages, module+"/internal/")
	}
	if len(packages) == 0 {
		return errors.New("selected package set is empty")
	}
	separator := "\n"
	if selected.format == "csv" {
		separator = ","
	}
	_, err = fmt.Fprintln(output, strings.Join(packages, separator))
	return err
}

func verifyDeclaredPackageRoots(module string, packages []string) error {
	for _, root := range []string{"cmd", "internal", "scripts"} {
		prefix := module + "/" + root
		found := false
		for _, packagePath := range packages {
			if packagePath == prefix || strings.HasPrefix(packagePath, prefix+"/") {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("go list declared root %q matched no Go packages", "./"+root+"/...")
		}
	}
	return nil
}

func classifyPackages(module string, packages []string) (packageSets, error) {
	var sets packageSets
	seen := make(map[string]struct{}, len(packages))
	for _, packagePath := range packages {
		if packagePath == "" {
			continue
		}
		if _, duplicate := seen[packagePath]; duplicate {
			return packageSets{}, fmt.Errorf("duplicate package %q", packagePath)
		}
		seen[packagePath] = struct{}{}
		if !rootCorePackagePath(module, packagePath) {
			return packageSets{}, fmt.Errorf("package %q has no root-core classification", packagePath)
		}
		sets.RootCore = append(sets.RootCore, packagePath)
	}
	if len(sets.RootCore) == 0 {
		return packageSets{}, errors.New("root-core package set is empty")
	}
	sort.Strings(sets.RootCore)
	return sets, nil
}

func rootCorePackagePath(module, packagePath string) bool {
	for _, root := range []string{module + "/cmd", module + "/internal", module + "/scripts"} {
		if packagePath == root || strings.HasPrefix(packagePath, root+"/") {
			return true
		}
	}
	return false
}

func verifyPackageBoundaries(root, module string, sets packageSets) error {
	classified := make(map[string]struct{}, len(sets.RootCore))
	for _, packagePath := range sets.RootCore {
		classified[packagePath] = struct{}{}
	}
	return verifyPackageDependencies(root, module, "root-core", sets.RootCore, classified)
}

func verifyPackageDependencies(
	root, module, class string,
	packages []string,
	classified map[string]struct{},
) error {
	// Exclude the rewritten package variants produced for in-package tests.
	// Their ImportPath contains a bracketed display suffix; the ordinary package
	// and every dependency still appear independently in this inventory.
	arguments := []string{"list", "-deps", "-test", "-f", `{{if and .Module (eq .ForTest "")}}{{.ImportPath}}{{end}}`}
	arguments = append(arguments, packages...)
	output, err := goOutput(root, arguments...)
	if err != nil {
		return err
	}
	syntheticTests := make(map[string]struct{}, len(packages))
	for _, packagePath := range packages {
		syntheticTests[packagePath+".test"] = struct{}{}
	}
	for _, dependency := range strings.Fields(output) {
		if _, synthetic := syntheticTests[dependency]; synthetic {
			continue
		}
		if dependency == module || strings.HasPrefix(dependency, module+"/") {
			if _, known := classified[dependency]; !known {
				return fmt.Errorf("%s test dependency %q is outside declared package roots", class, dependency)
			}
		}
	}
	return nil
}

func filterPackagePrefix(packages []string, prefix string) []string {
	filtered := make([]string, 0, len(packages))
	for _, packagePath := range packages {
		if strings.HasPrefix(packagePath, prefix) {
			filtered = append(filtered, packagePath)
		}
	}
	return filtered
}

func goOutput(root string, arguments ...string) (string, error) {
	command := exec.Command("go", arguments...)
	command.Dir = root
	command.Env = goCommandEnvironment()
	return separatedCommandOutput(command, "go "+strings.Join(arguments, " "))
}

func goCommandEnvironment() []string {
	environment := make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		name, _, found := strings.Cut(entry, "=")
		if found && (name == "GOROOT" || name == "GOTOOLCHAIN" || name == "GOWORK") {
			continue
		}
		environment = append(environment, entry)
	}
	return append(environment, "GOTOOLCHAIN=auto", "GOWORK=off")
}

const commandStderrMaxBytes = 64 << 10

type boundedStderr struct {
	tail      []byte
	truncated bool
}

func (writer *boundedStderr) Write(data []byte) (int, error) {
	originalLength := len(data)
	if len(data) >= commandStderrMaxBytes {
		writer.tail = append(writer.tail[:0], data[len(data)-commandStderrMaxBytes:]...)
		writer.truncated = true
		return originalLength, nil
	}
	overflow := len(writer.tail) + len(data) - commandStderrMaxBytes
	if overflow > 0 {
		copy(writer.tail, writer.tail[overflow:])
		writer.tail = writer.tail[:len(writer.tail)-overflow]
		writer.truncated = true
	}
	writer.tail = append(writer.tail, data...)
	return originalLength, nil
}

func (writer *boundedStderr) String() string {
	tail := strings.ToValidUTF8(string(writer.tail), "?")
	if writer.truncated {
		return "[... stderr truncated to final 64 KiB ...]\n" + tail
	}
	return tail
}

func separatedCommandOutput(command *exec.Cmd, description string) (string, error) {
	var stdout bytes.Buffer
	var stderr boundedStderr
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if err != nil {
		diagnostic := strings.TrimSpace(stderr.String())
		if diagnostic == "" {
			diagnostic = strings.TrimSpace(stdout.String())
		}
		if diagnostic == "" {
			return "", fmt.Errorf("%s: %w", description, err)
		}
		return "", fmt.Errorf("%s: %w: %s", description, err, diagnostic)
	}
	return stdout.String(), nil
}
