package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/fragment"
	"github.com/isukharev/atl/internal/mirror"
	"github.com/isukharev/atl/internal/safepath"
)

// CreatedMirrorRegistration reports whether an explicitly requested
// post-create registration reached a complete tracked state. A not_registered
// result always accompanies a non-nil error; the remote object is known to
// exist and the create operation must not be replayed.
type CreatedMirrorRegistration struct {
	Status             string   `json:"status"`
	Root               string   `json:"root"`
	Path               string   `json:"path,omitempty"`
	Version            int      `json:"version,omitempty"`
	SHA256             string   `json:"sha256,omitempty"`
	ReadbackReconciled bool     `json:"readback_reconciled"`
	Reason             string   `json:"reason,omitempty"`
	Recovery           string   `json:"recovery,omitempty"`
	Warnings           []string `json:"-"`
}

func newRegistration(root string) *CreatedMirrorRegistration {
	return &CreatedMirrorRegistration{Status: "not_registered", Root: filepath.Clean(root)}
}

func createdRegistrationRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", fmt.Errorf("%w: registered create/copy requires a non-empty mirror root", domain.ErrUsage)
	}
	return filepath.Clean(root), nil
}

func confluenceRegistrationFailure(page *domain.Resource, registration *CreatedMirrorRegistration, reason string, _ error) (*domain.Resource, *CreatedMirrorRegistration, error) {
	registration.Reason = reason
	if page != nil && strings.TrimSpace(page.ID) != "" {
		registration.Recovery = "preserve local files; recover only the emitted page id into the emitted registration root with a narrow conf pull; do not repeat page create/copy"
	}
	return page, registration, fmt.Errorf("%w: the returned page was created, but mirror registration failed (%s); do not replay the create operation", domain.ErrCheckFailed, reason)
}

func jiraRegistrationFailure(issue *domain.Issue, registration *CreatedMirrorRegistration, reason string, _ error) (*domain.Issue, *CreatedMirrorRegistration, error) {
	registration.Reason = reason
	if issue != nil && strings.TrimSpace(issue.Key) != "" {
		registration.Recovery = "preserve local files; recover only the emitted issue key into the emitted registration root with a narrow single-issue Jira pull; do not repeat issue create"
	}
	return issue, registration, fmt.Errorf("%w: the returned issue was created, but mirror registration failed (%s); do not replay the create operation", domain.ErrCheckFailed, reason)
}

func (s *ConfluenceService) CreateAndRegister(ctx context.Context, space, parent, title string, body []byte, root string) (*domain.Resource, *CreatedMirrorRegistration, error) {
	return s.createAndRegisterConfluence(ctx, space, parent, title, body, root, "create", runtime.GOOS)
}

func (s *ConfluenceService) CopyPageAndRegister(ctx context.Context, srcID, newTitle, space, parent, root string) (*domain.Resource, *CreatedMirrorRegistration, error) {
	return s.copyPageAndRegister(ctx, srcID, newTitle, space, parent, root, runtime.GOOS)
}

func (s *ConfluenceService) copyPageAndRegister(ctx context.Context, srcID, newTitle, space, parent, root, goos string) (*domain.Resource, *CreatedMirrorRegistration, error) {
	if err := validateCreatedRegistrationPlatform(goos); err != nil {
		return nil, nil, err
	}
	registration, m, rs, release, err := s.prepareConfluenceRegistration(root)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = release() }()

	src, err := s.store.GetPage(domain.WithRedactedHTTPTrace(domain.WithSingleAttempt(ctx)), srcID, domain.PullOpts{Format: "csf"})
	if err != nil {
		return nil, nil, err
	}
	if err := requireConfluenceNativeBody(src, srcID, "copy"); err != nil {
		return nil, nil, err
	}
	if src.ID != srcID || src.Type != "page" || strings.TrimSpace(src.SpaceKey) == "" || !src.AncestorsPresent {
		return nil, nil, fmt.Errorf("%w: copy source response did not prove exact page identity, space, and ancestry", domain.ErrCheckFailed)
	}
	if node, parseErr := csf.Parse(src.Body); parseErr == nil && rs.ExpandJiraMacros {
		if _, err := s.prepareConfluenceJiraMacroPopulation(m.Root, len(mirror.JiraMacroDescriptors(node)) > 0, false); err != nil {
			return nil, nil, err
		}
	}
	if space == "" {
		space = src.SpaceKey
	}
	if parent == "" {
		parent = src.Parent
	}
	created, err := s.store.CreatePage(domain.WithRedactedHTTPTrace(domain.WithSingleAttempt(ctx)), space, parent, newTitle, src.Body)
	if err != nil {
		return nil, nil, classifyCreateWriteError("page copy", err)
	}
	return s.finishConfluenceRegistration(ctx, m, rs, registration, created, space, parent, newTitle)
}

