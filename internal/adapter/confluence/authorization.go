package confluence

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/isukharev/atl/internal/domain"
)

const confluenceIdentityCacheLimit = 4096

type confluenceIdentity struct {
	id          string
	kind        string
	space       string
	ancestorIDs []string
}

type confluenceIdentityCache struct {
	mu             sync.Mutex
	values         map[string]confluenceIdentity
	recency        *list.List
	entries        map[string]*list.Element
	failures       map[string]error
	failureRecency *list.List
	failureEntries map[string]*list.Element
}

func cloneAncestorIDs(ids []string) []string {
	if ids == nil {
		return nil
	}
	return append(make([]string, 0, len(ids)), ids...)
}

func newConfluenceIdentityCache() *confluenceIdentityCache {
	return &confluenceIdentityCache{
		values: make(map[string]confluenceIdentity), recency: list.New(), entries: make(map[string]*list.Element),
		failures: make(map[string]error), failureRecency: list.New(), failureEntries: make(map[string]*list.Element),
	}
}

func (cache *confluenceIdentityCache) get(id string) (confluenceIdentity, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	value, ok := cache.values[id]
	if ok {
		cache.recency.MoveToFront(cache.entries[id])
		value.ancestorIDs = cloneAncestorIDs(value.ancestorIDs)
	}
	return value, ok
}

func (cache *confluenceIdentityCache) put(value confluenceIdentity) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if _, failed := cache.failures[value.id]; failed {
		return
	}
	value.ancestorIDs = cloneAncestorIDs(value.ancestorIDs)
	cache.values[value.id] = value
	if element := cache.entries[value.id]; element != nil {
		cache.recency.MoveToFront(element)
	} else {
		cache.entries[value.id] = cache.recency.PushFront(value.id)
	}
	for len(cache.entries) > confluenceIdentityCacheLimit {
		cache.remove(cache.recency.Back().Value.(string))
	}
}

func (cache *confluenceIdentityCache) failure(id string) (error, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	err, ok := cache.failures[id]
	if ok {
		cache.failureRecency.MoveToFront(cache.failureEntries[id])
	}
	return err, ok
}

func (cache *confluenceIdentityCache) fail(id string, err error) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.failures[id] = err
	if element := cache.failureEntries[id]; element != nil {
		cache.failureRecency.MoveToFront(element)
	} else {
		cache.failureEntries[id] = cache.failureRecency.PushFront(id)
	}
	for len(cache.failureEntries) > confluenceIdentityCacheLimit {
		cache.removeFailure(cache.failureRecency.Back().Value.(string))
	}
}

func (cache *confluenceIdentityCache) evictSubtree(id string) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	for cachedID, value := range cache.values {
		if cachedID == id || containsID(value.ancestorIDs, id) {
			cache.remove(cachedID)
		}
	}
	cache.removeFailure(id)
}

func (cache *confluenceIdentityCache) remove(id string) {
	delete(cache.values, id)
	if element := cache.entries[id]; element != nil {
		cache.recency.Remove(element)
		delete(cache.entries, id)
	}
}

func (cache *confluenceIdentityCache) removeFailure(id string) {
	delete(cache.failures, id)
	if element := cache.failureEntries[id]; element != nil {
		cache.failureRecency.Remove(element)
		delete(cache.failureEntries, id)
	}
}

func containsID(ids []string, wanted string) bool {
	for _, id := range ids {
		if id == wanted {
			return true
		}
	}
	return false
}

func (cf *Confluence) scopeRequirements() domain.WriteScopeRequirements {
	if cf == nil || cf.authorizer == nil {
		return domain.WriteScopeRequirements{}
	}
	reader, ok := cf.authorizer.(domain.WriteScopeRequirementReader)
	if !ok {
		return domain.WriteScopeRequirements{Kind: true, Space: true, Ancestors: true}
	}
	requirements := reader.RequiredWriteScope("confluence")
	if requirements.Ancestors {
		requirements.Space = true
	}
	return requirements
}

func (cf *Confluence) authorize(ctx context.Context, verbs domain.WriteVerbSet, targets ...domain.WriteTarget) (context.Context, error) {
	if cf.authorizer == nil {
		return ctx, nil
	}
	return cf.authorizer.Authorize(ctx, domain.WriteAuthorizationRequest{Verbs: verbs, Targets: targets})
}

func (cf *Confluence) authorizeScopeProblem(ctx context.Context, verbs domain.WriteVerbSet, problem domain.WriteScopeProblem, attribute string, targets ...domain.WriteTarget) (context.Context, error) {
	return cf.authorizeScopeProblemByRule(ctx, verbs, problem, attribute, "", targets...)
}

