package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
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
	RawCSV    bool
	// Writer is required only when Out is "-". The CLI supplies stdout; keeping
	// it explicit prevents the app layer from reaching process-global streams.
	Writer io.Writer
}

// JiraExportResult describes files written by Export.
type JiraExportResult struct {
	Path         string             `json:"path"`
	ManifestPath string             `json:"manifest_path,omitempty"`
	Format       string             `json:"format"`
	Count        int                `json:"count"`
	Transient    bool               `json:"transient,omitempty"`
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
	CSVRaw     bool     `json:"csv_raw,omitempty"`
	Backend    struct {
		Service string `json:"service"`
		URLHash string `json:"url_hash,omitempty"`
	} `json:"backend"`
}

const jiraAggregateExportMaxIssues = 10000
const jiraAggregateExportMaxBytes int64 = 64 << 20
const jiraRowExportMaxIdentities = 250000

// Export writes a compact Jira export and a backend-identity-hashed provenance
// manifest. Query selectors remain present verbatim for reproducibility.
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
	if opts.RawCSV && format != "csv" {
		return nil, fmt.Errorf("%w: --raw-csv requires --format csv", domain.ErrUsage)
	}
	if strings.TrimSpace(opts.Out) == "" {
		return nil, fmt.Errorf("%w: --out is required", domain.ErrUsage)
	}
	transient := opts.Out == "-"
	if transient && opts.Writer == nil {
		return nil, fmt.Errorf("%w: stdout writer is required for --out -", domain.ErrUsage)
	}
	if opts.Limit < 0 {
		return nil, fmt.Errorf("%w: --limit must be >= 0", domain.ErrUsage)
	}
	if len(opts.Fields) > 0 {
		resolved, resolveErr := s.resolveJiraFieldSelectors(ctx, opts.Fields)
		if resolveErr != nil {
			return nil, resolveErr
		}
		opts.Fields = fieldDefIDs(resolved)
	}
	count := 0
	var manifest JiraExportManifest
	if format == "json" {
		collectLimit := jiraAggregateExportMaxIssues + 1
		if opts.Limit > 0 && opts.Limit < collectLimit {
			collectLimit = opts.Limit
		}
		issues, collectErr := s.collectAggregateExportIssues(ctx, queries, opts.Fields, collectLimit, jiraAggregateExportMaxBytes)
		if collectErr != nil {
			return nil, collectErr
		}
		if len(issues) > jiraAggregateExportMaxIssues {
			return nil, fmt.Errorf("%w: aggregate JSON export exceeds %d issues; use jsonl/csv or a smaller --limit", domain.ErrUsage, jiraAggregateExportMaxIssues)
		}
		count = len(issues)
		if !transient {
			manifest = s.exportManifest(opts, format, queryMode, queries, count)
		}
		data, renderErr := renderJiraExport(format, issues, opts.Fields, manifest, opts.RawCSV, transient)
		if renderErr != nil {
			return nil, renderErr
		}
		if transient {
			if _, err := opts.Writer.Write(data); err != nil {
				return nil, err
			}
		} else {
			if err := writeUserFile(opts.Out, data); err != nil {
				return nil, err
			}
		}
	} else {
		writeStream := func(dst io.Writer) error {
			var streamErr error
			count, streamErr = s.streamJiraExport(ctx, dst, format, queries, opts.Fields, opts.Limit, opts.RawCSV)
			return streamErr
		}
		var writeErr error
		if transient {
			writeErr = writeStream(opts.Writer)
		} else {
			writeErr = writeUserFileStream(opts.Out, writeStream)
		}
		if writeErr != nil {
			return nil, writeErr
		}
		if !transient {
			manifest = s.exportManifest(opts, format, queryMode, queries, count)
		}
	}
	if transient {
		return &JiraExportResult{Path: "-", Format: format, Count: count, Transient: true}, nil
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
		Count:        count,
		Manifest:     manifest,
	}, nil
}

func (s *JiraService) collectAggregateExportIssues(ctx context.Context, queries, fields []string, limit int, maxBytes int64) ([]JiraIssueSnapshot, error) {
	var out []JiraIssueSnapshot
	var encodedBytes int64
	_, err := s.forEachExportIssue(ctx, queries, fields, limit, func(snapshot JiraIssueSnapshot) error {
		encoded, err := json.Marshal(snapshot)
		if err != nil {
			return err
		}
		encodedBytes += int64(len(encoded)) + 1
		if encodedBytes > maxBytes {
			return fmt.Errorf("%w: aggregate JSON export exceeds %d bytes; use jsonl/csv or a smaller --limit", domain.ErrUsage, maxBytes)
		}
		out = append(out, snapshot)
		return nil
	})
	return out, err
}

func (s *JiraService) streamJiraExport(ctx context.Context, dst io.Writer, format string, queries, fields []string, limit int, rawCSV bool) (int, error) {
	var encode func(JiraIssueSnapshot) error
	var csvWriter *csv.Writer
	switch format {
	case "jsonl":
		enc := json.NewEncoder(dst)
		enc.SetEscapeHTML(false)
		encode = func(snapshot JiraIssueSnapshot) error { return enc.Encode(snapshot) }
	case "csv":
		w := csv.NewWriter(dst)
		csvWriter = w
		exportFields := exportFields(fields)
		if err := w.Write(spreadsheetRecord(append([]string{"key", "id"}, exportFields...), rawCSV)); err != nil {
			return 0, err
		}
		encode = func(is JiraIssueSnapshot) error {
			row := []string{is.Key, is.ID}
			for _, field := range exportFields {
				row = append(row, csvFieldValue(is.Fields[field]))
			}
			return w.Write(spreadsheetRecord(row, rawCSV))
		}
	default:
		return 0, fmt.Errorf("%w: unsupported streaming export format %q", domain.ErrUsage, format)
	}
	count, err := s.forEachExportIssue(ctx, queries, fields, limit, encode)
	if csvWriter != nil {
		csvWriter.Flush()
		if err == nil {
			err = csvWriter.Error()
		}
	}
	return count, err
}

