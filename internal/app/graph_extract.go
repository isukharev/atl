package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

const (
	graphWalkMaxDepth    = 12
	graphWalkMaxStrings  = 20_000
	graphWalkMaxArray    = 100
	graphWalkMaxObject   = 256
	graphWalkMaxFields   = 4_096
	graphPointerMaxBytes = 1 << 10
	graphURLMaxBytes     = 2 << 10
	graphLabelMaxRunes   = 160
)

var (
	graphURLPattern                = regexp.MustCompile(`https?://[^\s<>"')\]]+`)
	graphJiraKeyPattern            = regexp.MustCompile(`[A-Z][A-Z0-9_]{1,31}-[1-9][0-9]*`)
	graphPageIDPattern             = regexp.MustCompile(`(?i)\bpageId(?:=|\s+)([1-9][0-9]*)\b`)
	graphBrowsePathPattern         = regexp.MustCompile(`(?i)(?:^|/)browse/([A-Z][A-Z0-9_]{1,31}-[1-9][0-9]*)(?:$|/)`)
	graphConfluencePagePathPattern = regexp.MustCompile(`(?i)(?:^|/)(?:pages|content)/([1-9][0-9]*)(?:$|/)`)
	graphNumericIDPattern          = regexp.MustCompile(`^[1-9][0-9]{0,31}$`)
	graphCustomFieldIDPattern      = regexp.MustCompile(`^customfield_[1-9][0-9]*$`)
)

var graphSkippedPathKeys = map[string]bool{
	"self": true, "avatarurls": true, "avatarurl": true, "iconurl": true,
	"iconurls": true, "thumbnail": true, "content": true, "watches": true,
	"watchers": true, "author": true, "creator": true, "reporter": true,
	"assignee": true, "updatedby": true, "updateauthor": true,
	"email": true, "emailaddress": true, "timezone": true,
	"icon": true, "avatar": true, "user": true, "users": true,
	"owner": true, "owners": true, "group": true, "groups": true,
	"account": true, "accountid": true, "profile": true, "profileurl": true,
	"picture": true, "photo": true,
}

type graphReference struct {
	Node       domain.ArtifactGraphNode
	Extraction string
	Confidence string
}

type graphExtractBudget struct {
	MaxBytes int
	Bytes    int
	Strings  int
	Clipped  bool
}

func (b *graphExtractBudget) consume(size int) bool {
	if b == nil || b.Clipped || size < 0 || b.Bytes > b.MaxBytes-size {
		if b != nil {
			b.Clipped = true
		}
		return false
	}
	b.Bytes += size
	return true
}

func walkGraphValue(value any, pointer string, allowBare bool, budget *graphExtractBudget, visit func(any, string, bool)) {
	walkGraphValueWithKeyPolicy(value, pointer, allowBare, true, budget, visit)
}

func walkInverseReferenceValue(value any, pointer string, allowBare bool, budget *graphExtractBudget, visit func(any, string, bool)) {
	walkGraphValueWithKeyPolicy(value, pointer, allowBare, false, budget, visit)
}

func walkGraphValueWithKeyPolicy(value any, pointer string, allowBare, skipExcludedKeys bool, budget *graphExtractBudget, visit func(any, string, bool)) {
	var walk func(any, string, int)
	walk = func(current any, currentPointer string, depth int) {
		if budget.Clipped {
			return
		}
		if depth > graphWalkMaxDepth || len(currentPointer) > graphPointerMaxBytes {
			budget.Clipped = true
			return
		}
		switch typed := current.(type) {
		case string:
			if budget.Strings >= graphWalkMaxStrings || !budget.consume(len(typed)+2) {
				budget.Clipped = true
				return
			}
			budget.Strings++
			visit(typed, currentPointer, allowBare)
		case json.Number:
			text := typed.String()
			if !budget.consume(len(text)) {
				return
			}
			visit(typed, currentPointer, false)
		case float64:
			if !budget.consume(len(strconv.FormatFloat(typed, 'g', -1, 64))) {
				return
			}
			visit(typed, currentPointer, false)
		case bool:
			if typed {
				budget.consume(4)
			} else {
				budget.consume(5)
			}
			visit(typed, currentPointer, false)
		case nil:
			budget.consume(4)
			visit(nil, currentPointer, false)
		case []any:
			if len(typed) > graphWalkMaxArray || !budget.consume(2+len(typed)) {
				budget.Clipped = true
				return
			}
			for index, item := range typed {
				childPointer := currentPointer + "/" + fmt.Sprint(index)
				if len(childPointer) > graphPointerMaxBytes {
					budget.Clipped = true
					return
				}
				walk(item, childPointer, depth+1)
			}
		case map[string]any:
			if len(typed) > graphWalkMaxObject || !budget.consume(2+len(typed)) {
				budget.Clipped = true
				return
			}
			keys := make([]string, 0, len(typed))
			for key := range typed {
				if !budget.consume(len(key) + 3) {
					return
				}
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				if skipExcludedKeys && graphSkippedPathKeys[strings.ToLower(key)] {
					continue
				}
				escapedKey := escapeJSONPointer(graphSafePointerToken(key))
				if len(currentPointer)+1+len(escapedKey) > graphPointerMaxBytes {
					budget.Clipped = true
					return
				}
				walk(typed[key], currentPointer+"/"+escapedKey, depth+1)
			}
		default:
			budget.Clipped = true
		}
	}
	walk(value, pointer, 0)
}

