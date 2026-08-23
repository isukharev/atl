package app

import (
	"bufio"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/strictjson"
)

const (
	jiraPlanSchemaVersion     = 2
	JiraPlanMaxDocumentBytes  = int64(16 << 20)
	JiraPlanMaxRows           = 100
	JiraPlanMaxFieldCellBytes = 8 << 20
	jiraPlanHeader            = "schema_version,operation,source,target,type,field,value"
)

type jiraPlanDocumentRow struct {
	row       int
	operation string
	source    string
	target    string
	typeName  string
	field     string
	value     string
	fieldJSON any
}

// JiraPlanDocument is an opaque, single-open normalized plan. Its mutable
// lifecycle state is deliberately unexported; callers can neither replace row
// storage nor alias encoding/csv's record buffer.
type JiraPlanDocument struct {
	mu               sync.Mutex
	rows             []jiraPlanDocumentRow
	normalizedSHA256 string
	command          string
	consumed         bool
}

// ReadJiraPlanDocument opens path exactly once and streams one strict schema-v2
// RFC 4180 document through a 16 MiB + 1 byte bound.
func ReadJiraPlanDocument(path string) (*JiraPlanDocument, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("%w: --csv is required", domain.ErrUsage)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, jiraPlanDocumentError("Jira plan document could not be opened", nil)
	}
	defer file.Close()

	limited := &io.LimitedReader{R: file, N: JiraPlanMaxDocumentBytes + 1}
	buffered := bufio.NewReader(limited)
	header, err := buffered.ReadString('\n')
	if err != nil || header != jiraPlanHeader+"\n" {
		return nil, jiraPlanDocumentError("CSV header must match schema version 2 exactly", err)
	}
	reader := csv.NewReader(buffered)
	reader.FieldsPerRecord = 7
	reader.ReuseRecord = false

	rows := make([]jiraPlanDocumentRow, 0, JiraPlanMaxRows)
	for {
		record, readErr := reader.Read()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, jiraPlanDocumentError("CSV row is malformed", readErr)
		}
		line, _ := reader.FieldPos(0)
		line++ // encoding/csv starts counting after the exact physical header.
		row, normalizeErr := normalizeJiraPlanDocumentRow(line, record)
		if normalizeErr != nil {
			return nil, normalizeErr
		}
		rows = append(rows, row)
		if len(rows) > JiraPlanMaxRows {
			return nil, jiraPlanDocumentError("CSV exceeds the 100-row limit", nil)
		}
	}
	if limited.N == 0 {
		return nil, jiraPlanDocumentError("CSV exceeds the 16 MiB limit", nil)
	}
	if len(rows) == 0 {
		return nil, jiraPlanDocumentError("CSV must contain at least one operation row", nil)
	}
	canonical, err := jiraPlanDocumentCanonicalBytes(rows)
	if err != nil {
		return nil, jiraPlanDocumentError("CSV normalization failed", err)
	}
	sum := sha256.Sum256(canonical)
	return &JiraPlanDocument{rows: rows, normalizedSHA256: hex.EncodeToString(sum[:])}, nil
}

func normalizeJiraPlanDocumentRow(rowNumber int, record []string) (jiraPlanDocumentRow, error) {
	if len(record) != 7 {
		return jiraPlanDocumentRow{}, jiraPlanDocumentError("CSV rows must contain exactly seven cells", nil)
	}
	for _, cell := range record {
		if !utf8.ValidString(cell) {
			return jiraPlanDocumentRow{}, jiraPlanDocumentError("CSV contains invalid UTF-8", nil)
		}
	}
	if record[0] != "2" {
		return jiraPlanDocumentRow{}, jiraPlanDocumentError("unsupported Jira plan schema version", nil)
	}
	row := jiraPlanDocumentRow{
		row: rowNumber, operation: strings.TrimSpace(record[1]),
		source:   strings.ToUpper(strings.TrimSpace(record[2])),
		target:   strings.ToUpper(strings.TrimSpace(record[3])),
		typeName: strings.TrimSpace(record[4]), field: strings.TrimSpace(record[5]),
		value: record[6],
	}
	if len(row.source) == 0 || len(row.source) > 64 || !domain.ValidJiraIssueKey(row.source) {
		return jiraPlanDocumentRow{}, jiraPlanDocumentError("source must be a canonical Jira issue key within 64 bytes", nil)
	}
	requireBlank := func(values ...string) bool {
		for _, value := range values {
			if strings.TrimSpace(value) != "" {
				return false
			}
		}
		return true
	}
	switch row.operation {
	case "link":
		if len(row.target) == 0 || len(row.target) > 64 || !domain.ValidJiraIssueKey(row.target) || row.target == row.source ||
			row.typeName == "" || len(row.typeName) > jiraGuardedLinkSelectorMaxBytes || !requireBlank(row.field, row.value) {
			return jiraPlanDocumentRow{}, jiraPlanDocumentError("link row has invalid source, target, type, field, or value", nil)
		}
	case "label_add", "label_remove":
		row.value = strings.TrimSpace(row.value)
		if !requireBlank(row.target, row.typeName, row.field) || row.value == "" || len(row.value) > jiraGuardedLabelMaxBytes ||
			strings.ContainsAny(row.value, "\x00\r\n") || !utf8.ValidString(row.value) {
			return jiraPlanDocumentRow{}, jiraPlanDocumentError("label row contains an invalid single label", nil)
		}
	case "comment":
		if !requireBlank(row.target, row.typeName, row.field) {
			return jiraPlanDocumentRow{}, jiraPlanDocumentError("comment row contains nonblank reserved cells", nil)
		}
		body, err := ValidateJiraCommentBody([]byte(row.value))
		if err != nil {
			return jiraPlanDocumentRow{}, err
		}
		row.value = string(body)
	case "field":
		if !requireBlank(row.target, row.typeName) || !domain.ValidJiraGuardedFieldID(row.field) || domain.JiraGuardedFieldReserved(row.field) ||
			len(row.value) > JiraPlanMaxFieldCellBytes {
			return jiraPlanDocumentRow{}, jiraPlanDocumentError("field row contains an invalid field id or oversized value", nil)
		}
		decoded, err := strictjson.DecodeValue([]byte(row.value))
		if err != nil {
			return jiraPlanDocumentRow{}, jiraPlanDocumentError("field value must be one strict JSON value", err)
		}
		switch decoded.(type) {
		case string, map[string]any, []any:
		default:
			return jiraPlanDocumentRow{}, jiraPlanDocumentError("field value must be a top-level string, object, or array", nil)
		}
		if !domain.JiraGuardedFieldValueWithinNestingBound(decoded) {
			return jiraPlanDocumentRow{}, jiraPlanDocumentError("field value exceeds the guarded nesting bound", nil)
		}
		row.fieldJSON = cloneGuardedFieldValue(decoded)
	default:
		return jiraPlanDocumentRow{}, jiraPlanDocumentError("CSV operation is not supported by schema version 2", nil)
	}
	return row, nil
}

