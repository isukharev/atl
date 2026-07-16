package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
	"github.com/isukharev/atl/internal/safepath"
)

const (
	confluenceIncrementalService  = "confluence"
	confluenceIncrementalProtocol = "absolute-overlap-v2"
	legacyIncrementalProtocol     = "absolute-overlap-v1"
	confluenceIncrementalOverlap  = 48 * time.Hour
)

var cqlOrderByRE = regexp.MustCompile(`(?i)\border\s+by\b`)

func hasUnquotedCQLOrderBy(value string) bool {
	var outside strings.Builder
	var quote rune
	escaped := false
	for _, r := range value {
		if quote != 0 {
			outside.WriteByte(' ')
			if escaped {
				escaped = false
			} else if r == '\\' {
				escaped = true
			} else if r == quote {
				quote = 0
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			outside.WriteByte(' ')
			continue
		}
		outside.WriteRune(r)
	}
	return cqlOrderByRE.MatchString(outside.String())
}

type IncrementalPullResult struct {
	SelectorSHA256       string `json:"selector_sha256"`
	WatermarkSource      string `json:"watermark_source"`
	WatermarkInstant     string `json:"watermark_instant"`
	QueryLiteral         string `json:"query_literal"`
	QueryLiteralBasis    string `json:"query_literal_basis"`
	BackendQueryTimeZone string `json:"backend_query_time_zone"`
	SafetyOverlapHours   int    `json:"safety_overlap_hours"`
	Complete             bool   `json:"complete"`
	Matched              int    `json:"matched"`
	Selected             int    `json:"selected"`
	OverlapSkipped       int    `json:"overlap_skipped"`
	BoundarySkipped      int    `json:"boundary_skipped"`
	NextInstant          string `json:"next_instant"`
	BoundaryCount        int    `json:"boundary_count"`
	WatermarkAdvanced    bool   `json:"watermark_advanced"`
	ViewMigrations       int    `json:"view_migrations,omitempty"`
}

type confluenceIncrementalSelection struct {
	ids     []string
	next    mirror.IncrementalWatermark
	changed bool
	result  *IncrementalPullResult
}

func confluenceSelector(o PullOpts) (string, error) {
	return confluenceSelectorForMode(o, "incremental")
}

func confluenceSelectorForMode(o PullOpts, mode string) (string, error) {
	switch {
	case strings.TrimSpace(o.CQL) != "":
		selector := strings.TrimSpace(o.CQL)
		if hasUnquotedCQLOrderBy(selector) {
			if mode == "incremental" {
				return "", fmt.Errorf("%w: incremental --cql must not contain ORDER BY; atl appends a stable lastmodified order", domain.ErrUsage)
			}
			return "", fmt.Errorf("%w: %s --cql must not contain ORDER BY; atl qualifies and canonicalizes the complete selector independently", domain.ErrUsage, mode)
		}
		return selector, nil
	case strings.TrimSpace(o.Space) != "":
		if o.Depth != 0 {
			return "", fmt.Errorf("%w: %s --space does not support --depth", domain.ErrUsage, mode)
		}
		space := strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(o.Space), `\`, `\\`), `"`, `\"`)
		return `space = "` + space + `" and type = page`, nil
	default:
		return "", fmt.Errorf("%w: %s pull needs --cql or --space", domain.ErrUsage, mode)
	}
}

func selectorHash(selector string) string {
	sum := sha256.Sum256([]byte(selector))
	return hex.EncodeToString(sum[:])
}

func parseIncrementalInstant(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: --since must be RFC3339 with an explicit offset, for example 2026-07-01T00:00:00Z: %v", domain.ErrUsage, err)
	}
	if parsed.Second() != 0 || parsed.Nanosecond() != 0 {
		return time.Time{}, fmt.Errorf("%w: --since must identify an exact minute (seconds and fractions must be zero)", domain.ErrUsage)
	}
	return parsed.UTC(), nil
}

func canonicalIncrementalInstant(value time.Time) string {
	return value.UTC().Truncate(time.Minute).Format(time.RFC3339)
}

