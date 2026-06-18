// Package fragment finds opaque CSF fragments (draw.io diagrams, user mentions,
// attachments, page links, inline images) and resolves them to human-readable
// form for the markdown read-view and meta annotations. It never rewrites the
// body: resolution is read-only and advisory. Adding a new opaque type
// (Mermaid, PlantUML) means extending detect(); fetching its render is an
// AssetResolver concern.
package fragment

import (
	"context"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
)

// Deps are the external capabilities resolution may use. All are optional;
// missing capabilities degrade gracefully (annotate without an asset/name).
type Deps struct {
	Assets   domain.AssetSink     // store fetched render bytes on disk
	Resolver domain.AssetResolver // fetch asset bytes (draw.io PNG, image)
	Users    domain.UserResolver  // userkey → display name
}

// Extract walks a parsed CSF DOM and returns the distinct opaque fragments it
// contains, deduplicated by (kind, key) in stable document order.
func Extract(root *csf.Node) []domain.Ref {
	var refs []domain.Ref
	seen := map[string]bool{}
	add := func(r domain.Ref) {
		k := string(r.Kind) + "\x00" + r.Key
		if r.Key == "" || seen[k] {
			return
		}
		seen[k] = true
		refs = append(refs, r)
	}
	csf.Walk(root, func(n *csf.Node) bool {
		switch {
		case n.MacroName() == "drawio":
			p := params(n)
			add(domain.Ref{Kind: domain.RefDrawio, Key: p["diagramName"],
				Display: p["diagramName"], Params: map[string]string{
					"diagramName": p["diagramName"], "revision": p["revision"]}})
			return false // diagram internals are opaque; don't descend
		case n.Name.Space == "ac" && n.Name.Local == "image":
			if fn := descendantAttachment(n); fn != "" {
				add(domain.Ref{Kind: domain.RefImage, Key: fn, Display: fn})
			}
			return false
		case n.Name.Space == "ri" && n.Name.Local == "user":
			key := n.Attrv("ri", "userkey")
			if key == "" {
				key = n.Attrv("ri", "account-id")
			}
			add(domain.Ref{Kind: domain.RefUser, Key: key, Display: "@" + key})
		case n.Name.Space == "ri" && n.Name.Local == "page":
			title := n.Attrv("ri", "content-title")
			add(domain.Ref{Kind: domain.RefPageLink, Key: title, Display: title})
		case n.Name.Space == "ri" && n.Name.Local == "attachment":
			fn := n.Attrv("ri", "filename")
			add(domain.Ref{Kind: domain.RefAttachment, Key: fn, Display: fn})
		}
		return true
	})
	return refs
}

// Resolve fills Display (names) and Asset (fetched renders) for each ref. It
// mutates and returns the slice. Failures are swallowed: a ref simply keeps its
// raw display and gains no asset.
func Resolve(ctx context.Context, page *domain.Resource, refs []domain.Ref, d Deps) []domain.Ref {
	userCache := map[string]string{}
	for i := range refs {
		r := &refs[i]
		switch r.Kind {
		case domain.RefDrawio, domain.RefImage:
			if d.Resolver == nil || d.Assets == nil {
				continue
			}
			data, filename, err := d.Resolver.Resolve(ctx, page, *r)
			if err != nil || len(data) == 0 {
				continue
			}
			if rel, err := d.Assets.Put(filename, data); err == nil {
				r.Asset = rel
			}
		case domain.RefUser:
			if d.Users == nil {
				continue
			}
			if name, ok := userCache[r.Key]; ok {
				r.Display = name
				continue
			}
			if name, err := d.Users(ctx, r.Key); err == nil && name != "" {
				userCache[r.Key] = name
				r.Display = name
			}
		}
	}
	return refs
}

func params(macro *csf.Node) map[string]string {
	out := map[string]string{}
	for _, c := range macro.Children {
		if c.Type == csf.Element && c.Name.Space == "ac" && c.Name.Local == "parameter" {
			out[c.Attrv("ac", "name")] = csf.TextContent(c)
		}
	}
	return out
}

func descendantAttachment(n *csf.Node) string {
	var fn string
	csf.Walk(n, func(x *csf.Node) bool {
		if x.Name.Space == "ri" && x.Name.Local == "attachment" {
			if v := x.Attrv("ri", "filename"); v != "" && fn == "" {
				fn = v
			}
		}
		return true
	})
	return fn
}
