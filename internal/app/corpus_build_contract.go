package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/corpus"
	"github.com/isukharev/atl/internal/domain"
)

const (
	CorpusBuildSchemaV1        = 1
	corpusBuildMaxDeadline     = 24 * time.Hour
	corpusBuildMaxResponse     = int64(64 << 30)
	corpusBuildMaxGeneration   = int64(64 << 30)
	corpusBuildMaxRequests     = 10_000_000
	corpusBuildMaxMembers      = 100_000
	corpusBuildMaxInFlight     = 8
	corpusBuildMaxRequestsRate = 1000
)

type CorpusBuildPhase string

const (
	CorpusBuildPhaseValidate  CorpusBuildPhase = "validate"
	CorpusBuildPhaseWorkspace CorpusBuildPhase = "workspace"
	CorpusBuildPhaseRecover   CorpusBuildPhase = "recover"
	CorpusBuildPhasePrincipal CorpusBuildPhase = "principal"
	CorpusBuildPhaseCapture   CorpusBuildPhase = "capture"
	CorpusBuildPhaseSnapshot  CorpusBuildPhase = "snapshot"
	CorpusBuildPhasePublish   CorpusBuildPhase = "publish"
)

type CorpusBuildReason string

const (
	CorpusBuildReasonUsage          CorpusBuildReason = "usage"
	CorpusBuildReasonBudget         CorpusBuildReason = "budget"
	CorpusBuildReasonDeadline       CorpusBuildReason = "deadline"
	CorpusBuildReasonBackend        CorpusBuildReason = "backend"
	CorpusBuildReasonIntegrity      CorpusBuildReason = "integrity"
	CorpusBuildReasonDrift          CorpusBuildReason = "drift"
	CorpusBuildReasonOutcomeUnknown CorpusBuildReason = "outcome_unknown"
)

// CorpusBuildError is deliberately content-free. Unwrap retains stable exit
// sentinels while Error never includes a backend value, selector, path, or
// provider diagnostic.
type CorpusBuildError struct {
	Phase  CorpusBuildPhase  `json:"phase"`
	Reason CorpusBuildReason `json:"reason"`
	cause  error
}

func (e *CorpusBuildError) Error() string {
	if e == nil {
		return "corpus build failed"
	}
	return fmt.Sprintf("corpus build failed: phase=%s reason=%s", e.Phase, e.Reason)
}

