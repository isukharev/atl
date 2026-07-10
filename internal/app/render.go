package app

import (
	"fmt"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/mirror"
)

// Canonical Jira render section/field names. The flat set is what include/exclude
// operate on: metadata fields plus body sections. Keeping them as string
// constants (not an enum) mirrors the config file's stringly-typed keys and keeps
// the include/exclude lists human-editable.
const (
	// Metadata fields (key + summary are always emitted, so they are not in
	// this set — they cannot be toggled off).
	SecStatus       = "status"
	SecType         = "type"
	SecProject      = "project"
	SecAssignee     = "assignee"
	SecLabels       = "labels"
	SecPriority     = "priority"
	SecParent       = "parent"
	SecReporter     = "reporter"
	SecCreated      = "created"
	SecUpdated      = "updated"
	SecResolution   = "resolution"
	SecDuedate      = "duedate"
	SecComponents   = "components"
	SecFixVersions  = "fix_versions"
	SecCustomFields = "custom_fields"
	// Body sections.
	SecAttachments    = "attachments"     // "## Image Attachments" (image links only)
	SecAttachmentsAll = "attachments_all" // "## Attachments" (full list incl. non-image)
	SecLinks          = "links"
	SecComments       = "comments"
	SecSprint         = "sprint"
	SecSubtasks       = "subtasks"
	SecEpicChildren   = "epic_children" // opt-in: requires an additional bounded Jira query

	// Confluence sections.
	SecFrontmatter = "frontmatter" // YAML frontmatter (title/space/version/labels/updated)
	// SecComments is shared with Jira (a "## Comments" section).
)

// jiraSections is the closed, ordered set of valid Jira section names.
var jiraSections = []string{
	SecStatus, SecType, SecProject, SecAssignee, SecLabels, SecPriority, SecParent,
	SecReporter, SecCreated, SecUpdated, SecResolution, SecDuedate, SecComponents,
	SecFixVersions, SecCustomFields, SecAttachments, SecAttachmentsAll, SecLinks,
	SecComments, SecSprint, SecSubtasks,
	SecEpicChildren,
}

// jiraFullSections deliberately excludes epic_children: every other full
// section is satisfied by widening the one issue search projection, while epic
// children require an additional related-issue query and must stay opt-in.
var jiraFullSections = []string{
	SecStatus, SecType, SecProject, SecAssignee, SecLabels, SecPriority, SecParent,
	SecReporter, SecCreated, SecUpdated, SecResolution, SecDuedate, SecComponents,
	SecFixVersions, SecCustomFields, SecAttachments, SecAttachmentsAll, SecLinks,
	SecComments, SecSprint, SecSubtasks,
}

// confSections is the closed set of valid Confluence section names.
var confSections = []string{SecFrontmatter, SecComments}

// jiraDefaultSections is what the `default` profile renders for Jira: the lean
// set routine read-scenarios consume (#88). It extends the original #148 view
// with priority + parent.
var jiraDefaultSections = []string{
	SecStatus, SecType, SecProject, SecAssignee, SecLabels, SecPriority, SecParent,
	SecAttachments, SecLinks, SecComments,
}

// RenderSettings is the resolved set of enabled sections for one render, plus the
// configured custom field ids (Jira only). It is the pure input the renderers
// consume — resolution (profile + include/exclude + flag override) happens once,
// upstream, so a renderer never touches config.
type RenderSettings struct {
	Sections     map[string]bool
	CustomFields []string
	FieldViews   []config.JiraFieldView
	EpicField    string
}

// On reports whether a section is enabled.
func (rs RenderSettings) On(name string) bool { return rs.Sections[name] }

// validSectionSet returns the closed set of valid section names for a backend.
func validSectionSet(backend string) map[string]bool {
	list := confSections
	if backend == "jira" {
		list = jiraSections
	}
	set := make(map[string]bool, len(list))
	for _, s := range list {
		set[s] = true
	}
	return set
}

