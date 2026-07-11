// Package domain holds transport-agnostic types and ports. It has no knowledge
// of HTTP, Confluence, Jira, the filesystem, or the CLI. Adapters implement the
// ports; use-cases (internal/app) depend only on this package.
package domain

// Resource is the generic unit the mirror/sync engine operates on. It is
// deliberately backend-agnostic: a Confluence page and a Jira issue both map
// onto it, so pull/status/push do not care which they hold.
type Resource struct {
	ID          string // backend id (Confluence content id, Jira issue key)
	Type        string `json:"-"` // backend resource type when safety checks require an explicit projection
	Title       string
	SpaceKey    string   // Confluence space key / Jira project key
	Version     int      // backend version number used for the optimistic gate
	Body        []byte   // native-format bytes (Confluence Storage Format or Jira wiki)
	BodyPresent bool     `json:"-"` // false when a successful partial response omitted the requested body projection
	Hash        string   // content hash of Body (canonical) — drives dirty detection
	Refs        []Ref    // opaque fragments discovered in Body (resolved for read)
	Parent      string   // parent content id, "" for top-level
	Ancestors   []string // ancestor titles top→down (drives mirror folder path)
	AncestorIDs []string `json:"-"` // ancestor content ids top→down (hierarchy safety checks)
	// AncestorsPresent distinguishes an explicit top-level [] projection from a
	// partial response that omitted or nulled hierarchy data.
	AncestorsPresent bool `json:"-"`
	Labels           []string
	Updated          string
	Restricted       *bool // nil when restriction metadata was not requested
	URL              string
}

// RefKind identifies the class of an opaque fragment inside a body.
type RefKind string

const (
	RefDrawio     RefKind = "drawio"
	RefUser       RefKind = "user"
	RefAttachment RefKind = "attachment"
	RefPageLink   RefKind = "page-link"
	RefImage      RefKind = "image"
)

// Ref is a resolved opaque fragment: an mxgraph diagram, a user mention, an
// attachment, a page link, or an inline image. ResolveForRead fills Display and
// (for visual fragments) Asset so the markdown read-view and sentinel comments
// can be human-legible; the underlying bytes are always preserved verbatim on
// write.
type Ref struct {
	Kind    RefKind           `json:"kind"`
	Key     string            `json:"key"`               // raw key (userkey, filename, diagramName, page id)
	Display string            `json:"display,omitempty"` // human-readable resolution
	Asset   string            `json:"asset,omitempty"`   // relative asset path when it renders to a file
	Params  map[string]string `json:"params,omitempty"`  // handler-specific bits (e.g. drawio revision)
}

// PageRef is a lightweight search/tree hit.
type PageRef struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Space   string `json:"space"`
	Version int    `json:"version"`
	Parent  string `json:"parent,omitempty"`
	Excerpt string `json:"excerpt,omitempty"`
	URL     string `json:"url,omitempty"`
}

// PageMeta is the non-body metadata of a Confluence page.
type PageMeta struct {
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	Space        string   `json:"space"`
	Version      int      `json:"version"`
	Ancestors    []string `json:"ancestors,omitempty"`
	Labels       []string `json:"labels,omitempty"`
	Restrictions *bool    `json:"restricted,omitempty"` // nil when restriction state was omitted/unknown
	Updated      string   `json:"updated,omitempty"`
	URL          string   `json:"url,omitempty"`
}

// Attachment describes a page/issue attachment.
type Attachment struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	MediaType string `json:"mediaType"`
	FileSize  int64  `json:"fileSize"`
	Version   int    `json:"version"`
	Comment   string `json:"comment,omitempty"`
	DownPath  string `json:"-"` // backend download path (relative to base)
}

// Comment is a page or issue comment.
type Comment struct {
	ID          string `json:"id"`
	Author      string `json:"author"`
	Created     string `json:"created"`
	Body        string `json:"body"`
	BodyStorage string `json:"body_storage,omitempty"` // native CSF when available; Body remains the plain fallback
}
