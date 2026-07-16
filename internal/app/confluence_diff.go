package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/fragment"
	"github.com/isukharev/atl/internal/mirror"
)

const confluenceDiffSchemaVersion = 1

// ConfluenceDiffResult is a deterministic, offline comparison between the
// pristine native body saved by pull and the current native mirror body.
type ConfluenceDiffResult struct {
	SchemaVersion int                   `json:"schema_version"`
	Root          string                `json:"root"`
	Target        string                `json:"target"`
	Complete      bool                  `json:"complete"`
	Summary       ConfluenceDiffSummary `json:"summary"`
	Pages         []ConfluencePageDiff  `json:"pages"`
}

type ConfluenceDiffSummary struct {
	Total            int `json:"total"`
	Unchanged        int `json:"unchanged"`
	Added            int `json:"added"`
	Removed          int `json:"removed"`
	Modified         int `json:"modified"`
	Malformed        int `json:"malformed"`
	MissingBaseline  int `json:"missing_baseline"`
	BaselineMismatch int `json:"baseline_mismatch,omitempty"`
	Unreadable       int `json:"unreadable"`
}

type ConfluencePageDiff struct {
	ID              string                   `json:"id,omitempty"`
	Title           string                   `json:"title,omitempty"`
	Path            string                   `json:"path"`
	State           string                   `json:"state"`
	SemanticChanged bool                     `json:"semantic_changed,omitempty"`
	ByteOnly        bool                     `json:"byte_only,omitempty"`
	Baseline        ConfluenceDiffSide       `json:"baseline"`
	Candidate       ConfluenceDiffSide       `json:"candidate"`
	Blocks          []ConfluenceBlockChange  `json:"blocks,omitempty"`
	Features        []ConfluenceFeatureDelta `json:"features,omitempty"`
	ByteEvidence    *ConfluenceByteEvidence  `json:"byte_evidence,omitempty"`
}

type ConfluenceDiffSide struct {
	Present  bool          `json:"present"`
	Bytes    int           `json:"bytes,omitempty"`
	SHA256   string        `json:"sha256,omitempty"`
	Valid    bool          `json:"valid"`
	Problems []csf.Problem `json:"problems,omitempty"`
}

type ConfluenceBlockChange struct {
	Change          string `json:"change"`
	Kind            string `json:"kind"`
	BaselineIndex   *int   `json:"baseline_index,omitempty"`
	CandidateIndex  *int   `json:"candidate_index,omitempty"`
	BaselineSHA256  string `json:"baseline_sha256,omitempty"`
	CandidateSHA256 string `json:"candidate_sha256,omitempty"`
}

type ConfluenceFeatureDelta struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Baseline  int    `json:"baseline"`
	Candidate int    `json:"candidate"`
}

type ConfluenceByteEvidence struct {
	CommonPrefixBytes      int    `json:"common_prefix_bytes"`
	CommonSuffixBytes      int    `json:"common_suffix_bytes"`
	BaselineChangedBytes   int    `json:"baseline_changed_bytes"`
	CandidateChangedBytes  int    `json:"candidate_changed_bytes"`
	BaselineChangedSHA256  string `json:"baseline_changed_sha256"`
	CandidateChangedSHA256 string `json:"candidate_changed_sha256"`
}

type confluenceDiffTarget struct {
	path  string
	state *mirror.SyncState
}

// DiffConfluenceMirror compares one page or a directory subtree without config,
// credentials, or backend access.
func DiffConfluenceMirror(target, into string) (*ConfluenceDiffResult, error) {
	root, target, err := canonicalConfluenceDiffPaths(target, into)
	if err != nil {
		return nil, err
	}
	m := mirror.New(root)
	targets, err := confluenceDiffTargets(m, target)
	if err != nil {
		return nil, err
	}
	res := &ConfluenceDiffResult{
		SchemaVersion: confluenceDiffSchemaVersion,
		Root:          root, Target: target, Complete: true, Pages: []ConfluencePageDiff{},
	}
	var worst error
	for _, target := range targets {
		page, pageErr := confluenceDiffPage(m, target)
		res.Pages = append(res.Pages, page)
		if pageErr != nil {
			res.Complete = false
			worst = moreSevereErr(worst, pageErr)
		}
		res.Summary.add(page.State)
		if page.State == "missing_baseline" || page.State == "baseline_mismatch" || page.State == "malformed" {
			res.Complete = false
		}
	}
	res.Summary.Total = len(res.Pages)
	return res, worst
}

