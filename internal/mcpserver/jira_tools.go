package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
)

type jiraProjectIssuePageReader interface {
	ProjectIssuePage(context.Context, domain.BrokerProjectPageArguments) (*app.JiraProjectIssuePageResult, error)
}

func registerJiraTools(server *mcp.Server, deps Dependencies) {
	addReadOnlyTool(server, readOnlyTool("jira_fields", "Discover or summarize Jira fields", "List value-free field definitions or return a compact summary with explicit catalog completeness and reconciled counts."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in JiraFieldsInput) (*mcp.CallToolResult, *app.JiraFieldCatalogResult, error) {
			maxBytes, err := boundedJiraEvidenceBytes(in.MaxBytes)
			if err != nil {
				return nil, nil, classified(err)
			}
			jira, err := jiraReader(deps)
			if err != nil {
				return nil, nil, classified(err)
			}
			custom := ""
			if in.Custom != nil {
				custom = fmt.Sprintf("%t", *in.Custom)
			}
			out, err := jira.FieldCatalog(ctx, app.JiraFieldCatalogOpts{
				ID: in.ID, NameLike: in.NameLike, IDLike: in.IDLike, Schema: in.Schema,
				Custom: custom, SummaryOnly: in.SummaryOnly,
			})
			if err == nil {
				err = boundedJiraEvidenceOutput(out, maxBytes)
			}
			return nil, out, classified(err)
		})

	addReadOnlyTool(server, readOnlyTool("jira_issue_search", "Search Jira issues", "Return one compact typed IssueList page. Use a bounded JQL and select fields with `columns` (preferred), `fields`, or `projection`; supply at most one non-empty selector."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in JiraIssueSearchInput) (*mcp.CallToolResult, *app.IssueList, error) {
			if strings.TrimSpace(in.JQL) == "" {
				return nil, nil, classified(fmt.Errorf("%w: jql is required", domain.ErrUsage))
			}
			nonEmptySelectors := 0
			for _, selector := range [][]string{in.Columns, in.Fields, in.Projection} {
				if len(selector) > 0 {
					nonEmptySelectors++
				}
			}
			if nonEmptySelectors > 1 {
				return nil, nil, classified(fmt.Errorf("%w: columns, fields, and projection are aliases; supply only one", domain.ErrUsage))
			}
			columns := in.Columns
			if len(columns) == 0 {
				columns = in.Fields
			}
			if len(columns) == 0 {
				columns = in.Projection
			}
			limit, err := boundedDefault(in.Limit, 50, 1000, "limit")
			if err != nil {
				return nil, nil, classified(err)
			}
			maxBytes, err := boundedJiraEvidenceBytes(in.MaxBytes)
			if err != nil {
				return nil, nil, classified(err)
			}
			jira, err := jiraReader(deps)
			if err != nil {
				return nil, nil, classified(err)
			}
			out, err := jira.SearchIssueListView(ctx, in.JQL, columns, in.View, limit, in.Cursor)
			if err == nil {
				err = boundedJiraEvidenceOutput(out, maxBytes)
			}
			return nil, out, classified(err)
		})

	addReadOnlyTool(server, readOnlyTool("jira_project_issue_page", "Read a Jira project issue page", "Read one Broker-qualified project page with only summary and description fields. The result preserves ordered identities, field presence, coordinate exhaustion, and always-false selection completeness; it never accepts JQL or continues automatically."),
		func(ctx context.Context, request *mcp.CallToolRequest, in JiraProjectIssuePageInput) (*mcp.CallToolResult, *app.JiraProjectIssuePageResult, error) {
			if err := validateJiraProjectIssuePageRawInput(request); err != nil {
				return nil, nil, classified(err)
			}
			limit, err := boundedDefault(in.Limit, domain.BrokerProjectPageMaxResults, domain.BrokerProjectPageMaxResults, "limit")
			if err != nil {
				return nil, nil, classified(err)
			}
			maxBytes, err := boundedJiraEvidenceBytes(in.MaxBytes)
			if err != nil {
				return nil, nil, classified(err)
			}
			arguments, err := app.NewJiraProjectIssuePageArguments(in.ProjectKey, in.Fields, limit, in.Cursor)
			if err != nil {
				return nil, nil, classified(err)
			}
			jira, err := jiraReader(deps)
			if err != nil {
				return nil, nil, classified(err)
			}
			reader, ok := jira.(jiraProjectIssuePageReader)
			if !ok {
				_, err = brokercontract.ErrorForReason(domain.BrokerReasonUnsupported)
				return nil, nil, classified(err)
			}
			out, err := reader.ProjectIssuePage(ctx, arguments)
			if err == nil {
				err = boundedJiraEvidenceOutput(out, maxBytes)
			}
			return nil, out, classified(err)
		})

	addReadOnlyTool(server, readOnlyTool("jira_issue_field_get", "Expand one Jira field", "Read one exact compact field value with snapshot provenance and an explicit byte bound. Use this for a required projection.clipped digest field; do not repeat the full digest."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in JiraIssueFieldGetInput) (*mcp.CallToolResult, *app.JiraIssueFieldEvidenceResult, error) {
			if strings.TrimSpace(in.Key) == "" || strings.TrimSpace(in.Field) == "" {
				return nil, nil, classified(fmt.Errorf("%w: key and field are required", domain.ErrUsage))
			}
			maxBytes, err := boundedBytes(in.MaxBytes, app.JiraIssueFieldEvidenceDefaultMaxBytes,
				app.JiraIssueFieldEvidenceMinMaxBytes, app.JiraIssueFieldEvidenceMaxMaxBytes)
			if err != nil {
				return nil, nil, classified(err)
			}
			jira, err := jiraReader(deps)
			if err != nil {
				return nil, nil, classified(err)
			}
			out, err := jira.IssueFieldEvidence(ctx, in.Key, app.JiraIssueFieldEvidenceOpts{Selector: in.Field, MaxBytes: maxBytes})
			return nil, out, classified(err)
		})

	registerJiraIssueGraphTool(server, deps)

	addReadOnlyTool(server, readOnlyTool("jira_issue_history", "Summarize Jira issue history", "Return the deterministic changelog summary for one issue: provenance, completeness, applied filters, cardinality and consistency facts, and per-field `last_changes` for explicitly selected fields. Raw changelog rows are never returned; use the CLI when individual changes are themselves required evidence."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in JiraIssueHistoryInput) (*mcp.CallToolResult, *app.JiraHistorySummaryResult, error) {
			if strings.TrimSpace(in.Key) == "" {
				return nil, nil, classified(fmt.Errorf("%w: key is required", domain.ErrUsage))
			}
			maxBytes, err := boundedJiraEvidenceBytes(in.MaxBytes)
			if err != nil {
				return nil, nil, classified(err)
			}
			jira, err := jiraReader(deps)
			if err != nil {
				return nil, nil, classified(err)
			}
			full, err := jira.HistoryFiltered(ctx, in.Key, app.JiraHistoryOpts{Fields: in.Fields, Since: in.Since, Until: in.Until})
			if err != nil {
				return nil, nil, classifiedJiraHistoryRead(err)
			}
			// Project before bounding so the raw History array can never reach a
			// client, not even inside an oversize diagnostic.
			out := app.JiraHistorySummaryProjection(full)
			if err := boundedJiraEvidenceOutput(out, maxBytes); err != nil {
				return nil, nil, classifiedJiraHistoryRead(err)
			}
			return nil, out, nil
		})

	addReadOnlyTool(server, readOnlyTool("jira_issue_refs", "Summarize Jira issue references", "Return bounded selection, source-qualification, reference-count, kind-count, completeness, truncation, and reconciliation facts for one exact issue or a bounded JQL selection. Raw reference URLs, issue summaries, issue types, and source text are never returned; use the CLI when URLs are themselves required evidence."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in JiraIssueRefsInput) (*mcp.CallToolResult, *app.JiraIssueRefsView, error) {
			opts, maxBytes, err := validatedJiraIssueRefsInput(in)
			if err != nil {
				return nil, nil, classifiedJiraIssueRefsRead(err)
			}
			jira, err := jiraReader(deps)
			if err != nil {
				return nil, nil, classifiedJiraIssueRefsRead(err)
			}
			full, err := jira.IssueRefs(ctx, opts)
			if err != nil {
				return nil, nil, classifiedJiraIssueRefsRead(err)
			}
			// Project before validation and bounding so a raw URL can never
			// reach a client, including inside an output-limit diagnostic.
			out := app.JiraIssueRefsViewProjection(full)
			if err := validateJiraIssueRefsView(out, opts); err != nil {
				return nil, nil, classifiedJiraIssueRefsRead(err)
			}
			if err := boundedJiraEvidenceOutput(out, maxBytes); err != nil {
				return nil, nil, classifiedJiraIssueRefsRead(err)
			}
			return nil, out, nil
		})

	addReadOnlyTool(server, readOnlyTool("jira_epic_digest", "Read qualified epic evidence", "Aggregate selected dated evidence sources. Omit sources already present in a portfolio snapshot."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in JiraEpicDigestInput) (*mcp.CallToolResult, *app.JiraEpicDigestResult, error) {
			if _, err := app.ProjectJiraEpicDigest(nil, in.Projection); err != nil {
				return nil, nil, classified(err)
			}
			if len(in.Include) == 0 {
				return nil, nil, classified(fmt.Errorf("%w: include must select at least one evidence source", domain.ErrUsage))
			}
			for _, include := range in.Include {
				if strings.EqualFold(strings.TrimSpace(include), "confluence") {
					return nil, nil, classified(fmt.Errorf("%w: use confluence_page_section separately for bounded linked evidence", domain.ErrUsage))
				}
			}
			childLimit, err := boundedDefault(in.ChildLimit, 1000, 1000, "child_limit")
			if err != nil {
				return nil, nil, classified(err)
			}
			commentLimit, err := boundedDefault(in.CommentLimit, 50, 50, "comment_limit")
			if err != nil {
				return nil, nil, classified(err)
			}
			historyLimit, err := boundedDefault(in.HistoryLimit, 500, 500, "history_limit")
			if err != nil {
				return nil, nil, classified(err)
			}
			maxBytes, err := boundedJiraEvidenceBytes(in.MaxBytes)
			if err != nil {
				return nil, nil, classified(err)
			}
			jira, err := jiraReader(deps)
			if err != nil {
				return nil, nil, classified(err)
			}
			out, err := jira.EpicDigest(ctx, in.Key, app.JiraEpicDigestOpts{
				Quarter: in.Quarter, Since: in.Since, Until: in.Until, Include: in.Include,
				StatusField: in.StatusField, DoDField: in.DoDField, EpicField: in.EpicField,
				ChildLimit: childLimit, CommentLimit: commentLimit, HistoryLimit: historyLimit,
			})
			if err == nil {
				out, err = app.ProjectJiraEpicDigest(out, in.Projection)
			}
			if err == nil {
				err = boundedJiraEvidenceOutput(out, maxBytes)
			}
			return nil, out, classified(err)
		})

	addReadOnlyTool(server, readOnlyTool("jira_board_view", "Read a Jira board snapshot", "Return one normalized board/backlog membership snapshot with explicit completeness."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in JiraBoardViewInput) (*mcp.CallToolResult, *app.BoardSnapshot, error) {
			limit, err := boundedDefault(in.Limit, 200, 1000, "limit")
			if err != nil {
				return nil, nil, classified(err)
			}
			maxBytes, err := boundedJiraEvidenceBytes(in.MaxBytes)
			if err != nil {
				return nil, nil, classified(err)
			}
			jira, err := jiraReader(deps)
			if err != nil {
				return nil, nil, classified(err)
			}
			out, err := jira.BoardSnapshot(ctx, in.BoardID, app.BoardSnapshotOpts{
				Scope: in.Scope, Columns: in.Columns, View: in.View, JQL: in.JQL, Limit: limit,
				EpicField: in.EpicField, DoneStatuses: in.DoneStatuses,
			})
			if err == nil {
				err = boundedJiraEvidenceOutput(out, maxBytes)
			}
			return nil, out, classified(err)
		})

	structureGetTool := readOnlyTool("jira_structure_get", "Read Jira Structure metadata", "Return compact metadata for one exact Structure id without owner, permission, view, or forest payloads. Accepts a positive integer or its canonical decimal string.")
	structureGetTool.InputSchema = jiraStructureGetInputSchema()
	addReadOnlyTool(server, structureGetTool,
		func(ctx context.Context, _ *mcp.CallToolRequest, in JiraStructureGetInput) (*mcp.CallToolResult, *app.StructureMetadataResult, error) {
			structureID, err := parseStructureIDInput(in.StructureID)
			if err != nil {
				return nil, nil, classifiedStructureRead(err)
			}
			jira, err := jiraReader(deps)
			if err != nil {
				return nil, nil, classifiedStructureRead(err)
			}
			out, err := jira.Structure(ctx, structureID)
			if err != nil {
				return nil, nil, classifiedStructureRead(err)
			}
			if out == nil || out.ID != structureID || strings.TrimSpace(out.Name) == "" {
				return nil, nil, classifiedStructureRead(fmt.Errorf("%w: Structure metadata is not reconciled", domain.ErrCheckFailed))
			}
			projected := &app.StructureMetadataResult{SchemaVersion: 1, ID: out.ID, Name: out.Name, ReadOnly: out.ReadOnly}
			if err := boundedStructureMetadataOutput(projected); err != nil {
				return nil, nil, classifiedStructureRead(err)
			}
			return nil, projected, nil
		})

	addReadOnlyTool(server, readOnlyTool("jira_structure_view", "Read a bounded Jira Structure view", "Return one normalized full or exact stored-folder subtree with explicit fields, completeness, forest-version provenance, row bound, and byte bound. When a subtree selector came from an earlier view, copy both forest_version.signature and forest_version.version into expected_forest_signature and expected_forest_version; omitting both leaves the selection explicitly ungated. A returned pair with either member zero is non-bindable and must remain ungated. Jira fields and folder labels are separately timed and are not transactionally covered by the forest version."),
		func(ctx context.Context, _ *mcp.CallToolRequest, in JiraStructureViewInput) (*mcp.CallToolResult, *app.StructureSnapshot, error) {
			fields, maxRows, maxBytes, selector, err := validatedStructureViewInput(in)
			if err != nil {
				return nil, nil, classifiedStructureRead(err)
			}
			expectedForestVersion, err := validatedExpectedStructureForestVersion(in)
			if err != nil {
				return nil, nil, classifiedStructureRead(err)
			}
			jira, err := jiraReader(deps)
			if err != nil {
				return nil, nil, classifiedStructureRead(err)
			}
			out, err := jira.StructureSnapshot(ctx, in.StructureID, app.StructureSnapshotOpts{
				Attributes: fields, BatchSize: 100, MaxRows: maxRows, MaxScanRows: jiraStructureViewMaxMaxRows,
				ExpectedForestVersion: expectedForestVersion, StructureFolderSelector: selector,
			})
			if err != nil {
				return nil, nil, classifiedStructureRead(err)
			}
			if err := validateStructureView(out, in.StructureID, fields, maxRows, selector, expectedForestVersion); err != nil {
				return nil, nil, classifiedStructureRead(err)
			}
			if err := boundedStructureOutput(out, maxBytes); err != nil {
				return nil, nil, classifiedStructureRead(err)
			}
			return nil, out, nil
		})
}

