package app

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/strictjson"
)

const brokerJiraCommentRecoveryMaxBytes = int64(4 << 20)

type brokerJiraCommentRecoveryEntry struct {
	ID           string
	RecordSHA256 string
}

type brokerJiraCommentRecoveryArtifact struct {
	OperationID   string
	TicketSHA256  string
	BindingSHA256 string
	NativeBody    []byte
	Issue         domain.JiraGuardedCommentIssue
	ActorSHA256   string
	Baseline      []brokerJiraCommentRecoveryEntry
}

type brokerJiraCommentRecoveryWire struct {
	SchemaVersion    int                                  `json:"schema_version"`
	OperationID      string                               `json:"operation_id"`
	TicketSHA256     string                               `json:"ticket_sha256"`
	BindingSHA256    string                               `json:"binding_sha256"`
	NativeBodyBase64 string                               `json:"native_body_base64"`
	Issue            brokerJiraCommentRecoveryIssueWire   `json:"issue"`
	ActorSHA256      string                               `json:"actor_sha256"`
	Baseline         []brokerJiraCommentRecoveryEntryWire `json:"baseline"`
}

type brokerJiraCommentRecoveryIssueWire struct {
	ID      string `json:"id"`
	Key     string `json:"key"`
	Project string `json:"project"`
	Updated string `json:"updated"`
}

type brokerJiraCommentRecoveryEntryWire struct {
	ID           string `json:"id"`
	RecordSHA256 string `json:"record_sha256"`
}

func encodeBrokerJiraCommentRecoveryArtifact(value brokerJiraCommentRecoveryArtifact) ([]byte, error) {
	if err := validateBrokerJiraCommentRecoveryArtifact(value); err != nil {
		return nil, err
	}
	wire := brokerJiraCommentRecoveryWire{
		SchemaVersion: 1, OperationID: value.OperationID, TicketSHA256: value.TicketSHA256,
		BindingSHA256: value.BindingSHA256, NativeBodyBase64: base64.StdEncoding.EncodeToString(value.NativeBody),
		Issue:       brokerJiraCommentRecoveryIssueWire{ID: value.Issue.ID, Key: value.Issue.Key, Project: value.Issue.Project, Updated: value.Issue.Updated},
		ActorSHA256: value.ActorSHA256, Baseline: make([]brokerJiraCommentRecoveryEntryWire, len(value.Baseline)),
	}
	for index, entry := range value.Baseline {
		wire.Baseline[index] = brokerJiraCommentRecoveryEntryWire(entry)
	}
	encoded, err := json.Marshal(wire)
	if err != nil || int64(len(encoded)) > brokerJiraCommentRecoveryMaxBytes {
		return nil, fmt.Errorf("%w: broker Jira comment recovery artifact exceeds its bound", domain.ErrCheckFailed)
	}
	return encoded, nil
}

func decodeBrokerJiraCommentRecoveryArtifact(data []byte) (brokerJiraCommentRecoveryArtifact, error) {
	var wire brokerJiraCommentRecoveryWire
	if len(data) == 0 || int64(len(data)) > brokerJiraCommentRecoveryMaxBytes || strictjson.DecodeExact(data, brokercontract.MaxCanonicalDepth, &wire) != nil || wire.SchemaVersion != 1 {
		return brokerJiraCommentRecoveryArtifact{}, fmt.Errorf("%w: broker Jira comment recovery artifact is invalid", domain.ErrCheckFailed)
	}
	if len(wire.NativeBodyBase64) == 0 || int64(base64.StdEncoding.DecodedLen(len(wire.NativeBodyBase64))) > JiraCommentBodyMaxBytes+2 {
		return brokerJiraCommentRecoveryArtifact{}, fmt.Errorf("%w: broker Jira comment recovery artifact is invalid", domain.ErrCheckFailed)
	}
	body, err := base64.StdEncoding.DecodeString(wire.NativeBodyBase64)
	if err != nil {
		return brokerJiraCommentRecoveryArtifact{}, fmt.Errorf("%w: broker Jira comment recovery artifact is invalid", domain.ErrCheckFailed)
	}
	value := brokerJiraCommentRecoveryArtifact{
		OperationID: wire.OperationID, TicketSHA256: wire.TicketSHA256, BindingSHA256: wire.BindingSHA256,
		NativeBody: body, Issue: domain.JiraGuardedCommentIssue{ID: wire.Issue.ID, Key: wire.Issue.Key, Project: wire.Issue.Project, Updated: wire.Issue.Updated, Complete: true},
		ActorSHA256: wire.ActorSHA256, Baseline: make([]brokerJiraCommentRecoveryEntry, len(wire.Baseline)),
	}
	for index, entry := range wire.Baseline {
		value.Baseline[index] = brokerJiraCommentRecoveryEntry(entry)
	}
	if err := validateBrokerJiraCommentRecoveryArtifact(value); err != nil {
		return brokerJiraCommentRecoveryArtifact{}, err
	}
	return value, nil
}