func (s *ConfluenceService) createAndRegisterConfluence(ctx context.Context, space, parent, title string, body []byte, root, operation, goos string) (*domain.Resource, *CreatedMirrorRegistration, error) {
	if err := validateCreatedRegistrationPlatform(goos); err != nil {
		return nil, nil, err
	}
	registration, m, rs, release, err := s.prepareConfluenceRegistration(root)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = release() }()
	if node, parseErr := csf.Parse(body); parseErr == nil && rs.ExpandJiraMacros {
		if _, err := s.prepareConfluenceJiraMacroPopulation(m.Root, len(mirror.JiraMacroDescriptors(node)) > 0, false); err != nil {
			return nil, nil, err
		}
	}
	created, err := s.store.CreatePage(domain.WithRedactedHTTPTrace(domain.WithSingleAttempt(ctx)), space, parent, title, body)
	if err != nil {
		return nil, nil, classifyCreateWriteError("page "+operation, err)
	}
	return s.finishConfluenceRegistration(ctx, m, rs, registration, created, space, parent, title)
}

func (s *ConfluenceService) prepareConfluenceRegistration(root string) (*CreatedMirrorRegistration, *mirror.Mirror, RenderSettings, func() error, error) {
	var err error
	root, err = createdRegistrationRoot(root)
	if err != nil {
		return nil, nil, RenderSettings{}, nil, err
	}
	registration := newRegistration(root)
	rs, warnings := ResolveRender(s.cfg, root, config.RenderService{}, "confluence")
	registration.Warnings = append(registration.Warnings, warnings...)
	lock, err := lockConfluenceMutations(root, true)
	if err != nil {
		return nil, nil, RenderSettings{}, nil, err
	}
	m := mirror.New(root)
	if err := m.EnsureScaffold(); err != nil {
		_ = lock.Unlock()
		return nil, nil, RenderSettings{}, nil, err
	}
	if _, err := m.SyncStates(); err != nil {
		_ = lock.Unlock()
		return nil, nil, RenderSettings{}, nil, err
	}
	if err := prepareMirrorBackendPopulation(root, "confluence", s.baseURL, ".csf", false); err != nil {
		_ = lock.Unlock()
		return nil, nil, RenderSettings{}, nil, err
	}
	return registration, m, rs, lock.Unlock, nil
}

