package domain

import (
	"context"
	"io"
)

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
	// truncated reports that an internal safety cap stopped the listing while
	// the backend still had more pages — callers must surface it, never hide it.
	Tree(ctx context.Context, space string, depth int) (refs []PageRef, truncated bool, err error)
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
	// DownloadAttachment streams an attachment's bytes; the caller must Close
	// the reader. version<=0 means latest.
	DownloadAttachment(ctx context.Context, pageID, filename string, version int) (io.ReadCloser, error)
	// UploadAttachment uploads file bytes as an attachment to a page.
	UploadAttachment(ctx context.Context, pageID, filename string, data []byte, comment string) (*Attachment, error)
	// DeleteAttachment deletes an attachment by its content id.
	DeleteAttachment(ctx context.Context, attachmentID string) error
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
	ID        string            `json:"id,omitempty"`
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
// view of one link). Type carries the human-readable directional phrase
// ("duplicates", "is blocked by"); TypeName carries the canonical link-type
// name ("Duplicate", "Blocks") that the create API expects — identity checks
// must use TypeName, since the phrase differs from the name for most types.
type IssueLink struct {
	ID        string `json:"id,omitempty"`
	Type      string `json:"type"`
	TypeName  string `json:"type_name,omitempty"`
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
	// Transition moves an issue to a status by name, optionally commenting and
	// setting fields on the transition (e.g. resolution at Done).
	Transition(ctx context.Context, key, to, comment string, fields map[string]string) error
	// DeleteIssue permanently deletes an issue (Jira DC has no trash for issues).
	DeleteIssue(ctx context.Context, key string, deleteSubtasks bool) error
	// UpdateLabels adds and/or removes labels on an issue.
	UpdateLabels(ctx context.Context, key string, add, remove []string) error
	// Assign sets the issue assignee by DC username; an empty username unassigns.
	Assign(ctx context.Context, key, username string) error
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
	// CurrentUser returns the authenticated user.
	CurrentUser(ctx context.Context) (*User, error)
	// SearchUsers finds users by a query (DC matches username/display name).
	SearchUsers(ctx context.Context, query string, limit int) ([]User, error)
	// GetUser fetches one user by DC username.
	GetUser(ctx context.Context, username string) (*User, error)
	ListAttachments(ctx context.Context, key string) ([]Attachment, error)
	// DownloadAttachment streams an attachment's bytes plus its filename; the
	// caller must Close the reader.
	DownloadAttachment(ctx context.Context, key, attachmentID string) (io.ReadCloser, string, error)
	// UploadAttachment uploads file bytes as an attachment to an issue.
	UploadAttachment(ctx context.Context, key, filename string, data []byte) (*Attachment, error)
	// Metadata helpers for valid edits.
	Fields(ctx context.Context) ([]FieldDef, error)
	FieldOptions(ctx context.Context, project, issueType, field string) ([]string, error)
	Transitions(ctx context.Context, key string) ([]TransitionDef, error)
	LinkTypes(ctx context.Context) ([]string, error)
}

// Board is an agile board (scrum/kanban) on Jira Software. ProjectKey is the
// board's location project when the backend reports one (board listings do;
// the single-board fetch may not).
type Board struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"` // scrum | kanban
	ProjectKey string `json:"project_key,omitempty"`
}

// Sprint is a sprint belonging to a scrum board. Dates are the backend's raw
// ISO-8601 strings (kept verbatim; not all are set depending on state).
type Sprint struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	State         string `json:"state"` // active | closed | future
	StartDate     string `json:"start_date,omitempty"`
	EndDate       string `json:"end_date,omitempty"`
	CompleteDate  string `json:"complete_date,omitempty"`
	Goal          string `json:"goal,omitempty"`
	OriginBoardID int    `json:"origin_board_id,omitempty"`
}