func (e *CorpusBuildError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// CorpusBuildFailure closes an inner failure at the public build boundary.
func CorpusBuildFailure(phase CorpusBuildPhase, err error) error {
	if err == nil {
		return nil
	}
	reason := CorpusBuildReasonBackend
	switch {
	case errors.Is(err, domain.ErrUsage):
		reason = CorpusBuildReasonUsage
	case errors.Is(err, domain.ErrReadAttemptBudgetExhausted), errors.Is(err, domain.ErrReadResponseBudgetExhausted):
		reason = CorpusBuildReasonBudget
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		reason = CorpusBuildReasonDeadline
	case errors.Is(err, corpus.ErrOutcomeUnknown):
		reason = CorpusBuildReasonOutcomeUnknown
	case errors.Is(err, corpus.ErrIntegrity), errors.Is(err, domain.ErrCheckFailed):
		reason = CorpusBuildReasonIntegrity
	}
	return &CorpusBuildError{Phase: phase, Reason: reason, cause: err}
}

type CorpusBuildOptions struct {
	Root                      string
	Initialize                bool
	Restart                   bool
	CacheRoot                 string
	InitializeCache           bool
	CacheMaxRequests          int
	CacheMaxResponseBytes     int64
	CacheDeadline             time.Duration
	JiraProject               string
	MaxJiraIssues             int
	ConfluenceSpace           string
	MaxConfluencePages        int
	MaxRequests               int
	MaxResponseBytes          int64
	MaxMembers                int
	MaxGenerationBytes        int64
	Deadline                  time.Duration
	MaxInFlight               int
	RequestsPerSecond         int
	Comments                  bool
	MaxCommentPagesPerItem    int
	MaxCommentsPerItem        int
	Attachments               bool
	MaxAttachmentPagesPerItem int
	MaxAttachmentsPerItem     int
	AttachmentBodies          bool
	AttachmentMediaTypes      []string
	MaxAttachmentBytes        int64
	MaxTotalAttachmentBytes   int64
	AllowPartialEvidence      bool
}

type CorpusBuildDependencies struct {
	Jira                  *JiraService
	Confluence            *ConfluenceService
	GeneratorVersion      string
	GeneratorCommit       string
	BuildState            corpus.BuildState
	ConfluenceTrustDigest string
	Now                   func() time.Time
}

type CorpusBuildServiceResult struct {
	Service     corpus.Service                    `json:"service"`
	Status      string                            `json:"status"`
	Count       int                               `json:"count"`
	StartedAt   string                            `json:"started_at"`
	CompletedAt string                            `json:"completed_at"`
	Usage       corpus.CaptureUsage               `json:"usage"`
	Dimensions  []corpus.CaptureDimensionEvidence `json:"dimensions"`
}

type CorpusBuildResult struct {
	SchemaVersion int                        `json:"schema_version"`
	Source        string                     `json:"source"`
	Services      []CorpusBuildServiceResult `json:"services"`
	Usage         corpus.CaptureUsage        `json:"usage"`
	ElapsedMS     int64                      `json:"elapsed_ms"`
	Reused        bool                       `json:"reused"`
	Projection    corpus.IndexerReceiptV2    `json:"projection"`
	Generation    corpus.Summary             `json:"generation"`
	Cache         *CorpusBuildCacheResult    `json:"cache,omitempty"`
}

// CorpusBuildCacheResult is deliberately content-free. It reports only the
// closed cache decision and the separately bounded probe usage; backend,
// selector, principal, object, and filesystem identities never enter stdout.
type CorpusBuildCacheResult struct {
	Status     string              `json:"status"`
	Reason     string              `json:"reason"`
	ProbeUsage corpus.CaptureUsage `json:"probe_usage"`
}

type corpusBuildBinding struct {
	JiraProject        string                `json:"jira_project,omitempty"`
	MaxJiraIssues      int                   `json:"max_jira_issues,omitempty"`
	ConfluenceSpace    string                `json:"confluence_space,omitempty"`
	MaxConfluencePages int                   `json:"max_confluence_pages,omitempty"`
	MaxRequests        int                   `json:"max_requests"`
	MaxResponseBytes   int64                 `json:"max_response_bytes"`
	MaxMembers         int                   `json:"max_members"`
	MaxGenerationBytes int64                 `json:"max_generation_bytes"`
	DeadlineNanos      int64                 `json:"deadline_nanos"`
	MaxInFlight        int                   `json:"max_in_flight"`
	RequestsPerSecond  int                   `json:"requests_per_second"`
	GeneratorVersion   string                `json:"generator_version"`
	BuildState         corpus.BuildState     `json:"build_state"`
	Evidence           corpusEvidenceBinding `json:"evidence"`
	CachePublication   bool                  `json:"cache_publication,omitempty"`
	GeneratorCommit    string                `json:"generator_commit,omitempty"`
	TrustDigest        string                `json:"trust_digest,omitempty"`
}

// ValidateCorpusBuildOptions checks the complete static command contract. It
// performs no configuration, credential, filesystem, or backend access.
func ValidateCorpusBuildOptions(options CorpusBuildOptions) error {
	if strings.TrimSpace(options.Root) == "" {
		return fmt.Errorf("%w: corpus build requires --root", domain.ErrUsage)
	}
	if options.Initialize && options.Restart {
		return fmt.Errorf("%w: --initialize and --restart are mutually exclusive", domain.ErrUsage)
	}
	cacheEnabled := strings.TrimSpace(options.CacheRoot) != ""
	if !cacheEnabled {
		if options.InitializeCache || options.CacheMaxRequests != 0 || options.CacheMaxResponseBytes != 0 || options.CacheDeadline != 0 {
			return fmt.Errorf("%w: cache policy requires --cache-root", domain.ErrUsage)
		}
	} else if options.CacheMaxRequests <= 0 || options.CacheMaxRequests > corpusBuildMaxRequests ||
		options.CacheMaxResponseBytes <= 0 || options.CacheMaxResponseBytes > corpusBuildMaxResponse ||
		options.CacheDeadline <= 0 || options.CacheDeadline > corpusBuildMaxDeadline {
		return fmt.Errorf("%w: cache probe bounds are invalid", domain.ErrUsage)
	}
	selectedJira := options.JiraProject != ""
	selectedConfluence := options.ConfluenceSpace != ""
	if !selectedJira && !selectedConfluence {
		return fmt.Errorf("%w: corpus build requires a Jira project, Confluence space, or both", domain.ErrUsage)
	}
	if selectedJira {
		if options.MaxJiraIssues <= 0 {
			return fmt.Errorf("%w: selected Jira capture requires a positive cap", domain.ErrUsage)
		}
		if _, err := jiraCompleteProject(options.JiraProject); err != nil {
			return err
		}
	} else if options.MaxJiraIssues != 0 {
		return fmt.Errorf("%w: Jira cap requires a selected project", domain.ErrUsage)
	}
	if selectedConfluence {
		if !validCorpusConfluenceSpace(options.ConfluenceSpace) || options.MaxConfluencePages <= 0 {
			return fmt.Errorf("%w: selected Confluence capture requires a canonical space and positive cap", domain.ErrUsage)
		}
	} else if options.MaxConfluencePages != 0 {
		return fmt.Errorf("%w: Confluence cap requires a selected space", domain.ErrUsage)
	}
	if options.MaxJiraIssues > corpusBuildMaxMembers || options.MaxConfluencePages > corpusBuildMaxMembers ||
		options.MaxRequests <= 0 || options.MaxRequests > corpusBuildMaxRequests ||
		options.MaxResponseBytes <= 0 || options.MaxResponseBytes > corpusBuildMaxResponse ||
		options.MaxMembers <= 0 || options.MaxMembers > corpusBuildMaxMembers ||
		options.MaxGenerationBytes <= 0 || options.MaxGenerationBytes > corpusBuildMaxGeneration ||
		options.Deadline <= 0 || options.Deadline > corpusBuildMaxDeadline ||
		options.MaxInFlight <= 0 || options.MaxInFlight > corpusBuildMaxInFlight ||
		options.RequestsPerSecond <= 0 || options.RequestsPerSecond > corpusBuildMaxRequestsRate {
		return fmt.Errorf("%w: corpus build bounds are invalid", domain.ErrUsage)
	}
	return validateCorpusEvidenceOptions(options)
}

func validCorpusConfluenceSpace(value string) bool {
	if value == "" || len(value) > 256 || strings.TrimSpace(value) != value || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}
