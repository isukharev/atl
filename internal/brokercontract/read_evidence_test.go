package brokercontract

import (
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestExactReadIdentityEvidenceIsCanonicalAndClosed(t *testing.T) {
	jira := domain.BrokerJiraIssueIdentity{ID: "10001", Key: "EXAMPLE-1", Project: "EXAMPLE", Updated: "revision-1", Complete: true}
	jiraVersion, jiraProjection, err := JiraIssueIdentityEvidenceSHA256V1(jira)
	if err != nil || jiraVersion != "ae9ede9487b1f8e84503f7e7e207fc2eb87040ec12fdd43ac3341ddf1f901f3f" || jiraProjection != "dc9fdec90c90ca01a9d1703b176f5457077c826ef3d48456a86b09d4929255dd" {
		t.Fatalf("Jira evidence=%q/%q err=%v", jiraVersion, jiraProjection, err)
	}
	changedJira := jira
	changedJira.Project = "OTHER"
	if _, _, err := JiraIssueIdentityEvidenceSHA256V1(changedJira); err == nil {
		t.Fatal("cross-project Jira identity accepted")
	}

	page := domain.BrokerConfluencePageIdentity{ID: "42", Type: "page", Status: "current", Space: "DOCS", Version: 7, Updated: "revision-1", AncestorIDs: []string{}, AncestorsPresent: true, Complete: true}
	pageVersion, pageProjection, err := ConfluencePageIdentityEvidenceSHA256V1(page)
	if err != nil || pageVersion != "7a07c87171ef123c0265667ea71ac6f885e457e3a1ca5229490c1e6abac10230" || pageProjection != "54f9b1baed142d9b5906d6dba9e8536f771e7ba645507a3be95c147d7d72e777" {
		t.Fatalf("Confluence evidence=%q/%q err=%v", pageVersion, pageProjection, err)
	}
	changedPage := page
	changedPage.AncestorIDs = []string{"9"}
	_, changedProjection, err := ConfluencePageIdentityEvidenceSHA256V1(changedPage)
	if err != nil || changedProjection == pageProjection {
		t.Fatalf("ancestor drift projection=%q err=%v", changedProjection, err)
	}
	changedPage.AncestorIDs = nil
	if _, _, err := ConfluencePageIdentityEvidenceSHA256V1(changedPage); err == nil {
		t.Fatal("missing ancestor presence accepted")
	}
}
