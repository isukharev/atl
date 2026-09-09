package brokerserver

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
)

const (
	maxGuardCredentials     = 16
	maxGuardCredentialBytes = 64 << 10
	minGuardCredentialBytes = 8
)

type CredentialGuard struct {
	credentials [][]byte
}

func NewCredentialGuard(credentials ...[]byte) (*CredentialGuard, error) {
	if len(credentials) > maxGuardCredentials {
		return nil, fmt.Errorf("%w: too many Broker response credentials", domain.ErrUsage)
	}
	guard := &CredentialGuard{credentials: make([][]byte, len(credentials))}
	total := 0
	for index, credential := range credentials {
		if len(credential) < minGuardCredentialBytes || len(credential) > maxGuardCredentialBytes || total+len(credential) > maxGuardCredentialBytes {
			guard.Close()
			return nil, fmt.Errorf("%w: invalid Broker response credential", domain.ErrUsage)
		}
		guard.credentials[index] = bytes.Clone(credential)
		total += len(credential)
	}
	return guard, nil
}

func (g *CredentialGuard) Close() {
	if g == nil {
		return
	}
	for _, credential := range g.credentials {
		clear(credential)
	}
	g.credentials = nil
}

func (g *CredentialGuard) Check(result app.BrokerExactReadResult, encoded []byte, requestCredential []byte) error {
	capacity := 1
	if g != nil {
		capacity += len(g.credentials)
	}
	credentials := make([][]byte, 0, capacity)
	if g != nil {
		credentials = append(credentials, g.credentials...)
	}
	if len(requestCredential) >= minGuardCredentialBytes {
		credentials = append(credentials, requestCredential)
	}
	for _, credential := range credentials {
		if brokerResultContains(result, credential) || encodedCredentialContains(encoded, credential) {
			return fmt.Errorf("%w: Broker response contains a configured credential", domain.ErrCheckFailed)
		}
	}
	return nil
}

func brokerResultContains(result app.BrokerExactReadResult, credential []byte) bool {
	if result.JiraIssue != nil {
		if containsCredential(credential, result.JiraIssue.IssueID, result.JiraIssue.Key, result.JiraIssue.Project, result.JiraIssue.Updated) {
			return true
		}
		for _, field := range result.JiraIssue.Fields {
			if containsCredential(credential, field.Value) {
				return true
			}
		}
	}
	return result.ConfluencePage != nil && (containsCredential(credential,
		result.ConfluencePage.PageID,
		result.ConfluencePage.Type,
		result.ConfluencePage.Space,
		result.ConfluencePage.Title,
		result.ConfluencePage.Updated,
		string(result.ConfluencePage.Projection),
	) || bytes.Contains(result.ConfluencePage.Storage, credential))
}

func containsCredential(credential []byte, values ...string) bool {
	for _, value := range values {
		if bytes.Contains([]byte(value), credential) {
			return true
		}
	}
	return false
}

func encodedCredentialContains(encoded, credential []byte) bool {
	found := false
	_ = visitOwnedCredentialRepresentations(credential, func(pattern []byte) error {
		defer clear(pattern)
		if len(pattern) > 0 && bytes.Contains(encoded, pattern) {
			found = true
		}
		return nil
	})
	return found
}

// visitOwnedCredentialRepresentations constructs one independently owned
// representation at a time. A successful visitor assumes ownership; on an
// error this helper clears the pattern that the visitor rejected.
func visitOwnedCredentialRepresentations(credential []byte, visit func([]byte) error) error {
	deliver := func(pattern []byte) error {
		if err := visit(pattern); err != nil {
			clear(pattern)
			return err
		}
		return nil
	}
	if err := deliver(bytes.Clone(credential)); err != nil {
		return err
	}
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.RawURLEncoding} {
		encoded := make([]byte, encoding.EncodedLen(len(credential)))
		encoding.Encode(encoded, credential)
		if err := deliver(encoded); err != nil {
			return err
		}
	}
	var quoted strings.Builder
	encoder := json.NewEncoder(&quoted)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(string(credential)); err == nil {
		value := strings.TrimSuffix(quoted.String(), "\n")
		if len(value) >= 2 {
			pattern := []byte(value[1 : len(value)-1])
			if err := deliver(pattern); err != nil {
				return err
			}
		}
	}
	return nil
}

func clearCredentialPatterns(patterns [][]byte) {
	for _, pattern := range patterns {
		clear(pattern)
	}
}