func (cf *Confluence) authorizeScopeProblemByRule(ctx context.Context, verbs domain.WriteVerbSet, problem domain.WriteScopeProblem, attribute, ruleID string, targets ...domain.WriteTarget) (context.Context, error) {
	if cf.authorizer == nil {
		return ctx, nil
	}
	return cf.authorizer.Authorize(ctx, domain.WriteAuthorizationRequest{Verbs: verbs, Targets: targets, ScopeProblem: problem, ScopeAttribute: attribute, ScopeRuleID: ruleID})
}

func (cf *Confluence) pageIdentity(ctx context.Context, id string) (confluenceIdentity, error) {
	if !domain.ValidConfluenceContentID(id) {
		return confluenceIdentity{}, fmt.Errorf("%w: Confluence content id is not canonical", domain.ErrUsage)
	}
	requirements := cf.scopeRequirements()
	if !requirements.Space && !requirements.Ancestors {
		return confluenceIdentity{id: id, kind: "page"}, nil
	}
	if cf.identity == nil {
		cf.identity = newConfluenceIdentityCache()
	}
	if value, ok := cf.identity.get(id); ok {
		return value, nil
	}
	if err, failed := cf.identity.failure(id); failed {
		return confluenceIdentity{}, err
	}
	meta, err := cf.GetMeta(ctx, id)
	if err != nil {
		cf.identity.fail(id, err)
		return confluenceIdentity{}, err
	}
	value, err := identityFromMeta(meta, id, requirements)
	if err != nil {
		cf.identity.fail(id, err)
		return confluenceIdentity{}, err
	}
	return value, nil
}

func identityFromMeta(meta *domain.PageMeta, expectedID string, requirements domain.WriteScopeRequirements) (confluenceIdentity, error) {
	if meta == nil || meta.ID != expectedID || !validConfluencePolicyKind(meta.Type) {
		return confluenceIdentity{}, fmt.Errorf("%w: Confluence did not return exact canonical content metadata", domain.ErrCheckFailed)
	}
	if requirements.Space && (meta.Space == "" || meta.Space != strings.ToUpper(meta.Space)) {
		return confluenceIdentity{}, fmt.Errorf("%w: Confluence did not return a canonical space identity", domain.ErrCheckFailed)
	}
	if requirements.Ancestors {
		if meta.AncestorIDs == nil {
			return confluenceIdentity{}, fmt.Errorf("%w: Confluence did not return hierarchy identity", domain.ErrCheckFailed)
		}
		for _, ancestor := range meta.AncestorIDs {
			if !domain.ValidConfluenceContentID(ancestor) {
				return confluenceIdentity{}, fmt.Errorf("%w: Confluence returned a non-canonical ancestor identity", domain.ErrCheckFailed)
			}
		}
	}
	return confluenceIdentity{id: meta.ID, kind: meta.Type, space: meta.Space, ancestorIDs: cloneAncestorIDs(meta.AncestorIDs)}, nil
}

func validConfluencePolicyKind(kind string) bool {
	switch kind {
	case "page", "blogpost", "attachment", "comment":
		return true
	}
	return false
}

func exactIdentityFromResource(resource *domain.Resource) (confluenceIdentity, error) {
	if resource == nil || !domain.ValidConfluenceContentID(resource.ID) ||
		(resource.Type != "page" && resource.Type != "blogpost") || resource.SpaceKey == "" ||
		resource.SpaceKey != strings.ToUpper(resource.SpaceKey) || !resource.AncestorsPresent ||
		len(resource.Ancestors) != len(resource.AncestorIDs) {
		return confluenceIdentity{}, fmt.Errorf("%w: Confluence resource metadata is not authorization-qualified", domain.ErrCheckFailed)
	}
	for _, ancestor := range resource.AncestorIDs {
		if !domain.ValidConfluenceContentID(ancestor) {
			return confluenceIdentity{}, fmt.Errorf("%w: Confluence resource has a non-canonical ancestor identity", domain.ErrCheckFailed)
		}
	}
	if len(resource.AncestorIDs) == 0 && resource.Parent != "" ||
		len(resource.AncestorIDs) > 0 && resource.Parent != resource.AncestorIDs[len(resource.AncestorIDs)-1] {
		return confluenceIdentity{}, fmt.Errorf("%w: Confluence resource hierarchy is inconsistent", domain.ErrCheckFailed)
	}
	return confluenceIdentity{id: resource.ID, kind: resource.Type, space: resource.SpaceKey, ancestorIDs: cloneAncestorIDs(resource.AncestorIDs)}, nil
}

func (cf *Confluence) rememberResource(resource *domain.Resource) {
	if cf == nil || cf.identity == nil {
		return
	}
	if value, err := exactIdentityFromResource(resource); err == nil {
		cf.identity.put(value)
	}
}

