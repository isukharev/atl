package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // Git SHA-1 object identity, not a security primitive.
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	maxSourceFiles = 20_000
	maxSourceBytes = 1 << 30
	maxGitTree     = 32 << 20
	maxGoList      = 64 << 20
)

type goEnvironment struct {
	GOARCH       string `json:"GOARCH"`
	GOAMD64      string `json:"GOAMD64"`
	CGOEnabled   string `json:"CGO_ENABLED"`
	GOENV        string `json:"GOENV"`
	GOExperiment string `json:"GOEXPERIMENT"`
	GOFlags      string `json:"GOFLAGS"`
	GOVersion    string `json:"GOVERSION"`
	GOOS         string `json:"GOOS"`
}

type listedPackage struct {
	Dir             string
	ImportPath      string
	GoFiles         []string
	CgoFiles        []string
	CFiles          []string
	CXXFiles        []string
	MFiles          []string
	HFiles          []string
	FFiles          []string
	SFiles          []string
	SwigFiles       []string
	SwigCXXFiles    []string
	SysoFiles       []string
	EmbedFiles      []string
	TestGoFiles     []string
	XTestGoFiles    []string
	TestEmbedFiles  []string
	XTestEmbedFiles []string
	Error           *struct{ Err string }
}

type sourceInventoryRole int

const (
	productBuildSources sourceInventoryRole = iota
	evaluatorRaceSources
)

func certifySource(ctx context.Context, root, evaluator, want string, allowProduct bool, env []string) (goEnvironment, string, error) {
	checkCtx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()
	repository, stderr, err := captureCommand(checkCtx, commandSpec{dir: root, name: "git", args: []string{"rev-parse", "--show-toplevel"}, env: env}, 4096)
	if err != nil || len(stderr) != 0 || strings.TrimSpace(string(repository)) != root {
		return goEnvironment{}, "", errors.New("hosted race checkout is not the repository root")
	}
	head, stderr, err := captureCommand(checkCtx, commandSpec{dir: root, name: "git", args: []string{"rev-parse", "HEAD"}, env: env}, 4096)
	if err != nil || len(stderr) != 0 || strings.TrimSpace(string(head)) != want {
		return goEnvironment{}, "", errors.New("hosted race checkout does not match the selected source")
	}
	if err := verifyWorktreeResidue(checkCtx, root, allowProduct, env); err != nil {
		return goEnvironment{}, "", err
	}

	rootVersion, err := moduleGoVersion(filepath.Join(root, "go.mod"))
	if err != nil {
		return goEnvironment{}, "", err
	}
	evaluatorVersion, err := moduleGoVersion(filepath.Join(evaluator, "go.mod"))
	if err != nil || rootVersion != evaluatorVersion {
		return goEnvironment{}, "", errors.New("root and evaluator Go directives must match exactly")
	}
	goBody, _, err := captureCommand(checkCtx, commandSpec{
		dir: root, name: "go", args: []string{"env", "-json", "GOVERSION", "GOOS", "GOARCH", "GOAMD64", "GOEXPERIMENT", "GOENV", "GOFLAGS", "CGO_ENABLED"}, env: env,
	}, 64<<10)
	var goEnv goEnvironment
	if err != nil || json.Unmarshal(goBody, &goEnv) != nil ||
		goEnv.GOVersion != "go"+rootVersion || goEnv.GOOS != "linux" || goEnv.GOARCH != "amd64" ||
		goEnv.GOAMD64 != "v1" || goEnv.GOExperiment != "" || goEnv.GOENV != "" ||
		goEnv.GOFlags != "" || goEnv.CGOEnabled != "1" {
		return goEnvironment{}, "", errors.New("hosted race toolchain does not match the exact Go and platform contract")
	}

	tree, err := committedTree(checkCtx, root, want, env)
	if err != nil {
		return goEnvironment{}, "", err
	}
	productActive := map[string]bool{
		filepath.Join(root, "go.mod"): true, filepath.Join(root, "go.sum"): true,
	}
	evaluatorActive := map[string]bool{
		filepath.Join(evaluator, "go.mod"): true, filepath.Join(evaluator, "go.sum"): true,
	}
	productEnv := environmentWith(env, "CGO_ENABLED", "0")
	rootPackages, err := listSourcePackages(checkCtx, root, []string{"-deps", "-json", "./cmd/atl"}, productEnv)
	if err != nil {
		return goEnvironment{}, "", err
	}
	evaluatorPackages, err := listSourcePackages(checkCtx, evaluator, []string{"-race", "-json", "./..."}, env)
	if err != nil {
		return goEnvironment{}, "", err
	}
	if err := collectActiveFiles(root, rootPackages, productActive, productBuildSources); err != nil {
		return goEnvironment{}, "", err
	}
	if err := collectActiveFiles(root, evaluatorPackages, evaluatorActive, evaluatorRaceSources); err != nil {
		return goEnvironment{}, "", err
	}
	if err := mergeActiveFiles(productActive, evaluatorActive); err != nil {
		return goEnvironment{}, "", err
	}
	if err := verifyActiveFiles(checkCtx, root, tree, productActive); err != nil {
		return goEnvironment{}, "", err
	}
	if err := rejectVerboseTests(checkCtx, evaluatorActive); err != nil {
		return goEnvironment{}, "", err
	}
	if err := verifyWorktreeResidue(checkCtx, root, allowProduct, env); err != nil {
		return goEnvironment{}, "", err
	}
	return goEnv, strings.TrimSpace(string(head)), nil
}