func mirrorRootDefaultForApp(into string) string {
	if into != "" {
		return into
	}
	return "mirror"
}

func (s *ConfluenceDiffSummary) add(state string) {
	switch state {
	case "unchanged":
		s.Unchanged++
	case "added":
		s.Added++
	case "removed":
		s.Removed++
	case "modified":
		s.Modified++
	case "malformed":
		s.Malformed++
	case "missing_baseline":
		s.MissingBaseline++
	case "baseline_mismatch":
		s.BaselineMismatch++
	case "unreadable":
		s.Unreadable++
	}
}

func confluenceDiffTargets(m *mirror.Mirror, target string) ([]confluenceDiffTarget, error) {
	if !within(m.Root, target) {
		return nil, fmt.Errorf("%w: diff target %q is outside mirror root %q", domain.ErrUsage, target, m.Root)
	}
	states, err := m.SyncStates()
	if err != nil {
		return nil, err
	}
	byPath := map[string]mirror.SyncState{}
	for _, state := range states {
		if strings.HasSuffix(filepath.ToSlash(state.Path), ".csf") {
			byPath[filepath.Clean(filepath.Join(m.Root, filepath.FromSlash(state.Path)))] = state
		}
	}
	info, statErr := os.Stat(target)
	if statErr != nil && !os.IsNotExist(statErr) {
		return nil, localConfluenceTargetError("diff", target, statErr)
	}
	if statErr == nil && !info.IsDir() {
		path := target
		if strings.HasSuffix(path, ".md") {
			path = strings.TrimSuffix(path, ".md") + ".csf"
		}
		if !strings.HasSuffix(path, ".csf") {
			return nil, fmt.Errorf("%w: diff target %q must be a directory, a .md, or a .csf file", domain.ErrUsage, target)
		}
		st, ok := byPath[filepath.Clean(path)]
		var state *mirror.SyncState
		if ok {
			copy := st
			state = &copy
		}
		return []confluenceDiffTarget{{path: path, state: state}}, nil
	}
	if os.IsNotExist(statErr) {
		path := target
		if strings.HasSuffix(path, ".md") {
			path = strings.TrimSuffix(path, ".md") + ".csf"
		}
		st, ok := byPath[filepath.Clean(path)]
		if !ok || !strings.HasSuffix(path, ".csf") {
			return nil, localConfluenceTargetError("diff", target, statErr)
		}
		copy := st
		return []confluenceDiffTarget{{path: path, state: &copy}}, nil
	}

	paths, err := m.ListCSFPaths()
	if err != nil {
		return nil, err
	}
	targets := map[string]confluenceDiffTarget{}
	for _, path := range paths {
		if !within(target, path) {
			continue
		}
		entry := confluenceDiffTarget{path: path}
		if st, ok := byPath[filepath.Clean(path)]; ok {
			copy := st
			entry.state = &copy
		}
		targets[filepath.Clean(path)] = entry
	}
	for path, state := range byPath {
		if within(target, path) {
			copy := state
			if _, ok := targets[path]; !ok {
				targets[path] = confluenceDiffTarget{path: path, state: &copy}
			}
		}
	}
	out := make([]confluenceDiffTarget, 0, len(targets))
	for _, entry := range targets {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out, nil
}

func confluenceDiffPage(m *mirror.Mirror, target confluenceDiffTarget) (ConfluencePageDiff, error) {
	page := ConfluencePageDiff{Path: target.path, State: "unreadable"}
	if target.state != nil {
		page.ID = target.state.ID
	}
	var candidate []byte
	lc, body, err := m.LoadCSF(target.path)
	if os.IsNotExist(err) {
		page.Candidate.Present = false
	} else if err != nil {
		return page, fmt.Errorf("%w: read diff candidate %s: %v", domain.ErrCheckFailed, target.path, err)
	} else {
		candidate = body
		page.ID, page.Title = lc.Meta.ID, lc.Meta.Title
		page.Candidate = confluenceDiffSide(body)
	}
	if candidate != nil && csf.HasErrors(page.Candidate.Problems) {
		page.State = "malformed"
		return page, nil
	}
	if candidate != nil && target.state == nil {
		page.State = "added"
		return page, nil
	}
	if page.ID == "" {
		if candidate != nil {
			page.State = "added"
			return page, nil
		}
		return page, fmt.Errorf("%w: diff target %s has no page identity", domain.ErrCheckFailed, target.path)
	}
	if target.state != nil && target.state.ID != page.ID {
		return page, fmt.Errorf("%w: diff target %s metadata id %s does not match tracked id %s", domain.ErrCheckFailed, target.path, page.ID, target.state.ID)
	}
	base, present, baseErr := m.ReadBaseBody(page.ID)
	if baseErr != nil {
		return page, fmt.Errorf("%w: read diff baseline for %s: %v", domain.ErrCheckFailed, page.ID, baseErr)
	}
	if !present {
		if candidate == nil {
			page.State = "missing_baseline"
		} else if target.state != nil {
			page.State = "missing_baseline"
		} else {
			page.State = "added"
		}
		return page, nil
	}
	page.Baseline = confluenceDiffSide(base)
	if target.state == nil || target.state.Hash == "" {
		page.State = "missing_baseline"
		return page, nil
	}
	if mirror.Hash(base) != target.state.Hash {
		page.State = "baseline_mismatch"
		return page, fmt.Errorf("%w: diff baseline hash for %s does not match tracked mirror state", domain.ErrCheckFailed, page.ID)
	}
	if candidate == nil {
		page.State = "removed"
		return page, nil
	}
	page.ByteEvidence = confluenceByteEvidence(base, candidate)
	if csf.HasErrors(page.Baseline.Problems) || csf.HasErrors(page.Candidate.Problems) {
		page.State = "malformed"
		return page, nil
	}
	if bytes.Equal(base, candidate) {
		page.State = "unchanged"
		return page, nil
	}
	page.State = "modified"
	baseRoot, _ := csf.Parse(base)
	candidateRoot, _ := csf.Parse(candidate)
	page.Blocks = confluenceBlockChanges(baseRoot, candidateRoot)
	page.Features = confluenceFeatureDeltas(baseRoot, candidateRoot)
	baseDocumentHash, candidateDocumentHash := semanticNodeHash(baseRoot), semanticNodeHash(candidateRoot)
	if len(page.Blocks) == 0 && baseDocumentHash != candidateDocumentHash {
		page.Blocks = []ConfluenceBlockChange{{
			Change: "modified", Kind: "document",
			BaselineSHA256: baseDocumentHash, CandidateSHA256: candidateDocumentHash,
		}}
	}
	page.SemanticChanged = len(page.Blocks) > 0 || len(page.Features) > 0
	page.ByteOnly = !page.SemanticChanged
	return page, nil
}

func confluenceDiffSide(body []byte) ConfluenceDiffSide {
	problems := csf.Validate(body)
	return ConfluenceDiffSide{Present: true, Bytes: len(body), SHA256: hashHex(body), Valid: !csf.HasErrors(problems), Problems: problems}
}

func confluenceByteEvidence(base, candidate []byte) *ConfluenceByteEvidence {
	prefix := 0
	for prefix < len(base) && prefix < len(candidate) && base[prefix] == candidate[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(base)-prefix && suffix < len(candidate)-prefix && base[len(base)-1-suffix] == candidate[len(candidate)-1-suffix] {
		suffix++
	}
	baseChanged := base[prefix : len(base)-suffix]
	candidateChanged := candidate[prefix : len(candidate)-suffix]
	return &ConfluenceByteEvidence{CommonPrefixBytes: prefix, CommonSuffixBytes: suffix,
		BaselineChangedBytes: len(baseChanged), CandidateChangedBytes: len(candidateChanged),
		BaselineChangedSHA256: hashHex(baseChanged), CandidateChangedSHA256: hashHex(candidateChanged)}
}

type semanticBlock struct{ kind, hash string }

func semanticBlocks(root *csf.Node) []semanticBlock {
	_, nodes := mirror.RenderBlockNodes(root, nil)
	out := make([]semanticBlock, 0, len(nodes))
	for _, node := range nodes {
		out = append(out, semanticBlock{kind: semanticNodeKind(node), hash: semanticNodeHash(node)})
	}
	return out
}

func semanticNodeKind(n *csf.Node) string {
	if n.Type != csf.Element {
		return "text"
	}
	if name := n.MacroName(); name != "" {
		return "macro:" + name
	}
	if n.Name.Space == "ac" && n.Name.Local == "task-list" {
		return "tasklist"
	}
	if n.Name.Space != "" {
		return n.Name.String()
	}
	switch n.Name.Local {
	case "h1", "h2", "h3", "h4", "h5", "h6", "p", "table", "blockquote", "pre", "hr":
		return n.Name.Local
	case "ul", "ol":
		return "list"
	default:
		return n.Name.Local
	}
}

func semanticNodeHash(n *csf.Node) string {
	var b strings.Builder
	var walk func(*csf.Node)
	walk = func(node *csf.Node) {
		if node.Type != csf.Element {
			b.WriteString("T:")
			b.WriteString(node.Data)
			b.WriteByte(0)
			return
		}
		b.WriteString("E:")
		b.WriteString(node.Name.String())
		b.WriteByte(0)
		attrs := append([]csf.Attr(nil), node.Attr...)
		sort.Slice(attrs, func(i, j int) bool {
			if attrs[i].Name.String() != attrs[j].Name.String() {
				return attrs[i].Name.String() < attrs[j].Name.String()
			}
			return attrs[i].Value < attrs[j].Value
		})
		for _, attr := range attrs {
			b.WriteString(attr.Name.String())
			b.WriteByte('=')
			b.WriteString(attr.Value)
			b.WriteByte(0)
		}
		for _, child := range node.Children {
			walk(child)
		}
		b.WriteString("/E")
		b.WriteByte(0)
	}
	walk(n)
	return hashHex([]byte(b.String()))
}

func confluenceBlockChanges(baseRoot, candidateRoot *csf.Node) []ConfluenceBlockChange {
	a, b := semanticBlocks(baseRoot), semanticBlocks(candidateRoot)
	// Dynamic-programming LCS is capped by the native page size indirectly; pages
	// with pathological block counts use deterministic positional pairing.
	if len(a)*len(b) > 1_000_000 {
		return positionalBlockChanges(a, b)
	}
	dp := make([][]int, len(a)+1)
	for i := range dp {
		dp[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	var out []ConfluenceBlockChange
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		if i < len(a) && j < len(b) && a[i] == b[j] {
			i++
			j++
			continue
		}
		if j < len(b) && i < len(a) && dp[i][j+1] > dp[i+1][j] {
			cj := j
			out = append(out, ConfluenceBlockChange{Change: "added", Kind: b[j].kind, CandidateIndex: &cj, CandidateSHA256: b[j].hash})
			j++
			continue
		}
		if i < len(a) && j < len(b) && dp[i+1][j] > dp[i][j+1] {
			bi := i
			out = append(out, ConfluenceBlockChange{Change: "removed", Kind: a[i].kind, BaselineIndex: &bi, BaselineSHA256: a[i].hash})
			i++
			continue
		}
		if i < len(a) && j < len(b) && a[i].kind == b[j].kind {
			bi, cj := i, j
			out = append(out, ConfluenceBlockChange{Change: "modified", Kind: a[i].kind, BaselineIndex: &bi, CandidateIndex: &cj, BaselineSHA256: a[i].hash, CandidateSHA256: b[j].hash})
			i++
			j++
			continue
		}
		if j < len(b) && (i == len(a) || dp[i][j+1] >= dp[i+1][j]) {
			cj := j
			out = append(out, ConfluenceBlockChange{Change: "added", Kind: b[j].kind, CandidateIndex: &cj, CandidateSHA256: b[j].hash})
			j++
			continue
		}
		bi := i
		out = append(out, ConfluenceBlockChange{Change: "removed", Kind: a[i].kind, BaselineIndex: &bi, BaselineSHA256: a[i].hash})
		i++
	}
	return out
}

func positionalBlockChanges(a, b []semanticBlock) []ConfluenceBlockChange {
	var out []ConfluenceBlockChange
	for i := 0; i < len(a) || i < len(b); i++ {
		switch {
		case i >= len(a):
			ci := i
			out = append(out, ConfluenceBlockChange{Change: "added", Kind: b[i].kind, CandidateIndex: &ci, CandidateSHA256: b[i].hash})
		case i >= len(b):
			bi := i
			out = append(out, ConfluenceBlockChange{Change: "removed", Kind: a[i].kind, BaselineIndex: &bi, BaselineSHA256: a[i].hash})
		case a[i] != b[i]:
			bi, ci := i, i
			out = append(out, ConfluenceBlockChange{Change: "modified", Kind: b[i].kind, BaselineIndex: &bi, CandidateIndex: &ci, BaselineSHA256: a[i].hash, CandidateSHA256: b[i].hash})
		}
	}
	return out
}

func confluenceFeatureDeltas(base, candidate *csf.Node) []ConfluenceFeatureDelta {
	counts := func(root *csf.Node) map[string]int {
		out := map[string]int{}
		csf.Walk(root, func(n *csf.Node) bool {
			if name := n.MacroName(); name != "" {
				out["macro\x00"+name]++
			}
			switch {
			case n.Name.Space == "ac" && n.Name.Local == "link":
				out["link\x00storage"]++
			case n.Name.Space == "ri" && n.Name.Local == "url":
				out["link\x00url-target"]++
			case n.Name.Space == "" && n.Name.Local == "a":
				out["link\x00html"]++
			}
			return true
		})
		for _, ref := range fragment.Extract(root) {
			out["fragment\x00"+string(ref.Kind)]++
		}
		return out
	}
	a, b := counts(base), counts(candidate)
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	ordered := make([]string, 0, len(keys))
	for k := range keys {
		if a[k] != b[k] {
			ordered = append(ordered, k)
		}
	}
	sort.Strings(ordered)
	out := make([]ConfluenceFeatureDelta, 0, len(ordered))
	for _, k := range ordered {
		parts := strings.SplitN(k, "\x00", 2)
		out = append(out, ConfluenceFeatureDelta{Kind: parts[0], Name: parts[1], Baseline: a[k], Candidate: b[k]})
	}
	return out
}

func hashHex(body []byte) string { sum := sha256.Sum256(body); return hex.EncodeToString(sum[:]) }

func canonicalConfluenceDiffPaths(target, into string) (root, canonicalTarget string, err error) {
	if target == "" {
		target = mirrorRootDefaultForApp(into)
	}
	root = into
	if root == "" {
		root = mirrorRootOf(target)
	}
	requestedRoot := root
	root, err = evalSymlinksAbsolute(requestedRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", fmt.Errorf("%w: no Confluence mirror found at %q; run conf pull --into %q or pass --into", domain.ErrNotFound, requestedRoot, requestedRoot)
		}
		return "", "", localConfluenceTargetError("diff root", requestedRoot, err)
	}
	canonicalTarget, err = evalSymlinksAllowMissing(target)
	if err != nil {
		return "", "", localConfluenceTargetError("diff", target, err)
	}
	if !within(root, canonicalTarget) {
		return "", "", fmt.Errorf("%w: diff target %q is outside mirror root %q", domain.ErrUsage, canonicalTarget, root)
	}
	return root, canonicalTarget, nil
}

func evalSymlinksAbsolute(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	return filepath.Abs(resolved)
}

func evalSymlinksAllowMissing(path string) (string, error) {
	probe, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	var missing []string
	for {
		resolved, resolveErr := filepath.EvalSymlinks(probe)
		if resolveErr == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return filepath.Clean(resolved), nil
		}
		if !os.IsNotExist(resolveErr) {
			return "", resolveErr
		}
		if info, lstatErr := os.Lstat(probe); lstatErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("dangling symlink %q", probe)
		} else if lstatErr != nil && !os.IsNotExist(lstatErr) {
			return "", lstatErr
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return "", resolveErr
		}
		missing = append(missing, filepath.Base(probe))
		probe = parent
	}
}

