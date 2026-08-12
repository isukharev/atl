package app

import "context"

// cqlPullCap bounds how many ids a `--cql` pull collects. Confluence offers no
// "unbounded" escape, so the loop stops here; the boolean returned alongside the
// ids lets the caller surface that a cap was hit instead of silently dropping
// the overflow.
const cqlPullCap = 1000

// collectSearch pages a CQL query into ids, stopping at cqlPullCap. truncated is
// true only when matches genuinely remain beyond the cap, so the caller can warn
// without crying wolf when the results happen to end exactly at the cap.
func (s *ConfluenceService) collectSearch(ctx context.Context, cql string) (ids []string, truncated bool, err error) {
	cursor := ""
	for len(ids) < cqlPullCap {
		hits, next, err := s.store.Search(ctx, cql, 100, cursor)
		if err != nil {
			return nil, false, err
		}
		for _, hit := range hits {
			if hit.ID != "" {
				ids = append(ids, hit.ID)
			}
		}
		if next == "" || len(hits) == 0 {
			return ids, false, nil
		}
		cursor = next
	}
	// Reached the cap. A dangling next cursor does not prove more matches exist
	// (the next page may be empty), so probe one row rather than warn falsely.
	hits, _, probeErr := s.store.Search(ctx, cql, 1, cursor)
	if probeErr != nil {
		// Don't fail the pull over a truncation probe; assume truncated so we
		// under-claim completeness rather than over-claim it.
		return ids, true, nil
	}
	return ids, len(hits) > 0, nil
}