func mergeActiveFiles(target, source map[string]bool) error {
	for path := range source {
		target[path] = true
	}
	if len(target) > maxSourceFiles {
		return errors.New("active Go source inventory exceeds its file bound")
	}
	return nil
}

func environmentWith(env []string, key, value string) []string {
	result := make([]string, 0, len(env)+1)
	for _, item := range env {
		name, _, _ := strings.Cut(item, "=")
		if name != key {
			result = append(result, item)
		}
	}
	return append(result, key+"="+value)
}

func rejectVerboseTests(ctx context.Context, active map[string]bool) error {
	for path := range active {
		if ctx.Err() != nil {
			return errors.New("active test output-mode inspection exceeded its bound")
		}
		if !strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			return errors.New("cannot inspect active test output-mode behavior")
		}
		aliases := map[string]bool{}
		dotImport := false
		for _, imported := range file.Imports {
			if imported.Path.Value != `"testing"` {
				continue
			}
			if imported.Name == nil {
				aliases["testing"] = true
			} else if imported.Name.Name == "." {
				dotImport = true
			} else if imported.Name.Name != "_" {
				aliases[imported.Name.Name] = true
			}
		}
		unsafe := false
		ast.Inspect(file, func(node ast.Node) bool {
			switch expression := node.(type) {
			case *ast.Ident:
				unsafe = unsafe || dotImport && expression.Name == "Verbose"
			case *ast.SelectorExpr:
				owner, ok := expression.X.(*ast.Ident)
				unsafe = unsafe || ok && aliases[owner.Name] && expression.Sel.Name == "Verbose"
			}
			return !unsafe
		})
		if unsafe {
			return errors.New("active evaluator tests must not branch on verbose output mode")
		}
	}
	return nil
}

func verifyWorktreeResidue(ctx context.Context, root string, allowProduct bool, env []string) error {
	body, stderr, err := captureCommand(ctx, commandSpec{
		dir: root, name: "git", args: []string{"status", "--porcelain=v1", "--untracked-files=all", "--ignored=matching"}, env: env,
	}, 1<<20)
	if err != nil || len(stderr) != 0 {
		return errors.New("cannot inspect hosted race source residue")
	}
	lines := strings.Split(strings.TrimSuffix(string(body), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		if allowProduct {
			return errors.New("expected product build artifact is missing")
		}
		return nil
	}
	if allowProduct && len(lines) == 1 && lines[0] == "!! atl" {
		return nil
	}
	return errors.New("hosted race checkout contains unexpected source residue")
}

func moduleGoVersion(path string) (string, error) {
	body, err := os.ReadFile(path)
	if err != nil || len(body) > 1<<20 {
		return "", errors.New("cannot read bounded module Go directive")
	}
	version := ""
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == "go" {
			if len(fields) != 2 || version != "" {
				return "", errors.New("module Go directive is malformed")
			}
			version = fields[1]
		}
	}
	if version == "" {
		return "", errors.New("module Go directive is missing")
	}
	return version, nil
}