func validateLegacyCQLMinute(value string) error {
	if _, err := time.Parse("2006-01-02 15:04", value); err != nil {
		return fmt.Errorf("%w: recorded incremental watermark has an invalid legacy wall boundary", domain.ErrCheckFailed)
	}
	return nil
}

func parseConfluenceUpdated(value string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.000-0700", "2006-01-02T15:04:05-0700"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported Confluence updated timestamp %q", value)
}

func cqlMinute(value time.Time, location *time.Location) string {
	return value.In(location).Format("2006-01-02 15:04")
}

func (s *ConfluenceService) prepareIncrementalPull(ctx context.Context, m *mirror.Mirror, o PullOpts) (*confluenceIncrementalSelection, error) {
	if strings.TrimSpace(o.TimeZone) != "" {
		return nil, fmt.Errorf("%w: --time-zone was removed; pass an explicit offset in RFC3339 --since instead", domain.ErrUsage)
	}
	selector, err := confluenceSelector(o)
	if err != nil {
		return nil, err
	}
	hash := selectorHash(selector)
	previous, found, err := m.IncrementalWatermark(confluenceIncrementalService, hash)
	if err != nil {
		return nil, err
	}
	source := "recorded"
	migrated := false
	var boundary time.Time
	if !found {
		if strings.TrimSpace(o.Since) == "" {
			return nil, fmt.Errorf("%w: first incremental pull for this selector requires --since <RFC3339 instant with explicit offset>", domain.ErrUsage)
		}
		boundary, err = parseIncrementalInstant(o.Since)
		if err != nil {
			return nil, err
		}
		instant := canonicalIncrementalInstant(boundary)
		previous = mirror.IncrementalWatermark{Service: confluenceIncrementalService, SelectorSHA256: hash, Selector: selector, Since: instant, Protocol: confluenceIncrementalProtocol, Boundary: instant, BoundaryVersions: map[string]int{}}
		source = "explicit"
	} else {
		if previous.Service != confluenceIncrementalService || previous.SelectorSHA256 != hash || previous.Selector != selector {
			return nil, fmt.Errorf("%w: recorded incremental watermark does not match its selector", domain.ErrCheckFailed)
		}
		if previous.Boundary == "" {
			return nil, fmt.Errorf("%w: recorded incremental watermark predates the fail-safe absolute-boundary protocol; preserve this mirror and bootstrap the selector in a new mirror root", domain.ErrCheckFailed)
		}
		boundary, err = time.Parse(time.RFC3339, previous.Boundary)
		if err != nil || !boundary.Equal(boundary.Truncate(time.Minute)) {
			return nil, fmt.Errorf("%w: recorded incremental watermark has an invalid absolute boundary", domain.ErrCheckFailed)
		}
		switch previous.Protocol {
		case confluenceIncrementalProtocol:
			if previous.TimeZone != "" || previous.Since != canonicalIncrementalInstant(boundary) || previous.Boundary != canonicalIncrementalInstant(boundary) {
				return nil, fmt.Errorf("%w: recorded incremental watermark is not canonical UTC", domain.ErrCheckFailed)
			}
		case legacyIncrementalProtocol:
			if err := validateLegacyCQLMinute(previous.Since); err != nil {
				return nil, err
			}
			location, loadErr := time.LoadLocation(previous.TimeZone)
			if previous.TimeZone == "" || loadErr != nil || cqlMinute(boundary, location) != previous.Since {
				return nil, fmt.Errorf("%w: recorded legacy wall and absolute boundaries disagree", domain.ErrCheckFailed)
			}
			instant := canonicalIncrementalInstant(boundary)
			previous.Since = instant
			previous.TimeZone = ""
			previous.Protocol = confluenceIncrementalProtocol
			previous.Boundary = instant
			source = "migrated"
			migrated = true
		default:
			return nil, fmt.Errorf("%w: recorded incremental watermark uses unsupported protocol %q", domain.ErrCheckFailed, previous.Protocol)
		}
		for id, version := range previous.BoundaryVersions {
			if strings.TrimSpace(id) == "" || version < 0 {
				return nil, fmt.Errorf("%w: recorded incremental watermark has an invalid boundary identity", domain.ErrCheckFailed)
			}
		}
		if strings.TrimSpace(o.Since) != "" {
			explicit, parseErr := parseIncrementalInstant(o.Since)
			if parseErr != nil {
				return nil, parseErr
			}
			if !explicit.Equal(boundary) {
				return nil, fmt.Errorf("%w: --since %q conflicts with recorded watermark %q for this selector", domain.ErrCheckFailed, strings.TrimSpace(o.Since), previous.Since)
			}
		}
	}
	searcher, ok := s.store.(domain.CompletePageSearcher)
	if !ok {
		return nil, fmt.Errorf("%w: backend cannot qualify search completeness for incremental pull", domain.ErrCheckFailed)
	}
	querySince := cqlMinute(boundary.Add(-confluenceIncrementalOverlap), time.UTC)
	query := fmt.Sprintf(`(%s) and type = page and lastmodified >= "%s" order by lastmodified asc`, selector, querySince)
	maxPages := o.MaxPages
	if maxPages <= 0 {
		maxPages = 10000
	}
	first, err := collectIncrementalHits(ctx, searcher, query, maxPages)
	if err != nil {
		return nil, err
	}
	hitsByID, err := collectIncrementalHits(ctx, searcher, query, maxPages)
	if err != nil {
		return nil, err
	}
	if !sameIncrementalHitSet(first, hitsByID) {
		return nil, fmt.Errorf("%w: incremental selection changed during pagination; retry the same command after the backend settles (watermark unchanged)", domain.ErrCheckFailed)
	}
	hits := make([]domain.PageRef, 0, len(hitsByID))
	for _, hit := range hitsByID {
		hits = append(hits, hit)
	}
	sort.Slice(hits, func(i, j int) bool {
		ti, _ := parseConfluenceUpdated(hits[i].Updated)
		tj, _ := parseConfluenceUpdated(hits[j].Updated)
		if !ti.Equal(tj) {
			return ti.Before(tj)
		}
		return hits[i].ID < hits[j].ID
	})
	next := previous
	next.BoundaryVersions = map[string]int{}
	ids := make([]string, 0, len(hits))
	boundarySkipped := 0
	overlapSkipped := 0
	var maxTime time.Time
	for _, hit := range hits {
		updated, _ := parseConfluenceUpdated(hit.Updated)
		hitBoundary := updated.Truncate(time.Minute)
		if hitBoundary.Before(boundary) {
			overlapSkipped++
			continue
		}
		if maxTime.IsZero() || updated.After(maxTime) {
			maxTime = updated
		}
		if hitBoundary.Equal(boundary) && previous.BoundaryVersions[hit.ID] >= hit.Version {
			boundarySkipped++
			continue
		}
		ids = append(ids, hit.ID)
	}
	if !maxTime.IsZero() {
		nextBoundary := maxTime.Truncate(time.Minute)
		next.Since = canonicalIncrementalInstant(nextBoundary)
		next.Boundary = canonicalIncrementalInstant(nextBoundary)
		next.Observed = maxTime.Format(time.RFC3339Nano)
		for _, hit := range hits {
			updated, _ := parseConfluenceUpdated(hit.Updated)
			if updated.Truncate(time.Minute).Equal(nextBoundary) {
				next.BoundaryVersions[hit.ID] = hit.Version
			}
		}
	} else {
		next.BoundaryVersions = previous.BoundaryVersions
	}
	result := &IncrementalPullResult{
		SelectorSHA256: hash, WatermarkSource: source, WatermarkInstant: previous.Since, QueryLiteral: querySince, QueryLiteralBasis: "UTC", BackendQueryTimeZone: "unknown",
		SafetyOverlapHours: int(confluenceIncrementalOverlap / time.Hour), Complete: true, Matched: len(hits), Selected: len(ids), OverlapSkipped: overlapSkipped, BoundarySkipped: boundarySkipped,
		NextInstant: next.Since, BoundaryCount: len(next.BoundaryVersions),
		WatermarkAdvanced: false,
	}
	changed := !found || migrated || previous.Since != next.Since || previous.Boundary != next.Boundary || !reflect.DeepEqual(previous.BoundaryVersions, next.BoundaryVersions)
	return &confluenceIncrementalSelection{ids: ids, next: next, changed: changed, result: result}, nil
}