func jiraPlanDocumentCanonicalBytes(rows []jiraPlanDocumentRow) ([]byte, error) {
	type projection struct {
		SchemaVersion int    `json:"schema_version"`
		Row           int    `json:"row"`
		Operation     string `json:"operation"`
		Source        string `json:"source"`
		Target        string `json:"target"`
		Type          string `json:"type"`
		Field         string `json:"field"`
		ValueSHA256   string `json:"value_sha256"`
		ValueBytes    int    `json:"value_bytes"`
	}
	out := make([]projection, len(rows))
	for i, row := range rows {
		out[i] = projection{jiraPlanSchemaVersion, row.row, row.operation, row.source, row.target, row.typeName, row.field, sha256Hex([]byte(row.value)), len(row.value)}
	}
	return jsonMarshalCanonical(out)
}

func jsonMarshalCanonical(value any) ([]byte, error) { return json.Marshal(value) }

func jiraPlanDocumentError(message string, cause error) error {
	return fmt.Errorf("%w: %s%v", domain.ErrUsage, message, contentFreeCauseSuffix(cause))
}

func contentFreeCauseSuffix(err error) string {
	if err == nil {
		return ""
	}
	return ": invalid encoding"
}

// BindJiraPlanDocument fixes the single CLI leaf allowed to consume document.
func BindJiraPlanDocument(document *JiraPlanDocument, command string) error {
	if document == nil {
		return fmt.Errorf("%w: Jira plan document is required", domain.ErrCheckFailed)
	}
	if command != "preview" && command != "apply" {
		return fmt.Errorf("%w: invalid Jira plan command", domain.ErrCheckFailed)
	}
	document.mu.Lock()
	defer document.mu.Unlock()
	if len(document.rows) == 0 || document.normalizedSHA256 == "" || document.command != "" || document.consumed {
		return fmt.Errorf("%w: Jira plan document lifecycle is invalid", domain.ErrCheckFailed)
	}
	document.command = command
	return nil
}

// JiraPlanDocumentPolicyRequests returns copied syntax-only requests and never
// exposes normalized row storage.
func JiraPlanDocumentPolicyRequests(document *JiraPlanDocument, command string) ([]domain.WriteAuthorizationRequest, error) {
	if document == nil {
		return nil, fmt.Errorf("%w: Jira plan document is required", domain.ErrCheckFailed)
	}
	document.mu.Lock()
	defer document.mu.Unlock()
	if document.command != command || document.consumed || len(document.rows) == 0 {
		return nil, fmt.Errorf("%w: Jira plan document lifecycle is invalid", domain.ErrCheckFailed)
	}
	requests := make([]domain.WriteAuthorizationRequest, 0, len(document.rows))
	for _, row := range document.rows {
		verb := domain.WriteVerbUpdate
		kind := "issue"
		if row.operation == "comment" {
			verb = domain.WriteVerbComment
		}
		targets := jiraPlanRequestedTargets(row.source, kind)
		if row.operation == "link" {
			kind = "link"
			targets = append(jiraPlanRequestedTargets(row.source, kind), jiraPlanRequestedTargets(row.target, kind)...)
		}
		requests = append(requests, domain.WriteAuthorizationRequest{Verbs: domain.WriteVerbSet{verb}, Targets: targets})
	}
	return cloneJiraPlanAuthorization(requests), nil
}

func jiraPlanRequestedTargets(key, kind string) []domain.WriteTarget {
	project := key[:strings.IndexByte(key, '-')]
	return []domain.WriteTarget{{Service: "jira", Kind: kind, Key: key, Project: project}}
}

func cloneJiraPlanAuthorization(input []domain.WriteAuthorizationRequest) []domain.WriteAuthorizationRequest {
	out := make([]domain.WriteAuthorizationRequest, len(input))
	for i, request := range input {
		out[i] = request
		out[i].Verbs = append(domain.WriteVerbSet(nil), request.Verbs...)
		out[i].Targets = append([]domain.WriteTarget(nil), request.Targets...)
		for j := range out[i].Targets {
			out[i].Targets[j].AncestorIDs = append([]string(nil), request.Targets[j].AncestorIDs...)
		}
	}
	return out
}
