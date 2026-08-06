package jira

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/isukharev/atl/internal/domain"
)

type issueIdentityCache struct {
	mu             sync.Mutex
	values         map[string]domain.WriteTarget
	recency        *list.List
	entries        map[string]*list.Element
	failures       map[string]error
	failureRecency *list.List
	failureEntries map[string]*list.Element
}

const issueIdentityCacheLimit = 4096

func newIssueIdentityCache() *issueIdentityCache {
	return &issueIdentityCache{
		values: make(map[string]domain.WriteTarget), recency: list.New(),
		entries:  make(map[string]*list.Element),
		failures: make(map[string]error), failureRecency: list.New(),
		failureEntries: make(map[string]*list.Element),
	}
}

func (cache *issueIdentityCache) get(reference string) (domain.WriteTarget, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	target, ok := cache.values[reference]
	if ok {
		cache.recency.MoveToFront(cache.entries[target.Key])
	}
	return target, ok
}

func (cache *issueIdentityCache) put(reference string, target domain.WriteTarget) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if element, exists := cache.entries[target.Key]; exists {
		cache.recency.MoveToFront(element)
	} else {
		cache.entries[target.Key] = cache.recency.PushFront(target.Key)
	}
	cache.values[reference], cache.values[target.Key] = target, target
	cache.removeFailure(reference)
	cache.removeFailure(target.Key)
	for len(cache.entries) > issueIdentityCacheLimit {
		oldest := cache.recency.Back()
		cache.removeCanonical(oldest.Value.(string))
	}
}

func (cache *issueIdentityCache) failure(reference string) (bool, error) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	err, ok := cache.failures[reference]
	if ok {
		cache.failureRecency.MoveToFront(cache.failureEntries[reference])
	}
	return ok, err
}

func (cache *issueIdentityCache) fail(reference string, err error) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.failures[reference] = err
	if element := cache.failureEntries[reference]; element != nil {
		cache.failureRecency.MoveToFront(element)
	} else {
		cache.failureEntries[reference] = cache.failureRecency.PushFront(reference)
	}
	for len(cache.failureEntries) > issueIdentityCacheLimit {
		oldest := cache.failureRecency.Back().Value.(string)
		cache.removeFailure(oldest)
	}
}

func (cache *issueIdentityCache) evictReference(reference string) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	canonical := reference
	if target, ok := cache.values[reference]; ok {
		canonical = target.Key
	}
	for cachedReference, target := range cache.values {
		if cachedReference == reference || cachedReference == canonical || target.Key == canonical {
			delete(cache.values, cachedReference)
		}
	}
	if element := cache.entries[canonical]; element != nil {
		cache.recency.Remove(element)
		delete(cache.entries, canonical)
	}
	cache.removeFailure(reference)
	cache.removeFailure(canonical)
}

func (cache *issueIdentityCache) removeCanonical(canonical string) {
	for reference, target := range cache.values {
		if reference == canonical || target.Key == canonical {
			delete(cache.values, reference)
		}
	}
	cache.recency.Remove(cache.entries[canonical])
	delete(cache.entries, canonical)
}

func (cache *issueIdentityCache) removeFailure(reference string) {
	delete(cache.failures, reference)
	if element := cache.failureEntries[reference]; element != nil {
		cache.failureRecency.Remove(element)
		delete(cache.failureEntries, reference)
	}
}

func (j *Jira) authorize(ctx context.Context, verbs domain.WriteVerbSet, targets []domain.WriteTarget) (context.Context, error) {
	if j.authorizer == nil {
		return ctx, nil
	}
	return j.authorizer.Authorize(ctx, domain.WriteAuthorizationRequest{Verbs: verbs, Targets: targets})
}

func (j *Jira) authorizeScopeProblem(ctx context.Context, verbs domain.WriteVerbSet, problem domain.WriteScopeProblem, attribute string, targets ...domain.WriteTarget) (context.Context, error) {
	if j.authorizer == nil {
		return ctx, nil
	}
	return j.authorizer.Authorize(ctx, domain.WriteAuthorizationRequest{Verbs: verbs, Targets: targets, ScopeProblem: problem, ScopeAttribute: attribute})
}

// authorizePartialTarget admits structurally incomplete targets only through a
// rule that does not depend on the missing attribute. This is the service-wide
// escape hatch for operations such as link deletion whose issue scope cannot
// be resolved from the adapter input.
func (j *Jira) authorizePartialTarget(ctx context.Context, verbs domain.WriteVerbSet, attribute string, target domain.WriteTarget) (context.Context, error) {
	if j.authorizer == nil {
		return ctx, nil
	}
	return j.authorizer.Authorize(ctx, domain.WriteAuthorizationRequest{Verbs: verbs, Targets: []domain.WriteTarget{target}, ScopeAttribute: attribute})
}

func (j *Jira) issueTargets(ctx context.Context, kind string, references ...string) ([]domain.WriteTarget, error) {
	targets := make([]domain.WriteTarget, 0, len(references))
	for _, reference := range references {
		target, err := j.issueTarget(ctx, kind, reference)
		if err != nil {
			return nil, &issueTargetResolutionError{Reference: reference, Err: err}
		}
		targets = append(targets, target)
	}
	return targets, nil
}

type issueTargetResolutionError struct {
	Reference string
	Err       error
}

func (e *issueTargetResolutionError) Error() string { return e.Err.Error() }
func (e *issueTargetResolutionError) Unwrap() error { return e.Err }

