// Command check-maintainability enforces reviewed production growth ratchets.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const manifestPath = "docs/maintainability-ratchets.v1.json"

var revisionPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

type manifest struct {
	SchemaVersion int                `json:"schema_version"`
	Owners        []owner            `json:"owners"`
	Hotspots      []hotspot          `json:"hotspots"`
	PackageTotals []packageTotal     `json:"package_totals"`
	Timing        timingObservations `json:"timing"`
}

type owner struct {
	ID           string   `json:"id"`
	PathPrefixes []string `json:"path_prefixes"`
}

type hotspot struct {
	Owner     string `json:"owner"`
	Path      string `json:"path"`
	Function  string `json:"function,omitempty"`
	MaxLines  int    `json:"max_lines"`
	Rationale string `json:"rationale"`
}

type packageTotal struct {
	Owner     string `json:"owner"`
	Path      string `json:"path"`
	MaxLines  int    `json:"max_lines"`
	Rationale string `json:"rationale"`
}

type timingObservations struct {
	Mode         string              `json:"mode"`
	Observations []timingObservation `json:"observations"`
}

type timingObservation struct {
	MakeTarget      string `json:"make_target"`
	Source          string `json:"source"`
	WorkflowRunID   int64  `json:"workflow_run_id"`
	Revision        string `json:"revision"`
	ObservedAt      string `json:"observed_at"`
	DurationSeconds int    `json:"duration_seconds"`
	Rationale       string `json:"rationale"`
}

type measurement struct {
	Owner    string `json:"owner"`
	Path     string `json:"path"`
	Function string `json:"function,omitempty"`
	Lines    int    `json:"lines"`
	Maximum  int    `json:"maximum"`
}

type report struct {
	Status             string        `json:"status"`
	Measurements       []measurement `json:"measurements"`
	TimingMode         string        `json:"timing_mode"`
	TimingObservations int           `json:"timing_observations"`
}

var reviewedOwners = []owner{
	{ID: "app", PathPrefixes: []string{"internal/app/"}},
	{ID: "cli", PathPrefixes: []string{"internal/cli/"}},
	{ID: "evaluator", PathPrefixes: []string{"internal/agenteval/"}},
	{ID: "mcp", PathPrefixes: []string{"internal/mcpserver/"}},
}

func main() {
	if err := run(".", os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "check-maintainability:", err)
		os.Exit(1)
	}
}

