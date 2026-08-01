package confluence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

const legacyIdentityBodyCap = 256 << 10

var (
	legacyHeadOpenPattern        = regexp.MustCompile(`(?is)<head(?:\s[^>]{0,1000})?>`)
	legacyMetaTagPattern         = regexp.MustCompile(`(?is)<meta\s+[^>]{1,1000}>`)
	legacyDoubleAttributePattern = regexp.MustCompile(`(?i)([a-z][a-z0-9_-]{0,63})\s*=\s*"([^"]{0,255})"`)
	legacySingleAttributePattern = regexp.MustCompile(`(?i)([a-z][a-z0-9_-]{0,63})\s*=\s*'([^']{0,255})'`)
)

// ServerMetadata reads Confluence's product/version endpoint and projects only
// its version. A legacy deployment that lacks that endpoint is qualified by one
// body-free content-collection probe. Product is static so no backend-controlled
// product or deployment text crosses this adapter boundary.
func (cf *Confluence) ServerMetadata(ctx context.Context) (domain.ServerMetadata, error) {
	metadata, err := cf.modernServerMetadata(ctx)
	if err == nil {
		return metadata, nil
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return domain.ServerMetadata{}, err
	}
	if _, headErr := cf.c.Do(ctx, http.MethodHead, "/rest/api/content", nil, nil); headErr != nil {
		return domain.ServerMetadata{}, headErr
	}
	return domain.ServerMetadata{Product: domain.ServerProductConfluence}, nil
}

func (cf *Confluence) modernServerMetadata(ctx context.Context) (domain.ServerMetadata, error) {
	var response struct {
		Version     string          `json:"version"`
		BuildNumber json.RawMessage `json:"buildNumber"`
	}
	if err := cf.c.GetJSON(ctx, "/rest/api/server-information", &response); err != nil {
		return domain.ServerMetadata{}, err
	}
	buildNumber, _ := confluenceBuildNumber(response.BuildNumber)
	return domain.ServerMetadata{Product: domain.ServerProductConfluence, Version: response.Version, BuildNumber: buildNumber}, nil
}

// ExactServerMetadata prefers the documented modern endpoint and falls back,
// only on a typed 404, to the bounded product metadata embedded in the legacy
// root HTML head. The parser projects two numeric identifiers and discards all
// other HTML. A single-attempt context prevents redirects and retries across
// both routes.
func (cf *Confluence) ExactServerMetadata(ctx context.Context) (domain.ServerMetadata, error) {
	metadata, err := cf.modernServerMetadata(ctx)
	if err == nil {
		if metadata.Version == "" || metadata.BuildNumber == "" {
			return domain.ServerMetadata{}, fmt.Errorf("%w: Confluence exact product identity is unavailable", domain.ErrCheckFailed)
		}
		return metadata, nil
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return domain.ServerMetadata{}, err
	}
	body, readErr := cf.c.DoWithBodyLimit(ctx, http.MethodGet, "/", nil, map[string]string{"Accept": "text/html"}, legacyIdentityBodyCap)
	if readErr != nil {
		return domain.ServerMetadata{}, readErr
	}
	version, buildNumber := legacyConfluenceIdentity(body)
	if version == "" || buildNumber == "" {
		return domain.ServerMetadata{}, fmt.Errorf("%w: Confluence exact product identity is unavailable", domain.ErrCheckFailed)
	}
	return domain.ServerMetadata{Product: domain.ServerProductConfluence, Version: version, BuildNumber: buildNumber}, nil
}

func legacyConfluenceIdentity(body []byte) (version, buildNumber string) {
	headMatches := legacyHeadOpenPattern.FindAllIndex(body, 2)
	if len(headMatches) != 1 {
		return "", ""
	}
	headStart := headMatches[0][1]
	headTail := bytes.ToLower(body[headStart:])
	headEndRelative := bytes.Index(headTail, []byte("</head>"))
	if headEndRelative < 0 {
		return "", ""
	}
	headEnd := headStart + headEndRelative
	var versionSeen, buildSeen bool
	for _, tag := range legacyMetaTagPattern.FindAll(body[headStart:headEnd], -1) {
		attributes := map[string]string{}
		attributesValid := true
		for _, pattern := range []*regexp.Regexp{legacyDoubleAttributePattern, legacySingleAttributePattern} {
			for _, match := range pattern.FindAllSubmatch(tag, -1) {
				name := strings.ToLower(string(match[1]))
				if _, duplicate := attributes[name]; duplicate {
					attributesValid = false
					break
				}
				attributes[name] = string(bytes.TrimSpace(match[2]))
			}
		}
		if !attributesValid {
			return "", ""
		}
		switch strings.ToLower(attributes["name"]) {
		case "ajs-version-number":
			if versionSeen {
				return "", ""
			}
			version = attributes["content"]
			versionSeen = true
		case "ajs-build-number":
			if buildSeen {
				return "", ""
			}
			buildNumber = attributes["content"]
			buildSeen = true
		}
	}
	return version, buildNumber
}

func confluenceBuildNumber(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	value := strings.TrimSpace(string(raw))
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		var decoded string
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return "", fmt.Errorf("%w: Confluence build identity is malformed", domain.ErrCheckFailed)
		}
		value = decoded
	}
	if len(value) == 0 || len(value) > 20 {
		return "", fmt.Errorf("%w: Confluence build identity is malformed", domain.ErrCheckFailed)
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return "", fmt.Errorf("%w: Confluence build identity is malformed", domain.ErrCheckFailed)
		}
	}
	return value, nil
}