func (cf *Confluence) contentTarget(ctx context.Context, kind, subjectID, containerID string) (domain.WriteTarget, error) {
	var identity confluenceIdentity
	var err error
	requirements := cf.scopeRequirements()
	if kind == "" || subjectID == containerID && requirements.Kind {
		identity, err = cf.exactContentIdentity(ctx, containerID)
	} else {
		identity, err = cf.pageIdentity(ctx, containerID)
	}
	if err != nil {
		return domain.WriteTarget{}, err
	}
	if kind == "" {
		kind = identity.kind
	} else if subjectID == containerID && requirements.Kind && identity.kind != kind {
		return domain.WriteTarget{}, fmt.Errorf("%w: Confluence content kind does not match the write operation", domain.ErrCheckFailed)
	}
	if subjectID != containerID && identity.kind != "page" {
		return domain.WriteTarget{}, fmt.Errorf("%w: Confluence container is not a canonical page", domain.ErrCheckFailed)
	}
	target := domain.WriteTarget{Service: "confluence", Kind: kind, ID: subjectID, Space: identity.space}
	if requirements.Ancestors {
		target.AncestorIDs = cloneAncestorIDs(identity.ancestorIDs)
		if subjectID != containerID {
			target.AncestorIDs = append(target.AncestorIDs, containerID)
		}
	}
	return target, nil
}

func (cf *Confluence) exactContentIdentity(ctx context.Context, id string) (confluenceIdentity, error) {
	if !domain.ValidConfluenceContentID(id) {
		return confluenceIdentity{}, fmt.Errorf("%w: Confluence content id is not canonical", domain.ErrUsage)
	}
	if cf.identity == nil {
		cf.identity = newConfluenceIdentityCache()
	}
	if value, ok := cf.identity.get(id); ok {
		return value, nil
	}
	if err, failed := cf.identity.failure(id); failed {
		return confluenceIdentity{}, err
	}
	meta, err := cf.GetMeta(ctx, id)
	if err != nil {
		cf.identity.fail(id, err)
		return confluenceIdentity{}, err
	}
	value, err := identityFromMeta(meta, id, cf.scopeRequirements())
	if err != nil {
		cf.identity.fail(id, err)
		return confluenceIdentity{}, err
	}
	return value, nil
}

func (cf *Confluence) authorizeContent(ctx context.Context, verbs domain.WriteVerbSet, kind, subjectID, containerID string) (context.Context, domain.WriteTarget, error) {
	if cf.authorizer == nil {
		return ctx, domain.WriteTarget{}, nil
	}
	target, err := cf.contentTarget(ctx, kind, subjectID, containerID)
	if err != nil {
		return cf.authorizeResolutionFailure(ctx, verbs, kind, subjectID, err)
	}
	writeContext, err := cf.authorize(ctx, verbs, target)
	return writeContext, target, err
}

func (cf *Confluence) authorizeCreate(ctx context.Context, kind, space, parent string) (context.Context, error) {
	if cf.authorizer == nil {
		return ctx, nil
	}
	space = strings.TrimSpace(space)
	target := domain.WriteTarget{Service: "confluence", Kind: kind, Space: space, AncestorIDs: make([]string, 0)}
	if space == "" || space != strings.ToUpper(space) {
		return cf.authorizeScopeProblem(ctx, domain.WriteVerbSet{domain.WriteVerbCreate}, domain.WriteScopeContradiction, "space", target)
	}
	requirements := cf.scopeRequirements()
	if parent != "" && (requirements.Space || requirements.Ancestors) {
		identity, err := cf.pageIdentity(ctx, parent)
		if err != nil {
			writeContext, _, authorizationErr := cf.authorizeResolutionFailure(ctx, domain.WriteVerbSet{domain.WriteVerbCreate}, kind, parent, err)
			return writeContext, authorizationErr
		}
		if identity.kind != "page" || identity.space != space {
			return cf.authorizeScopeProblem(ctx, domain.WriteVerbSet{domain.WriteVerbCreate}, domain.WriteScopeContradiction, "space", target)
		}
		if requirements.Ancestors {
			target.AncestorIDs = append(target.AncestorIDs, identity.ancestorIDs...)
			target.AncestorIDs = append(target.AncestorIDs, parent)
		}
	}
	if kind == "blogpost" {
		target.AncestorIDs = make([]string, 0)
	}
	return cf.authorize(ctx, domain.WriteVerbSet{domain.WriteVerbCreate}, target)
}

