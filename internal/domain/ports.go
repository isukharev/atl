package domain

import "context"

// PullOpts narrows what a DocStore.Pull returns.
type PullOpts struct {
	Format string // "csf" (default, native) | "view"
}

// DocStore is the port for a document backend (Confluence today; Notion/etc.
// later). Bodies flow in the backend's native storage format.
type DocStore interface {
	// Search runs a backend query (CQL) and returns hits plus a next cursor.
	Search(ctx context.Context, query string, limit int, cursor string) ([]PageRef, string, error)
	// Tree returns the page hierarchy rooted at a space (or page), to depth.
	Tree(ctx context.Context, space string, depth int) ([]PageRef, error)
	// GetPage fetches a single page; Body holds native CSF unless Format=view.
	GetPage(ctx context.Context, id string, opts PullOpts) (*Resource, error)
	// GetMeta fetches non-body metadata.
	GetMeta(ctx context.Context, id string) (*PageMeta, error)
	// History returns version records (newest first).
	History(ctx context.Context, id string) ([]Version, error)
	// UpdatePage pushes a new body. expectVersion is the optimistic gate: the
	// store must refuse (ErrVersionConflict) if the remote moved past it unless
	// force is set. Returns the new version.
	UpdatePage(ctx context.Context, id string, expectVersion int, title string, body []byte, force bool) (int, error)
	// CreatePage creates a new page under parent (may be "").
	CreatePage(ctx context.Context, space, parent, title string, body []byte) (*Resource, error)
	// MovePage reparents a page.
	MovePage(ctx context.Context, id, newParent string) error
	// DeletePage trashes a page. May return ErrForbidden on per-space perms.
	DeletePage(ctx context.Context, id string) error
	// Comments.
	ListComments(ctx context.Context, id string) ([]Comment, error)
	AddComment(ctx context.Context, id string, body []byte) (*Comment, error)
	// Attachments.
	ListAttachments(ctx context.Context, id string) ([]Attachment, error)
	// DownloadAttachment streams an attachment's bytes. version<=0 means latest.
	DownloadAttachment(ctx context.Context, pageID, filename string, version int) ([]byte, error)
}

// Version is a single revision record.
type Version struct {
	Number  int    `json:"number"`
	When    string `json:"when"`
	By      string `json:"by"`
	Message string `json:"message,omitempty"`
}

// Issue is a Jira issue (export-mostly: read locally, write via commands).
type Issue struct {
	Key       string            `json:"key"`
	Summary   string            `json:"summary"`
	Status    string            `json:"status"`
	Type      string            `json:"type"`
	Project   string            `json:"project"`
	Assignee  string            `json:"assignee,omitempty"`
	Reporter  string            `json:"reporter,omitempty"`
	Body      string            `json:"description,omitempty"` // native wiki
	Fields    map[string]any    `json:"fields,omitempty"`
	Labels    []string          `json:"labels,omitempty"`
	Version   int               `json:"-"`
	Links     []IssueLink       `json:"links,omitempty"`
	Comments  []Comment         `json:"comments,omitempty"`
	Raw       map[string]any    `json:"-"`
	FieldText map[string]string `json:"-"`
}

// IssueLink is a typed link between issues. ID is the backend link id, needed
// to delete a specific link (the same id appears on both the inward and outward
// view of one link).
type IssueLink struct {
	ID        string `json:"id,omitempty"`
	Type      string `json:"type"`
	Direction string `json:"direction"` // inward|outward
	Key       string `json:"key"`
}

// ChangelogEntry is one history record of an issue (who changed what, when).
type ChangelogEntry struct {
	ID      string          `json:"id"`
	Author  string          `json:"author"`
	Created string          `json:"created"`
	Items   []ChangelogItem `json:"items"`
}

// ChangelogItem is a single field change inside a ChangelogEntry. From/To are
// the human-readable values (Jira's fromString/toString).
type ChangelogItem struct {
	Field string `json:"field"`
	From  string `json:"from,omitempty"`
	To    string `json:"to,omitempty"`
}

// Tracker is the port for an issue tracker (Jira today; Linear/GitLab later).
type Tracker interface {
	GetIssue(ctx context.Context, key string, fields []string) (*Issue, error)
	Search(ctx context.Context, jql string, fields []string, limit int, cursor string) ([]Issue, string, error)
	Create(ctx context.Context, project, issueType, summary string, body []byte, fields map[string]string) (*Issue, error)
	Update(ctx context.Context, key, summary string, body []byte, fields map[string]string) error
	Transition(ctx context.Context, key, to, comment string) error
	AddComment(ctx context.Context, key string, body []byte) (*Comment, error)
	// ListComments returns an issue's comments (newest-last, as Jira returns them).
	ListComments(ctx context.Context, key string) ([]Comment, error)
	// DeleteComment removes a comment by id.
	DeleteComment(ctx context.Context, key, commentID string) error
	Link(ctx context.Context, from, to, linkType string) error
	// DeleteLink removes an issue link by its backend id.
	DeleteLink(ctx context.Context, linkID string) error
	LinkEpic(ctx context.Context, issue, epic string) error
	// Changelog returns an issue's history (newest-last).
	Changelog(ctx context.Context, key string) ([]ChangelogEntry, error)
	ListAttachments(ctx context.Context, key string) ([]Attachment, error)
	DownloadAttachment(ctx context.Context, key, attachmentID string) ([]byte, string, error)
	// Metadata helpers for valid edits.
	Fields(ctx context.Context) ([]FieldDef, error)
	FieldOptions(ctx context.Context, project, issueType, field string) ([]string, error)
	Transitions(ctx context.Context, key string) ([]TransitionDef, error)
	LinkTypes(ctx context.Context) ([]string, error)
}

// FieldDef describes a Jira field.
type FieldDef struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Custom bool   `json:"custom"`
	Schema string `json:"schema,omitempty"`
}

// TransitionDef is an available workflow transition.
type TransitionDef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	To   string `json:"to"`
}

// Verifier confirms the configured credentials work against the backend and
// returns the authenticated user's display name. It is an optional capability,
// kept separate from DocStore/Tracker so existing port implementations and test
// mocks are unaffected. Used by `atl auth login` to validate a PAT before it is
// persisted.
type Verifier interface {
	Whoami(ctx context.Context) (string, error)
}
