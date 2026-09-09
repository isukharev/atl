package brokerconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"

	"github.com/isukharev/atl/internal/backendid"
	"github.com/isukharev/atl/internal/domain"
)

const maxCommentPolicyBytes = int64(64 << 10)

// JiraComment is an explicit operator opt-in to the immutable-identity,
// point-in-time snapshot profile, not atomic current-project enforcement.
type JiraComment struct {
	QualificationProfile string `json:"qualification_profile"`
	JournalDirectory     string `json:"journal_directory"`
	JournalRecords       int    `json:"journal_records"`
	JournalReservedBytes int64  `json:"journal_reserved_bytes"`
	LocalPolicyFile      string `json:"local_policy_file"`
	LocalPolicySHA256    string `json:"local_policy_sha256"`
}

// JournalConfiguration contains only explicit local storage configuration.
// Directory is owner-private and must never enter command output or errors.
type JournalConfiguration struct {
	Directory     string
	BrokerID      string
	Backend       domain.BrokerBackendBinding
	Records       int
	ReservedBytes int64
}

// LoadJournalConfiguration reads the config file only. Initialization requires
// no runtime credential, policy, certificate, environment or network access.
func LoadJournalConfiguration(configPath string) (*JournalConfiguration, error) {
	cfg, err := loadConfiguration(configPath)
	if err != nil || cfg.JiraComment == nil {
		return nil, configError()
	}
	return journalConfiguration(configPath, cfg)
}

func journalConfiguration(configPath string, cfg Config) (*JournalConfiguration, error) {
	if cfg.Jira == nil || cfg.JiraComment == nil {
		return nil, configError()
	}
	origin, err := backendid.OriginSHA256(cfg.Jira.BaseURL)
	if err != nil {
		return nil, configError()
	}
	absolute, err := filepath.Abs(configPath)
	if err != nil {
		return nil, configError()
	}
	return &JournalConfiguration{
		Directory:     filepath.Join(filepath.Dir(absolute), cfg.JiraComment.JournalDirectory),
		BrokerID:      cfg.BrokerID,
		Backend:       domain.BrokerBackendBinding{Service: "jira", OriginSHA256: strings.TrimPrefix(origin, backendid.Prefix), WorkloadBackendID: cfg.Jira.WorkloadBackendID},
		Records:       cfg.JiraComment.JournalRecords,
		ReservedBytes: cfg.JiraComment.JournalReservedBytes,
	}, nil
}

func validateJiraComment(value JiraComment) error {
	if value.QualificationProfile != domain.BrokerJiraCommentQualificationProfileV1 ||
		validateReference(value.JournalDirectory) != nil || validateReference(value.LocalPolicyFile) != nil ||
		!validDigest(value.LocalPolicySHA256) || value.JournalRecords < 1 || value.JournalRecords > 1024 ||
		value.JournalReservedBytes < 1 || value.JournalReservedBytes > 256<<20 {
		return configError()
	}
	return nil
}

func readCommentPolicy(parent string, cfg JiraComment) ([]byte, error) {
	data, err := readReference(parent, cfg.LocalPolicyFile, maxCommentPolicyBytes)
	if err != nil {
		clear(data)
		return nil, configError()
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != cfg.LocalPolicySHA256 {
		clear(data)
		return nil, configError()
	}
	return data, nil
}