func run(root string, output io.Writer) error {
	m, err := readManifest(filepath.Join(root, manifestPath))
	if err != nil {
		return err
	}
	measurements, err := validateManifest(root, m)
	if err != nil {
		return err
	}
	report := report{
		Status:             "ok",
		Measurements:       measurements,
		TimingMode:         m.Timing.Mode,
		TimingObservations: len(m.Timing.Observations),
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}

func readManifest(path string) (manifest, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return manifest{}, fmt.Errorf("read manifest: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	var m manifest
	if err := decoder.Decode(&m); err != nil {
		return manifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return manifest{}, errors.New("decode manifest: trailing JSON value")
	}
	return m, nil
}

func validateManifest(root string, m manifest) ([]measurement, error) {
	if m.SchemaVersion != 1 {
		return nil, fmt.Errorf("schema_version = %d, want 1", m.SchemaVersion)
	}
	if !ownersEqual(m.Owners, reviewedOwners) {
		return nil, errors.New("owners must retain the reviewed sorted owner/path mapping")
	}
	if len(m.Hotspots) == 0 {
		return nil, errors.New("hotspots must not be empty")
	}
	if !sort.SliceIsSorted(m.Hotspots, func(i, j int) bool { return hotspotKey(m.Hotspots[i]) < hotspotKey(m.Hotspots[j]) }) {
		return nil, errors.New("hotspots must be sorted by owner, path, and function")
	}
	if len(m.PackageTotals) != len(reviewedOwners) {
		return nil, fmt.Errorf("package_totals must contain one row for each of the %d reviewed owners", len(reviewedOwners))
	}
	if !sort.SliceIsSorted(m.PackageTotals, func(i, j int) bool {
		return m.PackageTotals[i].Owner+"\x00"+m.PackageTotals[i].Path < m.PackageTotals[j].Owner+"\x00"+m.PackageTotals[j].Path
	}) {
		return nil, errors.New("package_totals must be sorted by owner and path")
	}

	ownerKinds := make(map[string]map[string]bool, len(reviewedOwners))
	seen := make(map[string]bool, len(m.Hotspots))
	parsed := make(map[string]*parsedGoFile)
	measurements := make([]measurement, 0, len(m.Hotspots))
	for _, item := range m.Hotspots {
		key := hotspotKey(item)
		if seen[key] {
			return nil, fmt.Errorf("duplicate hotspot %q", key)
		}
		seen[key] = true
		if strings.TrimSpace(item.Rationale) == "" {
			return nil, fmt.Errorf("hotspot %q has an empty rationale", key)
		}
		if item.MaxLines <= 0 {
			return nil, fmt.Errorf("hotspot %q max_lines must be positive", key)
		}
		prefixes, ok := ownerPrefixes(item.Owner)
		if !ok || !hasAnyPrefix(item.Path, prefixes) {
			return nil, fmt.Errorf("hotspot %q has an invalid owner/path mapping", key)
		}
		file, ok := parsed[item.Path]
		if !ok {
			var err error
			file, err = parseProductionGoFile(root, item.Path)
			if err != nil {
				return nil, fmt.Errorf("hotspot %q: %w", key, err)
			}
			parsed[item.Path] = file
		}
		lines := file.lines
		kind := "file"
		if item.Function != "" {
			kind = "function"
			var found bool
			lines, found = file.functions[item.Function]
			if !found {
				return nil, fmt.Errorf("hotspot %q function was not found", key)
			}
		}
		if lines > item.MaxLines {
			return nil, fmt.Errorf("hotspot %q is %d lines, exceeds reviewed maximum %d", key, lines, item.MaxLines)
		}
		if ownerKinds[item.Owner] == nil {
			ownerKinds[item.Owner] = make(map[string]bool)
		}
		ownerKinds[item.Owner][kind] = true
		measurements = append(measurements, measurement{Owner: item.Owner, Path: item.Path, Function: item.Function, Lines: lines, Maximum: item.MaxLines})
	}
	for _, owner := range reviewedOwners {
		if !ownerKinds[owner.ID]["file"] || !ownerKinds[owner.ID]["function"] {
			return nil, fmt.Errorf("owner %q must have selected file and function ratchets", owner.ID)
		}
	}
	seenPackageOwners := make(map[string]bool, len(m.PackageTotals))
	for _, item := range m.PackageTotals {
		key := item.Owner + ":" + item.Path
		prefixes, ok := ownerPrefixes(item.Owner)
		if !ok || !hasAnyPrefix(item.Path, prefixes) {
			return nil, fmt.Errorf("package total %q has an invalid owner/path mapping", key)
		}
		if seenPackageOwners[item.Owner] {
			return nil, fmt.Errorf("package total owner %q is duplicated", item.Owner)
		}
		seenPackageOwners[item.Owner] = true
		if strings.TrimSpace(item.Rationale) == "" || item.MaxLines <= 0 {
			return nil, fmt.Errorf("package total %q must have a positive maximum and rationale", key)
		}
		lines, err := countProductionGoLines(root, item.Path)
		if err != nil {
			return nil, fmt.Errorf("package total %q: %w", key, err)
		}
		if lines > item.MaxLines {
			return nil, fmt.Errorf("package total %q is %d lines, exceeds reviewed maximum %d", key, lines, item.MaxLines)
		}
		measurements = append(measurements, measurement{Owner: item.Owner, Path: item.Path, Lines: lines, Maximum: item.MaxLines})
	}
	if err := validateTiming(root, m.Timing); err != nil {
		return nil, err
	}
	return measurements, nil
}

func countProductionGoLines(root, directory string) (int, error) {
	clean := filepath.Clean(directory)
	if directory == "" || filepath.IsAbs(directory) || clean == "." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return 0, errors.New("path must be a clean repository-relative directory")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return 0, err
	}
	absDirectory := filepath.Join(absRoot, filepath.FromSlash(clean))
	if err := rejectSymlinkPath(absRoot, absDirectory); err != nil {
		return 0, err
	}
	total := 0
	err = filepath.WalkDir(absDirectory, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("symlinked paths are not allowed")
		}
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" || strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		relative, err := filepath.Rel(absRoot, path)
		if err != nil {
			return err
		}
		parsed, err := parseProductionGoFile(root, filepath.ToSlash(relative))
		if err != nil {
			return err
		}
		total += parsed.lines
		return nil
	})
	return total, err
}

type parsedGoFile struct {
	lines     int
	functions map[string]int
}

func parseProductionGoFile(root, path string) (*parsedGoFile, error) {
	if path == "" || filepath.IsAbs(path) || filepath.Clean(path) != path || strings.HasPrefix(path, ".."+string(filepath.Separator)) {
		return nil, errors.New("path must be a clean repository-relative path")
	}
	if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
		return nil, errors.New("path must name a production Go file")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve root: %w", err)
	}
	absPath := filepath.Join(absRoot, filepath.FromSlash(path))
	if err := rejectSymlinkPath(absRoot, absPath); err != nil {
		return nil, err
	}
	info, err := os.Lstat(absPath)
	if err != nil {
		return nil, fmt.Errorf("inspect path: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("path must name a regular file")
	}
	contents, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("read path: %w", err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, absPath, contents, 0)
	if err != nil {
		return nil, fmt.Errorf("parse Go file: %w", err)
	}
	functions := make(map[string]int)
	for _, declaration := range file.Decls {
		fn, ok := declaration.(*ast.FuncDecl)
		if !ok {
			continue
		}
		name, err := functionName(fset, fn)
		if err != nil {
			return nil, err
		}
		if _, duplicate := functions[name]; duplicate {
			return nil, fmt.Errorf("duplicate function identity %q", name)
		}
		functions[name] = fset.Position(fn.End()).Line - fset.Position(fn.Pos()).Line + 1
	}
	return &parsedGoFile{lines: physicalLines(contents), functions: functions}, nil
}

