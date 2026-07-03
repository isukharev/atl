package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
)

// JiraExportOpts controls compact, single-artifact Jira exports.
type JiraExportOpts struct {
	JQL       string
	IDs       []string
	Keys      []string
	BatchSize int
	Out       string
	Format    string // jsonl | json | csv
	Limit     int
	Fields    []string
	Version   string
}

// JiraExportResult describes files written by Export.
type JiraExportResult struct {
	Path         string             `json:"path"`
	ManifestPath string             `json:"manifest_path"`
	Format       string             `json:"format"`
	Count        int                `json:"count"`
	Manifest     JiraExportManifest `json:"-"`
}

// JiraExportManifest captures enough non-secret provenance to reproduce an
// export without writing backend hostnames or tokens to disk.
type JiraExportManifest struct {
	CreatedAt  string   `json:"created_at"`
	Command    string   `json:"command"`
	Format     string   `json:"format"`
	JQL        string   `json:"jql"`
	QueryMode  string   `json:"query_mode"`
	Queries    []string `json:"queries,omitempty"`
	BatchSize  int      `json:"batch_size,omitempty"`
	Fields     []string `json:"fields,omitempty"`
	Limit      int      `json:"limit"`
	Count      int      `json:"count"`
	ATLVersion string   `json:"atl_version,omitempty"`
	Backend    struct {
		Service string `json:"service"`
		URLHash string `json:"url_hash,omitempty"`
	} `json:"backend"`
}

// Export writes a compact Jira export and a sanitized provenance manifest.
func (s *JiraService) Export(ctx context.Context, opts JiraExportOpts) (*JiraExportResult, error) {
	queries, queryMode, err := exportQueries(opts)
	if err != nil {
		return nil, err
	}
	format := strings.ToLower(strings.TrimSpace(opts.Format))
	if format == "" {
		format = "jsonl"
	}
	switch format {
	case "jsonl", "json", "csv":
	default:
		return nil, fmt.Errorf("%w: --format must be jsonl, json, or csv", domain.ErrUsage)
	}
	if strings.TrimSpace(opts.Out) == "" || opts.Out == "-" {
		return nil, fmt.Errorf("%w: --out is required and must be a file path", domain.ErrUsage)
	}
	issues, err := s.collectExportIssues(ctx, queries, opts.Fields, opts.Limit)
	if err != nil {
		return nil, err
	}
	manifest := s.exportManifest(opts, format, queryMode, queries, len(issues))
	data, err := renderJiraExport(format, issues, opts.Fields, manifest)
	if err != nil {
		return nil, err
	}
	if err := writeUserFile(opts.Out, data); err != nil {
		return nil, err
	}
	manifestPath := opts.Out + ".manifest.json"
	mb, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := writeUserFile(manifestPath, append(mb, '\n')); err != nil {
		return nil, err
	}
	return &JiraExportResult{
		Path:         opts.Out,
		ManifestPath: manifestPath,
		Format:       format,
		Count:        len(issues),
		Manifest:     manifest,
	}, nil
}

func (s *JiraService) collectExportIssues(ctx context.Context, queries []string, fields []string, limit int) ([]JiraIssueSnapshot, error) {
	var out []JiraIssueSnapshot
	seen := map[string]bool{}
	searchFields := exportFields(fields)
	for _, jql := range queries {
		cursor := ""
		for len(out) < limit || limit == 0 {
			pageLimit := 100
			if limit > 0 && limit-len(out) < pageLimit {
				pageLimit = limit - len(out)
			}
			issues, next, err := s.tr.Search(ctx, jql, searchFields, pageLimit, cursor)
			if err != nil {
				return out, err
			}
			for _, is := range issues {
				identity := is.Key
				if identity == "" {
					identity = is.ID
				}
				if seen[identity] {
					continue
				}
				seen[identity] = true
				fields := is.Fields
				if fields == nil {
					fields = map[string]any{}
				}
				out = append(out, JiraIssueSnapshot{Key: is.Key, ID: is.ID, Fields: fields})
				if limit > 0 && len(out) >= limit {
					return out, nil
				}
			}
			if next == "" || len(issues) == 0 {
				break
			}
			cursor = next
		}
	}
	return out, nil
}

