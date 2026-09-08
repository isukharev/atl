package brokercontract

import (
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

func JiraIssueIdentityEvidenceSHA256V1(value domain.BrokerJiraIssueIdentity) (versionSHA256, projectionSHA256 string, err error) {
	if !validBrokerJiraIssueIdentity(value) {
		return "", "", reject(domain.BrokerReasonMalformed)
	}
	version := struct {
		ID      string `json:"id"`
		Updated string `json:"updated"`
	}{value.ID, value.Updated}
	projection := struct {
		ID      string `json:"id"`
		Key     string `json:"key"`
		Project string `json:"project"`
		Updated string `json:"updated"`
	}{value.ID, value.Key, value.Project, value.Updated}
	versionSHA256, err = digestValue("jira-issue-version-evidence", version)
	if err != nil {
		return "", "", err
	}
	projectionSHA256, err = digestValue("jira-issue-identity-projection", projection)
	return versionSHA256, projectionSHA256, err
}

func ConfluencePageIdentityEvidenceSHA256V1(value domain.BrokerConfluencePageIdentity) (versionSHA256, projectionSHA256 string, err error) {
	if !validBrokerConfluencePageIdentity(value) {
		return "", "", reject(domain.BrokerReasonMalformed)
	}
	version := struct {
		ID      string `json:"id"`
		Version int    `json:"version"`
		Updated string `json:"updated"`
	}{value.ID, value.Version, value.Updated}
	projection := struct {
		ID               string   `json:"id"`
		Type             string   `json:"type"`
		Status           string   `json:"status"`
		Space            string   `json:"space"`
		Version          int      `json:"version"`
		Updated          string   `json:"updated"`
		AncestorIDs      []string `json:"ancestor_ids"`
		AncestorsPresent bool     `json:"ancestors_present"`
	}{value.ID, value.Type, value.Status, value.Space, value.Version, value.Updated, wireStrings(value.AncestorIDs), value.AncestorsPresent}
	versionSHA256, err = digestValue("confluence-page-version-evidence", version)
	if err != nil {
		return "", "", err
	}
	projectionSHA256, err = digestValue("confluence-page-identity-projection", projection)
	return versionSHA256, projectionSHA256, err
}

func validBrokerJiraIssueIdentity(value domain.BrokerJiraIssueIdentity) bool {
	return value.Complete && validPositiveDecimal(value.ID) && domain.ValidJiraIssueKey(value.Key) && len(value.Key) <= domain.BrokerMaxIdentifierBytes &&
		validIdentifier(value.Project) && strings.HasPrefix(value.Key, value.Project+"-") && value.Updated != "" && len(value.Updated) <= domain.BrokerMaxIdentifierBytes && validBrokerText(value.Updated)
}

func validBrokerConfluencePageIdentity(value domain.BrokerConfluencePageIdentity) bool {
	if !value.Complete || !domain.ValidConfluenceContentID(value.ID) || value.Type != "page" || value.Status != "current" || !validIdentifier(value.Space) ||
		value.Version <= 0 || value.Updated == "" || len(value.Updated) > domain.BrokerMaxIdentifierBytes || !validBrokerText(value.Updated) ||
		!value.AncestorsPresent || value.AncestorIDs == nil || len(value.AncestorIDs) > MaxResources {
		return false
	}
	seen := map[string]bool{}
	for _, ancestor := range value.AncestorIDs {
		if !domain.ValidConfluenceContentID(ancestor) || ancestor == value.ID || seen[ancestor] {
			return false
		}
		seen[ancestor] = true
	}
	return true
}
