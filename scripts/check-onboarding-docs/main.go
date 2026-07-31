// Command check-onboarding-docs validates the documentation paths that a new
// user or agent follows. It is deliberately offline: command checks execute a
// supplied atl binary with --help and an isolated, credential-free environment.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const commandManifestVersion = 1

var requiredDocuments = []string{
	"README.md",
	"README.ru.md",
	"docs/README.md",
	"docs/getting-started.md",
	"docs/agent-setup.md",
	"docs/safe-writes.md",
	"docs/troubleshooting.md",
	"docs/compatibility.md",
}

// commandPaths is intentionally small and versioned. These are the command
// paths used by the focused onboarding guides, not a duplicate of the complete
// CLI reference. Every path is checked using --help only.
var commandPaths = [][]string{
	{},
	{"version"},
	{"auth"},
	{"auth", "login"},
	{"auth", "status"},
	{"config"},
	{"config", "set"},
	{"config", "show"},
	{"capabilities"},
	{"environment", "inspect"},
	{"conf"},
	{"conf", "search"},
	{"conf", "page", "get"},
	{"conf", "pull"},
	{"conf", "status"},
	{"conf", "diff"},
	{"conf", "validate"},
	{"conf", "apply"},
	{"conf", "push"},
	{"jira"},
	{"jira", "fields"},
	{"jira", "issue", "get"},
	{"jira", "issue", "search"},
	{"jira", "issue", "comment", "preview"},
	{"jira", "issue", "comment", "add"},
	{"jira", "pull"},
	{"jira", "status"},
	{"jira", "apply"},
	{"jira", "push"},
	{"profile"},
	{"profile", "show"},
	{"profile", "apply"},
	{"mcp", "serve"},
}

// commandHelpRequirements binds documented flags that a path-only --help
// check would otherwise miss. Keep this list focused on first-use examples.
var commandHelpRequirements = map[string][]string{
	"auth login":                 {"--service"},
	"config set":                 {"--confluence-url", "--jira-url"},
	"conf search":                {"--cql", "--limit"},
	"conf pull":                  {"--id", "--into"},
	"conf push":                  {"--dry-run"},
	"jira fields":                {"--summary-only"},
	"jira issue search":          {"--jql", "--limit"},
	"jira issue comment preview": {"--from-md"},
	"jira issue comment add":     {"--from-md", "--apply", "--expected-proposal-hash"},
	"jira pull":                  {"--jql", "--into"},
	"jira push":                  {"--apply"},
	"mcp serve":                  {"--service"},
}

type report struct {
	Documents int
	Links     int
	Commands  int
}

type markdownLink struct {
	destination string
	line        int
}

func main() {
	root := flag.String("root", ".", "repository root")
	atlBinary := flag.String("atl", "", "path to the atl binary to validate")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "check-onboarding-docs does not accept positional arguments")
		os.Exit(2)
	}
	if strings.TrimSpace(*atlBinary) == "" {
		fmt.Fprintln(os.Stderr, "check-onboarding-docs requires -atl <path>")
		os.Exit(2)
	}

	result, err := validateRepository(*root, *atlBinary)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("onboarding docs: %d documents, %d local links, %d command paths (manifest v%d)\n",
		result.Documents, result.Links, result.Commands, commandManifestVersion)
}

func validateRepository(root, atlBinary string) (report, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return report{}, fmt.Errorf("resolve repository root: %w", err)
	}
	rootAbs = filepath.Clean(rootAbs)
	rootInfo, err := os.Stat(rootAbs)
	if err != nil {
		return report{}, fmt.Errorf("inspect repository root: %w", err)
	}
	if !rootInfo.IsDir() {
		return report{}, fmt.Errorf("repository root %q is not a directory", rootAbs)
	}

	result, err := validateDocumentation(rootAbs)
	if err != nil {
		return result, err
	}
	commands, err := validateCommands(rootAbs, atlBinary)
	result.Commands = commands
	if err != nil {
		return result, err
	}
	return result, nil
}