func (cf *Confluence) authorizeMove(ctx context.Context, id, parent string) (context.Context, error) {
	if cf.authorizer == nil {
		return ctx, nil
	}
	source, err := cf.contentTarget(ctx, "page", id, id)
	if err != nil {
		writeContext, _, authorizationErr := cf.authorizeResolutionFailure(ctx, domain.WriteVerbSet{domain.WriteVerbUpdate, domain.WriteVerbMove}, "page", id, err)
		return writeContext, authorizationErr
	}
	destination, err := cf.contentTarget(ctx, "page", parent, parent)
	if err != nil {
		writeContext, _, authorizationErr := cf.authorizeResolutionFailure(ctx, domain.WriteVerbSet{domain.WriteVerbMove}, "page", parent, err)
		return writeContext, authorizationErr
	}
	if source.Space != "" && destination.Space != "" && source.Space != destination.Space {
		return cf.authorizeScopeProblem(ctx, domain.WriteVerbSet{domain.WriteVerbUpdate, domain.WriteVerbMove}, domain.WriteScopeContradiction, "space", source)
	}
	writeContext, err := cf.authorize(ctx, domain.WriteVerbSet{domain.WriteVerbUpdate, domain.WriteVerbMove}, source)
	if err != nil {
		return writeContext, err
	}
	writeContext, err = cf.authorize(writeContext, domain.WriteVerbSet{domain.WriteVerbMove}, destination)
	if err != nil {
		return writeContext, err
	}
	return cf.authorizeMoveHierarchy(writeContext, source, destination)
}

func (cf *Confluence) authorizeResolutionFailure(ctx context.Context, verbs domain.WriteVerbSet, kind, id string, err error) (context.Context, domain.WriteTarget, error) {
	if errors.Is(err, domain.ErrAuth) || errors.Is(err, domain.ErrForbidden) || errors.Is(err, domain.ErrUsage) || errors.Is(err, domain.ErrConfig) {
		return ctx, domain.WriteTarget{}, err
	}
	problem := domain.WriteScopeUnavailable
	attribute := "identity"
	if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrCheckFailed) {
		problem = domain.WriteScopeUnresolved
		attribute = "id"
	}
	target := domain.WriteTarget{Service: "confluence", Kind: kind}
	if domain.ValidConfluenceContentID(id) {
		target.ID = id
	}
	writeContext, authorizationErr := cf.authorizeScopeProblem(ctx, verbs, problem, attribute, target)
	return writeContext, target, authorizationErr
}

func (cf *Confluence) authorizeHierarchy(ctx context.Context, verbs domain.WriteVerbSet, target domain.WriteTarget) (context.Context, error) {
	reader, ok := cf.authorizer.(domain.WriteHierarchyPolicyReader)
	if !ok {
		return ctx, nil
	}
	for _, anchor := range reader.DenyUnderAnchors() {
		identity, err := cf.pageIdentity(ctx, anchor.ID)
		if err != nil {
			writeContext, _, authorizationErr := cf.authorizeResolutionFailure(ctx, verbs, "page", anchor.ID, err)
			return writeContext, authorizationErr
		}
		if anchor.ID == target.ID || containsID(identity.ancestorIDs, target.ID) || containsID(target.AncestorIDs, anchor.ID) {
			return cf.authorizeScopeProblemByRule(ctx, verbs, domain.WriteScopeProtectedSubtree, "under", anchor.RuleID, target)
		}
	}
	return ctx, nil
}

func (cf *Confluence) authorizeMoveHierarchy(ctx context.Context, source, destination domain.WriteTarget) (context.Context, error) {
	reader, ok := cf.authorizer.(domain.WriteHierarchyPolicyReader)
	if !ok {
		return ctx, nil
	}
	verbs := domain.WriteVerbSet{domain.WriteVerbUpdate, domain.WriteVerbMove}
	for _, anchor := range reader.DenyUnderAnchors() {
		if !containsID(source.AncestorIDs, anchor.ID) {
			continue
		}
		if destination.ID == anchor.ID || containsID(destination.AncestorIDs, anchor.ID) {
			continue
		}
		return cf.authorizeScopeProblemByRule(ctx, verbs, domain.WriteScopeProtectedSubtree, "under", anchor.RuleID, source)
	}
	return ctx, nil
}

func (cf *Confluence) authorizePageDelete(ctx context.Context, target domain.WriteTarget) (context.Context, error) {
	reader, ok := cf.authorizer.(domain.WriteHierarchyPolicyReader)
	if !ok {
		return ctx, nil
	}
	problem, attribute, ruleID := reader.PageDeleteScopeProblem(target)
	if problem == domain.WriteScopeResolved {
		return ctx, nil
	}
	return cf.authorizeScopeProblemByRule(ctx, domain.WriteVerbSet{domain.WriteVerbDelete}, problem, attribute, ruleID, target)
}