// Agile is the optional capability for Jira Software boards & sprints, backed by
// the Data Center Agile REST API (/rest/agile/1.0/). It is kept separate from
// Tracker — like Verifier — because it requires Jira Software (GreenHopper) and
// is not part of every issue tracker's surface, so a non-agile backend need not
// implement it. cursor is the startAt offset; a method returns the next offset
// or "" when the listing is exhausted.
type Agile interface {
	// Boards lists agile boards, optionally filtered to a project (key or id).
	Boards(ctx context.Context, project string, limit int, cursor string) ([]Board, string, error)
	// Board fetches one board by id.
	Board(ctx context.Context, id int) (*Board, error)
	// Sprints lists a board's sprints, optionally filtered by state
	// (active|closed|future; "" for all).
	Sprints(ctx context.Context, boardID int, state string, limit int, cursor string) ([]Sprint, string, error)
	// Sprint fetches one sprint by id.
	Sprint(ctx context.Context, id int) (*Sprint, error)
	// SprintIssues lists the issues assigned to a sprint.
	SprintIssues(ctx context.Context, sprintID int, fields []string, limit int, cursor string) ([]Issue, string, error)
	// MoveIssuesToSprint moves issues (by key) into a sprint.
	MoveIssuesToSprint(ctx context.Context, sprintID int, keys []string) error
	// MoveIssuesToBacklog removes issues (by key) from any sprint (to backlog).
	MoveIssuesToBacklog(ctx context.Context, keys []string) error
}

// StructureReader is the optional read-only capability for Tempo Structure for
// Jira Server/Data Center. It is separate from Tracker because Structure is a
// plugin API and may not exist on every Jira instance.
type StructureReader interface {
	GetStructure(ctx context.Context, id int64) (*Structure, error)
	StructureForest(ctx context.Context, id int64) (*StructureForest, error)
	StructureValues(ctx context.Context, id int64, rows []int64, fields []string) (*StructureValues, error)
}

// Structure is the metadata returned by the Structure REST resource.
type Structure struct {
	ID                                int64            `json:"id"`
	Name                              string           `json:"name"`
	Description                       string           `json:"description,omitempty"`
	ReadOnly                          bool             `json:"read_only,omitempty"`
	EditRequiresParentIssuePermission bool             `json:"edit_requires_parent_issue_permission,omitempty"`
	Owner                             any              `json:"owner,omitempty"`
	Permissions                       []map[string]any `json:"permissions,omitempty"`
	Views                             []map[string]any `json:"views,omitempty"`
}

// StructureVersion identifies a Structure forest/value snapshot.
type StructureVersion struct {
	Signature int64 `json:"signature"`
	Version   int64 `json:"version"`
}

// StructureForest is the raw forest payload returned by /forest/latest.
type StructureForest struct {
	Spec      map[string]any    `json:"spec,omitempty"`
	Formula   string            `json:"formula"`
	ItemTypes map[string]string `json:"item_types,omitempty"`
	Version   StructureVersion  `json:"version"`
}

// StructureRow is one parsed formula component.
type StructureRow struct {
	RowID       int64  `json:"row_id"`
	Depth       int    `json:"depth"`
	ItemType    string `json:"item_type"`
	ItemID      string `json:"item_id"`
	Semantic    string `json:"semantic,omitempty"`
	Position    int    `json:"position"`
	ParentRowID int64  `json:"parent_row_id,omitempty"`
}

// StructureValues preserves Structure's value matrix and explicitly exposes
// inaccessible rows when the backend reports them.
type StructureValues struct {
	Responses        []map[string]any  `json:"responses,omitempty"`
	ItemTypes        map[string]string `json:"item_types,omitempty"`
	ItemsVersion     StructureVersion  `json:"items_version,omitempty"`
	InaccessibleRows []int64           `json:"inaccessible_rows"`
	Raw              map[string]any    `json:"raw,omitempty"`
}

// User is an account on the tracker. On Jira Data Center the identity is the
// username (Name) / user key (Key); AccountID is Cloud-only and kept for
// forward compatibility.
type User struct {
	Name        string `json:"name,omitempty"`
	Key         string `json:"key,omitempty"`
	AccountID   string `json:"accountId,omitempty"`
	DisplayName string `json:"displayName"`
	Email       string `json:"email,omitempty"`
	Active      bool   `json:"active"`
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
