package domain

import "context"

// ConfluenceGraphPageMetadata is the body-free page identity projected for
// graph resolution. Title remains backend metadata; graph callers are
// responsible for applying their output bound before publishing it.
type ConfluenceGraphPageMetadata struct {
	ID    string
	Title string
}

// ConfluenceGraphPageMetadataReader is the narrow read-only capability used to
// resolve one exact Confluence page identity without reading page content or
// broader metadata.
type ConfluenceGraphPageMetadataReader interface {
	ReadGraphPageMetadata(ctx context.Context, id string) (ConfluenceGraphPageMetadata, error)
}