func (s *ConfluenceService) finishConfluenceRegistration(ctx context.Context, m *mirror.Mirror, rs RenderSettings, registration *CreatedMirrorRegistration, created *domain.Resource, space, parent, title string) (*domain.Resource, *CreatedMirrorRegistration, error) {
	if created == nil || strings.TrimSpace(created.ID) == "" {
		return confluenceRegistrationFailure(created, registration, "create_response_unqualified", fmt.Errorf("response omitted the new page id"))
	}
	readback, err := s.store.GetPage(domain.WithRedactedHTTPTrace(domain.WithSingleAttempt(ctx)), created.ID, domain.PullOpts{Format: "csf", IncludeRestrictions: confluenceNeedsRestrictions(rs)})
	if err != nil {
		return confluenceRegistrationFailure(created, registration, "readback_unavailable", err)
	}
	if err := validateCreatedConfluenceReadback(readback, created.ID, space, parent, title); err != nil {
		return confluenceRegistrationFailure(created, registration, "readback_unqualified", err)
	}
	registration.ReadbackReconciled = true
	created = readback
	refs := []domain.Ref{}
	md := []byte(mirror.MDUnavailableStub)
	var macroSidecar *confluenceJiraMacroSidecar
	if node, parseErr := csf.Parse(readback.Body); parseErr == nil {
		refs = fragment.Extract(node)
		refs = fragment.Resolve(ctx, readback, refs, fragment.Deps{Users: s.users})
		mdOpts := confMDViewOptsForCommentsView(rs, readback, confluenceCommentsView{})
		if rs.ExpandJiraMacros {
			hasJiraMacros := len(mirror.JiraMacroDescriptors(node)) > 0
			jiraReady, bindErr := s.prepareConfluenceJiraMacroPopulation(m.Root, hasJiraMacros, false)
			switch {
			case bindErr != nil:
				registration.Warnings = append(registration.Warnings, "render: Jira query macro(s) kept as placeholders because the mirror backend binding was not qualified")
			case hasJiraMacros && !jiraReady:
				registration.Warnings = append(registration.Warnings, "render: Jira query macro(s) kept as placeholders because qualified Jira read access is unavailable")
			default:
				var macroWarnings []string
				macroSidecar, macroWarnings = s.resolveConfluenceJiraMacros(ctx, readback.ID, node, "")
				registration.Warnings = append(registration.Warnings, macroWarnings...)
			}
			mdOpts.JiraMacros = confluenceJiraMacroViews(macroSidecar)
		}
		md = mirror.RenderMarkdownOpts(node, refs, mdOpts)
	}
	readback.Refs = refs
	dir, slug, err := m.ClaimPageDir(readback.SpaceKey, readback.Ancestors, readback.Title, readback.ID)
	if err != nil {
		return confluenceRegistrationFailure(created, registration, "target_collision", err)
	}
	if action, targetErr := qualifyConfluenceClaimedTarget(m, readback.ID, dir, slug, filepath.Join(dir, slug+".csf"), &confluenceLocalQualification{byID: map[string]*confluenceLocalPage{}}); targetErr != nil {
		_ = action
		return confluenceRegistrationFailure(created, registration, "target_collision", targetErr)
	}
	meta := mirror.Meta{
		ID: readback.ID, Title: readback.Title, Space: readback.SpaceKey, Version: readback.Version,
		Hash: mirror.Hash(readback.Body), Parent: readback.Parent, Ancestors: readback.Ancestors,
		Labels: readback.Labels, Updated: readback.Updated, Restricted: readback.Restricted, Refs: refs,
	}
	metaBytes, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return confluenceRegistrationFailure(created, registration, "local_registration_failed", err)
	}
	nativeRel, _ := filepath.Rel(m.Root, filepath.Join(dir, slug+".csf"))
	mdRel, _ := filepath.Rel(m.Root, filepath.Join(dir, slug+".md"))
	metaRel, _ := filepath.Rel(m.Root, filepath.Join(dir, slug+".meta.json"))
	artifacts := []mirror.RegistrationArtifact{
		{Path: filepath.ToSlash(nativeRel), Data: readback.Body, Mode: 0o644},
		{Path: filepath.ToSlash(mdRel), Data: md, Mode: 0o644},
		{Path: filepath.ToSlash(metaRel), Data: append(metaBytes, '\n'), Mode: 0o644},
	}
	if macroSidecar != nil {
		macroBytes, encodeErr := encodeConfluenceJiraMacroSidecar(macroSidecar)
		if encodeErr != nil {
			return confluenceRegistrationFailure(created, registration, "local_registration_failed", encodeErr)
		}
		macroRel, _ := filepath.Rel(m.Root, confluenceJiraMacroPath(dir, slug))
		artifacts = append(artifacts, mirror.RegistrationArtifact{Path: filepath.ToSlash(macroRel), Data: macroBytes, Mode: 0o600})
	}
	state := mirror.SyncState{ID: readback.ID, Version: readback.Version, Hash: mirror.Hash(readback.Body), Path: filepath.ToSlash(nativeRel)}
	if err := m.RegisterNew(state, viewStateOf(rs), ".csf", readback.Body, artifacts); err != nil {
		return confluenceRegistrationFailure(created, registration, "local_registration_failed", err)
	}
	registration.Status = "registered"
	registration.Path = filepath.ToSlash(nativeRel)
	registration.Version = readback.Version
	registration.SHA256 = state.Hash
	return created, registration, nil
}

