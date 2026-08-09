package mirror

import (
	"encoding/json"
	"fmt"
)

type jiraCompletePullSnapshot struct {
	Key    string                     `json:"key"`
	ID     string                     `json:"id"`
	Fields map[string]json.RawMessage `json:"fields"`
}

func validateJiraCompletePullPayloads(entry CompletePullJournalEntry, artifacts []CompletePullArtifact) error {
	for _, artifact := range artifacts {
		switch artifact.Role {
		case CompletePullArtifactRoleNative, CompletePullArtifactRoleBase:
			if Hash(artifact.Data) != entry.State.Hash {
				return fmt.Errorf("Jira %s payload does not match the accepted native hash", artifact.Role)
			}
		case CompletePullArtifactRoleMetadata:
			var snapshot jiraCompletePullSnapshot
			if err := json.Unmarshal(artifact.Data, &snapshot); err != nil || snapshot.Key != entry.State.ID || snapshot.ID != entry.Identity || snapshot.Fields == nil {
				return fmt.Errorf("Jira metadata payload does not match the accepted issue identity")
			}
		}
	}
	return nil
}
