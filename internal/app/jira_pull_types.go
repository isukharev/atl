package app

import "github.com/isukharev/atl/internal/config"

// JiraPulled is one exported issue. Path points at the rendered derived .md
// staging view; WikiPath points at the sibling <KEY>.wiki substrate — the
// editable native-wiki source of truth. Assets and EpicChildren are omitted at
// zero so ordinary pull output remains compatible.
type JiraPulled struct {
	Key          string `json:"key"`
	Path         string `json:"path"`
	WikiPath     string `json:"wiki_path,omitempty"`
	Status       string `json:"status,omitempty"`
	Assets       int    `json:"assets,omitempty"`
	EpicChildren int    `json:"epic_children,omitempty"`
}

// JiraPullOpts selects either the established JQL path or the explicit
// complete-project path. Complete-only fields are rejected by ordinary Pull.
type JiraPullOpts struct {
	JQL       string
	Project   string
	Into      string
	Limit     int
	MaxIssues int
	Fields    []string
	Assets    bool
	// Comments and Attachments select bounded, provenance-bound evidence only
	// for a qualified complete pull. They deliberately do not alter the
	// established ordinary --assets best-effort mirror layout.
	Comments                bool
	MaxCommentPagesPerItem  int
	MaxCommentsPerItem      int
	Attachments             bool
	AttachmentBodies        bool
	AttachmentMediaTypes    []string
	MaxAttachmentsPerItem   int
	MaxAttachmentBytes      int64
	MaxTotalAttachmentBytes int64
	Complete                bool
	RestartComplete         bool
	DryRun                  bool
	OverwriteLocal          bool
	StashLocal              bool
	Render                  config.RenderService
	exactRender             *RenderSettings
	exactFields             []string
	evidence                *corpusPullEvidenceOptions
}

// JiraPullResult is the pull summary. Optional fields preserve the ordinary
// JSON shape when their feature is not selected.
type JiraPullResult struct {
	Into                    string              `json:"into"`
	Issues                  []JiraPulled        `json:"issues"`
	AssetsSkipped           int                 `json:"assets_skipped,omitempty"`
	EpicChildrenTruncated   bool                `json:"epic_children_truncated,omitempty"`
	EpicChildrenTruncatedAt int                 `json:"epic_children_truncated_at,omitempty"`
	Warnings                []string            `json:"-"`
	LocalSafety             *PullLocalSafety    `json:"local_safety,omitempty"`
	Complete                *CompletePullResult `json:"complete_pull,omitempty"`
}

// JiraIssueAsset is one image attachment selected for mirroring. Path is empty
// until its bytes land in the issue's local asset directory.
type JiraIssueAsset struct {
	ID         string
	Title      string
	MediaType  string
	FileSize   int64
	ContentURL string
	Path       string
}