// profileBase returns the base section set for a profile on a backend. An empty
// profile falls back to the default profile.
func profileBase(backend, profile string) map[string]bool {
	if profile == "" {
		profile = config.DefaultProfile
	}
	out := map[string]bool{}
	if backend == "jira" {
		switch profile {
		case "minimal":
			// {} — only key + summary metadata and the description body.
		case "full":
			for _, s := range jiraFullSections {
				out[s] = true
			}
		default: // "default"
			for _, s := range jiraDefaultSections {
				out[s] = true
			}
		}
		return out
	}
	// confluence
	switch profile {
	case "full":
		for _, s := range confSections {
			out[s] = true
		}
	default: // "minimal" and "default" are both body-only (byte-identical to today)
	}
	return out
}

// computeSettings turns a resolved config.RenderService (profile + include +
// exclude + custom_fields) into a RenderSettings: profile base, then include
// (add), then exclude (remove). Unknown section names in include/exclude are
// reported as warnings and skipped — never an error, so a stale config key
// degrades gracefully.
func computeSettings(backend string, svc config.RenderService) (RenderSettings, []string) {
	valid := validSectionSet(backend)
	sections := profileBase(backend, svc.Profile)
	var warns []string
	for _, name := range svc.Include {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if !valid[name] {
			warns = append(warns, unknownSectionWarn(backend, "include", name))
			continue
		}
		sections[name] = true
	}
	for _, name := range svc.Exclude {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if !valid[name] {
			warns = append(warns, unknownSectionWarn(backend, "exclude", name))
			continue
		}
		delete(sections, name)
	}
	if backend != "jira" {
		if len(svc.CustomFields) > 0 || len(svc.FieldViews) > 0 || svc.EpicField != "" {
			warns = append(warns, "render: ignoring Jira-only custom_fields/field_views/epic_field for confluence")
		}
		return RenderSettings{Sections: sections}, warns
	}
	views := make([]config.JiraFieldView, 0, len(svc.FieldViews))
	viewIDs := map[string]bool{}
	for i, fv := range svc.FieldViews {
		norm, err := config.NormalizeJiraFieldView(fv)
		if err != nil {
			warns = append(warns, fmt.Sprintf("render: ignoring jira field_views[%d]: %v", i, err))
			continue
		}
		if viewIDs[norm.ID] {
			warns = append(warns, fmt.Sprintf("render: ignoring jira field_views[%d]: duplicate field id %q", i, norm.ID))
			continue
		}
		viewIDs[norm.ID] = true
		views = append(views, norm)
	}
	cf := make([]string, 0, len(svc.CustomFields))
	seenFields := map[string]bool{}
	for _, field := range svc.CustomFields {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		if seenFields[field] {
			warns = append(warns, fmt.Sprintf("render: ignoring duplicate custom_fields entry %q", field))
			continue
		}
		seenFields[field] = true
		if viewIDs[field] {
			continue // the typed descriptor owns this field's presentation
		}
		cf = append(cf, field)
	}
	return RenderSettings{Sections: sections, CustomFields: cf, FieldViews: views, EpicField: strings.TrimSpace(svc.EpicField)}, warns
}

func unknownSectionWarn(backend, verb, name string) string {
	return fmt.Sprintf("render: ignoring unknown %s section %q for %s (valid: %s)", verb, name, backend, strings.Join(validSectionsList(backend), ", "))
}

func validSectionsList(backend string) []string {
	if backend == "jira" {
		return jiraSections
	}
	return confSections
}

// ResolveRender merges the effective render config (defaults → global → local)
// for one backend, applies the per-run flag override (each of profile / include /
// exclude / custom_fields overrides when set), and computes the enabled section
// set. It must be called with the operation's actual mirror root so the local
// config that governs *this* mirror is the one consulted. Returned warnings
// (malformed/forbidden local keys, unknown section names) are advisory; the CLI
// surfaces them on stderr.
func ResolveRender(global *config.Config, root string, override config.RenderService, backend string) (RenderSettings, []string) {
	var warns []string
	local, lw, _ := config.LoadLocal(root)
	warns = append(warns, lw...)
	eff, _ := config.EffectiveRender(global, local)
	svc := serviceForBackend(eff, backend)
	merged := applyRenderOverride(svc, override)
	settings, cw := computeSettings(backend, merged)
	warns = append(warns, cw...)
	return settings, warns
}