func validateCreatedConfluenceReadback(page *domain.Resource, id, space, parent, title string) error {
	if page == nil || page.ID != id || page.Type != "page" || page.Version <= 0 || !page.BodyPresent || !page.AncestorsPresent {
		return fmt.Errorf("authoritative response did not prove exact id/type/version/body/ancestry")
	}
	if page.SpaceKey != space || page.Parent != parent || page.Title != title {
		return fmt.Errorf("authoritative response did not match the requested space, parent, and title")
	}
	return nil
}

func classifyCreateWriteError(operation string, err error) error {
	if definitiveWriteRejection(err) {
		return err
	}
	return ambiguousWriteFailure(fmt.Sprintf("%v: %s outcome is unknown; the object may already exist, so do not retry automatically", domain.ErrCheckFailed, operation))
}

func (s *JiraService) CreateAndRegister(ctx context.Context, project, issueType, summary string, body []byte, fields map[string]string, root string) (*domain.Issue, *CreatedMirrorRegistration, error) {
	return s.createAndRegister(ctx, project, issueType, summary, body, fields, root, runtime.GOOS)
}

func (s *JiraService) createAndRegister(ctx context.Context, project, issueType, summary string, body []byte, fields map[string]string, root, goos string) (*domain.Issue, *CreatedMirrorRegistration, error) {
	if err := validateCreatedRegistrationPlatform(goos); err != nil {
		return nil, nil, err
	}
	var err error
	root, err = createdRegistrationRoot(root)
	if err != nil {
		return nil, nil, err
	}
	registration := newRegistration(root)
	rs, warnings := ResolveRender(s.cfg, root, config.RenderService{}, "jira")
	registration.Warnings = append(registration.Warnings, warnings...)
	m := mirror.New(root)
	if err := m.EnsureScaffold(); err != nil {
		return nil, nil, err
	}
	lock, err := lockJiraPendingFields(root, "create-registration")
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = lock.Unlock() }()
	if _, err := m.SyncStates(); err != nil {
		return nil, nil, err
	}
	if err := prepareMirrorBackendPopulation(root, "jira", s.baseURL, wikiExt, false); err != nil {
		return nil, nil, err
	}
	rs, err = s.resolveRenderFieldSelectors(ctx, rs)
	if err != nil {
		return nil, nil, err
	}
	created, err := s.tr.Create(domain.WithRedactedHTTPTrace(domain.WithSingleAttempt(ctx)), project, issueType, summary, body, fields)
	if err != nil {
		return nil, nil, classifyCreateWriteError("issue create", err)
	}
	if created == nil || strings.TrimSpace(created.Key) == "" {
		return jiraRegistrationFailure(created, registration, "create_response_unqualified", fmt.Errorf("response omitted the new issue key"))
	}
	extra := make([]string, 0, len(fields))
	for key := range fields {
		extra = append(extra, key)
	}
	sort.Strings(extra)
	projection := jiraPullFields(extra, rs)
	projection = appendUniqueString(projection, "updated")
	readback, err := s.tr.GetIssue(domain.WithRedactedHTTPTrace(domain.WithSingleAttempt(ctx)), created.Key, projection)
	if err != nil {
		return jiraRegistrationFailure(created, registration, "readback_unavailable", err)
	}
	if err := validateCreatedJiraReadback(readback, created.Key, project, issueType, projection); err != nil {
		return jiraRegistrationFailure(created, registration, "readback_unqualified", err)
	}
	registration.ReadbackReconciled = true
	created = readback
	projectSeg := safepath.Segment(readback.Project)
	keySeg := safepath.Segment(readback.Key)
	if projectSeg != readback.Project || keySeg != readback.Key {
		return jiraRegistrationFailure(created, registration, "readback_unqualified", fmt.Errorf("project or issue key is not a canonical safe path segment"))
	}
	dir := filepath.Join(root, projectSeg)
	wikiPath := filepath.Join(dir, keySeg+wikiExt)
	if err := qualifyNewJiraRegistrationTarget(m, root, dir, keySeg); err != nil {
		return jiraRegistrationFailure(created, registration, "target_collision", err)
	}
	snapshot := JiraIssueSnapshot{Key: readback.Key, ID: readback.ID, Fields: readback.Fields}
	snapshotBytes, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return jiraRegistrationFailure(created, registration, "local_registration_failed", err)
	}
	wikiRel, _ := filepath.Rel(root, wikiPath)
	mdRel, _ := filepath.Rel(root, filepath.Join(dir, keySeg+".md"))
	snapshotRel, _ := filepath.Rel(root, filepath.Join(dir, keySeg+".json"))
	artifacts := []mirror.RegistrationArtifact{
		{Path: filepath.ToSlash(wikiRel), Data: []byte(readback.Body), Mode: 0o644},
		{Path: filepath.ToSlash(mdRel), Data: renderIssueMarkdown(readback, nil, rs), Mode: 0o644},
		{Path: filepath.ToSlash(snapshotRel), Data: append(snapshotBytes, '\n'), Mode: 0o644},
	}
	state := mirror.SyncState{ID: keySeg, Version: 0, Hash: mirror.Hash([]byte(readback.Body)), Path: filepath.ToSlash(wikiRel)}
	if err := m.RegisterNew(state, viewStateOf(rs), wikiExt, []byte(readback.Body), artifacts); err != nil {
		return jiraRegistrationFailure(created, registration, "local_registration_failed", err)
	}
	registration.Status = "registered"
	registration.Path = filepath.ToSlash(wikiRel)
	registration.SHA256 = state.Hash
	return created, registration, nil
}