func collectIncrementalHits(ctx context.Context, searcher domain.CompletePageSearcher, query string, maxPages int) (map[string]domain.PageRef, error) {
	cursor := ""
	seenCursors := map[string]bool{}
	hitsByID := map[string]domain.PageRef{}
	for {
		if seenCursors[cursor] {
			return nil, fmt.Errorf("%w: incremental search repeated cursor %q", domain.ErrCheckFailed, cursor)
		}
		seenCursors[cursor] = true
		page, err := searcher.SearchComplete(ctx, query, 100, cursor)
		if err != nil {
			return nil, err
		}
		for _, hit := range page.Results {
			if hit.ID == "" || hit.Updated == "" {
				return nil, fmt.Errorf("%w: incremental search result omitted id or updated timestamp", domain.ErrCheckFailed)
			}
			if _, err := parseConfluenceUpdated(hit.Updated); err != nil {
				return nil, fmt.Errorf("%w: %v", domain.ErrCheckFailed, err)
			}
			current, exists := hitsByID[hit.ID]
			currentTime, _ := parseConfluenceUpdated(current.Updated)
			hitTime, _ := parseConfluenceUpdated(hit.Updated)
			if !exists || hit.Version > current.Version || (hit.Version == current.Version && hitTime.After(currentTime)) {
				hitsByID[hit.ID] = hit
			}
			if len(hitsByID) > maxPages {
				return nil, fmt.Errorf("%w: incremental selection exceeded --max-pages=%d; increase the explicit cap and retry (watermark unchanged)", domain.ErrCheckFailed, maxPages)
			}
		}
		if page.Next == "" {
			if !page.Complete {
				reason := page.PartialReason
				if reason == "" {
					reason = "backend did not prove terminal search completeness"
				}
				return nil, fmt.Errorf("%w: incomplete incremental selection: %s (watermark unchanged)", domain.ErrCheckFailed, reason)
			}
			break
		}
		cursor = page.Next
	}
	return hitsByID, nil
}

