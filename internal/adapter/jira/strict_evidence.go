package jira

import (
	"context"
	"fmt"
	"net/http"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/strictjson"
)

// getStrictJiraEvidenceJSON is reserved for qualified or guarded evidence.
// Legacy compatibility reads deliberately keep their existing decoder.
func (j *Jira) getStrictJiraEvidenceJSON(ctx context.Context, path string, out any) error {
	data, err := j.c.Do(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return err
	}
	if strictjson.Validate(data) != nil {
		return fmt.Errorf("%w: Jira returned malformed qualification evidence", domain.ErrCheckFailed)
	}
	return decodeOneJSON(data, out)
}