func exportQueries(opts JiraExportOpts) ([]string, string, error) {
	hasJQL := strings.TrimSpace(opts.JQL) != ""
	hasIDs := len(opts.IDs) > 0
	hasKeys := len(opts.Keys) > 0
	modes := 0
	for _, ok := range []bool{hasJQL, hasIDs, hasKeys} {
		if ok {
			modes++
		}
	}
	if modes != 1 {
		return nil, "", fmt.Errorf("%w: pass exactly one of --jql, --ids, or --keys", domain.ErrUsage)
	}
	if hasJQL {
		return []string{opts.JQL}, "jql", nil
	}
	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = 100
	}
	if hasIDs {
		ids := cleanList(opts.IDs)
		if len(ids) == 0 {
			return nil, "", fmt.Errorf("%w: --ids is empty", domain.ErrUsage)
		}
		for _, id := range ids {
			if _, err := strconv.ParseInt(id, 10, 64); err != nil {
				return nil, "", fmt.Errorf("%w: --ids must contain numeric issue ids, got %q", domain.ErrUsage, id)
			}
		}
		return batchedJQL("id", ids, batchSize, false), "ids", nil
	}
	keys := cleanList(opts.Keys)
	if len(keys) == 0 {
		return nil, "", fmt.Errorf("%w: --keys is empty", domain.ErrUsage)
	}
	return batchedJQL("key", keys, batchSize, true), "keys", nil
}

func cleanList(values []string) []string {
	var out []string
	for _, raw := range values {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

func batchedJQL(field string, values []string, batchSize int, quote bool) []string {
	var queries []string
	for start := 0; start < len(values); start += batchSize {
		end := start + batchSize
		if end > len(values) {
			end = len(values)
		}
		chunk := values[start:end]
		parts := make([]string, len(chunk))
		for i, value := range chunk {
			if quote {
				parts[i] = strconv.Quote(value)
			} else {
				parts[i] = value
			}
		}
		queries = append(queries, field+" in ("+strings.Join(parts, ",")+")")
	}
	return queries
}

func exportFields(extra []string) []string {
	base := []string{"summary", "status", "issuetype", "project"}
	seen := make(map[string]bool, len(base)+len(extra))
	out := make([]string, 0, len(base)+len(extra))
	for _, f := range append(base, extra...) {
		f = strings.TrimSpace(f)
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}

func (s *JiraService) exportManifest(opts JiraExportOpts, format, queryMode string, queries []string, count int) JiraExportManifest {
	batchSize := opts.BatchSize
	if queryMode != "jql" && batchSize <= 0 {
		batchSize = 100
	}
	m := JiraExportManifest{
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		Command:    "atl jira export",
		Format:     format,
		JQL:        opts.JQL,
		QueryMode:  queryMode,
		BatchSize:  batchSize,
		Fields:     exportFields(opts.Fields),
		Limit:      opts.Limit,
		Count:      count,
		ATLVersion: opts.Version,
	}
	if queryMode != "jql" {
		m.Queries = queries
	}
	m.Backend.Service = "jira"
	if s.baseURL != "" {
		sum := sha256.Sum256([]byte(strings.TrimRight(s.baseURL, "/")))
		m.Backend.URLHash = "sha256:" + hex.EncodeToString(sum[:])
	}
	return m
}

// JiraExportDiff is a deterministic comparison of two compact exports.
type JiraExportDiff struct {
	OldCount int      `json:"old_count"`
	NewCount int      `json:"new_count"`
	Added    []string `json:"added,omitempty"`
	Removed  []string `json:"removed,omitempty"`
	Changed  []string `json:"changed,omitempty"`
}

// DiffJiraExports compares two compact export artifacts by issue key/id.
func DiffJiraExports(oldPath, newPath string) (*JiraExportDiff, error) {
	oldIssues, err := readJiraExportSnapshots(oldPath)
	if err != nil {
		return nil, err
	}
	newIssues, err := readJiraExportSnapshots(newPath)
	if err != nil {
		return nil, err
	}
	oldMap := snapshotsByIdentity(oldIssues)
	newMap := snapshotsByIdentity(newIssues)
	diff := &JiraExportDiff{OldCount: len(oldMap), NewCount: len(newMap)}
	for id, newSnap := range newMap {
		oldSnap, ok := oldMap[id]
		if !ok {
			diff.Added = append(diff.Added, id)
			continue
		}
		if !reflect.DeepEqual(oldSnap.Fields, newSnap.Fields) {
			diff.Changed = append(diff.Changed, id)
		}
	}
	for id := range oldMap {
		if _, ok := newMap[id]; !ok {
			diff.Removed = append(diff.Removed, id)
		}
	}
	sortStrings(diff.Added)
	sortStrings(diff.Removed)
	sortStrings(diff.Changed)
	return diff, nil
}

func readJiraExportSnapshots(path string) ([]JiraIssueSnapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, nil
	}
	if trimmed[0] == '{' {
		var wrapped struct {
			Issues []JiraIssueSnapshot `json:"issues"`
		}
		if err := json.Unmarshal(trimmed, &wrapped); err == nil && wrapped.Issues != nil {
			return wrapped.Issues, nil
		}
		return readJiraExportJSONL(data)
	}
	if trimmed[0] == '[' {
		var issues []JiraIssueSnapshot
		if err := json.Unmarshal(trimmed, &issues); err != nil {
			return nil, err
		}
		return issues, nil
	}
	if strings.HasSuffix(strings.ToLower(path), ".csv") {
		return readJiraExportCSV(data)
	}
	return readJiraExportJSONL(data)
}

func readJiraExportJSONL(data []byte) ([]JiraIssueSnapshot, error) {
	var out []JiraIssueSnapshot
	for lineNo, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var snap JiraIssueSnapshot
		if err := json.Unmarshal([]byte(line), &snap); err != nil {
			return nil, fmt.Errorf("decode jsonl line %d: %w", lineNo+1, err)
		}
		out = append(out, snap)
	}
	return out, nil
}

func readJiraExportCSV(data []byte) ([]JiraIssueSnapshot, error) {
	r := csv.NewReader(bytes.NewReader(data))
	records, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, nil
	}
	header := records[0]
	var out []JiraIssueSnapshot
	for _, record := range records[1:] {
		snap := JiraIssueSnapshot{Fields: map[string]any{}}
		for i, name := range header {
			if i >= len(record) {
				continue
			}
			switch name {
			case "key":
				snap.Key = record[i]
			case "id":
				snap.ID = record[i]
			default:
				snap.Fields[name] = record[i]
			}
		}
		out = append(out, snap)
	}
	return out, nil
}