func brokerJiraCommentRecoveryFromPrewrite(record domain.BrokerJournalRecord, prewrite *jiraGuardedCommentPrewrite) (brokerJiraCommentRecoveryArtifact, error) {
	ticketSHA256, err := brokercontract.OperationTicketSHA256(record.Intent.Ticket)
	if err != nil {
		return brokerJiraCommentRecoveryArtifact{}, err
	}
	ids := make([]string, 0, len(prewrite.baselineByID))
	for id := range prewrite.baselineByID {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return guardedCommentDecimalLess(ids[i], ids[j]) })
	baseline := make([]brokerJiraCommentRecoveryEntry, len(ids))
	for index, id := range ids {
		baseline[index] = brokerJiraCommentRecoveryEntry{ID: id, RecordSHA256: prewrite.baselineByID[id]}
	}
	value := brokerJiraCommentRecoveryArtifact{
		OperationID: record.OperationID, TicketSHA256: ticketSHA256, BindingSHA256: record.BindingSHA256,
		NativeBody: append([]byte(nil), prewrite.body...), Issue: prewrite.issue,
		ActorSHA256: prewrite.actorSHA256, Baseline: baseline,
	}
	return value, validateBrokerJiraCommentRecoveryArtifact(value)
}

func validateBrokerJiraCommentRecoveryArtifact(value brokerJiraCommentRecoveryArtifact) error {
	if !brokerJiraCommentDigest(value.OperationID) || !brokerJiraCommentDigest(value.TicketSHA256) || !brokerJiraCommentDigest(value.BindingSHA256) ||
		!brokerJiraCommentDigest(value.ActorSHA256) || len(value.Baseline) > domain.JiraCommentReadMaxItems {
		return fmt.Errorf("%w: broker Jira comment recovery artifact is invalid", domain.ErrCheckFailed)
	}
	if _, err := ValidateJiraCommentBody(value.NativeBody); err != nil {
		return fmt.Errorf("%w: broker Jira comment recovery artifact is invalid", domain.ErrCheckFailed)
	}
	if _, _, err := validateGuardedCommentIssue(value.Issue, value.Issue.Key, value.Issue.ID); err != nil {
		return fmt.Errorf("%w: broker Jira comment recovery artifact is invalid", domain.ErrCheckFailed)
	}
	previous := ""
	for _, entry := range value.Baseline {
		if !canonicalPositiveNumericString(entry.ID) || len(entry.ID) > domain.JiraEvidenceIDMaxBytes || !brokerJiraCommentDigest(entry.RecordSHA256) ||
			(previous != "" && !guardedCommentDecimalLess(previous, entry.ID)) {
			return fmt.Errorf("%w: broker Jira comment recovery artifact is invalid", domain.ErrCheckFailed)
		}
		previous = entry.ID
	}
	return nil
}

func brokerJiraCommentDigest(value string) bool {
	if len(value) != domain.BrokerMaxDigestBytes {
		return false
	}
	for _, current := range value {
		if current < '0' || current > '9' {
			if current < 'a' || current > 'f' {
				return false
			}
		}
	}
	return true
}