// ConfluenceDiffMarkdown is the compact human projection of Diff.
func ConfluenceDiffMarkdown(result *ConfluenceDiffResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Confluence mirror diff\n\nComplete: **%t** · total %d · modified %d · added %d · removed %d · malformed %d · missing baseline %d · baseline mismatch %d · unreadable %d\n\n", result.Complete, result.Summary.Total, result.Summary.Modified, result.Summary.Added, result.Summary.Removed, result.Summary.Malformed, result.Summary.MissingBaseline, result.Summary.BaselineMismatch, result.Summary.Unreadable)
	rows := make([][]string, 0, len(result.Pages))
	for _, page := range result.Pages {
		label := page.Title
		if label == "" {
			label = page.ID
		}
		if label == "" {
			label = "unknown"
		}
		rows = append(rows, []string{page.State, label, confluenceDiffMarkdownPath(result.Root, page.Path), confluenceDiffReviewKind(page), fmt.Sprint(len(page.Blocks) + len(page.Features))})
	}
	b.WriteString(MarkdownTable([]string{"State", "Page", "Path (relative to root)", "Review", "Deltas"}, rows))
	return strings.TrimRight(b.String(), "\n")
}

func confluenceDiffMarkdownPath(root, path string) string {
	if root == "" || path == "" {
		return filepath.ToSlash(path)
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(relative)
}

func confluenceDiffReviewKind(page ConfluencePageDiff) string {
	switch {
	case page.State == "modified" && page.SemanticChanged:
		return "semantic"
	case page.State == "modified" && page.ByteOnly:
		return "byte-only"
	case page.State == "unchanged":
		return "none"
	default:
		return "n/a"
	}
}
