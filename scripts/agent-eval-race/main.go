// Command agent-eval-race runs one source-bound shard of the hosted evaluator
// race contour. Ordinary developer and release race targets remain unsharded.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const (
	shardCount       = 4
	listTimeout      = 5 * time.Minute
	buildTimeout     = 10 * time.Minute
	testTimeout      = 47 * time.Minute
	goTestTimeout    = "45m"
	maxCommandOutput = 64 << 20
	maxFailureOutput = 64 << 10
	maxJSONLine      = 1 << 20
	maxJSONEvents    = 2_000_000
	maxInventory     = 100_000
	maxSelectorBytes = 128 << 10
	maxSlowTests     = 12
)

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

type options struct {
	shard  int
	source string
}

type commandSpec struct {
	dir  string
	name string
	args []string
	env  []string
}

type evidence struct {
	SchemaVersion int               `json:"schema_version"`
	Source        string            `json:"source_commit"`
	GoVersion     string            `json:"go_version"`
	GOOS          string            `json:"goos"`
	GOARCH        string            `json:"goarch"`
	GOAMD64       string            `json:"goamd64"`
	GOEXPERIMENT  string            `json:"goexperiment"`
	GOENV         string            `json:"goenv"`
	GOFLAGS       string            `json:"goflags"`
	CGOEnabled    string            `json:"cgo_enabled"`
	Shard         int               `json:"shard"`
	ShardCount    int               `json:"shard_count"`
	Root          executionSummary  `json:"root"`
	NonRoot       *executionSummary `json:"non_root,omitempty"`
}

type shardResult struct {
	scope   string
	summary executionSummary
	failure []byte
	err     error
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, terminationSignal())
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "agent-eval-race:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	opts, err := parseOptions(args)
	if err != nil {
		return err
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return errors.New("hosted race shards require linux/amd64")
	}
	for key, want := range map[string]string{
		"GOTOOLCHAIN": "auto", "GOWORK": "off", "GOENV": "off", "GOFLAGS": "",
		"GOOS": "linux", "GOARCH": "amd64", "GOAMD64": "v1", "GOEXPERIMENT": "", "CGO_ENABLED": "1",
	} {
		if os.Getenv(key) != want {
			return fmt.Errorf("hosted race bootstrap environment does not bind %s", key)
		}
	}

	root, err := os.Getwd()
	if err != nil {
		return errors.New("cannot resolve repository root")
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return errors.New("cannot canonicalize repository root")
	}
	evaluator := filepath.Join(root, "internal", "agenteval")
	goEnv := controlledEnvironment()

	toolchain, source, err := certifySource(ctx, root, evaluator, opts.source, false, goEnv)
	if err != nil {
		return err
	}
	if err := buildProduct(ctx, root, goEnv, stderr); err != nil {
		return err
	}
	if _, _, err := certifySource(ctx, root, evaluator, opts.source, true, goEnv); err != nil {
		return err
	}
	if err := verifyProduct(ctx, root, opts.source, goEnv); err != nil {
		return err
	}

	packages, err := discoverPackages(ctx, evaluator, goEnv)
	if err != nil {
		return err
	}
	rootPackage, nonRoot, err := splitRootPackage(packages)
	if err != nil {
		return err
	}
	rootInventory, err := discoverInventory(ctx, evaluator, []string{"."}, []string{rootPackage}, goEnv)
	if err != nil {
		return err
	}
	partitions, err := partitionInventory(rootInventory, rootPackage)
	if err != nil {
		return err
	}

	selected := inventoryForNames(rootPackage, partitions[opts.shard])
	selector, err := exactSelector(partitions[opts.shard])
	if err != nil {
		return err
	}
	var rootSummary executionSummary
	var nonRootSummary *executionSummary
	if opts.shard == 0 {
		inventory, err := discoverInventory(ctx, evaluator, nonRoot, nonRoot, goEnv)
		if err != nil {
			return err
		}
		results := make(chan shardResult, 2)
		for _, execution := range []struct {
			scope     string
			packages  []string
			selector  string
			inventory testInventory
		}{
			{scope: "root", packages: []string{"."}, selector: selector, inventory: selected},
			{scope: "non-root", packages: nonRoot, inventory: inventory},
		} {
			execution := execution
			go func() {
				var failure bytes.Buffer
				summary, err := executeInventory(ctx, evaluator, execution.packages, execution.selector, execution.inventory, goEnv, &failure)
				results <- shardResult{scope: execution.scope, summary: summary, failure: failure.Bytes(), err: err}
			}()
		}
		var failures []string
		for range 2 {
			result := <-results
			if result.err != nil {
				failures = append(failures, result.scope)
				_, _ = stderr.Write(result.failure)
				continue
			}
			if result.scope == "root" {
				rootSummary = result.summary
			} else {
				summary := result.summary
				nonRootSummary = &summary
			}
		}
		if len(failures) != 0 {
			return errors.New("hosted race shard command failed")
		}
	} else {
		rootSummary, err = executeInventory(ctx, evaluator, []string{"."}, selector, selected, goEnv, stderr)
		if err != nil {
			return err
		}
	}
	rootSummary.InventoryCount = len(rootInventory.Items[rootPackage])
	rootSummary.InventorySHA256 = inventoryDigest(rootInventory)
	if _, _, err := certifySource(ctx, root, evaluator, opts.source, true, goEnv); err != nil {
		return err
	}

	result := evidence{
		SchemaVersion: 1, Source: source, GoVersion: toolchain.GOVersion,
		GOOS: toolchain.GOOS, GOARCH: toolchain.GOARCH, GOAMD64: toolchain.GOAMD64,
		GOEXPERIMENT: toolchain.GOExperiment, GOENV: "off",
		GOFLAGS: toolchain.GOFlags, CGOEnabled: toolchain.CGOEnabled, Shard: opts.shard,
		ShardCount: shardCount, Root: rootSummary, NonRoot: nonRootSummary,
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(result); err != nil {
		return errors.New("cannot emit hosted race evidence")
	}
	return nil
}

