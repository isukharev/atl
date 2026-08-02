// Package backendid derives content-minimized identities for configured
// backend origins. Canonical URL bytes exist only in memory; callers persist or
// emit only the tagged SHA-256 digest.
package backendid

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/net/idna"
)

const Prefix = "sha256:"

// OriginSHA256 returns a tagged digest for one absolute HTTP(S) adapter base.
// Normalization is deliberately conservative: only URL spellings guaranteed
// to select the same origin/context path are folded together.
func OriginSHA256(raw string) (string, error) {
	canonical, err := canonicalOrigin(raw)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(canonical))
	return Prefix + hex.EncodeToString(sum[:]), nil
}

func canonicalOrigin(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.IndexFunc(raw, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("backend origin must be a non-empty URL without control characters")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid backend origin")
	}
	scheme := strings.ToLower(u.Scheme)
	if (scheme != "http" && scheme != "https") || u.Opaque != "" || !u.IsAbs() || u.Hostname() == "" {
		return "", fmt.Errorf("backend origin must be an absolute HTTP(S) URL")
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" {
		return "", fmt.Errorf("backend origin must not contain userinfo, query, or fragment data")
	}
	contextPath := strings.TrimRight(u.Path, "/")
	if strings.IndexFunc(contextPath, unicode.IsControl) >= 0 || ambiguousPath(contextPath) {
		return "", fmt.Errorf("backend origin contains an ambiguous context path")
	}

	hostname := strings.ToLower(u.Hostname())
	if ip := net.ParseIP(hostname); ip != nil {
		hostname = ip.String()
	} else {
		ascii, err := idna.Lookup.ToASCII(strings.TrimSuffix(hostname, "."))
		if err != nil || ascii == "" {
			return "", fmt.Errorf("backend origin contains an invalid hostname")
		}
		hostname = strings.ToLower(ascii)
	}
	port := u.Port()
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", fmt.Errorf("backend origin contains an invalid port")
		}
		if (scheme == "https" && n == 443) || (scheme == "http" && n == 80) {
			port = ""
		}
	}
	host := hostname
	if strings.Contains(hostname, ":") {
		host = "[" + hostname + "]"
	}
	if port != "" {
		host = net.JoinHostPort(hostname, port)
	}
	path := strings.TrimRight(canonicalEscapedPath(u.EscapedPath()), "/")
	return scheme + "://" + host + path, nil
}

func canonicalEscapedPath(path string) string {
	var b strings.Builder
	b.Grow(len(path))
	const hexDigits = "0123456789ABCDEF"
	for i := 0; i < len(path); i++ {
		if path[i] != '%' || i+2 >= len(path) {
			b.WriteByte(path[i])
			continue
		}
		hi, hiOK := hexNibble(path[i+1])
		lo, loOK := hexNibble(path[i+2])
		if !hiOK || !loOK {
			b.WriteByte(path[i])
			continue
		}
		decoded := hi<<4 | lo
		if isURIUnreserved(decoded) {
			b.WriteByte(decoded)
		} else {
			b.WriteByte('%')
			b.WriteByte(hexDigits[hi])
			b.WriteByte(hexDigits[lo])
		}
		i += 2
	}
	return b.String()
}

func hexNibble(value byte) (byte, bool) {
	switch {
	case value >= '0' && value <= '9':
		return value - '0', true
	case value >= 'a' && value <= 'f':
		return value - 'a' + 10, true
	case value >= 'A' && value <= 'F':
		return value - 'A' + 10, true
	default:
		return 0, false
	}
}

func isURIUnreserved(value byte) bool {
	return (value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z') ||
		(value >= '0' && value <= '9') || value == '-' || value == '.' || value == '_' || value == '~'
}

func ambiguousPath(path string) bool {
	if strings.Contains(path, "//") {
		return true
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "." || segment == ".." {
			return true
		}
	}
	return false
}
