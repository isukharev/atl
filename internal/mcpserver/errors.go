package mcpserver

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/diagnostic"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

type toolErrorKind string

type toolRemediation string

type toolError struct {
	Kind        string              `json:"kind"`
	Remediation string              `json:"remediation,omitempty"`
	Message     string              `json:"message"`
	Recovery    diagnostic.Recovery `json:"recovery"`
}

func (e toolError) Error() string {
	data, _ := json.Marshal(e)
	return string(data)
}

// toolErrorRule is one tool family's policy for one diagnostic category: the
// static, content-free sentence the client sees, or safeToolMessage's redacted
// backend detail for the categories a family explicitly opts into, plus an
// optional remediation that replaces diagnostic.Classify's coarse default.
type toolErrorRule struct {
	message     string
	safeMessage bool
	remediation toolRemediation
}

// toolErrorOverride is a named hook that upgrades one diagnostic category to a
// recoverable remediation and a typed, content-free message. Hooks are matched
// on kind and applied in slice order, and the last match wins — a policy states
// its precedence by ordering rather than by nested conditionals, and the slice
// is never map-iterated, so the outcome stays deterministic.
type toolErrorOverride struct {
	kind  toolErrorKind
	apply func(err error) (remediation toolRemediation, message string, ok bool)
}

// toolErrorPolicy is a tool family's read-error policy: a per-category rule
// table, a fallback for the categories it does not name, and ordered overrides.
type toolErrorPolicy struct {
	fallback  toolErrorRule
	kinds     map[toolErrorKind]toolErrorRule
	overrides []toolErrorOverride
	operation diagnostic.OperationContext
}

func (p toolErrorPolicy) classify(err error) error {
	if err == nil {
		return nil
	}
	classifiedKind, classifiedRemediation := diagnostic.Classify(err)
	kind := toolErrorKind(classifiedKind)
	remediation := toolRemediation(classifiedRemediation)
	rule, named := p.kinds[kind]
	if !named {
		rule = p.fallback
	}
	message := rule.message
	if rule.safeMessage {
		message = safeToolMessage(err)
	}
	if rule.remediation != "" {
		remediation = rule.remediation
	}
	for _, override := range p.overrides {
		if override.kind != kind {
			continue
		}
		if overrideRemediation, overrideMessage, matched := override.apply(err); matched {
			remediation, message = overrideRemediation, overrideMessage
		}
	}
	operation := p.operation
	if operation == diagnostic.OperationUnknown {
		operation = diagnostic.OperationRead
	}
	return toolError{Kind: string(kind), Remediation: string(remediation), Message: message, Recovery: diagnostic.Recover(err, operation)}
}

func staticMessage(message string) toolErrorRule { return toolErrorRule{message: message} }

func staticMessageWithRemediation(message string, remediation toolRemediation) toolErrorRule {
	return toolErrorRule{message: message, remediation: remediation}
}

// redactedBackendDetail opts a category into safeToolMessage, which is the only
// path allowed to derive dynamic text from a backend failure and admits nothing
// beyond an HTTP status or a transport category.
var redactedBackendDetail = toolErrorRule{safeMessage: true}

// confluencePageVersionMismatchOverride reports a page that moved out from under
// a positional selection. Only the typed application error qualifies — never a
// string match — and it carries two integers and nothing else. Each family
// supplies its own remediation because the correct re-read differs per tool.
func confluencePageVersionMismatchOverride(remediation toolRemediation) toolErrorOverride {
	return toolErrorOverride{kind: "check_failed", apply: func(err error) (toolRemediation, string, bool) {
		var mismatch *app.ConfluencePageVersionMismatchError
		if !errors.As(err, &mismatch) || mismatch == nil {
			return "", "", false
		}
		return remediation, fmt.Sprintf("expected Confluence page version %d does not match the current page version %d", mismatch.Expected, mismatch.Current), true
	}}
}

// confluenceTableOutOfRangeOverride reports an out-of-range table selection,
// which the caller can recover by re-summarizing. The typed error carries no
// page or cell content.
var confluenceTableOutOfRangeOverride = toolErrorOverride{
	kind: "not_found",
	apply: func(err error) (toolRemediation, string, bool) {
		var selection *app.ConfluenceTableSelectionError
		if !errors.As(err, &selection) {
			return "", "", false
		}
		return "summarize_then_select_table", fmt.Sprintf("selected Confluence table index %d is out of range; available table count is %d", selection.Requested, selection.Available), true
	},
}

