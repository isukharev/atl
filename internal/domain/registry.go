package domain

import "context"

// AssetSink is how a fragment handler hands fetched asset bytes to the mirror,
// which decides the on-disk path. It returns the relative path stored.
type AssetSink interface {
	Put(name string, data []byte) (relPath string, err error)
}

// AssetResolver fetches the bytes of a visual asset (draw.io PNG of the exact
// revision, inline image) for a ref on a page. The Confluence adapter
// implements it; the fragment layer consumes it. Returning ErrNotFound lets the
// caller degrade gracefully (annotate without an asset).
type AssetResolver interface {
	Resolve(ctx context.Context, page *Resource, ref Ref) (data []byte, filename string, err error)
}

// UserResolver maps an opaque Confluence userkey to a display name. Optional;
// nil or an error degrades to showing the raw key.
type UserResolver func(ctx context.Context, userkey string) (string, error)