func snapshotsByIdentity(snapshots []JiraIssueSnapshot) map[string]JiraIssueSnapshot {
	out := make(map[string]JiraIssueSnapshot, len(snapshots))
	for _, snap := range snapshots {
		id := snap.Key
		if id == "" {
			id = snap.ID
		}
		if id != "" {
			out[id] = snap
		}
	}
	return out
}

func sortStrings(values []string) {
	sort.Strings(values)
}

func renderJiraExport(format string, issues []JiraIssueSnapshot, fields []string, manifest JiraExportManifest) ([]byte, error) {
	switch format {
	case "jsonl":
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false)
		for _, is := range issues {
			if err := enc.Encode(is); err != nil {
				return nil, err
			}
		}
		return b.Bytes(), nil
	case "json":
		out := map[string]any{"manifest": manifest, "issues": issues}
		b, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return nil, err
		}
		return append(b, '\n'), nil
	case "csv":
		return renderJiraExportCSV(issues, exportFields(fields))
	default:
		return nil, fmt.Errorf("%w: unsupported export format %q", domain.ErrUsage, format)
	}
}

func renderJiraExportCSV(issues []JiraIssueSnapshot, fields []string) ([]byte, error) {
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	header := append([]string{"key", "id"}, fields...)
	if err := w.Write(header); err != nil {
		return nil, err
	}
	for _, is := range issues {
		row := []string{is.Key, is.ID}
		for _, f := range fields {
			row = append(row, csvFieldValue(is.Fields[f]))
		}
		if err := w.Write(row); err != nil {
			return nil, err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func csvFieldValue(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case fmt.Stringer:
		return t.String()
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return fmt.Sprintf("%v", t)
		}
		return string(b)
	}
}

func writeUserFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	clean := filepath.Clean(path)
	if !safepath.Within(dir, clean) {
		return fmt.Errorf("refusing unsafe output path %q", path)
	}
	return safepath.WriteFile(clean, data, 0o644)
}