// confluenceSectionOutOfRangeOverride and confluenceSectionAmbiguousOverride
// split the one typed section selection error across the two categories it
// unwraps to. Both are recoverable through the outline and both carry only
// occurrence counts — no heading, page reference, or backend text.
var confluenceSectionOutOfRangeOverride = toolErrorOverride{
	kind: "not_found",
	apply: func(err error) (toolRemediation, string, bool) {
		var selection *app.ConfluenceSectionSelectionError
		if !errors.As(err, &selection) || selection.Requested <= 0 {
			return "", "", false
		}
		return "outline_then_select_section", fmt.Sprintf("selected Confluence heading occurrence %d is out of range; available occurrence count is %d", selection.Requested, selection.Available), true
	},
}

var confluenceSectionAmbiguousOverride = toolErrorOverride{
	kind: "check_failed",
	apply: func(err error) (toolRemediation, string, bool) {
		var selection *app.ConfluenceSectionSelectionError
		if !errors.As(err, &selection) || selection.Requested != 0 {
			return "", "", false
		}
		return "outline_then_select_section", fmt.Sprintf("Confluence heading selection is ambiguous; available occurrence count is %d, so select an occurrence from 1 to %d", selection.Available, selection.Available), true
	},
}

func structureFolderSelection(err error) (*app.StructureFolderSelectionError, bool) {
	var selection *app.StructureFolderSelectionError
	if !errors.As(err, &selection) || selection == nil {
		return nil, false
	}
	return selection, true
}

// structureFolderNotFoundOverride and structureFolderSelectorOverride report a
// stale, ambiguous, or unvalidatable stored-folder selector, which the caller
// recovers by re-viewing the Structure. The typed error carries no folder id,
// row id, path, label, Structure content, or backend text.
var structureFolderNotFoundOverride = toolErrorOverride{
	kind: "not_found",
	apply: func(err error) (toolRemediation, string, bool) {
		selection, ok := structureFolderSelection(err)
		if !ok || selection.Reason != app.StructureFolderSelectionNotFound {
			return "", "", false
		}
		return "view_then_select_subtree", fmt.Sprintf("selected Jira Structure folder was not found; available stored-folder count is %d", selection.Available), true
	},
}

var structureFolderSelectorOverride = toolErrorOverride{
	kind: "check_failed",
	apply: func(err error) (toolRemediation, string, bool) {
		selection, ok := structureFolderSelection(err)
		if !ok {
			return "", "", false
		}
		switch selection.Reason {
		case app.StructureFolderSelectionAmbiguous:
			return "view_then_select_subtree", fmt.Sprintf("Jira Structure folder selector is ambiguous; matching stored-folder count is %d and available stored-folder count is %d", selection.Matches, selection.Available), true
		case app.StructureFolderSelectionLabelsIncomplete:
			return "view_then_select_subtree", fmt.Sprintf("Jira Structure folder path cannot be validated because folder labels are incomplete; available stored-folder count is %d", selection.Available), true
		}
		return "", "", false
	},
}

// structureForestVersionMismatchOverride reports a forest that moved out from
// under a stored-folder selection; the typed error carries four integers.
var structureForestVersionMismatchOverride = toolErrorOverride{
	kind: "check_failed",
	apply: func(err error) (toolRemediation, string, bool) {
		var mismatch *app.StructureForestVersionMismatchError
		if !errors.As(err, &mismatch) || mismatch == nil {
			return "", "", false
		}
		return "reread_structure_view_then_retry_expected_forest_version", fmt.Sprintf(
			"expected Jira Structure forest signature %d version %d does not match current signature %d version %d",
			mismatch.Expected.Signature, mismatch.Expected.Version,
			mismatch.Current.Signature, mismatch.Current.Version,
		), true
	},
}

// genericToolPolicy is the default: diagnostic.Classify picks the kind and
// remediation, and safeToolMessage decides how much of the failure may cross
// the privacy boundary.
var genericToolPolicy = toolErrorPolicy{fallback: redactedBackendDetail}