func validateDocumentation(root string) (report, error) {
	rootResolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return report{}, fmt.Errorf("resolve repository root: %w", err)
	}
	rootResolved, err = filepath.Abs(rootResolved)
	if err != nil {
		return report{}, fmt.Errorf("resolve repository root: %w", err)
	}

	var problems []string
	result := report{}
	for _, relative := range requiredDocuments {
		document := filepath.Join(root, filepath.FromSlash(relative))
		contents, readErr := os.ReadFile(document)
		if readErr != nil {
			if errors.Is(readErr, os.ErrNotExist) {
				problems = append(problems, fmt.Sprintf("required onboarding document %s is missing", relative))
			} else {
				problems = append(problems, fmt.Sprintf("read %s: %v", relative, readErr))
			}
			continue
		}
		result.Documents++
		for _, link := range markdownLinks(string(contents)) {
			local, linkErr := validateLocalLink(root, rootResolved, document, link.destination)
			if !local {
				continue
			}
			result.Links++
			if linkErr != nil {
				problems = append(problems, fmt.Sprintf("%s:%d: link %q: %v", relative, link.line, link.destination, linkErr))
			}
		}
	}
	if len(problems) != 0 {
		sort.Strings(problems)
		return result, errors.New("onboarding documentation check failed:\n- " + strings.Join(problems, "\n- "))
	}
	return result, nil
}

func validateLocalLink(root, rootResolved, document, destination string) (bool, error) {
	destination = strings.TrimSpace(destination)
	if destination == "" || strings.HasPrefix(destination, "#") {
		return true, nil
	}
	parsed, err := url.Parse(destination)
	if err != nil {
		return true, fmt.Errorf("invalid destination: %w", err)
	}
	if parsed.IsAbs() || parsed.Host != "" || strings.HasPrefix(destination, "//") {
		return false, nil
	}
	if parsed.Path == "" {
		return true, nil
	}
	decodedPath, err := url.PathUnescape(parsed.EscapedPath())
	if err != nil {
		return true, fmt.Errorf("invalid path escaping: %w", err)
	}

	var target string
	if strings.HasPrefix(decodedPath, "/") {
		target = filepath.Join(root, filepath.FromSlash(strings.TrimLeft(decodedPath, "/")))
	} else {
		target = filepath.Join(filepath.Dir(document), filepath.FromSlash(decodedPath))
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return true, fmt.Errorf("resolve target: %w", err)
	}
	target = filepath.Clean(target)
	if !within(root, target) {
		return true, errors.New("target escapes repository root")
	}
	info, err := os.Stat(target)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return true, errors.New("target does not exist")
		}
		return true, fmt.Errorf("inspect target: %w", err)
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return true, fmt.Errorf("resolve target symlinks: %w", err)
	}
	resolvedTarget, err = filepath.Abs(resolvedTarget)
	if err != nil {
		return true, fmt.Errorf("resolve target: %w", err)
	}
	if !within(rootResolved, resolvedTarget) {
		return true, errors.New("target resolves outside repository root")
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return true, errors.New("unresolved symbolic link target")
	}
	return true, nil
}

func within(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func markdownLinks(contents string) []markdownLink {
	lines := strings.Split(contents, "\n")
	links := make([]markdownLink, 0)
	inFence := false
	fenceMarker := byte(0)
	fenceLength := 0
	for index, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		if marker, length, ok := fence(trimmed); ok {
			if !inFence {
				inFence, fenceMarker, fenceLength = true, marker, length
			} else if marker == fenceMarker && length >= fenceLength {
				inFence = false
			}
			continue
		}
		if inFence {
			continue
		}

		if destination, ok := referenceDestination(line); ok {
			links = append(links, markdownLink{destination: destination, line: index + 1})
		}
		links = append(links, inlineLinks(line, index+1)...)
	}
	return links
}

func fence(line string) (byte, int, bool) {
	if len(line) < 3 || (line[0] != '`' && line[0] != '~') {
		return 0, 0, false
	}
	marker := line[0]
	length := 0
	for length < len(line) && line[length] == marker {
		length++
	}
	return marker, length, length >= 3
}

