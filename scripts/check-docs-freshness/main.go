// Command check-docs-freshness binds the CLI command registry, canonical
// documentation, mutation safety routes, and diff-aware maintainer checks.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/cli"
)

const (
	commandManifestPath = "docs/command-coverage.v1.json"
	impactManifestPath  = "docs/maintainer-impact.v1.json"
	docsCatalogPath     = "docs/catalog.v1.json"
	maxManifestBytes    = 2 << 20
	maxMarkerBytes      = 1 << 20
	maxTrackedFileBytes = 64 << 20
)

var (
	idPattern      = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
	headingPattern = regexp.MustCompile(`^#{1,6} \S`)
)

type commandManifest struct {
	SchemaVersion    int             `json:"schema_version"`
	Routes           []commandRoute  `json:"routes"`
	MutationProfiles []mutationRoute `json:"mutation_profiles"`
}

type commandRoute struct {
	ID             string   `json:"id"`
	Document       string   `json:"document"`
	Evidence       string   `json:"evidence"`
	SafetyDocument string   `json:"safety_document,omitempty"`
	SafetyEvidence string   `json:"safety_evidence,omitempty"`
	Commands       []string `json:"commands"`
}

type mutationRoute struct {
	Profile  string `json:"profile"`
	Document string `json:"document"`
	Evidence string `json:"evidence"`
}

type impactManifest struct {
	SchemaVersion int           `json:"schema_version"`
	Checks        []impactCheck `json:"checks"`
	Rules         []impactRule  `json:"rules"`
}

type impactCheck struct {
	ID         string `json:"id"`
	MakeTarget string `json:"make_target"`
}

type impactRule struct {
	Path   string   `json:"path,omitempty"`
	Prefix string   `json:"prefix,omitempty"`
	Suffix string   `json:"suffix,omitempty"`
	Checks []string `json:"checks"`
}

type docsCatalog struct {
	SchemaVersion int         `json:"schema_version"`
	Documents     []docsEntry `json:"documents"`
}

type docsEntry struct {
	Path  string `json:"path"`
	Lane  string `json:"lane"`
	Topic string `json:"topic"`
}

type markerRegistry struct {
	SchemaVersion int             `json:"schema_version"`
	Markers       []privateMarker `json:"markers"`
}

type privateMarker struct {
	ID        string `json:"id"`
	Category  string `json:"category"`
	MatchType string `json:"match_type"`
	Value     string `json:"value"`
	State     string `json:"state"`
}

type report struct {
	Commands         int
	Routes           int
	MutationProfiles int
	ImpactRules      int
	SelectedChecks   []string
	PrivateMarkers   int
}

type changedPathSet struct {
	Paths      []string
	Historical map[string]bool
}

func main() {
	root := flag.String("root", ".", "repository root")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "check-docs-freshness: unexpected arguments")
		os.Exit(1)
	}
	result, err := validateRepository(*root, os.Getenv("ATL_DOCS_BASE"), os.Getenv("ATL_DOCS_HEAD"), os.Getenv("ATL_PRIVATE_MARKERS_FILE"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "check-docs-freshness:", err)
		os.Exit(1)
	}
	checks := "none"
	if len(result.SelectedChecks) != 0 {
		checks = strings.Join(result.SelectedChecks, ",")
	}
	fmt.Printf("documentation freshness: commands=%d routes=%d mutation_profiles=%d impact_rules=%d selected_checks=%s private_markers=%d\n",
		result.Commands, result.Routes, result.MutationProfiles, result.ImpactRules, checks, result.PrivateMarkers)
}

