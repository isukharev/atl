package domain

import "context"

// JiraGuardedLabelSnapshot is one complete labels snapshot for a qualified
// immutable Jira issue identity. Labels are unique and byte-sorted.
type JiraGuardedLabelSnapshot struct {
	ID       string
	Key      string
	Project  string
	Labels   []string
	Updated  string
	Complete bool
}

// JiraGuardedLabelWrite is an already-qualified, deterministic label delta.
// ID is the sole transport lookup identity; Key and Project are retained for
// exact last-hop authorization without a hidden read.
type JiraGuardedLabelWrite struct {
	ID      string
	Key     string
	Project string
	Add     []string
	Remove  []string
}

// JiraGuardedLabelPort is intentionally separate from Tracker. Legacy label,
// CSV, plan, MCP, and non-CLI callers retain Tracker.UpdateLabels, while the
// guarded CLI has no fallback to that broad writer.
type JiraGuardedLabelPort interface {
	ReadGuardedLabelSnapshot(context.Context, string) (JiraGuardedLabelSnapshot, error)
	WriteGuardedLabelDelta(context.Context, JiraGuardedLabelWrite) error
}