func parseOptions(args []string) (options, error) {
	set := flag.NewFlagSet("agent-eval-race", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	shard := set.Int("shard", -1, "closed hosted shard index")
	source := set.String("source", "", "exact workflow source commit")
	if err := set.Parse(args); err != nil || len(set.Args()) != 0 || *shard < 0 || *shard >= shardCount || !commitPattern.MatchString(*source) {
		return options{}, errors.New("expected exact -shard 0..3 and -source commit")
	}
	return options{shard: *shard, source: *source}, nil
}

func controlledEnvironment() []string {
	allowed := map[string]bool{
		"PATH": true, "HOME": true, "XDG_CACHE_HOME": true,
		"TMPDIR": true, "TMP": true, "TEMP": true,
		"LANG": true, "LC_ALL": true, "LC_CTYPE": true,
		"GOCACHE": true, "GOMODCACHE": true, "GOPATH": true,
	}
	env := make([]string, 0, len(allowed)+10)
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if allowed[key] {
			env = append(env, item)
		}
	}
	return append(env, "GOTOOLCHAIN=auto", "GOWORK=off", "GOENV=off", "GOFLAGS=", "GOOS=linux",
		"GOARCH=amd64", "GOAMD64=v1", "GOEXPERIMENT=", "CGO_ENABLED=1")
}

func buildProduct(ctx context.Context, root string, env []string, failure io.Writer) error {
	buildCtx, cancel := context.WithTimeout(ctx, buildTimeout)
	defer cancel()
	stdout := newTailBuffer(maxFailureOutput)
	stderr := newTailBuffer(maxFailureOutput)
	err := runCommand(buildCtx, commandSpec{dir: root, name: "make", args: []string{"build"}, env: env}, stdout, stderr)
	if err == nil && !stdout.overflow && !stderr.overflow {
		return nil
	}
	writeFailureTail(failure, stdout, stderr)
	if buildCtx.Err() != nil {
		return errors.New("product build exceeded its bound")
	}
	return errors.New("product build failed")
}

func verifyProduct(ctx context.Context, root, source string, env []string) error {
	path := filepath.Join(root, "atl")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return errors.New("product build did not produce the expected regular executable")
	}
	verifyCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	stdout, stderr, err := captureCommand(verifyCtx, commandSpec{
		dir: root, name: path, args: []string{"version"},
		env: append(env, "ATL_NO_UPDATE=1"),
	}, 64<<10)
	if err != nil || len(stderr) != 0 {
		return errors.New("cannot verify product build provenance")
	}
	var version struct {
		Commit     string `json:"commit"`
		BuildState string `json:"build_state"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(stdout)))
	if decoder.Decode(&version) != nil || decoder.Decode(new(any)) != io.EOF || version.Commit != source || version.BuildState != "clean" {
		return errors.New("product build provenance does not match the selected source")
	}
	return nil
}

func splitRootPackage(packages []string) (string, []string, error) {
	if len(packages) < 2 {
		return "", nil, errors.New("evaluator race inventory requires root and non-root packages")
	}
	root := ""
	// Derive the root structurally as the unique path that prefixes every other
	// candidate followed by a slash.
	for _, candidate := range packages {
		isRoot := true
		for _, other := range packages {
			if candidate != other && !strings.HasPrefix(other, candidate+"/") {
				isRoot = false
				break
			}
		}
		if isRoot {
			if root != "" {
				return "", nil, errors.New("evaluator package inventory has ambiguous root")
			}
			root = candidate
		}
	}
	if root == "" {
		return "", nil, errors.New("evaluator package inventory has no root")
	}
	nonRoot := make([]string, 0, len(packages)-1)
	for _, pkg := range packages {
		if pkg != root {
			nonRoot = append(nonRoot, pkg)
		}
	}
	return root, nonRoot, nil
}

func exactSelector(names []string) (string, error) {
	if len(names) == 0 {
		return "", errors.New("evaluator race shard is empty")
	}
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = regexp.QuoteMeta(name)
	}
	selector := "^(" + strings.Join(quoted, "|") + ")$"
	if len(selector) > maxSelectorBytes {
		return "", errors.New("evaluator race selector exceeds its reviewed bound")
	}
	return selector, nil
}

func inventoryForNames(pkg string, names []string) testInventory {
	items := make([]testItem, 0, len(names))
	for _, name := range names {
		items = append(items, testItem{Name: name, Kind: itemKind(name)})
	}
	return testInventory{Items: map[string][]testItem{pkg: items}, Packages: []string{pkg}}
}