func validateJiraProjectIssuePageRawInput(request *mcp.CallToolRequest) error {
	var arguments map[string]json.RawMessage
	if request == nil || request.Params == nil || json.Unmarshal(request.Params.Arguments, &arguments) != nil {
		return fmt.Errorf("%w: invalid project-page input", domain.ErrUsage)
	}
	for _, name := range []string{"fields", "limit", "cursor", "max_bytes"} {
		if raw, present := arguments[name]; present && strings.TrimSpace(string(raw)) == "null" {
			return fmt.Errorf("%w: %s must not be null", domain.ErrUsage, name)
		}
	}
	for _, name := range []string{"limit", "max_bytes"} {
		if raw, present := arguments[name]; present {
			var value int
			if json.Unmarshal(raw, &value) != nil || value == 0 {
				return fmt.Errorf("%w: %s must use its declared positive bound", domain.ErrUsage, name)
			}
		}
	}
	if raw, present := arguments["fields"]; present {
		var fields []string
		if json.Unmarshal(raw, &fields) != nil || fields == nil || len(fields) > 2 {
			return fmt.Errorf("%w: fields must be an array with at most 2 entries", domain.ErrUsage)
		}
	}
	if raw, present := arguments["cursor"]; present {
		var cursor string
		if json.Unmarshal(raw, &cursor) != nil || cursor == "" {
			return fmt.Errorf("%w: cursor must be a canonical decimal string", domain.ErrUsage)
		}
	}
	return nil
}
