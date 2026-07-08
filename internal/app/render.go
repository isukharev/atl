package app

import (
	"fmt"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/config"
)

// Canonical Jira render section/field names. The flat set is what include/exclude
// operate on: frontmatter fields plus body sections. Keeping them as string
// constants (not an enum) mirrors the config file's stringly-typed keys and keeps
// the include/exclude lists human-editable.
const (
	// Frontmatter fields (key + summary are always emitted, so they are not in
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
			// {} — only key + summary frontmatter and the description body.
		case "full":
			for _, s := range jiraSections {
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
	cf := append([]string(nil), svc.CustomFields...)
	return RenderSettings{Sections: sections, CustomFields: cf}, warns
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
	return out
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