func validateCreatedRegistrationPlatform(goos string) error {
	switch goos {
	case "windows":
		return fmt.Errorf("%w: post-create mirror registration requires directory durability, which is unsupported on windows", domain.ErrCheckFailed)
	default:
		return nil
	}
}

func validateCreatedJiraReadback(issue *domain.Issue, key, project, issueType string, projection []string) error {
	if issue == nil || issue.ID == "" || issue.Key != key || issue.Fields == nil || issue.Project != project || issue.Type != issueType {
		return fmt.Errorf("authoritative response did not prove exact key/id/project identity")
	}
	for _, field := range projection {
		if _, present := issue.Fields[field]; !present {
			return fmt.Errorf("authoritative response omitted requested field %q", field)
		}
	}
	description := issue.Fields["description"]
	if description != nil {
		if _, ok := description.(string); !ok {
			return fmt.Errorf("authoritative description is neither a string nor null")
		}
	}
	updated, ok := issue.Fields["updated"].(string)
	if !ok || strings.TrimSpace(updated) == "" {
		return fmt.Errorf("authoritative updated field is not a non-empty string")
	}
	if _, err := parseJiraHistoryTime(updated); err != nil {
		return fmt.Errorf("authoritative updated field is invalid: %w", err)
	}
	return nil
}

func appendUniqueString(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func qualifyNewJiraRegistrationTarget(m *mirror.Mirror, root, dir, key string) error {
	states, err := m.SyncStates()
	if err != nil {
		return err
	}
	wikiRel, _ := filepath.Rel(root, filepath.Join(dir, key+wikiExt))
	for _, state := range states {
		if state.ID == key || filepath.Clean(state.Path) == filepath.Clean(wikiRel) {
			return fmt.Errorf("%w: issue key or canonical wiki path is already tracked", domain.ErrCheckFailed)
		}
	}
	entries, err := safepath.ReadDirWithin(root, dir)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	owned := map[string]bool{
		key + wikiExt: true, key + ".md": true, key + ".json": true,
		key + ".assets": true, key + ".epic-children.json": true,
	}
	for _, entry := range entries {
		if owned[entry.Name()] {
			return fmt.Errorf("%w: untracked Jira registration target already contains issue artifacts", domain.ErrCheckFailed)
		}
	}
	for _, pendingPath := range []string{jiraPendingFieldsPath(root, key), jiraPendingFieldsTxnPath(root, key)} {
		if _, statErr := safepath.StatWithin(root, pendingPath); statErr == nil {
			return fmt.Errorf("%w: Jira registration target has unrelated pending state", domain.ErrCheckFailed)
		} else if !os.IsNotExist(statErr) {
			return statErr
		}
	}
	return nil
}