func validateRepository(root, base, head, markerPath string) (report, error) {
	if strings.TrimSpace(base) == "" && strings.TrimSpace(head) != "" {
		return report{}, errors.New("ATL_DOCS_HEAD requires ATL_DOCS_BASE")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return report{}, errors.New("resolve repository root")
	}
	root = filepath.Clean(root)
	tracked, err := trackedFiles(root)
	if err != nil {
		return report{}, err
	}
	documents, err := loadDocuments(filepath.Join(root, filepath.FromSlash(docsCatalogPath)))
	if err != nil {
		return report{}, err
	}
	commands, err := cli.RepositoryCommandInventory()
	if err != nil {
		return report{}, fmt.Errorf("command inventory: %w", err)
	}
	commandContract, err := loadCommandManifest(filepath.Join(root, filepath.FromSlash(commandManifestPath)))
	if err != nil {
		return report{}, err
	}
	if err := validateCommandCoverage(root, commandContract, commands, documents); err != nil {
		return report{}, err
	}
	impactContract, err := loadImpactManifest(filepath.Join(root, filepath.FromSlash(impactManifestPath)))
	if err != nil {
		return report{}, err
	}
	if err := validateImpactManifest(impactContract, tracked, makeTargets(filepath.Join(root, "Makefile"))); err != nil {
		return report{}, err
	}
	result := report{
		Commands: len(commands), Routes: len(commandContract.Routes),
		MutationProfiles: len(commandContract.MutationProfiles), ImpactRules: len(impactContract.Rules),
	}
	if strings.TrimSpace(base) != "" {
		changed, err := changedFiles(root, base, head)
		if err != nil {
			return result, err
		}
		baseline, err := loadImpactManifestAtRevision(root, strings.TrimSpace(base))
		if err != nil {
			return result, err
		}
		result.SelectedChecks, err = classifyImpact(impactContract, baseline, changed)
		if err != nil {
			return result, err
		}
	}
	if strings.TrimSpace(markerPath) != "" {
		added, diffErr := addedDiffContent(root, base, head)
		if diffErr != nil {
			return result, diffErr
		}
		result.PrivateMarkers, err = scanPrivateMarkers(markerPath, added)
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

func loadCommandManifest(path string) (commandManifest, error) {
	var value commandManifest
	if err := decodeStrictFile(path, maxManifestBytes, &value); err != nil {
		return value, fmt.Errorf("command coverage manifest: %w", err)
	}
	if value.SchemaVersion != 1 || value.Routes == nil || value.MutationProfiles == nil {
		return value, errors.New("command coverage manifest requires schema_version 1 and non-null arrays")
	}
	return value, nil
}

func loadImpactManifest(path string) (impactManifest, error) {
	var value impactManifest
	if err := decodeStrictFile(path, maxManifestBytes, &value); err != nil {
		return value, fmt.Errorf("maintainer impact manifest: %w", err)
	}
	if value.SchemaVersion != 1 || value.Checks == nil || value.Rules == nil {
		return value, errors.New("maintainer impact manifest requires schema_version 1 and non-null arrays")
	}
	return value, nil
}

func loadDocuments(path string) (map[string]docsEntry, error) {
	var value docsCatalog
	body, err := os.ReadFile(path)
	if err != nil || len(body) > maxManifestBytes || json.Unmarshal(body, &value) != nil || value.SchemaVersion != 1 {
		return nil, errors.New("documentation catalog projection")
	}
	documents := make(map[string]docsEntry, len(value.Documents))
	for _, entry := range value.Documents {
		documents[entry.Path] = entry
	}
	return documents, nil
}

func validateCommandCoverage(root string, manifest commandManifest, commands []cli.RepositoryCommand, documents map[string]docsEntry) error {
	want := make(map[string]cli.RepositoryCommand, len(commands))
	wantProfiles := map[string]bool{}
	for _, command := range commands {
		want[command.Path] = command
		if command.Access == "mutating" {
			if command.MutationProfile == "" {
				return fmt.Errorf("mutating command %q has no profile", command.Path)
			}
			wantProfiles[command.MutationProfile] = true
		}
	}
	seenCommands := map[string]string{}
	seenIDs := map[string]bool{}
	previousID := ""
	for _, route := range manifest.Routes {
		if !idPattern.MatchString(route.ID) || route.ID <= previousID || seenIDs[route.ID] {
			return errors.New("command routes require unique sorted ids")
		}
		previousID = route.ID
		seenIDs[route.ID] = true
		if len(route.Commands) == 0 || !canonicalDocumentRoute(route.Document, documents, "reference") {
			return fmt.Errorf("command route %q has an empty command list or invalid document", route.ID)
		}
		if err := requireEvidence(root, route.Document, route.Evidence); err != nil {
			return fmt.Errorf("command route %q: %w", route.ID, err)
		}
		mutating := false
		previousCommand := ""
		for _, path := range route.Commands {
			if path <= previousCommand {
				return fmt.Errorf("command route %q commands are unsorted or duplicated", route.ID)
			}
			previousCommand = path
			if _, ok := want[path]; !ok {
				return fmt.Errorf("command route %q contains stale command %q", route.ID, path)
			}
			if prior := seenCommands[path]; prior != "" {
				return fmt.Errorf("command %q is covered by routes %q and %q", path, prior, route.ID)
			}
			seenCommands[path] = route.ID
			mutating = mutating || want[path].Access == "mutating"
		}
		if mutating {
			if !canonicalSafetyRoute(route.SafetyDocument, documents) {
				return fmt.Errorf("mutating command route %q requires a canonical safety document", route.ID)
			}
			if err := requireEvidence(root, route.SafetyDocument, route.SafetyEvidence); err != nil {
				return fmt.Errorf("mutating command route %q safety evidence: %w", route.ID, err)
			}
		} else if route.SafetyDocument != "" || route.SafetyEvidence != "" {
			return fmt.Errorf("read-only command route %q must not declare mutation safety evidence", route.ID)
		}
	}
	var missing []string
	for path := range want {
		if seenCommands[path] == "" {
			missing = append(missing, path)
		}
	}
	if len(missing) != 0 {
		sort.Strings(missing)
		return fmt.Errorf("executable command documentation is incomplete: %s", strings.Join(missing, ", "))
	}
	seenProfiles := map[string]bool{}
	previousProfile := ""
	for _, route := range manifest.MutationProfiles {
		if route.Profile <= previousProfile || seenProfiles[route.Profile] || !wantProfiles[route.Profile] {
			return fmt.Errorf("mutation safety route %q is stale, duplicated, or unsorted", route.Profile)
		}
		previousProfile = route.Profile
		seenProfiles[route.Profile] = true
		if !canonicalSafetyRoute(route.Document, documents) {
			return fmt.Errorf("mutation safety route %q has an invalid document", route.Profile)
		}
		if err := requireEvidence(root, route.Document, route.Evidence); err != nil {
			return fmt.Errorf("mutation safety route %q: %w", route.Profile, err)
		}
	}
	for profile := range wantProfiles {
		if !seenProfiles[profile] {
			return fmt.Errorf("mutation profile %q has no documented safety route", profile)
		}
	}
	return nil
}

func canonicalDocumentRoute(path string, documents map[string]docsEntry, requiredLane string) bool {
	entry, ok := documents[path]
	return ok && canonicalRelative(path) && strings.HasPrefix(path, "docs/") &&
		(requiredLane == "" || entry.Lane == requiredLane)
}

func canonicalSafetyRoute(path string, documents map[string]docsEntry) bool {
	entry, ok := documents[path]
	return ok && canonicalDocumentRoute(path, documents, "") &&
		(entry.Topic == "safe-writes" || entry.Topic == "jira-guarded-writeback")
}

func requireEvidence(root, document, evidence string) error {
	if strings.TrimSpace(evidence) != evidence || len(evidence) < 4 || len(evidence) > 300 ||
		strings.ContainsAny(evidence, "\r\n") || !headingPattern.MatchString(evidence) {
		return errors.New("evidence must be one trimmed bounded Markdown heading")
	}
	body, err := readRegular(filepath.Join(root, filepath.FromSlash(document)), maxTrackedFileBytes)
	if err != nil {
		return errors.New("document is unavailable")
	}
	count := 0
	for _, line := range strings.Split(string(body), "\n") {
		if line == evidence {
			count++
		}
	}
	if count != 1 {
		return fmt.Errorf("evidence line occurs %d times", count)
	}
	return nil
}

func validateImpactManifest(manifest impactManifest, tracked []string, makeTargets map[string]bool) error {
	if len(manifest.Checks) == 0 || len(manifest.Rules) == 0 {
		return errors.New("maintainer impact manifest is empty")
	}
	checks := map[string]bool{}
	previous := ""
	for _, check := range manifest.Checks {
		if !idPattern.MatchString(check.ID) || check.ID <= previous || checks[check.ID] || !makeTargets[check.MakeTarget] {
			return fmt.Errorf("impact check %q is stale, duplicated, unsorted, or lacks a Make target", check.ID)
		}
		previous = check.ID
		checks[check.ID] = true
	}
	previous = ""
	for _, rule := range manifest.Rules {
		key, err := impactRuleKey(rule)
		if err != nil || key <= previous {
			return errors.New("impact rules require valid unique sorted selectors")
		}
		previous = key
		if len(rule.Checks) == 0 {
			return fmt.Errorf("impact rule %q has no checks", key)
		}
		priorCheck := ""
		for _, check := range rule.Checks {
			if check <= priorCheck || !checks[check] {
				return fmt.Errorf("impact rule %q has a stale, duplicated, or unsorted check", key)
			}
			priorCheck = check
		}
		matched := false
		for _, path := range tracked {
			matched = matched || impactRuleMatches(rule, path)
		}
		if !matched {
			return fmt.Errorf("impact rule %q matches no tracked path", key)
		}
	}
	for _, path := range tracked {
		matched := false
		for _, rule := range manifest.Rules {
			if impactRuleMatches(rule, path) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("tracked path %q has no maintainer impact classification", path)
		}
	}
	return nil
}

func impactRuleKey(rule impactRule) (string, error) {
	count := 0
	value, kind := "", ""
	for _, candidate := range []struct{ kind, value string }{{"path", rule.Path}, {"prefix", rule.Prefix}, {"suffix", rule.Suffix}} {
		if candidate.value != "" {
			count++
			kind, value = candidate.kind, candidate.value
		}
	}
	if count != 1 || strings.TrimSpace(value) != value || value == "" || strings.Contains(value, "\\") {
		return "", errors.New("exactly one canonical selector is required")
	}
	if kind != "suffix" && !canonicalRelative(strings.TrimSuffix(value, "/")) {
		return "", errors.New("path selector is noncanonical")
	}
	if (kind == "prefix" && !strings.HasSuffix(value, "/")) || (kind == "suffix" && !strings.HasPrefix(value, ".")) {
		return "", errors.New("prefix or suffix selector is malformed")
	}
	return kind + ":" + value, nil
}

func impactRuleMatches(rule impactRule, path string) bool {
	switch {
	case rule.Path != "":
		return path == rule.Path
	case rule.Prefix != "":
		return strings.HasPrefix(path, rule.Prefix)
	default:
		return strings.HasSuffix(path, rule.Suffix)
	}
}

func classifyImpact(manifest impactManifest, baseline *impactManifest, changed changedPathSet) ([]string, error) {
	selected := map[string]bool{}
	currentChecks := map[string]bool{}
	for _, check := range manifest.Checks {
		currentChecks[check.ID] = true
	}
	for _, path := range changed.Paths {
		checks := impactChecksForPath(manifest.Rules, path)
		if changed.Historical[path] && baseline != nil {
			for check := range impactChecksForPath(baseline.Rules, path) {
				checks[check] = true
			}
		}
		if len(checks) == 0 {
			return nil, fmt.Errorf("changed path %q has no maintainer impact classification", path)
		}
		for check := range checks {
			if !currentChecks[check] {
				return nil, fmt.Errorf("historical impact for changed path %q requires unavailable check %q", path, check)
			}
			selected[check] = true
		}
	}
	checks := make([]string, 0, len(selected))
	for check := range selected {
		checks = append(checks, check)
	}
	sort.Strings(checks)
	return checks, nil
}

func impactChecksForPath(rules []impactRule, path string) map[string]bool {
	checks := map[string]bool{}
	for _, rule := range rules {
		if impactRuleMatches(rule, path) {
			for _, check := range rule.Checks {
				checks[check] = true
			}
		}
	}
	return checks
}

func scanPrivateMarkers(markerPath string, content []byte) (int, error) {
	var registry markerRegistry
	if err := decodePermissiveFile(markerPath, maxMarkerBytes, &registry); err != nil || registry.SchemaVersion != 1 || registry.Markers == nil {
		return 0, errors.New("private marker registry is invalid")
	}
	type compiledMarker struct {
		literal []byte
		pattern *regexp.Regexp
	}
	var active []compiledMarker
	seen := map[string]bool{}
	allowedCategories := map[string]bool{
		"email": true, "fixture": true, "host": true, "organization": true,
		"other": true, "path": true, "target": true, "title": true,
	}
	for _, marker := range registry.Markers {
		if !idPattern.MatchString(marker.ID) || seen[marker.ID] || marker.Value == "" || len(marker.Value) > 4096 ||
			!allowedCategories[marker.Category] || (marker.MatchType != "literal" && marker.MatchType != "regexp") ||
			(marker.State != "active" && marker.State != "retired") {
			return 0, errors.New("private marker registry entry is invalid")
		}
		seen[marker.ID] = true
		if marker.State == "retired" {
			continue
		}
		compiled := compiledMarker{}
		if marker.MatchType == "literal" {
			compiled.literal = []byte(marker.Value)
		} else {
			pattern, err := regexp.Compile(marker.Value)
			if err != nil {
				return 0, errors.New("private marker regexp is invalid")
			}
			compiled.pattern = pattern
		}
		active = append(active, compiled)
	}
	for _, marker := range active {
		matched := (marker.pattern != nil && marker.pattern.Match(content)) ||
			(marker.pattern == nil && bytes.Contains(content, marker.literal))
		if matched {
			return 0, errors.New("private marker matched added public diff content")
		}
	}
	return len(active), nil
}

// addedDiffContent returns only added lines from a zero-context diff. When a
// base is configured it inspects base..head (or base..working-tree); otherwise
// it combines staged and unstaged local diffs. Marker values and matching lines
// never enter diagnostics.
func addedDiffContent(root, base, head string) ([]byte, error) {
	var outputs [][]byte
	if strings.TrimSpace(base) != "" {
		base = strings.TrimSpace(base)
		head = strings.TrimSpace(head)
		for _, ref := range []string{base, head} {
			if ref == "" {
				continue
			}
			if err := exec.Command("git", "-C", root, "rev-parse", "--verify", ref+"^{commit}").Run(); err != nil {
				return nil, errors.New("private scan diff reference is unavailable")
			}
		}
		arguments := []string{"-C", root, "diff", "--unified=0", "--no-color", "--no-ext-diff", "--no-textconv", "--text", base}
		if head != "" {
			arguments = append(arguments, head)
		}
		arguments = append(arguments, "--")
		output, err := exec.Command("git", arguments...).Output()
		if err != nil {
			return nil, errors.New("read public diff for private marker scan")
		}
		outputs = append(outputs, output)
	} else {
		for _, arguments := range [][]string{
			{"-C", root, "diff", "--cached", "--unified=0", "--no-color", "--no-ext-diff", "--no-textconv", "--text", "--"},
			{"-C", root, "diff", "--unified=0", "--no-color", "--no-ext-diff", "--no-textconv", "--text", "--"},
		} {
			output, err := exec.Command("git", arguments...).Output()
			if err != nil {
				return nil, errors.New("read local public diff for private marker scan")
			}
			outputs = append(outputs, output)
		}
	}
	var added bytes.Buffer
	for _, output := range outputs {
		appendAddedLines(&added, output)
	}
	if strings.TrimSpace(head) == "" {
		output, err := exec.Command("git", "-C", root, "ls-files", "--others", "--exclude-standard", "-z").Output()
		if err != nil {
			return nil, errors.New("enumerate untracked public content for private marker scan")
		}
		for _, path := range splitNULPaths(output) {
			body, err := readRegular(filepath.Join(root, filepath.FromSlash(path)), maxTrackedFileBytes)
			if err != nil {
				return nil, errors.New("read untracked public content for private marker scan")
			}
			added.Write(body)
			added.WriteByte('\n')
		}
	}
	return added.Bytes(), nil
}

func appendAddedLines(destination *bytes.Buffer, diff []byte) {
	inHunk := false
	for _, line := range bytes.Split(diff, []byte{'\n'}) {
		switch {
		case bytes.HasPrefix(line, []byte("diff --git ")):
			inHunk = false
		case bytes.HasPrefix(line, []byte("@@ ")):
			inHunk = true
		case inHunk && len(line) > 0 && line[0] == '+':
			destination.Write(line[1:])
			destination.WriteByte('\n')
		}
	}
}

func trackedFiles(root string) ([]string, error) {
	command := exec.Command("git", "-C", root, "ls-files", "-z")
	output, err := command.Output()
	if err != nil {
		return nil, errors.New("enumerate tracked files")
	}
	return splitNULPaths(output), nil
}

func changedFiles(root, base, head string) (changedPathSet, error) {
	base = strings.TrimSpace(base)
	head = strings.TrimSpace(head)
	if base == "" {
		return changedPathSet{}, errors.New("empty diff base")
	}
	for _, ref := range []string{base, head} {
		if ref == "" {
			continue
		}
		command := exec.Command("git", "-C", root, "rev-parse", "--verify", ref+"^{commit}")
		if err := command.Run(); err != nil {
			return changedPathSet{}, errors.New("diff reference is unavailable")
		}
	}
	arguments := []string{"-C", root, "diff", "--name-status", "-z", "--find-renames", "--find-copies", base}
	if head != "" {
		arguments = append(arguments, head)
	}
	arguments = append(arguments, "--")
	output, err := exec.Command("git", arguments...).Output()
	if err != nil {
		return changedPathSet{}, errors.New("enumerate changed files")
	}
	changed, err := parseNameStatus(output)
	if err != nil {
		return changedPathSet{}, errors.New("parse changed files")
	}
	if head == "" {
		untracked, err := exec.Command("git", "-C", root, "ls-files", "--others", "--exclude-standard", "-z").Output()
		if err != nil {
			return changedPathSet{}, errors.New("enumerate untracked changed files")
		}
		seen := map[string]bool{}
		for _, path := range changed.Paths {
			seen[path] = true
		}
		for _, path := range splitNULPaths(untracked) {
			if !seen[path] {
				changed.Paths = append(changed.Paths, path)
				seen[path] = true
			}
		}
		sort.Strings(changed.Paths)
	}
	return changed, nil
}

func parseNameStatus(output []byte) (changedPathSet, error) {
	parts := bytes.Split(output, []byte{0})
	if len(parts) > 0 && len(parts[len(parts)-1]) == 0 {
		parts = parts[:len(parts)-1]
	}
	paths := map[string]bool{}
	historical := map[string]bool{}
	for index := 0; index < len(parts); {
		status := string(parts[index])
		index++
		if status == "" {
			return changedPathSet{}, errors.New("empty change status")
		}
		count := 1
		if status[0] == 'R' || status[0] == 'C' {
			count = 2
		} else if !strings.ContainsRune("ADMRTUXB", rune(status[0])) {
			return changedPathSet{}, errors.New("unsupported change status")
		}
		if len(parts)-index < count {
			return changedPathSet{}, errors.New("truncated change status")
		}
		for offset := 0; offset < count; offset++ {
			path := filepath.ToSlash(string(parts[index+offset]))
			if path == "" {
				return changedPathSet{}, errors.New("empty changed path")
			}
			paths[path] = true
			if status[0] == 'D' || status[0] == 'R' && offset == 0 {
				historical[path] = true
			}
		}
		index += count
	}
	result := changedPathSet{Historical: historical}
	for path := range paths {
		result.Paths = append(result.Paths, path)
	}
	sort.Strings(result.Paths)
	return result, nil
}

func loadImpactManifestAtRevision(root, revision string) (*impactManifest, error) {
	command := exec.Command("git", "-C", root, "show", revision+":"+impactManifestPath)
	body, err := command.Output()
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			return nil, nil
		}
		return nil, errors.New("read baseline maintainer impact manifest")
	}
	var manifest impactManifest
	if len(body) > maxManifestBytes || decodeStrict(bytes.NewReader(body), &manifest) != nil ||
		manifest.SchemaVersion != 1 || manifest.Checks == nil || manifest.Rules == nil {
		return nil, errors.New("baseline maintainer impact manifest is invalid")
	}
	return &manifest, nil
}

