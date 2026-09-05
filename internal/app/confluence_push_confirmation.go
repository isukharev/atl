package app

import (
	"bytes"
	"context"
	"errors"
	"time"

	"github.com/isukharev/atl/internal/domain"
)

func (s *ConfluenceService) updateConfluencePush(ctx context.Context, id string, expected int, title string, body []byte, force bool) (int, error) {
	version, err := s.store.UpdatePage(ctx, id, expected, title, body, force)
	var unconfirmed *domain.PageUpdateUnconfirmedError
	if !errors.As(err, &unconfirmed) || unconfirmed == nil || unconfirmed.ExpectedVersion <= 0 {
		return version, err
	}
	// Reconciliation is a bounded read of the exact target, never another PUT.
	budget, budgetErr := domain.NewChildReadBudget(domain.ReadBudgetFromContext(ctx), 1, 64<<20)
	if budgetErr != nil {
		return 0, err
	}
	readCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	readCtx = domain.WithRedactedHTTPTrace(domain.WithSingleAttempt(domain.WithReadBudget(readCtx, budget)))
	page, readErr := s.store.GetPage(readCtx, id, domain.PullOpts{Format: "csf"})
	if readErr != nil || confluencePushRefreshWarning(page, id, unconfirmed.ExpectedVersion, body) != "" || (title != "" && page.Title != title) {
		return 0, err
	}
	return unconfirmed.ExpectedVersion, nil
}

func confluencePushRefreshWarning(page *domain.Resource, id string, version int, body []byte) string {
	if page == nil || !page.BodyPresent {
		return "pushed but local refresh returned a partial body projection; local files were preserved (re-pull recommended)"
	}
	if version <= 0 || page.ID != id || page.Version != version || !bytes.Equal(page.Body, body) {
		return "pushed but local refresh did not match the confirmed page identity, version and native body; local files were preserved — inspect remote state and reconcile before refreshing"
	}
	return ""
}