func (j *Jira) issueTarget(ctx context.Context, kind, reference string) (domain.WriteTarget, error) {
	keyReference := domain.ValidJiraIssueKey(reference)
	idReference := domain.ValidConfluenceContentID(reference)
	if !keyReference && !idReference {
		return domain.WriteTarget{}, &issueReferenceInvalidError{}
	}
	if target, ok := j.identity.get(reference); ok {
		target.Kind = kind
		return target, nil
	}
	if failed, err := j.identity.failure(reference); failed {
		return domain.WriteTarget{}, err
	}
	var response issueDTO
	path := "/rest/api/2/issue/" + url.PathEscape(reference) + "?fields=project"
	if err := j.c.GetJSONUseNumber(ctx, path, &response); err != nil {
		j.identity.fail(reference, err)
		return domain.WriteTarget{}, err
	}
	issue := j.mapIssue(response)
	if issue == nil || !domain.ValidConfluenceContentID(issue.ID) || !domain.ValidJiraIssueKey(issue.Key) || issue.Project == "" || issue.Project != strings.ToUpper(issue.Project) {
		err := fmt.Errorf("%w: Jira did not return a canonical issue key and project for policy evaluation", domain.ErrCheckFailed)
		j.identity.fail(reference, err)
		return domain.WriteTarget{}, err
	}
	if keyReference && reference != issue.Key {
		err := &issueReferenceMovedError{Reference: reference, Canonical: issue.Key}
		j.identity.fail(reference, err)
		return domain.WriteTarget{}, err
	}
	target := domain.WriteTarget{Service: "jira", Kind: kind, Project: issue.Project, Key: issue.Key}
	base := target
	base.Kind = "issue"
	j.identity.put(reference, base)
	return target, nil
}

type issueReferenceMovedError struct {
	Reference string
	Canonical string
}

type issueReferenceInvalidError struct{}

func (*issueReferenceInvalidError) Error() string { return "Jira issue reference is not canonical" }

func (e *issueReferenceMovedError) Error() string {
	return "Jira issue reference resolves to a different canonical key"
}

func (j *Jira) authorizeIssues(ctx context.Context, verbs domain.WriteVerbSet, kind string, references ...string) (context.Context, error) {
	if j.authorizer == nil {
		return ctx, nil
	}
	targets, err := j.issueTargets(ctx, kind, references...)
	if err != nil {
		return j.authorizeResolutionFailure(ctx, verbs, kind, references[0], err)
	}
	return j.authorize(ctx, verbs, targets)
}

func (j *Jira) authorizeResolutionFailure(ctx context.Context, verbs domain.WriteVerbSet, kind, reference string, err error) (context.Context, error) {
	var resolution *issueTargetResolutionError
	if errors.As(err, &resolution) {
		reference = resolution.Reference
	}
	if errors.Is(err, domain.ErrAuth) || errors.Is(err, domain.ErrForbidden) || errors.Is(err, domain.ErrUsage) || errors.Is(err, domain.ErrConfig) {
		return ctx, err
	}
	problem := domain.WriteScopeUnavailable
	attribute := "identity"
	var moved *issueReferenceMovedError
	var invalid *issueReferenceInvalidError
	errors.As(err, &moved)
	errors.As(err, &invalid)
	if moved != nil || invalid != nil || errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrCheckFailed) {
		problem = domain.WriteScopeUnresolved
		attribute = "key"
	}
	target := domain.WriteTarget{Service: "jira", Kind: kind}
	if domain.ValidJiraIssueKey(reference) {
		target.Key = reference
	} else if domain.ValidConfluenceContentID(reference) {
		target.ID = reference
	}
	return j.authorizeScopeProblem(ctx, verbs, problem, attribute, target)
}

func canonicalProjectFromField(value any) (string, bool) {
	var project string
	switch typed := value.(type) {
	case string:
		project = typed
	case map[string]string:
		if len(typed) != 1 {
			return "", false
		}
		project = typed["key"]
	case map[string]any:
		if len(typed) != 1 {
			return "", false
		}
		project, _ = typed["key"].(string)
	}
	project = strings.TrimSpace(project)
	return project, project != "" && project == strings.ToUpper(project) && domain.ValidJiraIssueKey(project+"-1")
}

func addWriteVerb(verbs domain.WriteVerbSet, wanted domain.WriteVerb) domain.WriteVerbSet {
	for _, verb := range verbs {
		if verb == wanted {
			return verbs
		}
	}
	return append(append(domain.WriteVerbSet(nil), verbs...), wanted)
}

func (j *Jira) authorizeIssueMutation(ctx context.Context, verbs domain.WriteVerbSet, kind, reference string, projectValue any, relocates bool) (context.Context, error) {
	if j.authorizer == nil {
		return ctx, nil
	}
	target, err := j.issueTarget(ctx, kind, reference)
	if err != nil {
		return j.authorizeResolutionFailure(ctx, verbs, kind, reference, err)
	}
	targets := []domain.WriteTarget{target}
	if relocates {
		project, canonical := canonicalProjectFromField(projectValue)
		if !canonical {
			return j.authorizeScopeProblem(ctx, verbs, domain.WriteScopeUnresolved, "project", target)
		}
		destination := target
		destination.Project = project
		targets = append(targets, destination)
		verbs = addWriteVerb(verbs, domain.WriteVerbUpdate)
		verbs = addWriteVerb(verbs, domain.WriteVerbMove)
	}
	return j.authorize(ctx, verbs, targets)
}
