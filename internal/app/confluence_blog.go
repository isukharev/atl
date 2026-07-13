package app

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
)

// CreateBlogPost creates one native Confluence blog entry and requires the
// response to prove the new identity, type, space, version, and body presence.
// A malformed success response is an ambiguous write outcome: callers must not
// retry automatically because the content may already exist.
func (s *ConfluenceService) CreateBlogPost(ctx context.Context, space, title string, body []byte) (*domain.Resource, error) {
	space = strings.TrimSpace(space)
	title = strings.TrimSpace(title)
	if space == "" || title == "" {
		return nil, fmt.Errorf("%w: blog post space and title are required", domain.ErrUsage)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, fmt.Errorf("%w: blog post body must not be empty", domain.ErrUsage)
	}
	if problems := csf.Validate(body); csf.HasErrors(problems) {
		return nil, fmt.Errorf("%w: blog post body is not well-formed Confluence Storage Format", domain.ErrUsage)
	}
	creator, ok := s.store.(domain.BlogPostCreator)
	if !ok || creator == nil {
		return nil, fmt.Errorf("%w: configured document backend does not support native blog posts", domain.ErrCheckFailed)
	}
	created, err := creator.CreateBlogPost(ctx, space, title, body)
	if err != nil {
		if !definitiveWriteRejection(err) {
			return nil, fmt.Errorf("%w: blog post creation outcome is unknown; the post may already exist, so do not retry automatically: %v", domain.ErrCheckFailed, err)
		}
		return nil, err
	}
	if created == nil || created.ID == "" || created.Type != "blogpost" || created.SpaceKey != space ||
		created.Title != title || created.Version <= 0 || !created.BodyPresent {
		return nil, fmt.Errorf("%w: blog post creation response did not prove id/type/space/title/version/body; the post may have been created, so do not retry automatically", domain.ErrCheckFailed)
	}
	return created, nil
}