func sameIncrementalHitSet(a, b map[string]domain.PageRef) bool {
	if len(a) != len(b) {
		return false
	}
	for id, left := range a {
		right, ok := b[id]
		if !ok || left.Version != right.Version || left.Updated != right.Updated {
			return false
		}
	}
	return true
}

// preflightIncrementalOverwrite rejects both native edits and unapplied edits
// to the derived Markdown view before the first remote body read or local write.
func preflightIncrementalOverwrite(m *mirror.Mirror, ids []string) (int, error) {
	return preflightConfluenceOverwrite(m, ids)
}

func preflightConfluenceOverwrite(m *mirror.Mirror, ids []string) (int, error) {
	states, err := m.SyncStates()
	if err != nil {
		return 0, err
	}
	byID := map[string]mirror.SyncState{}
	for _, state := range states {
		if filepath.Ext(state.Path) == ".csf" {
			byID[state.ID] = state
		}
	}
	migrations := 0
	for _, id := range ids {
		state, ok := byID[id]
		if !ok {
			continue
		}
		csfPath := filepath.Join(m.Root, filepath.FromSlash(state.Path))
		primary := []string{csfPath, strings.TrimSuffix(csfPath, ".csf") + ".md", strings.TrimSuffix(csfPath, ".csf") + ".meta.json"}
		present := 0
		for _, path := range primary {
			if _, readErr := safepath.ReadFileWithin(m.Root, path); readErr == nil {
				present++
			} else if !os.IsNotExist(readErr) {
				return 0, fmt.Errorf("%w: inspect incremental target %s: %v", domain.ErrCheckFailed, path, readErr)
			}
		}
		if present == 0 {
			continue
		}
		if present != len(primary) {
			return 0, fmt.Errorf("%w: tracked incremental target for page %s is only partially present; preserve or restore its .csf/.md/.meta.json files", domain.ErrCheckFailed, id)
		}
		lc, _, err := m.LoadCSF(csfPath)
		if err != nil {
			return 0, err
		}
		if lc.Dirty {
			return 0, fmt.Errorf("%w: page %s has local native edits; apply/push or preserve them before incremental pull", domain.ErrCheckFailed, id)
		}
		if lc.Meta.Hash != state.Hash || lc.Meta.Version != state.Version {
			return 0, fmt.Errorf("%w: page %s metadata diverges from its tracked version/hash; preserve and reconcile it before incremental pull", domain.ErrCheckFailed, id)
		}
		base, ok := m.BaseBody(id)
		if !ok {
			return 0, fmt.Errorf("%w: page %s has no pristine base; re-pull it explicitly before incremental refresh", domain.ErrCheckFailed, id)
		}
		node, parseErr := csf.Parse(base)
		if parseErr != nil {
			return 0, fmt.Errorf("%w: page %s pristine CSF cannot reproduce its Markdown view; preserve local files and re-pull the page explicitly", domain.ErrCheckFailed, id)
		}
		view, hasView, err := m.ViewStateOf(id)
		if err != nil {
			return 0, err
		}
		opts := mirror.MDViewOpts{}
		if hasView {
			dir := filepath.Dir(csfPath)
			slug := strings.TrimSuffix(filepath.Base(csfPath), ".csf")
			opts, err = confMDViewOptsFromSidecars(settingsFromViewState(view), confPageFromMeta(lc.Meta), readCommentsSidecar(m.Root, dir, slug), m.Root, dir, slug, id, node)
			if err != nil {
				return 0, fmt.Errorf("%w: cannot reproduce page %s derived view: %v", domain.ErrCheckFailed, id, err)
			}
		}
		actual, err := safepath.ReadFileWithin(m.Root, primary[1])
		if err != nil {
			return 0, err
		}
		migrates, matchErr := matchConfluencePristineView(actual, mirror.RenderMarkdownOpts(node, lc.Meta.Refs, opts))
		if matchErr != nil {
			return 0, fmt.Errorf("%w: page %s %v", domain.ErrCheckFailed, id, matchErr)
		}
		if migrates {
			migrations++
		}
	}
	return migrations, nil
}

