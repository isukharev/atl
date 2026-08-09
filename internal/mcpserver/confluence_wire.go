package mcpserver

type ConfluenceReferenceInput struct {
	Reference string `json:"reference" jsonschema:"numeric page id or same-origin page URL/path"`
}

type ConfluenceSearchInput struct {
	CQL      string `json:"cql" jsonschema:"bounded CQL selection; required"`
	Limit    int    `json:"limit,omitempty" jsonschema:"page size from 1 to 100; default 25"`
	Cursor   string `json:"cursor,omitempty" jsonschema:"opaque pagination cursor from a previous result"`
	MaxBytes int    `json:"max_bytes,omitempty" jsonschema:"maximum encoded result bytes from 1024 to 1048576; default 131072"`
}

// ConfluenceSectionInput carries an optional page-version binding whose
// requirement follows the provenance of the selection, not the tool. Heading
// occurrence and path are positional, so a selection read out of
// confluence_page_outline — or out of an earlier section result being re-read —
// must name the version it came from: if the page moved in between, the same
// occurrence can resolve to a different section with no observable symptom, and
// the binding turns that substitution into a refusal. A selection the caller
// fixed externally has no earlier revision to reconcile against; omitting the
// field then leaves the read explicitly ungated (page_version_gated:false)
// rather than pretending a binding that was never established.
type ConfluenceSectionInput struct {
	Reference           string `json:"reference" jsonschema:"numeric page id or same-origin page URL/path"`
	ExpectedPageVersion int    `json:"expected_page_version,omitempty" jsonschema:"the exact positive version integer this selection came from - the version in the confluence_page_outline result for this same page, or the version the previous section result returned when re-reading it; omit it only when the heading and occurrence were fixed outside any earlier read, which leaves the section explicitly ungated; the section is refused when a supplied version differs from the current one"`
	Heading             string `json:"heading" jsonschema:"exact heading title from confluence_page_outline, without a Markdown # prefix"`
	Occurrence          int    `json:"occurrence,omitempty" jsonschema:"1-based occurrence when the heading repeats"`
	MaxBytes            int    `json:"max_bytes,omitempty" jsonschema:"maximum Markdown bytes from 1 to 1048576; default 32768"`
}

type ConfluenceSectionSelectorInput struct {
	Heading    string `json:"heading" jsonschema:"exact heading title from confluence_page_outline, without a Markdown # prefix"`
	Occurrence int    `json:"occurrence,omitempty" jsonschema:"1-based occurrence when the heading repeats"`
}

// ConfluenceSectionsInput carries an ordered, bounded selection resolved from
// one page body. The version-binding rule is the same as for the one-section
// tool, but one gate covers every selector in the request.
type ConfluenceSectionsInput struct {
	Reference           string                           `json:"reference" jsonschema:"numeric page id or same-origin page URL/path"`
	ExpectedPageVersion int                              `json:"expected_page_version,omitempty" jsonschema:"the exact positive version integer these selections came from; omit only when every heading and occurrence was fixed outside an earlier read"`
	Selectors           []ConfluenceSectionSelectorInput `json:"selectors" jsonschema:"one to 32 ordered heading selectors; repeated selectors remain separate ordered results"`
	MaxBytes            int                              `json:"max_bytes,omitempty" jsonschema:"aggregate maximum Markdown bytes from 1 to 1048576; default 262144"`
}

// ConfluenceAttachmentListInput requires the page version the caller already
// observed. The gate is mandatory here (unlike the CLI flag) so a typed agent
// cannot silently attribute an inventory to a page revision it never read.
type ConfluenceAttachmentListInput struct {
	Reference           string `json:"reference" jsonschema:"numeric page id or same-origin page URL/path"`
	ExpectedPageVersion int    `json:"expected_page_version" jsonschema:"positive page version already observed for this exact page; the inventory is refused when the current version differs"`
	MaxBytes            int    `json:"max_bytes,omitempty" jsonschema:"maximum encoded result bytes from 1024 to 1048576; default 131072"`
}

// ConfluenceCommentListInput deliberately accepts a page id rather than the
// general Confluence reference grammar. This keeps URLs, paths, and titles out
// of both the request and every closed failure path. The backend request bound
// is fixed by the server and therefore is not model-selectable.
type ConfluenceCommentListInput struct {
	PageID              string `json:"page_id" jsonschema:"canonical positive decimal Confluence page id"`
	Location            string `json:"location,omitempty" jsonschema:"closed location selector: all, footer, inline, or resolved; default all"`
	State               string `json:"state,omitempty" jsonschema:"closed resolution selector: all, open, resolved, or unknown; default all"`
	Depth               string `json:"depth,omitempty" jsonschema:"closed relationship depth: root or all; default all"`
	ExpectedPageVersion int    `json:"expected_page_version,omitempty" jsonschema:"exact positive page version from earlier evidence; omit only for an externally fixed page id, leaving the read explicitly ungated"`
	MaxItems            int    `json:"max_items,omitempty" jsonschema:"aggregate raw backend comment-object bound before de-duplication or filtering, from 1 to 1000; default 100"`
	MaxBytes            int    `json:"max_bytes,omitempty" jsonschema:"maximum encoded result bytes from 1024 to 1048576; default 131072"`
}

type ConfluenceCommentThreadInput struct {
	PageID              string `json:"page_id" jsonschema:"canonical positive decimal Confluence page id"`
	CommentID           string `json:"comment_id" jsonschema:"exact canonical positive decimal comment id selected from confluence_comment_list"`
	ExpectedPageVersion int    `json:"expected_page_version,omitempty" jsonschema:"exact positive page_version from the confluence_comment_list that supplied comment_id; omit only for externally fixed evidence, leaving the thread explicitly ungated"`
	MaxItems            int    `json:"max_items,omitempty" jsonschema:"aggregate raw backend comment-object bound before de-duplication or filtering, from 1 to 1000; default 100"`
	MaxBytes            int    `json:"max_bytes,omitempty" jsonschema:"maximum encoded result bytes from 1024 to 1048576; default 262144"`
}

type ConfluenceTableSummaryInput struct {
	Reference           string `json:"reference" jsonschema:"numeric page id or same-origin page URL/path"`
	ExpectedPageVersion int    `json:"expected_page_version,omitempty" jsonschema:"exact positive page version already observed for this page; pass it when re-reading a table summary at a known revision, or omit it for an explicitly ungated summary"`
	Table               int    `json:"table,omitempty" jsonschema:"optional 1-based table index; omit to summarize all tables"`
	MaxBytes            int    `json:"max_bytes,omitempty" jsonschema:"maximum encoded result bytes from 1024 to 1048576; default 131072"`
}

type ConfluenceTableExtractInput struct {
	Reference           string `json:"reference" jsonschema:"numeric page id or same-origin page URL/path"`
	ExpectedPageVersion int    `json:"expected_page_version,omitempty" jsonschema:"exact positive version from the confluence_table_summary result that supplied this table index; omit it only when the table index was fixed outside any earlier read, which leaves the extract explicitly ungated"`
	Table               int    `json:"table" jsonschema:"required 1-based table index; all-table extraction is forbidden"`
	MaxBytes            int    `json:"max_bytes,omitempty" jsonschema:"maximum encoded result bytes from 1024 to 1048576; default 262144"`
}

// MirrorSnapshotInput is intentionally empty. The owner binds the only mirror
// root through the server environment; the model cannot select a filesystem
// path or request a remote check.
type MirrorSnapshotInput struct{}
