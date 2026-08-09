// Package scmref owns transport-neutral source-control reference identities.
package scmref

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

const (
	maxGitLabURLBytes     = 2048
	maxGitLabProjectBytes = 2048
	maxGitLabSegments     = 32
)

var gitLabProjectSegment = regexp.MustCompile(`^[A-Za-z0-9._-]{1,255}$`)

// GitLabProject is one canonical project identity. Host is lowercase with a
// default HTTPS port removed; ProjectPath remains case-sensitive and never
// carries a trailing .git suffix.
type GitLabProject struct {
	Host        string
	ProjectPath string
}

// ParseGitLabProject parses an exact HTTPS project URL without contacting its
// host. Query, fragment, userinfo, artifact paths, and ambiguous spellings are
// rejected.
func ParseGitLabProject(raw string) (GitLabProject, bool) {
	u, escaped, ok := parseGitLabURL(raw)
	if !ok || strings.Contains(escaped, "/-/") || strings.HasSuffix(escaped, "/-") {
		return GitLabProject{}, false
	}
	return gitLabProjectFromEscaped(u, escaped)
}

// ParseGitLabReference parses either an exact project URL or a modern GitLab
// artifact URL containing the reserved /-/ separator. Only the project prefix
// participates in identity; the artifact suffix is never retained or fetched.
func ParseGitLabReference(raw string) (GitLabProject, bool) {
	u, escaped, ok := parseGitLabURL(raw)
	if !ok || strings.HasSuffix(escaped, "/-") {
		return GitLabProject{}, false
	}
	if marker := strings.Index(escaped, "/-/"); marker >= 0 {
		if marker == 0 || marker+3 == len(escaped) {
			return GitLabProject{}, false
		}
		escaped = escaped[:marker]
	}
	return gitLabProjectFromEscaped(u, escaped)
}

// ValidateGitLabProject accepts only coordinates already in canonical form.
func ValidateGitLabProject(host, projectPath string) (GitLabProject, bool) {
	if host == "" || projectPath == "" || strings.Contains(projectPath, "%") {
		return GitLabProject{}, false
	}
	project, ok := ParseGitLabProject("https://" + host + "/" + projectPath)
	if !ok || project.Host != host || project.ProjectPath != projectPath {
		return GitLabProject{}, false
	}
	return project, true
}

func parseGitLabURL(raw string) (*url.URL, string, bool) {
	if raw == "" || len(raw) > maxGitLabURLBytes {
		return nil, "", false
	}
	u, err := url.Parse(raw)
	if err != nil || strings.ToLower(u.Scheme) != "https" || u.Opaque != "" || u.User != nil ||
		u.Hostname() == "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") {
		return nil, "", false
	}
	escaped := strings.Trim(u.EscapedPath(), "/")
	if escaped == "" {
		return nil, "", false
	}
	return u, escaped, true
}

func gitLabProjectFromEscaped(u *url.URL, escaped string) (GitLabProject, bool) {
	parts := strings.Split(strings.Trim(escaped, "/"), "/")
	if len(parts) < 2 || len(parts) > maxGitLabSegments {
		return GitLabProject{}, false
	}
	decoded := make([]string, len(parts))
	for index, part := range parts {
		value, err := url.PathUnescape(part)
		if err != nil || value == "" || !gitLabProjectSegment.MatchString(value) || value == "." || value == ".." {
			return GitLabProject{}, false
		}
		decoded[index] = value
	}
	decoded[len(decoded)-1] = strings.TrimSuffix(decoded[len(decoded)-1], ".git")
	if decoded[len(decoded)-1] == "" || !gitLabProjectSegment.MatchString(decoded[len(decoded)-1]) {
		return GitLabProject{}, false
	}
	projectPath := strings.Join(decoded, "/")
	if len(projectPath) > maxGitLabProjectBytes {
		return GitLabProject{}, false
	}
	hostname := strings.ToLower(u.Hostname())
	if strings.Contains(hostname, ":") {
		hostname = "[" + hostname + "]"
	}
	host := hostname
	if port := u.Port(); port != "" {
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 1 || portNumber > 65535 {
			return GitLabProject{}, false
		}
		if portNumber != 443 {
			host += ":" + strconv.Itoa(portNumber)
		}
	}
	return GitLabProject{Host: host, ProjectPath: projectPath}, true
}
