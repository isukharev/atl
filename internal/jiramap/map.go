// Package jiramap maps transport-neutral Jira snapshot fields into domain
// issues. It contains no HTTP or filesystem behavior, so live adapters and
// offline use cases can share identical defensive mapping semantics.
package jiramap

import (
	"encoding/json"
	"fmt"
	"maps"
	"strconv"

	"github.com/isukharev/atl/internal/domain"
)

// Issue builds a domain.Issue from a raw Jira fields map. Server-controlled
// missing or oddly typed values degrade to zero values rather than panicking.
func Issue(id, key string, fields map[string]any) *domain.Issue {
	is := &domain.Issue{ID: id, Key: key, Fields: fields, Raw: maps.Clone(fields), FieldText: map[string]string{}}
	if fields == nil {
		return is
	}
	is.Summary = str(fields["summary"])
	is.Body = str(fields["description"])
	is.Status = nestedName(fields["status"])
	is.Type = nestedName(fields["issuetype"])
	is.Project = nestedKey(fields["project"])
	is.Assignee = nestedDisplay(fields["assignee"])
	is.Reporter = nestedDisplay(fields["reporter"])
	if labels, ok := fields["labels"].([]any); ok {
		for _, label := range labels {
			is.Labels = append(is.Labels, str(label))
		}
	}
	if links, ok := fields["issuelinks"].([]any); ok {
		for _, raw := range links {
			link, _ := raw.(map[string]any)
			id := str(link["id"])
			typeName := ""
			if linkType, ok := link["type"].(map[string]any); ok {
				typeName = str(linkType["name"])
			}
			if inward, ok := link["inwardIssue"].(map[string]any); ok {
				is.Links = append(is.Links, domain.IssueLink{ID: id, Type: str(typeField(link["type"], "inward")), TypeName: typeName, Direction: "inward", Key: str(inward["key"])})
			}
			if outward, ok := link["outwardIssue"].(map[string]any); ok {
				is.Links = append(is.Links, domain.IssueLink{ID: id, Type: str(typeField(link["type"], "outward")), TypeName: typeName, Direction: "outward", Key: str(outward["key"])})
			}
		}
	}
	if comments, ok := fields["comment"].(map[string]any); ok {
		if values, ok := comments["comments"].([]any); ok {
			for _, raw := range values {
				comment, _ := raw.(map[string]any)
				is.Comments = append(is.Comments, domain.Comment{
					ID: str(comment["id"]), Author: nestedDisplay(comment["author"]),
					Created: str(comment["created"]), Body: str(comment["body"]),
				})
			}
		}
	}
	return is
}

func str(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64)
	case json.Number:
		return value.String()
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", value)
	}
}

func nestedName(value any) string {
	if object, ok := value.(map[string]any); ok {
		return str(object["name"])
	}
	return ""
}

func nestedKey(value any) string {
	if object, ok := value.(map[string]any); ok {
		return str(object["key"])
	}
	return ""
}

func nestedDisplay(value any) string {
	if object, ok := value.(map[string]any); ok {
		if display := str(object["displayName"]); display != "" {
			return display
		}
		return str(object["name"])
	}
	return ""
}

func typeField(value any, field string) any {
	if object, ok := value.(map[string]any); ok {
		if phrase := str(object[field]); phrase != "" {
			return phrase
		}
		return str(object["name"])
	}
	return ""
}
