package brokerclient

import (
	"context"
	"io"

	"github.com/isukharev/atl/internal/domain"
)

// Confluence exposes the existing DocStore port while admitting only exact
// numeric page reads. Unknown metadata stays absent in the mapped domain type.
type Confluence struct{ client *Client }

var _ domain.DocStore = (*Confluence)(nil)

func NewConfluence(client *Client) (*Confluence, error) {
	if client == nil {
		return nil, clientError(domain.ErrConfig)
	}
	return &Confluence{client: client}, nil
}

func (c *Confluence) GetPage(ctx context.Context, id string, opts domain.PullOpts) (*domain.Resource, error) {
	if opts.Format != "" && opts.Format != "csf" || opts.IncludeRestrictions {
		return nil, unsupported()
	}
	result, err := c.client.ReadConfluencePage(ctx, id, domain.BrokerConfluenceProjectionStorage)
	if err != nil {
		return nil, err
	}
	return &domain.Resource{
		ID: result.PageID, Type: result.Type, Title: result.Title, SpaceKey: result.Space,
		Version: result.Version, Body: append([]byte(nil), result.Storage...), BodyPresent: result.StoragePresent,
		Updated: result.Updated,
	}, nil
}

func (c *Confluence) GetMeta(ctx context.Context, id string) (*domain.PageMeta, error) {
	result, err := c.client.ReadConfluencePage(ctx, id, domain.BrokerConfluenceProjectionMetadata)
	if err != nil {
		return nil, err
	}
	return &domain.PageMeta{ID: result.PageID, Type: result.Type, Title: result.Title, Space: result.Space, Version: result.Version, Updated: result.Updated}, nil
}

func (*Confluence) Search(context.Context, string, int, string) ([]domain.PageRef, string, error) {
	return nil, "", unsupported()
}
func (*Confluence) Tree(context.Context, string, int) ([]domain.PageRef, bool, error) {
	return nil, false, unsupported()
}
func (*Confluence) History(context.Context, string) ([]domain.Version, error) {
	return nil, unsupported()
}
func (*Confluence) UpdatePage(context.Context, string, int, string, []byte, bool) (int, error) {
	return 0, unsupported()
}
func (*Confluence) CreatePage(context.Context, string, string, string, []byte) (*domain.Resource, error) {
	return nil, unsupported()
}
func (*Confluence) MovePage(context.Context, string, string, int, string, []byte) (int, error) {
	return 0, unsupported()
}
func (*Confluence) DeletePage(context.Context, string) error { return unsupported() }
func (*Confluence) ListComments(context.Context, string) ([]domain.Comment, bool, error) {
	return nil, false, unsupported()
}
func (*Confluence) AddComment(context.Context, string, []byte) (*domain.Comment, error) {
	return nil, unsupported()
}
func (*Confluence) ListAttachments(context.Context, string) ([]domain.Attachment, error) {
	return nil, unsupported()
}
func (*Confluence) DownloadAttachment(context.Context, string, string, int) (io.ReadCloser, error) {
	return nil, unsupported()
}
func (*Confluence) UploadAttachment(_ context.Context, _ string, _ string, data io.ReadCloser, _ int64, _ string) (*domain.Attachment, error) {
	if data != nil {
		_ = data.Close()
	}
	return nil, unsupported()
}
func (*Confluence) DeleteAttachment(context.Context, string, string) error { return unsupported() }
