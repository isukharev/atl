package app

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

type ConfluencePageResolution struct {
	ID              string `json:"id"`
	Kind            string `json:"kind"`
	Via             string `json:"via,omitempty"`
	NetworkRequests int    `json:"network_requests"`
	Space           string `json:"space,omitempty"`
	Title           string `json:"title,omitempty"`
}

// ResolvePageReference converts one numeric id or supported same-origin page
// URL into a stable content id. It never performs fuzzy title matching.
func (s *ConfluenceService) ResolvePageReference(ctx context.Context, reference string) (*ConfluencePageResolution, error) {
	return s.resolvePageReference(ctx, strings.TrimSpace(reference), true)
}

func (s *ConfluenceService) resolvePageReference(ctx context.Context, reference string, allowShort bool) (*ConfluencePageResolution, error) {
	if reference == "" {
		return nil, fmt.Errorf("%w: Confluence page reference is required", domain.ErrUsage)
	}
	if isOpaquePageID(reference) {
		return &ConfluencePageResolution{ID: reference, Kind: "id"}, nil
	}
	base, err := url.Parse(strings.TrimSpace(s.baseURL))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("%w: configured Confluence URL is required to resolve page URLs", domain.ErrConfig)
	}
	u, err := url.Parse(reference)
	if err != nil {
		return nil, fmt.Errorf("%w: malformed Confluence page reference", domain.ErrUsage)
	}
	if u.User != nil {
		return nil, fmt.Errorf("%w: Confluence page reference must not contain user information", domain.ErrUsage)
	}
	abs := u.IsAbs()
	if abs {
		if !sameConfluenceOrigin(base, u) {
			return nil, fmt.Errorf("%w: Confluence page reference is outside the configured origin", domain.ErrUsage)
		}
	} else if u.Host != "" || !strings.HasPrefix(u.Path, "/") {
		return nil, fmt.Errorf("%w: Confluence page reference must be a numeric id, absolute URL, or root-relative path", domain.ErrUsage)
	}
	refPath, ok := confluenceReferencePath(base, u, abs)
	if !ok {
		return nil, fmt.Errorf("%w: Confluence page reference is outside the configured context path", domain.ErrUsage)
	}
	if id, kind, directErr := directConfluencePageID(refPath, u.Query()); directErr != nil {
		return nil, directErr
	} else if id != "" {
		return &ConfluencePageResolution{ID: id, Kind: kind}, nil
	}
	if space, title, display := confluenceDisplayReference(refPath); display {
		query := `type = page AND space = "` + cqlQuoted(space) + `" AND title = "` + cqlQuoted(title) + `"`
		refs, next, searchErr := s.store.Search(ctx, query, 2, "")
		if searchErr != nil {
			return nil, searchErr
		}
		if len(refs) == 0 {
			return nil, fmt.Errorf("%w: Confluence display reference did not match a page", domain.ErrNotFound)
		}
		if len(refs) != 1 || next != "" {
			return nil, fmt.Errorf("%w: Confluence display reference is ambiguous", domain.ErrCheckFailed)
		}
		return &ConfluencePageResolution{ID: refs[0].ID, Kind: "display", NetworkRequests: 1, Space: space, Title: title}, nil
	}
	if isConfluenceShortReference(refPath) {
		if len(u.Query()) != 0 {
			return nil, fmt.Errorf("%w: Confluence short reference must not contain query parameters", domain.ErrUsage)
		}
		if !allowShort {
			return nil, fmt.Errorf("%w: Confluence short link resolved to another short link", domain.ErrCheckFailed)
		}
		resolver, ok := s.store.(domain.PageShortLinkResolver)
		if !ok {
			return nil, fmt.Errorf("%w: Confluence backend does not support short-link resolution", domain.ErrConfig)
		}
		u.Fragment = ""
		finalURL, resolveErr := resolver.ResolveShortPageLink(ctx, u.String())
		if resolveErr != nil {
			return nil, resolveErr
		}
		resolved, resolveErr := s.resolvePageReference(ctx, finalURL, false)
		if resolveErr != nil {
			return nil, fmt.Errorf("%w: Confluence short link did not resolve to a supported page URL: %v", domain.ErrCheckFailed, resolveErr)
		}
		resolved.Via = resolved.Kind
		resolved.Kind = "short"
		resolved.NetworkRequests++
		return resolved, nil
	}
	return nil, fmt.Errorf("%w: unsupported Confluence page reference", domain.ErrUsage)
}

func sameConfluenceOrigin(base, candidate *url.URL) bool {
	return strings.EqualFold(base.Scheme, candidate.Scheme) && strings.EqualFold(base.Host, candidate.Host)
}

func confluenceReferencePath(base, reference *url.URL, absolute bool) (string, bool) {
	cleaned := path.Clean("/" + strings.TrimPrefix(reference.EscapedPath(), "/"))
	contextPath := strings.TrimRight(path.Clean("/"+strings.TrimPrefix(base.EscapedPath(), "/")), "/")
	if contextPath == "." || contextPath == "/" {
		contextPath = ""
	}
	if contextPath != "" && strings.HasPrefix(cleaned, contextPath+"/") {
		return strings.TrimPrefix(cleaned, contextPath), true
	}
	if absolute && contextPath != "" {
		return "", false
	}
	return cleaned, true
}

func directConfluencePageID(refPath string, query url.Values) (string, string, error) {
	if refPath == "/pages/viewpage.action" {
		values := query["pageId"]
		if len(values) != 1 || !isDecimalID(values[0]) {
			return "", "", fmt.Errorf("%w: Confluence viewpage URL must contain exactly one numeric pageId", domain.ErrUsage)
		}
		return values[0], "viewpage", nil
	}
	segments := strings.Split(strings.Trim(refPath, "/"), "/")
	if len(segments) >= 4 && segments[0] == "spaces" && segments[1] != "" && segments[2] == "pages" && isDecimalID(segments[3]) {
		return segments[3], "canonical", nil
	}
	if len(segments) == 4 && segments[0] == "rest" && segments[1] == "api" && segments[2] == "content" && isDecimalID(segments[3]) {
		return segments[3], "rest", nil
	}
	return "", "", nil
}

func confluenceDisplayReference(refPath string) (string, string, bool) {
	segments := strings.Split(strings.Trim(refPath, "/"), "/")
	if len(segments) != 3 || segments[0] != "display" || segments[1] == "" || segments[2] == "" {
		return "", "", false
	}
	space, err := url.PathUnescape(segments[1])
	if err != nil {
		return "", "", false
	}
	title, err := url.PathUnescape(strings.ReplaceAll(segments[2], "+", " "))
	if err != nil || strings.TrimSpace(title) == "" {
		return "", "", false
	}
	return space, title, true
}

func isConfluenceShortReference(refPath string) bool {
	segments := strings.Split(strings.Trim(refPath, "/"), "/")
	if len(segments) != 2 || segments[0] != "x" || len(segments[1]) == 0 || len(segments[1]) > 128 {
		return false
	}
	for _, char := range segments[1] {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '_' && char != '-' {
			return false
		}
	}
	return true
}

func isDecimalID(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return value != "0"
}

func isOpaquePageID(value string) bool {
	if len(value) == 0 || len(value) > 256 {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '_' && char != '-' {
			return false
		}
	}
	return true
}

func cqlQuoted(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, `\`, `\\`), `"`, `\"`)
}
