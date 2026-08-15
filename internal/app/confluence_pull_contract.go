package app

// PullOpts selects what to mirror and where. Render is the per-run flag override
// for the markdown view profile; a zero value leaves the effective settings
// (local + global config) untouched.
type PullOpts struct {
	ID       string
	CQL      string
	Space    string
	Depth    int
	Assets   bool
	Comments bool
	// Attachments captures a qualified per-page attachment inventory during a
	// complete pull. It is distinct from Assets, which only resolves diagram and
	// image render dependencies from the CSF view.
	Attachments               bool
	AttachmentBodies          bool
	AttachmentMediaTypes      []string
	MaxAttachmentPagesPerItem int
	MaxAttachmentsPerItem     int
	MaxAttachmentBytes        int64
	MaxTotalAttachmentBytes   int64
	// AllowPartialArtifacts is an explicit complete-pull-only policy. It may
	// persist a versioned, qualified partial comments or attachments sidecar but
	// never upgrades that evidence to complete.
	AllowPartialArtifacts bool
	Into                  string
	Render                pullRenderService
	JiraView              string
	Incremental           bool
	Complete              bool
	DryRun                bool
	OverwriteLocal        bool
	StashLocal            bool
	// RestartComplete explicitly replaces an unfinished complete-pull snapshot
	// after a fresh two-pass selection and local overwrite preflight succeed.
	RestartComplete   bool
	Since             string
	TimeZone          string
	MaxPages          int
	PagePrefetch      int
	RequestsPerSecond int
	exactRender       *RenderSettings
	evidence          *corpusPullEvidenceOptions
	// deterministicRawUsers keeps cache-qualified projections independent of
	// mutable directory display names. It is private and can only be selected by
	// the corpus builder; the complete-pull options receipt binds it explicitly.
	deterministicRawUsers bool
}

// PulledPage is one mirrored page.
type PulledPage struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Path    string `json:"path"`
	Version int    `json:"version"`
	Assets  int    `json:"assets"`
	Status  string `json:"status,omitempty"`
	// Comments is the number of comments mirrored for this page. It is a pointer
	// so a --comments pull that found zero comments still emits `"comments": 0`,
	// distinguishable from a pull that never fetched them (field omitted).
	Comments *int `json:"comments,omitempty"`
	// Attachments is the observed inventory count when --attachments was
	// requested. A count on a partial inventory is intentionally only the
	// retained observed prefix; the include and sidecar carry its qualification.
	Attachments *int `json:"attachments,omitempty"`
}

// PullResult is the pull summary.
type PullResult struct {
	Root        string                  `json:"root"`
	Pages       []PulledPage            `json:"pages"`
	Includes    []ConfluencePullInclude `json:"includes"`
	Incremental *IncrementalPullResult  `json:"incremental,omitempty"`
	Complete    *CompletePullResult     `json:"complete_pull,omitempty"`
	LocalSafety *PullLocalSafety        `json:"local_safety,omitempty"`
	// Truncated is true when a --cql selection hit the silent pagination cap, so
	// some matching pages were NOT mirrored. TruncatedAt is the cap that was hit
	// (the number of ids collected). Both are omitted from JSON in the common,
	// non-truncated case so existing consumers see an unchanged shape.
	Truncated   bool `json:"truncated,omitempty"`
	TruncatedAt int  `json:"truncated_at,omitempty"`
	// CommentsTruncated is true when at least one page's comment listing hit the
	// adapter's fetch cap, so its mirrored comments sidecar is incomplete. The CLI
	// surfaces it as a stderr warning; omitted otherwise so the shape is unchanged.
	CommentsTruncated bool `json:"comments_truncated,omitempty"`
	// Warnings are advisory render-resolution messages; CLI-only and not serialized.
	Warnings        []string        `json:"-"`
	Scheduling      *PullScheduling `json:"scheduling,omitempty"`
	includeProgress *confluencePullIncludeProgress
}

// PullScheduling reports the exact opt-in load policy. PagePrefetch overlaps
// native body GETs only; MaxInFlight and RequestsPerSecond cover every HTTP
// attempt made through the shared Confluence/Jira scheduler.
type PullScheduling struct {
	PagePrefetch      int `json:"page_prefetch"`
	MaxInFlight       int `json:"max_in_flight"`
	RequestsPerSecond int `json:"requests_per_second"`
}
