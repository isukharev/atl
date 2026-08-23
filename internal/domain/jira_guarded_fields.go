package domain

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

const (
	JiraGuardedFieldMaxCatalogEntries    = 4096
	JiraGuardedFieldMaxSelected          = 1024
	JiraGuardedFieldMaxAllowlist         = 1024
	JiraGuardedFieldMaxIDBytes           = 1024
	JiraGuardedFieldMaxRequestedKeyBytes = 64
	JiraGuardedFieldMaxImmutableIDBytes  = 64
	JiraGuardedFieldMaxJSONNestingDepth  = 10_000
	// Three released-result containers surround a structured desired value:
	// the result object, fields array, and selected-field object. This leaves
	// the exact value ceiling below encoding/json's 10,000-container limit.
	JiraGuardedFieldMaxValueNestingDepth    = JiraGuardedFieldMaxJSONNestingDepth - 3
	JiraGuardedFieldMaxCatalogResponseBytes = int64(16 << 20)
	JiraGuardedFieldMaxIssueResponseBytes   = int64(64 << 20)
	JiraGuardedFieldMaxInputBytes           = int64(64 << 20)
	JiraGuardedFieldMaxDesiredBytes         = int64(64 << 20)
	JiraGuardedFieldMaxCurrentBytes         = int64(64 << 20)
	JiraGuardedFieldMaxPreparedBytes        = int64(64 << 20)
	JiraGuardedFieldMaxQueryAndPathBytes    = 64 << 10
	JiraGuardedFieldMaxWriteResponseBytes   = int64(1 << 20)
	JiraGuardedFieldPreviewMaxRequests      = 2
	JiraGuardedFieldApplyMaxRequests        = 6
	JiraGuardedFieldPreviewMaxResponseBytes = int64(80 << 20)
	JiraGuardedFieldApplyMaxResponseBytes   = int64(225 << 20)
	JiraGuardedFieldDeadlineMillis          = int64(60_000)
)

type jiraGuardedValueDepthFrame struct {
	value  any
	array  []any
	object map[string]any
	keys   []string
	index  int
	depth  int
	kind   byte
}

// JiraGuardedFieldValueWithinNestingBound validates the dynamic JSON types and
// exact value depth shared by the app and adapter. Its stack expands one child
// at a time, so flat arrays do not create an element-count-sized work stack.
func JiraGuardedFieldValueWithinNestingBound(value any) bool {
	stack := []jiraGuardedValueDepthFrame{{value: value}}
	for len(stack) > 0 {
		last := len(stack) - 1
		frame := &stack[last]
		if frame.kind == '[' {
			if frame.index == len(frame.array) {
				stack = stack[:last]
				continue
			}
			member := frame.array[frame.index]
			frame.index++
			stack = append(stack, jiraGuardedValueDepthFrame{value: member, depth: frame.depth})
			continue
		}
		if frame.kind == '{' {
			if frame.index == len(frame.keys) {
				stack = stack[:last]
				continue
			}
			member := frame.object[frame.keys[frame.index]]
			frame.index++
			stack = append(stack, jiraGuardedValueDepthFrame{value: member, depth: frame.depth})
			continue
		}
		switch typed := frame.value.(type) {
		case nil, bool, json.Number, float64, string:
			stack = stack[:last]
		case []any:
			depth := frame.depth + 1
			if depth > JiraGuardedFieldMaxValueNestingDepth {
				return false
			}
			*frame = jiraGuardedValueDepthFrame{array: typed, depth: depth, kind: '['}
		case map[string]any:
			depth := frame.depth + 1
			if depth > JiraGuardedFieldMaxValueNestingDepth {
				return false
			}
			keys := make([]string, 0, len(typed))
			for key := range typed {
				keys = append(keys, key)
			}
			*frame = jiraGuardedValueDepthFrame{object: typed, keys: keys, depth: depth, kind: '{'}
		default:
			return false
		}
	}
	return true
}

var jiraGuardedReservedFields = map[string]struct{}{
	"project": {}, "issuetype": {}, "summary": {}, "description": {},
	"labels": {}, "assignee": {},
}

// JiraGuardedFieldReserved reports whether an identifier belongs to a field
// with a dedicated mutation workflow. Catalog metadata cannot override it.
func JiraGuardedFieldReserved(identifier string) bool {
	_, reserved := jiraGuardedReservedFields[strings.ToLower(strings.TrimSpace(identifier))]
	return reserved
}

// ValidJiraGuardedFieldID validates one exact, already-trimmed field id.
func ValidJiraGuardedFieldID(identifier string) bool {
	return identifier != "" && identifier == strings.TrimSpace(identifier) &&
		len(identifier) <= JiraGuardedFieldMaxIDBytes && utf8.ValidString(identifier) &&
		!strings.ContainsAny(identifier, "\x00\r\n")
}

type JiraGuardedFieldCatalogEntry struct {
	ID     string
	Custom bool
}

// JiraGuardedFieldCatalog is a complete bounded qualification. Fields contains
// only the selected content-free rows, sorted by id.
type JiraGuardedFieldCatalog struct {
	Fields   []JiraGuardedFieldCatalogEntry
	Complete bool
}

type JiraGuardedFieldEvidence struct {
	Present bool
	Value   any
}

type JiraGuardedFieldIssue struct {
	ID       string
	Key      string
	Project  string
	Updated  string
	Fields   map[string]JiraGuardedFieldEvidence
	Complete bool
}

type JiraGuardedFieldPreparedProjection struct {
	FieldID string
	Kind    string
	Bytes   int
	SHA256  string
}

type JiraGuardedFieldPreparationRequest struct {
	Values    map[string]any
	Qualified []JiraGuardedFieldCatalogEntry
}

// JiraGuardedFieldPreparation is immutable adapter-owned wire evidence.
type JiraGuardedFieldPreparation struct {
	Payload []byte
	Fields  []JiraGuardedFieldPreparedProjection
}

type JiraGuardedFieldWrite struct {
	ID        string
	Key       string
	Project   string
	Qualified []JiraGuardedFieldCatalogEntry
	Prepared  JiraGuardedFieldPreparation
}

// JiraGuardedFieldPort is intentionally separate from Tracker. Guarded field
// commands never fall back to broad catalog, issue, or field-write methods.
type JiraGuardedFieldPort interface {
	ReadGuardedFieldCatalog(context.Context, []string) (JiraGuardedFieldCatalog, error)
	ReadGuardedFieldIssue(context.Context, string, []string) (JiraGuardedFieldIssue, error)
	PrepareGuardedFields(JiraGuardedFieldPreparationRequest) (JiraGuardedFieldPreparation, error)
	WriteGuardedFields(context.Context, JiraGuardedFieldWrite) error
}