func referenceDestination(line string) (string, bool) {
	trimmed := strings.TrimLeft(line, " \t")
	if len(line)-len(trimmed) > 3 || !strings.HasPrefix(trimmed, "[") {
		return "", false
	}
	close := strings.Index(trimmed, "]:")
	if close <= 1 {
		return "", false
	}
	return firstDestination(trimmed[close+2:])
}

func inlineLinks(line string, lineNumber int) []markdownLink {
	var links []markdownLink
	inlineCode := byte(0)
	for index := 0; index+1 < len(line); index++ {
		if line[index] == '`' {
			if inlineCode == 0 {
				inlineCode = '`'
			} else {
				inlineCode = 0
			}
			continue
		}
		if inlineCode != 0 || line[index] != ']' || line[index+1] != '(' {
			continue
		}
		end := index + 2
		depth := 1
		escaped := false
		for ; end < len(line); end++ {
			character := line[end]
			if escaped {
				escaped = false
				continue
			}
			if character == '\\' {
				escaped = true
				continue
			}
			switch character {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					if destination, ok := firstDestination(line[index+2 : end]); ok {
						links = append(links, markdownLink{destination: destination, line: lineNumber})
					}
					index = end
				}
			}
			if depth == 0 {
				break
			}
		}
	}
	return links
}

func firstDestination(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", true
	}
	if value[0] == '<' {
		end := strings.IndexByte(value, '>')
		if end < 0 {
			return value, true
		}
		return value[1:end], true
	}
	for index, character := range value {
		if character == ' ' || character == '\t' {
			return strings.TrimSpace(value[:index]), true
		}
	}
	return value, true
}

func validateCommands(root, atlBinary string) (int, error) {
	binary, err := filepath.Abs(atlBinary)
	if err != nil {
		return 0, fmt.Errorf("resolve atl binary: %w", err)
	}
	info, err := os.Stat(binary)
	if err != nil {
		return 0, fmt.Errorf("inspect atl binary: %w", err)
	}
	if !info.Mode().IsRegular() {
		return 0, fmt.Errorf("atl binary %q is not a regular file", binary)
	}

	isolationRoot, err := os.MkdirTemp("", "atl-onboarding-check-")
	if err != nil {
		return 0, fmt.Errorf("create isolated command environment: %w", err)
	}
	defer func() { _ = os.RemoveAll(isolationRoot) }()
	environment := []string{
		"ATL_NO_UPDATE=1",
		"ATL_READ_ONLY=1",
		"ATL_CONFIG_DIR=" + filepath.Join(isolationRoot, "config"),
		"HOME=" + isolationRoot,
		"XDG_CONFIG_HOME=" + filepath.Join(isolationRoot, "xdg"),
		"HTTP_PROXY=http://127.0.0.1:1",
		"HTTPS_PROXY=http://127.0.0.1:1",
		"NO_PROXY=",
	}

	for index, path := range commandPaths {
		name := "atl"
		if len(path) != 0 {
			name += " " + strings.Join(path, " ")
		}
		arguments := append(append([]string{}, path...), "--help")
		command := exec.Command(binary, arguments...)
		command.Dir = root
		command.Env = environment
		var output bytes.Buffer
		command.Stdout = &output
		command.Stderr = &output
		if err := command.Run(); err != nil {
			detail := strings.TrimSpace(output.String())
			if len(detail) > 2048 {
				detail = detail[:2048] + "..."
			}
			if detail != "" {
				return index, fmt.Errorf("command manifest v%d path %q failed offline help validation: %w: %s", commandManifestVersion, name, err, detail)
			}
			return index, fmt.Errorf("command manifest v%d path %q failed offline help validation: %w", commandManifestVersion, name, err)
		}
		pathName := strings.Join(path, " ")
		for _, required := range commandHelpRequirements[pathName] {
			if !strings.Contains(output.String(), required) {
				return index, fmt.Errorf(
					"command manifest v%d path %q help is missing documented flag %q",
					commandManifestVersion, name, required,
				)
			}
		}
	}
	return len(commandPaths), nil
}