func committedTree(ctx context.Context, root, source string, env []string) (map[string]string, error) {
	format, formatStderr, formatErr := captureCommand(ctx, commandSpec{
		dir: root, name: "git", args: []string{"rev-parse", "--show-object-format"}, env: env,
	}, 4096)
	if formatErr != nil || len(formatStderr) != 0 || strings.TrimSpace(string(format)) != "sha1" {
		return nil, errors.New("selected source uses an unsupported object format")
	}
	body, stderr, err := captureCommand(ctx, commandSpec{
		dir: root, name: "git", args: []string{"ls-tree", "-r", "-z", "--full-tree", source}, env: env,
	}, maxGitTree)
	if err != nil || len(stderr) != 0 {
		return nil, errors.New("cannot read selected source tree")
	}
	tree := make(map[string]string)
	for _, record := range bytes.Split(bytes.TrimSuffix(body, []byte{0}), []byte{0}) {
		metadata, path, ok := bytes.Cut(record, []byte{'\t'})
		fields := strings.Fields(string(metadata))
		if !ok || len(fields) != 3 || fields[1] != "blob" || path == nil {
			continue
		}
		tree[filepath.FromSlash(string(path))] = fields[2]
	}
	if len(tree) == 0 {
		return nil, errors.New("selected source tree is empty")
	}
	return tree, nil
}

func listSourcePackages(ctx context.Context, dir string, args []string, env []string) ([]listedPackage, error) {
	body, _, err := captureCommand(ctx, commandSpec{dir: dir, name: "go", args: append([]string{"list"}, args...), env: env}, maxGoList)
	if err != nil {
		return nil, errors.New("cannot derive active Go source inventory")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	packages := []listedPackage{}
	for {
		var pkg listedPackage
		decodeErr := decoder.Decode(&pkg)
		if decodeErr == io.EOF {
			break
		}
		if decodeErr != nil || pkg.Error != nil || pkg.Dir == "" || pkg.ImportPath == "" {
			return nil, errors.New("active Go source inventory is malformed")
		}
		packages = append(packages, pkg)
		if len(packages) > maxInventory {
			return nil, errors.New("active Go package inventory exceeds its reviewed bound")
		}
	}
	if len(packages) == 0 {
		return nil, errors.New("active Go package inventory is empty")
	}
	return packages, nil
}

func collectActiveFiles(root string, packages []listedPackage, active map[string]bool, role sourceInventoryRole) error {
	if role != productBuildSources && role != evaluatorRaceSources {
		return errors.New("active Go source inventory has an unknown role")
	}
	for _, pkg := range packages {
		dir, err := filepath.Abs(pkg.Dir)
		if err != nil || !withinRoot(root, dir) {
			continue // Standard-library and module-cache dependencies are toolchain/sum bound.
		}
		groups := [][]string{pkg.GoFiles, pkg.CgoFiles, pkg.CFiles, pkg.CXXFiles, pkg.MFiles, pkg.HFiles,
			pkg.FFiles, pkg.SFiles, pkg.SwigFiles, pkg.SwigCXXFiles, pkg.SysoFiles, pkg.EmbedFiles}
		if role == evaluatorRaceSources {
			groups = append(groups, pkg.TestGoFiles, pkg.XTestGoFiles, pkg.TestEmbedFiles, pkg.XTestEmbedFiles)
		}
		for _, files := range groups {
			for _, name := range files {
				path := filepath.Clean(filepath.Join(dir, filepath.FromSlash(name)))
				if !withinRoot(root, path) {
					return errors.New("active Go source inventory escapes the repository")
				}
				active[path] = true
				if len(active) > maxSourceFiles {
					return errors.New("active Go source inventory exceeds its file bound")
				}
			}
		}
	}
	return nil
}

func verifyActiveFiles(ctx context.Context, root string, tree map[string]string, active map[string]bool) error {
	paths := make([]string, 0, len(active))
	for path := range active {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	var total int64
	for _, path := range paths {
		if ctx.Err() != nil {
			return errors.New("active Go source verification exceeded its bound")
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return errors.New("active Go source inventory is outside the selected tree")
		}
		want, ok := tree[rel]
		info, statErr := os.Lstat(path)
		if !ok || statErr != nil || !info.Mode().IsRegular() {
			return errors.New("active Go source is absent from the selected tree")
		}
		total += info.Size()
		if info.Size() < 0 || total > maxSourceBytes {
			return errors.New("active Go source inventory exceeds its byte bound")
		}
		got, err := gitBlobSHA1(path, info.Size())
		if err != nil || got != want {
			return errors.New("active Go source differs from the selected tree")
		}
	}
	return nil
}

func gitBlobSHA1(path string, size int64) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha1.New() //nolint:gosec // Git repository object format is SHA-1.
	_, _ = fmt.Fprintf(hash, "blob %d%c", size, byte(0))
	if _, err := io.Copy(hash, bufio.NewReader(file)); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func withinRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