func functionName(fset *token.FileSet, fn *ast.FuncDecl) (string, error) {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name, nil
	}
	var receiver bytes.Buffer
	if err := format.Node(&receiver, fset, fn.Recv.List[0].Type); err != nil {
		return "", fmt.Errorf("format receiver for %s: %w", fn.Name.Name, err)
	}
	receiverName := receiver.String()
	receiverName = "(" + receiverName + ")"
	return receiverName + "." + fn.Name.Name, nil
}

func physicalLines(contents []byte) int {
	if len(contents) == 0 {
		return 0
	}
	lines := bytes.Count(contents, []byte{'\n'})
	if contents[len(contents)-1] != '\n' {
		lines++
	}
	return lines
}

func rejectSymlinkPath(root, path string) error {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("path escapes repository root")
	}
	current := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("inspect path component: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("symlinked paths are not allowed")
		}
	}
	return nil
}

func validateTiming(root string, timing timingObservations) error {
	if timing.Mode != "observe" {
		return errors.New("timing mode must remain observe-only")
	}
	if len(timing.Observations) == 0 {
		return errors.New("timing observations must not be empty")
	}
	if !sort.SliceIsSorted(timing.Observations, func(i, j int) bool {
		return observationKey(timing.Observations[i]) < observationKey(timing.Observations[j])
	}) {
		return errors.New("timing observations must be sorted by target and observation time")
	}
	makeTargets, err := readMakeTargets(filepath.Join(root, "Makefile"))
	if err != nil {
		return err
	}
	required := map[string]bool{"agent-eval-race": false, "check-core-race-coverage": false}
	seen := make(map[string]bool)
	for _, observation := range timing.Observations {
		key := observationKey(observation)
		if seen[key] {
			return fmt.Errorf("duplicate timing observation %q", key)
		}
		seen[key] = true
		if !makeTargets[observation.MakeTarget] {
			return fmt.Errorf("timing observation %q references a missing Make target", key)
		}
		if observation.Source != "github_actions_ubuntu_step" || observation.WorkflowRunID <= 0 {
			return fmt.Errorf("timing observation %q has invalid hosted source evidence", key)
		}
		if !revisionPattern.MatchString(observation.Revision) {
			return fmt.Errorf("timing observation %q has an invalid revision", key)
		}
		if _, err := time.Parse(time.RFC3339, observation.ObservedAt); err != nil {
			return fmt.Errorf("timing observation %q has an invalid observed_at", key)
		}
		if observation.DurationSeconds <= 0 || strings.TrimSpace(observation.Rationale) == "" {
			return fmt.Errorf("timing observation %q requires a positive duration and rationale", key)
		}
		if _, ok := required[observation.MakeTarget]; ok {
			required[observation.MakeTarget] = true
		}
	}
	for target, present := range required {
		if !present {
			return fmt.Errorf("timing observations must include %q", target)
		}
	}
	return nil
}

func readMakeTargets(path string) (map[string]bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read Makefile: %w", err)
	}
	defer file.Close()
	targets := make(map[string]bool)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, "\t") || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		colon := strings.IndexByte(line, ':')
		if colon <= 0 || strings.Contains(line[:colon], "=") {
			continue
		}
		for _, target := range strings.Fields(line[:colon]) {
			if !strings.HasPrefix(target, ".") && !strings.ContainsAny(target, "%$()") {
				targets[target] = true
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read Makefile: %w", err)
	}
	return targets, nil
}

func ownersEqual(actual, expected []owner) bool {
	if len(actual) != len(expected) {
		return false
	}
	for i := range actual {
		if actual[i].ID != expected[i].ID || strings.Join(actual[i].PathPrefixes, "\x00") != strings.Join(expected[i].PathPrefixes, "\x00") {
			return false
		}
	}
	return true
}

func ownerPrefixes(id string) ([]string, bool) {
	for _, owner := range reviewedOwners {
		if owner.ID == id {
			return owner.PathPrefixes, true
		}
	}
	return nil, false
}

func hasAnyPrefix(path string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func hotspotKey(item hotspot) string {
	return item.Owner + "\x00" + item.Path + "\x00" + item.Function
}

func observationKey(item timingObservation) string {
	return item.MakeTarget + "\x00" + item.ObservedAt
}