var confluenceOutlineReadPolicy = toolErrorPolicy{
	fallback: staticMessage("Confluence page outline read failed"),
	kinds: map[toolErrorKind]toolErrorRule{
		"usage_error":           staticMessage("invalid Confluence page outline request"),
		"configuration_error":   staticMessage("Confluence page outline service is not configured"),
		"authentication_failed": staticMessage("Confluence page outline authentication failed"),
		"forbidden":             staticMessage("Confluence page outline access is forbidden"),
		"not_found":             staticMessage("Confluence page was not found"),
		"check_failed":          staticMessage("Confluence page outline result failed validation"),
		"output_limit_exceeded": staticMessage("Confluence page outline result exceeds its output bound"),
		"api_error":             redactedBackendDetail,
		"transport_error":       redactedBackendDetail,
	},
}

var confluencePageMetadataReadPolicy = toolErrorPolicy{
	fallback: staticMessage("Confluence page metadata read failed"),
	kinds: map[toolErrorKind]toolErrorRule{
		"usage_error":           staticMessage("invalid Confluence page metadata request"),
		"configuration_error":   staticMessage("Confluence page metadata service is not configured"),
		"authentication_failed": staticMessage("Confluence page metadata authentication failed"),
		"forbidden":             staticMessage("Confluence page metadata access is forbidden"),
		"not_found":             staticMessage("Confluence page was not found"),
		"check_failed":          staticMessage("Confluence page metadata failed validation"),
		"output_limit_exceeded": staticMessageWithRemediation("Confluence page metadata exceeds its output bound", "use_cli_conf_page_meta"),
		"rate_limited":          staticMessage("Confluence page metadata rate limit was exhausted"),
		"api_error":             staticMessage("Confluence page metadata API request failed"),
		"transport_error":       staticMessage("Confluence page metadata transport failed"),
	},
}

var jiraHistoryReadPolicy = toolErrorPolicy{
	fallback: staticMessage("Jira issue history read failed"),
	kinds: map[toolErrorKind]toolErrorRule{
		"usage_error":           staticMessage("invalid Jira issue history request"),
		"configuration_error":   staticMessage("Jira issue history service is not configured"),
		"authentication_failed": staticMessage("Jira issue history authentication failed"),
		"forbidden":             staticMessage("Jira issue history access is forbidden"),
		"not_found":             staticMessage("Jira issue history was not found"),
		"check_failed":          staticMessage("Jira issue history summary failed validation"),
		"output_limit_exceeded": staticMessage("Jira issue history result exceeds max_bytes"),
		"api_error":             redactedBackendDetail,
		"transport_error":       redactedBackendDetail,
	},
}

var jiraIssueRefsReadPolicy = toolErrorPolicy{
	fallback: staticMessage("Jira issue reference summary read failed"),
	kinds: map[toolErrorKind]toolErrorRule{
		"usage_error":           staticMessage("invalid Jira issue reference summary request"),
		"configuration_error":   staticMessage("Jira issue reference summary service is not configured"),
		"authentication_failed": staticMessage("Jira issue reference summary authentication failed"),
		"forbidden":             staticMessage("Jira issue reference summary access is forbidden"),
		"not_found":             staticMessage("Jira issue reference source was not found"),
		"check_failed":          staticMessage("Jira issue reference summary failed validation"),
		"output_limit_exceeded": staticMessage("Jira issue reference summary exceeds max_bytes"),
		"rate_limited":          staticMessage("Jira issue reference summary rate limit was exhausted"),
		"api_error":             staticMessage("Jira issue reference summary API request failed"),
		"transport_error":       staticMessage("Jira issue reference summary transport failed"),
	},
}