func splitNULPaths(output []byte) []string {
	var paths []string
	for _, part := range bytes.Split(output, []byte{0}) {
		if len(part) != 0 {
			paths = append(paths, filepath.ToSlash(string(part)))
		}
	}
	sort.Strings(paths)
	return paths
}

func makeTargets(path string) map[string]bool {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	targets := map[string]bool{}
	for _, line := range strings.Split(string(body), "\n") {
		if line == "" || strings.HasPrefix(line, "\t") || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		separator := strings.IndexByte(line, ':')
		if separator <= 0 || strings.ContainsAny(line[:separator], "=$%") {
			continue
		}
		for _, target := range strings.Fields(line[:separator]) {
			targets[target] = true
		}
	}
	return targets
}

func decodeStrictFile(path string, limit int64, destination any) error {
	body, err := readRegular(path, limit)
	if err != nil {
		return err
	}
	return decodeStrict(bytes.NewReader(body), destination)
}

func decodeStrict(reader io.Reader, destination any) error {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func decodePermissiveFile(path string, limit int64, destination any) error {
	body, err := readRegular(path, limit)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, destination)
}

func readRegular(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > limit {
		return nil, errors.New("missing, non-regular, symlink, or oversized file")
	}
	return os.ReadFile(path)
}

func canonicalRelative(path string) bool {
	return path != "" && !filepath.IsAbs(path) && !strings.Contains(path, "\\") &&
		path == filepath.ToSlash(filepath.Clean(filepath.FromSlash(path))) && path != "." && path != ".." && !strings.HasPrefix(path, "../")
}