func (s *JiraService) forEachExportIssue(ctx context.Context, queries []string, fields []string, limit int, yield func(JiraIssueSnapshot) error) (int, error) {
	return s.forEachExportIssueWithIdentityCap(ctx, queries, fields, limit, jiraRowExportMaxIdentities, yield)
}

func (s *JiraService) forEachExportIssueWithIdentityCap(ctx context.Context, queries []string, fields []string, limit, identityCap int, yield func(JiraIssueSnapshot) error) (int, error) {
	return s.forEachExportIssueWithSearch(ctx, queries, fields, limit, identityCap, s.tr.Search, yield)
}

type jiraIssueSearch func(context.Context, string, []string, int, string) ([]domain.Issue, string, error)

func (s *JiraService) forEachExportIssueWithSearch(ctx context.Context, queries []string, fields []string, limit, identityCap int, search jiraIssueSearch, yield func(JiraIssueSnapshot) error) (int, error) {
	count := 0
	seen := map[string]bool{}
	searchFields := exportFields(fields)
	for _, jql := range queries {
		cursor := ""
		for count < limit || limit == 0 {
			pageLimit := 100
			if limit > 0 && limit-count < pageLimit {
				pageLimit = limit - count
			}
			issues, next, err := search(ctx, jql, searchFields, pageLimit, cursor)
			if err != nil {
				return count, err
			}
			for _, issue := range issues {
				identity := issue.Key
				if identity != "" {
					identity = "key:" + strings.ToUpper(identity)
				} else {
					identity = "id:" + issue.ID
				}
				if seen[identity] {
					continue
				}
				if len(seen) >= identityCap {
					return count, fmt.Errorf("%w: row export exceeds the %d-issue identity safety cap; use a smaller --limit or split the selection", domain.ErrUsage, identityCap)
				}
				seen[identity] = true
				fields := issue.Fields
				if fields == nil {
					fields = map[string]any{}
				}
				if err := yield(JiraIssueSnapshot{Key: issue.Key, ID: issue.ID, Fields: fields}); err != nil {
					return count, err
				}
				count++
				if limit > 0 && count >= limit {
					return count, nil
				}
			}
			if next == "" || len(issues) == 0 {
				break
			}
			cursor = next
		}
	}
	return count, nil
}

func (s *JiraService) collectStructureIssues(ctx context.Context, queries []string, fields []string, limit int) ([]JiraIssueSnapshot, error) {
	search := s.tr.Search
	if lenient, ok := s.tr.(domain.LenientIssueSearcher); ok {
		search = lenient.SearchLenient
	}
	var out []JiraIssueSnapshot
	_, err := s.forEachExportIssueWithSearch(ctx, queries, fields, limit, jiraRowExportMaxIdentities, search, func(snapshot JiraIssueSnapshot) error {
		out = append(out, snapshot)
		return nil
	})
	return out, err
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
	seen := map[string]bool{}
	for _, raw := range values {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			identity := strings.ToUpper(part)
			if part != "" && !seen[identity] {
				seen[identity] = true
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
		CSVRaw:     opts.RawCSV,
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

func renderJiraExport(format string, issues []JiraIssueSnapshot, fields []string, manifest JiraExportManifest, rawCSV, transient bool) ([]byte, error) {
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
		if transient {
			b, err := json.MarshalIndent(issues, "", "  ")
			if err != nil {
				return nil, err
			}
			return append(b, '\n'), nil
		}
		out := map[string]any{"manifest": manifest, "issues": issues}
		b, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return nil, err
		}
		return append(b, '\n'), nil
	case "csv":
		return renderJiraExportCSV(issues, exportFields(fields), rawCSV)
	default:
		return nil, fmt.Errorf("%w: unsupported export format %q", domain.ErrUsage, format)
	}
}

func renderJiraExportCSV(issues []JiraIssueSnapshot, fields []string, rawCSV bool) ([]byte, error) {
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	header := append([]string{"key", "id"}, fields...)
	if err := w.Write(spreadsheetRecord(header, rawCSV)); err != nil {
		return nil, err
	}
	for _, is := range issues {
		row := []string{is.Key, is.ID}
		for _, f := range fields {
			row = append(row, csvFieldValue(is.Fields[f]))
		}
		if err := w.Write(spreadsheetRecord(row, rawCSV)); err != nil {
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
	return writeUserFileStream(path, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}

func writeUserFileStream(path string, write func(io.Writer) error) (retErr error) {
	return writeUserFileStreamWithSync(path, write, func(f *os.File) error { return f.Sync() })
}

func writeUserFileStreamWithSync(path string, write func(io.Writer) error, syncFile func(*os.File) error) (retErr error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	clean := filepath.Clean(path)
	if !safepath.Within(dir, clean) {
		return fmt.Errorf("refusing unsafe output path %q", path)
	}
	tmp, err := os.CreateTemp(dir, ".atl-export-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		if retErr != nil {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o644); err != nil {
		return err
	}
	if err := write(tmp); err != nil {
		return err
	}
	if err := syncFile(tmp); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, clean)
}