func extractGraphReferences(text, jiraBase, confluenceBase string, allowBare bool) []graphReference {
	seen := map[string]bool{}
	out := []graphReference{}
	bareText := []byte(text)
	add := func(reference graphReference) {
		key := reference.Node.ID + "\x00" + reference.Extraction
		if reference.Node.ID == "" || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, reference)
	}
	for _, span := range graphURLPattern.FindAllStringIndex(text, -1) {
		raw := text[span[0]:span[1]]
		raw = strings.TrimRight(raw, ".,;:")
		for index := span[0]; index < span[1]; index++ {
			bareText[index] = ' '
		}
		if reference, ok := normalizeGraphURL(raw, jiraBase, confluenceBase); ok {
			add(reference)
		} else {
			add(graphReference{
				Node: domain.ArtifactGraphNode{
					ID:        candidateGraphID("url", raw),
					Kind:      "url",
					Service:   "external",
					State:     domain.ArtifactNodeUnresolved,
					Depth:     1,
					Stability: domain.ArtifactStabilityHeuristic,
				},
				Extraction: "absolute_url",
				Confidence: "candidate",
			})
		}
	}
	if allowBare {
		for _, span := range graphJiraKeyPattern.FindAllIndex(bareText, -1) {
			if (span[0] > 0 && graphASCIIWordByte(bareText[span[0]-1])) ||
				(span[1] < len(bareText) && graphASCIIWordByte(bareText[span[1]])) {
				continue
			}
			key := strings.ToUpper(string(bareText[span[0]:span[1]]))
			add(graphReference{
				Node: domain.ArtifactGraphNode{
					ID:         "jira:issue:" + key,
					Kind:       "jira_issue",
					Service:    "jira",
					ExternalID: key,
					State:      domain.ArtifactNodeUnresolved,
					Depth:      1,
					Stability:  domain.ArtifactStabilityHeuristic,
				},
				Extraction: "jira_key",
				Confidence: "candidate",
			})
		}
		for _, match := range graphPageIDPattern.FindAllStringSubmatch(string(bareText), -1) {
			id := match[1]
			add(graphReference{
				Node: domain.ArtifactGraphNode{
					ID:         "confluence:page:" + id,
					Kind:       "confluence_page",
					Service:    "confluence",
					ExternalID: id,
					State:      domain.ArtifactNodeStub,
					Depth:      1,
					Stability:  domain.ArtifactStabilityHeuristic,
				},
				Extraction: "confluence_page_id",
				Confidence: "high",
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Node.ID != out[j].Node.ID {
			return out[i].Node.ID < out[j].Node.ID
		}
		return out[i].Extraction < out[j].Extraction
	})
	return out
}

func graphASCIIWordByte(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z' ||
		value >= '0' && value <= '9' || value == '_'
}

func normalizeGraphURL(raw, jiraBase, confluenceBase string) (graphReference, bool) {
	if len(raw) > graphURLMaxBytes {
		return graphReference{}, false
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Hostname() == "" || parsed.User != nil {
		return graphReference{}, false
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	host := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if port != "" && (parsed.Scheme != "https" || port != "443") && (parsed.Scheme != "http" || port != "80") {
		parsed.Host = net.JoinHostPort(host, port)
	} else {
		parsed.Host = host
	}
	parsed.Fragment = ""
	cleanedPath := path.Clean("/" + strings.TrimPrefix(parsed.EscapedPath(), "/"))
	if strings.HasSuffix(parsed.Path, "/") && cleanedPath != "/" {
		cleanedPath += "/"
	}
	decodedPath, err := url.PathUnescape(cleanedPath)
	if err != nil {
		return graphReference{}, false
	}
	parsed.RawPath = cleanedPath
	parsed.Path = decodedPath
	confluenceQueryPageID := graphQueryPageID(parsed.Query())
	if parsed.RawQuery != "" {
		parsed.RawQuery = "redacted=redacted"
	}
	safe := parsed.String()
	if len(safe) > graphURLMaxBytes {
		return graphReference{}, false
	}
	sensitivePath := graphSensitivePath(decodedPath)
	if sameGraphOrigin(parsed, jiraBase) {
		if match := graphBrowsePathPattern.FindStringSubmatch(parsed.Path); match != nil {
			key := strings.ToUpper(match[1])
			if sensitivePath {
				safe = ""
			}
			return graphReference{
				Node: domain.ArtifactGraphNode{
					ID: "jira:issue:" + key, Kind: "jira_issue", Service: "jira",
					ExternalID: key, URL: safe, State: domain.ArtifactNodeStub, Depth: 1,
					Stability: domain.ArtifactStabilityPublicAPI,
				},
				Extraction: "service_url", Confidence: "exact",
			}, true
		}
	}
	if sameGraphOrigin(parsed, confluenceBase) {
		if id := graphConfluencePageID(parsed, confluenceQueryPageID); id != "" {
			if sensitivePath {
				safe = ""
			}
			return graphReference{
				Node: domain.ArtifactGraphNode{
					ID: "confluence:page:" + id, Kind: "confluence_page", Service: "confluence",
					ExternalID: id, URL: safe, State: domain.ArtifactNodeStub, Depth: 1,
					Stability: domain.ArtifactStabilityPublicAPI,
				},
				Extraction: "service_url", Confidence: "exact",
			}, true
		}
	}
	if sensitivePath {
		return opaqueGraphURLReference(raw), true
	}
	return graphReference{
		Node: domain.ArtifactGraphNode{
			ID: "url:" + graphHash(safe), Kind: "url", Service: "external",
			URL: safe, State: domain.ArtifactNodeStub, Depth: 1,
			Stability: domain.ArtifactStabilityHeuristic,
		},
		Extraction: "absolute_url", Confidence: "high",
	}, true
}

func opaqueGraphURLReference(raw string) graphReference {
	return graphReference{
		Node: domain.ArtifactGraphNode{
			ID:        candidateGraphID("url", raw),
			Kind:      "url",
			Service:   "external",
			State:     domain.ArtifactNodeUnresolved,
			Depth:     1,
			Stability: domain.ArtifactStabilityHeuristic,
		},
		Extraction: "absolute_url",
		Confidence: "candidate",
	}
}

func graphSensitivePath(value string) bool {
	for _, segment := range strings.Split(value, "/") {
		if segment == "" {
			continue
		}
		lower := strings.ToLower(segment)
		for _, marker := range []string{
			"access_token", "apikey", "api_key", "auth", "credential", "jwt",
			"password", "passwd", "secret", "session", "signature", "ticket", "token",
		} {
			if strings.Contains(lower, marker) {
				return true
			}
		}
		if len(segment) >= 24 {
			return true
		}
	}
	return false
}

func graphQueryPageID(query url.Values) string {
	pageID := ""
	for key, values := range query {
		if !strings.EqualFold(key, "pageId") {
			continue
		}
		if len(values) != 1 || !graphNumericIDPattern.MatchString(values[0]) || pageID != "" {
			return ""
		}
		pageID = values[0]
	}
	return pageID
}

func graphConfluencePageID(parsed *url.URL, queryPageID string) string {
	if queryPageID != "" {
		return queryPageID
	}
	if match := graphConfluencePagePathPattern.FindStringSubmatch(parsed.Path); match != nil {
		return match[1]
	}
	return ""
}

func sameGraphOrigin(parsed *url.URL, base string) bool {
	if strings.TrimSpace(base) == "" {
		return false
	}
	other, err := url.Parse(base)
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Scheme, other.Scheme) &&
		strings.EqualFold(parsed.Hostname(), other.Hostname()) &&
		effectiveURLPort(parsed) == effectiveURLPort(other)
}

func effectiveURLPort(value *url.URL) string {
	if port := value.Port(); port != "" {
		return port
	}
	if strings.EqualFold(value.Scheme, "https") {
		return "443"
	}
	return "80"
}

func candidateGraphID(kind, value string) string {
	return "candidate:" + kind + ":" + graphHash(value)
}

func graphHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func graphBoundedLabel(value string) string {
	value = strings.TrimSpace(strings.ToValidUTF8(value, "�"))
	runes := []rune(value)
	if len(runes) > graphLabelMaxRunes {
		value = string(runes[:graphLabelMaxRunes])
	}
	return value
}

func graphSafePointerToken(value string) string {
	if oneOf(strings.ToLower(value),
		"attachment", "body", "comment", "comments", "description", "fields",
		"issuelinks", "parent", "pageid", "properties", "remote_links",
		"subtasks", "summary", "worklog", "worklogs") {
		return value
	}
	return "opaque-" + graphHash(value)
}

func graphSafeFieldToken(value string) string {
	lower := strings.ToLower(value)
	if graphCustomFieldIDPattern.MatchString(lower) || oneOf(lower,
		"attachment", "comment", "components", "creator", "description",
		"environment", "fixversions", "issuelinks", "issuetype", "labels",
		"parent", "priority", "project", "reporter", "resolution", "status",
		"subtasks", "summary", "versions", "worklog") {
		return value
	}
	return "opaque-" + graphHash(value)
}

func graphPointerLeaf(pointer string) string {
	index := strings.LastIndexByte(pointer, '/')
	if index < 0 || index == len(pointer)-1 {
		return ""
	}
	return strings.ReplaceAll(strings.ReplaceAll(pointer[index+1:], "~1", "/"), "~0", "~")
}

func escapeJSONPointer(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "~", "~0"), "/", "~1")
}