var confluenceTableReadPolicy = toolErrorPolicy{
	operation: diagnostic.OperationConfluenceTableRead,
	fallback:  staticMessage("Confluence table read failed"),
	kinds: map[toolErrorKind]toolErrorRule{
		"usage_error":           staticMessage("invalid Confluence table request"),
		"configuration_error":   staticMessage("Confluence table service is not configured"),
		"authentication_failed": staticMessage("Confluence table authentication failed"),
		"forbidden":             staticMessage("Confluence table access is forbidden"),
		"not_found":             staticMessage("Confluence page or table was not found"),
		"check_failed":          staticMessage("Confluence table result failed validation"),
		"output_limit_exceeded": staticMessage("Confluence table result exceeds the selected output bound"),
		"api_error":             redactedBackendDetail,
		"transport_error":       redactedBackendDetail,
	},
	overrides: []toolErrorOverride{
		confluenceTableOutOfRangeOverride,
		// A positional table index selected from a summary is meaningful only for
		// that summary's page revision, so a mismatch tells the caller to
		// re-summarize; retrying the old index would preserve the drift.
		confluencePageVersionMismatchOverride("reread_table_summary_then_retry_expected_version"),
	},
}

var confluenceSectionReadPolicy = toolErrorPolicy{
	operation: diagnostic.OperationConfluenceSectionRead,
	fallback:  staticMessage("Confluence page section read failed"),
	kinds: map[toolErrorKind]toolErrorRule{
		"usage_error":           staticMessage("invalid Confluence page section request"),
		"configuration_error":   staticMessage("Confluence page section service is not configured"),
		"authentication_failed": staticMessage("Confluence page section authentication failed"),
		"forbidden":             staticMessage("Confluence page section access is forbidden"),
		"not_found":             staticMessage("Confluence page, section, or heading was not found"),
		"check_failed":          staticMessage("Confluence page section result failed validation"),
		"output_limit_exceeded": staticMessage("Confluence page section result exceeds the selected output bound"),
		"api_error":             redactedBackendDetail,
		"transport_error":       redactedBackendDetail,
	},
	overrides: []toolErrorOverride{
		confluenceSectionOutOfRangeOverride,
		confluenceSectionAmbiguousOverride,
		// Ordered last so a moved page outranks an ambiguous selection: the new
		// body may have renumbered the very occurrence this request selected, so
		// re-reading the outline — not re-selecting — is the fix.
		confluencePageVersionMismatchOverride("reread_outline_then_retry_expected_version"),
	},
}

// confluenceAttachmentInventoryReadPolicy is deliberately coarser than the other
// policies: every category maps to a static sentence, including api_error and
// transport_error, so no backend diagnostic, page title, or attachment filename
// can reach the client through a failure path. The only dynamic content is the
// typed page-version mismatch, which carries two integers and nothing else.
var confluenceAttachmentInventoryReadPolicy = toolErrorPolicy{
	operation: diagnostic.OperationConfluenceAttachmentRead,
	fallback:  staticMessage("Confluence attachment inventory read failed"),
	kinds: map[toolErrorKind]toolErrorRule{
		"usage_error":           staticMessage("invalid Confluence attachment inventory request"),
		"configuration_error":   staticMessage("Confluence attachment inventory service is not configured"),
		"authentication_failed": staticMessage("Confluence attachment inventory authentication failed"),
		"forbidden":             staticMessage("Confluence attachment inventory access is forbidden"),
		"not_found":             staticMessage("Confluence page was not found"),
		"check_failed":          staticMessage("Confluence attachment inventory failed validation"),
		"output_limit_exceeded": staticMessageWithRemediation("Confluence attachment inventory exceeds the selected output bound", "raise_bound_or_use_cli_attachment_list"),
	},
	overrides: []toolErrorOverride{
		confluencePageVersionMismatchOverride("reread_page_then_retry_expected_version"),
	},
}

// confluenceCommentReadPolicy is fully static. Comment APIs can return page
// titles, user-controlled bodies, URLs, and transport prose in wrapped errors;
// none is allowed to influence an MCP response. A version conflict is useful
// only as a closed instruction to re-read, not as a replay hint.
var confluenceCommentReadPolicy = toolErrorPolicy{
	fallback: staticMessage("Confluence comment read failed"),
	kinds: map[toolErrorKind]toolErrorRule{
		"usage_error":           staticMessage("invalid Confluence comment request"),
		"configuration_error":   staticMessage("Confluence comment service is not configured"),
		"authentication_failed": staticMessage("Confluence comment authentication failed"),
		"forbidden":             staticMessage("Confluence comment access is forbidden"),
		"not_found":             staticMessage("Confluence page or comment was not found"),
		"version_conflict":      staticMessageWithRemediation("Confluence page version changed", "reread_page_then_retry_expected_version"),
		"check_failed":          staticMessage("Confluence comment result failed validation"),
		"output_limit_exceeded": staticMessageWithRemediation("Confluence comment result exceeds the selected output bound", "narrow_selection_or_raise_bound"),
		"rate_limited":          staticMessage("Confluence comment rate limit was exhausted"),
		"api_error":             staticMessage("Confluence comment API request failed"),
		"transport_error":       staticMessage("Confluence comment transport failed"),
	},
}

