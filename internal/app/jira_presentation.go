package app

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

func markdownTableCell(value string) string {
	value = strings.ReplaceAll(value, "\r\n", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "&", "&amp;")
	value = strings.ReplaceAll(value, "<", "&lt;")
	value = strings.ReplaceAll(value, ">", "&gt;")
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "|", `\|`)
	return strings.TrimSpace(value)
}

// snapshotText normalizes Jira values for compact list/table cells without
// leaking raw transport objects or URLs.
func snapshotText(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case json.Number:
		return v.String()
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(v), 'f', -1, 32)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case bool:
		return strconv.FormatBool(v)
	case []any:
		parts := make([]string, 0, len(v))
		for _, entry := range v {
			if text := snapshotText(entry); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, ", ")
	case map[string]any:
		for _, key := range []string{"displayName", "name", "value", "key", "label", "text", "id"} {
			if text := snapshotText(v[key]); text != "" {
				return text
			}
		}
		if len(v) > 0 {
			return "[object]"
		}
		return ""
	default:
		return fmt.Sprint(v)
	}
}

func normalizedSnapshotValue(value any) any {
	switch value.(type) {
	case map[string]any, []any:
		return snapshotText(value)
	default:
		return value
	}
}