// serviceForBackend returns the resolved RenderService for a backend (never nil:
// EffectiveRender always populates both with a defaulted profile).
func serviceForBackend(rc config.RenderConfig, backend string) config.RenderService {
	if backend == "jira" {
		if rc.Jira != nil {
			return *rc.Jira
		}
		return config.RenderService{Profile: config.DefaultProfile}
	}
	if rc.Confluence != nil {
		return *rc.Confluence
	}
	return config.RenderService{Profile: config.DefaultProfile}
}

// applyRenderOverride overlays a per-run flag override onto a resolved service:
// each field wins only when set (non-empty), so an unspecified flag leaves the
// config value intact.
func applyRenderOverride(svc, o config.RenderService) config.RenderService {
	out := svc
	if o.Profile != "" {
		out.Profile = o.Profile
	}
	if len(o.Include) > 0 {
		out.Include = append([]string(nil), o.Include...)
	}
	if len(o.Exclude) > 0 {
		out.Exclude = append([]string(nil), o.Exclude...)
	}
	if len(o.CustomFields) > 0 {
		out.CustomFields = append([]string(nil), o.CustomFields...)
	}
	if len(o.FieldViews) > 0 {
		out.FieldViews = append([]config.JiraFieldView(nil), o.FieldViews...)
	}
	if o.EpicField != "" {
		out.EpicField = o.EpicField
	}
	return out
}

// viewStateOf captures the render settings a .md view was written with as a
// mirror.ViewState: the sorted list of enabled sections plus a copy of the
// configured custom field ids. Storing the computed section list (not the
// profile name) keeps a recorded view valid even if a profile's definition
// later changes.
func viewStateOf(rs RenderSettings) mirror.ViewState {
	sections := make([]string, 0, len(rs.Sections))
	for name, on := range rs.Sections {
		if on {
			sections = append(sections, name)
		}
	}
	sort.Strings(sections)
	var cf []string
	if len(rs.CustomFields) > 0 {
		cf = append([]string(nil), rs.CustomFields...)
	}
	fields := make([]mirror.FieldViewState, 0, len(rs.FieldViews))
	for _, fv := range rs.FieldViews {
		fields = append(fields, mirror.FieldViewState{
			ID: fv.ID, Label: fv.Label, Placement: fv.Placement,
			Format: fv.Format, ShowEmpty: fv.ShowEmpty,
		})
	}
	return mirror.ViewState{Sections: sections, CustomFields: cf, FieldViews: fields, EpicField: rs.EpicField}
}

// settingsFromViewState is the inverse of viewStateOf: it rebuilds RenderSettings
// from a recorded mirror.ViewState so apply can reproduce the exact pristine view
// regardless of the ambient config. Section names are not validated against the
// closed set — an unknown name (a section added in a newer atl that wrote this
// mirror) is simply carried and ignored by renderers, keeping the mirror
// forward-compatible.
func settingsFromViewState(vs mirror.ViewState) RenderSettings {
	sections := make(map[string]bool, len(vs.Sections))
	for _, name := range vs.Sections {
		sections[name] = true
	}
	var cf []string
	if len(vs.CustomFields) > 0 {
		cf = append([]string(nil), vs.CustomFields...)
	}
	fields := make([]config.JiraFieldView, 0, len(vs.FieldViews))
	for _, fv := range vs.FieldViews {
		fields = append(fields, config.JiraFieldView{
			ID: fv.ID, Label: fv.Label, Placement: fv.Placement,
			Format: fv.Format, ShowEmpty: fv.ShowEmpty,
		})
	}
	return RenderSettings{Sections: sections, CustomFields: cf, FieldViews: fields, EpicField: vs.EpicField}
}

// renderOverrideSet reports whether a per-run render override carries any
// explicit flag (profile, include, exclude, or custom fields). apply uses it to
// decide whether the user asked for a specific view (honor the flags) or wants
// the recorded pristine view reproduced.
func renderOverrideSet(o config.RenderService) bool {
	return o.Profile != "" || len(o.Include) > 0 || len(o.Exclude) > 0 || len(o.CustomFields) > 0 || len(o.FieldViews) > 0 || o.EpicField != ""
}

// sortedFieldKeys returns a raw fields map's keys in deterministic order so a
// render (e.g. sprint discovery) is stable across runs.
func sortedFieldKeys(fields map[string]any) []string {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
