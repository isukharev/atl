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
	classCore  = "core"
	classHeavy = "heavy"
)

type options struct {
	class  string
	format string
	scope  string
}

type packageSets struct {
	Core  []string
	Heavy []string
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
	flags.StringVar(&selected.class, "class", "", "package class: core or heavy")
	flags.StringVar(&selected.format, "format", "lines", "output format: lines or csv")
	flags.StringVar(&selected.scope, "scope", "all", "package scope: all or internal")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || selected.class != classCore && selected.class != classHeavy {
		return errors.New("--class must be exactly core or heavy")
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
	listed, err := goOutput(absoluteRoot, "list", "-f", "{{.ImportPath}}", "./...")
	if err != nil {
		return err
	}
	sets, err := classifyPackages(module, strings.Fields(listed))
	if err != nil {
		return err
	}
	if err := verifyCoreBoundary(absoluteRoot, module, sets.Core); err != nil {
		return err
	}

	packages := sets.Core
	if selected.class == classHeavy {
		packages = sets.Heavy
	}
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

func classifyPackages(module string, packages []string) (packageSets, error) {
	var sets packageSets
	seen := make(map[string]struct{}, len(packages))
	heavyRoots := map[string]bool{
		module + "/internal/agenteval": false,
		module + "/scripts/agent-eval": false,
	}
	for _, packagePath := range packages {
		if packagePath == "" {
			continue
		}
		if _, duplicate := seen[packagePath]; duplicate {
			return packageSets{}, fmt.Errorf("duplicate package %q", packagePath)
		}
		seen[packagePath] = struct{}{}
		if root, heavy := heavyPackageRoot(module, packagePath); heavy {
			sets.Heavy = append(sets.Heavy, packagePath)
			if packagePath == root {
				heavyRoots[root] = true
			}
			continue
		}
		if !corePackagePath(module, packagePath) {
			return packageSets{}, fmt.Errorf("package %q has no core/heavy classification", packagePath)
		}
		sets.Core = append(sets.Core, packagePath)
	}
	for root, found := range heavyRoots {
		if !found {
			return packageSets{}, fmt.Errorf("required heavy package %q is missing", root)
		}
	}
	if len(sets.Core) == 0 || len(sets.Heavy) == 0 {
		return packageSets{}, errors.New("core and heavy package sets must both be non-empty")
	}
	sort.Strings(sets.Core)
	sort.Strings(sets.Heavy)
	return sets, nil
}

func corePackagePath(module, packagePath string) bool {
	for _, root := range []string{module + "/cmd", module + "/internal", module + "/scripts"} {
		if packagePath == root || strings.HasPrefix(packagePath, root+"/") {
			return true
		}
	}
	return false
}

func heavyPackageRoot(module, packagePath string) (string, bool) {
	for _, root := range []string{module + "/internal/agenteval", module + "/scripts/agent-eval"} {
		if packagePath == root || strings.HasPrefix(packagePath, root+"/") {
			return root, true
		}
	}
	return "", false
}

func verifyCoreBoundary(root, module string, corePackages []string) error {
	arguments := []string{"list", "-deps", "-test", "-f", "{{.ImportPath}}"}
	arguments = append(arguments, corePackages...)
	output, err := goOutput(root, arguments...)
	if err != nil {
		return err
	}
	for _, dependency := range strings.Fields(output) {
		if heavyRoot, heavy := heavyPackageRoot(module, dependency); heavy {
			return fmt.Errorf("core test dependency reaches heavy package %q", heavyRoot)
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
	return separatedCommandOutput(command, "go "+strings.Join(arguments, " "))
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