var jiraStructureReadPolicy = toolErrorPolicy{
	operation: diagnostic.OperationJiraStructureRead,
	fallback:  staticMessage("Jira Structure read failed"),
	kinds: map[toolErrorKind]toolErrorRule{
		"usage_error":           staticMessage("invalid Jira Structure request"),
		"configuration_error":   staticMessage("Jira Structure service is not configured"),
		"authentication_failed": staticMessage("Jira Structure authentication failed"),
		"forbidden":             staticMessage("Jira Structure access is forbidden"),
		"not_found":             staticMessage("Jira Structure or subtree was not found"),
		"check_failed":          staticMessage("Jira Structure result failed validation"),
		"output_limit_exceeded": staticMessage("Jira Structure result exceeds the selected output bound"),
		"api_error":             redactedBackendDetail,
		"transport_error":       redactedBackendDetail,
	},
	overrides: []toolErrorOverride{
		structureFolderNotFoundOverride,
		structureForestVersionMismatchOverride,
		// Ordered last so a recoverable stored-folder selector outranks a forest
		// version mismatch: the selector must be fixed before an expected forest
		// version can mean anything. This is the opposite of the section policy.
		structureFolderSelectorOverride,
	},
}

var mirrorReadPolicy = toolErrorPolicy{
	fallback: staticMessage("local mirror snapshot failed"),
	kinds: map[toolErrorKind]toolErrorRule{
		"configuration_error": staticMessage("local mirror root is not configured or is invalid"),
		"check_failed":        staticMessage("local mirror snapshot could not be completed"),
	},
}

func classified(err error) error { return genericToolPolicy.classify(err) }

func classifiedOutlineRead(err error) error { return confluenceOutlineReadPolicy.classify(err) }

func classifiedConfluencePageMetadataRead(err error) error {
	return confluencePageMetadataReadPolicy.classify(err)
}

func classifiedJiraHistoryRead(err error) error { return jiraHistoryReadPolicy.classify(err) }

func classifiedJiraIssueRefsRead(err error) error { return jiraIssueRefsReadPolicy.classify(err) }

func classifiedTableRead(err error) error { return confluenceTableReadPolicy.classify(err) }

func classifiedSectionRead(err error) error { return confluenceSectionReadPolicy.classify(err) }

func classifiedAttachmentInventoryRead(err error) error {
	return confluenceAttachmentInventoryReadPolicy.classify(err)
}

func classifiedConfluenceCommentRead(err error) error {
	return confluenceCommentReadPolicy.classify(err)
}

func classifiedStructureRead(err error) error { return jiraStructureReadPolicy.classify(err) }

func classifiedMirrorRead(err error) error { return mirrorReadPolicy.classify(err) }

func safeToolMessage(err error) string {
	if config.IsSecureURLError(err) {
		return "backend URL is not approved for authenticated reads"
	}
	if errors.Is(err, domain.ErrOutputLimit) {
		return "tool result exceeds max_bytes"
	}
	var apiErr *httpx.APIError
	if errors.As(err, &apiErr) {
		return fmt.Sprintf("backend returned HTTP %d", apiErr.Status)
	}
	var transportErr *httpx.TransportError
	if errors.As(err, &transportErr) {
		return fmt.Sprintf("backend transport failed (%s)", transportErr.Category)
	}
	// MCP responses cross a privacy boundary. Unknown errors may contain a
	// backend hostname, a server-supplied URL, a path, or response content, so
	// only explicitly typed and privacy-safe errors above may contribute dynamic
	// text. The original error remains available to local callers and logs.
	return "tool request failed"
}