// matchConfluencePristineView proves whether an existing derived view is either
// current or a byte-clean supported legacy format. Legacy views
// are accepted only when replacing the current first-line marker with their
// exact marker makes every byte match; this never guesses across renderer
// changes. The successful pull then writes and records the current format.
func matchConfluencePristineView(actual, current []byte) (bool, error) {
	if bytes.Equal(actual, current) {
		return false, nil
	}
	marker := mirror.ConfluenceDocumentMarkerLine(string(actual))
	if marker == mirror.ConfluenceDocumentMarker {
		return false, fmt.Errorf("has unapplied Markdown edits; apply or preserve them before replacing this derived view")
	}
	if isSupportedLegacyConfluenceMarker(marker) {
		currentMarker := []byte(mirror.ConfluenceDocumentMarker)
		if !bytes.HasPrefix(current, currentMarker) {
			return false, fmt.Errorf("current pristine renderer omitted its document marker")
		}
		legacy := make([]byte, 0, len(current)-len(currentMarker)+len(marker))
		legacy = append(legacy, marker...)
		legacy = append(legacy, current[len(currentMarker):]...)
		if bytes.Equal(actual, legacy) {
			return true, nil
		}
		return false, fmt.Errorf("uses supported legacy Markdown format %q but differs from its pristine reconstruction; preserve and reconcile edits before migrating or replacing this derived view", marker)
	}
	if strings.HasPrefix(marker, "<!-- atl:document confluence-page") {
		return false, fmt.Errorf("uses unsupported Markdown format %q; preserve it and update atl before replacing this derived view — do not downgrade it with this binary", marker)
	}
	return false, fmt.Errorf("has unapplied Markdown edits or an unrecognized document marker; apply or preserve them before replacing this derived view")
}

func isSupportedLegacyConfluenceMarker(marker string) bool {
	switch marker {
	case "<!-- atl:document confluence-page v3 -->",
		"<!-- atl:document confluence-page v2 -->",
		"<!-- atl:document confluence-page v1 -->",
		"<!-- atl:document confluence-page -->":
		return true
	default:
		return false
	}
}
