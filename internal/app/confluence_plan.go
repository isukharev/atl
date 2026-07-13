package app

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
	"github.com/isukharev/atl/internal/safepath"
)

const (
	confluencePlanSchema   = "atl.confluence.plan/v1"
	confluencePlanMaxBytes = 16 << 20
	confluencePlanMaxPages = 10_000
)

// ConfluencePlan is a durable review artifact. It contains no native body
// bytes: exact candidate and baseline hashes bind those private bytes to their
// contained mirror paths, while the safe semantic consequences remain visible.
type ConfluencePlan struct {
	Schema       string                `json:"schema"`
	Root         string                `json:"root"`
	Target       string                `json:"target"`
	Summary      ConfluenceDiffSummary `json:"summary"`
	Entries      []ConfluencePlanEntry `json:"entries"`
	ProposalHash string                `json:"proposal_hash"`
}

type ConfluencePlanEntry struct {
	Operation       string                   `json:"operation"`
	ID              string                   `json:"id"`
	Type            string                   `json:"type"`
	Title           string                   `json:"title"`
	Space           string                   `json:"space"`
	Path            string                   `json:"path"`
	ExpectedVersion int                      `json:"expected_version"`
	BaselineSHA256  string                   `json:"baseline_sha256"`
	CandidateSHA256 string                   `json:"candidate_sha256"`
	Problems        []csf.Problem            `json:"problems,omitempty"`
	Blocks          []ConfluenceBlockChange  `json:"blocks,omitempty"`
	Features        []ConfluenceFeatureDelta `json:"features,omitempty"`
	ByteEvidence    *ConfluenceByteEvidence  `json:"byte_evidence,omitempty"`
}

type ConfluencePlanCreateResult struct {
	Path           string                `json:"path"`
	Schema         string                `json:"schema"`
	ProposalHash   string                `json:"proposal_hash"`
	OperationCount int                   `json:"operation_count"`
	Summary        ConfluenceDiffSummary `json:"summary"`
}

// CreateConfluencePlan freezes every modified page selected by target into a
// deterministic, private (0600) JSON plan. Invalid/unsupported page states stop
// the whole build; an empty plan is valid and explicitly reviewable.
func CreateConfluencePlan(target, into, out string) (*ConfluencePlanCreateResult, error) {
	if strings.TrimSpace(out) == "" || out == "-" {
		return nil, fmt.Errorf("%w: --out must be a durable file path", domain.ErrUsage)
	}
	if _, err := os.Lstat(out); err == nil {
		return nil, fmt.Errorf("%w: Confluence plan output %q already exists; choose a new review artifact path", domain.ErrCheckFailed, out)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("%w: inspect Confluence plan output %q: %v", domain.ErrCheckFailed, out, err)
	}
	root, canonicalTarget, err := canonicalConfluencePlanPaths(target, into)
	if err != nil {
		return nil, err
	}
	lock, err := lockConfluenceMutations(root, false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = lock.Unlock() }()

	diff, err := DiffConfluenceMirror(canonicalTarget, root)
	if err != nil {
		return nil, err
	}
	m := mirror.New(root)
	plan := &ConfluencePlan{
		Schema: confluencePlanSchema, Root: root, Target: canonicalTarget,
		Summary: diff.Summary, Entries: []ConfluencePlanEntry{},
	}
	for _, page := range diff.Pages {
		switch page.State {
		case "unchanged":
			continue
		case "modified":
			// supported below
		default:
			return nil, fmt.Errorf("%w: cannot plan page %s in state %s; reconcile the mirror first", domain.ErrCheckFailed, page.Path, page.State)
		}
		lc, body, loadErr := m.LoadCSF(page.Path)
		if loadErr != nil {
			return nil, loadErr
		}
		if lc.Synced == nil || lc.TrackedElsewhere {
			return nil, fmt.Errorf("%w: page %s has no canonical synced state", domain.ErrCheckFailed, page.Path)
		}
		if mirror.Hash(body) != page.Candidate.SHA256 || lc.Synced.Hash != page.Baseline.SHA256 {
			return nil, fmt.Errorf("%w: page %s changed while its plan was being built", domain.ErrCheckFailed, page.Path)
		}
		rel, relErr := filepath.Rel(root, page.Path)
		if relErr != nil || strings.HasPrefix(rel, "..") {
			return nil, fmt.Errorf("%w: page %s is outside plan root", domain.ErrCheckFailed, page.Path)
		}
		plan.Entries = append(plan.Entries, ConfluencePlanEntry{
			Operation: "update", ID: page.ID, Type: "page", Title: lc.Meta.Title, Space: lc.Meta.Space,
			Path: filepath.ToSlash(filepath.Clean(rel)), ExpectedVersion: lc.Synced.Version,
			BaselineSHA256: page.Baseline.SHA256, CandidateSHA256: page.Candidate.SHA256,
			Problems: page.Candidate.Problems, Blocks: page.Blocks, Features: page.Features,
			ByteEvidence: page.ByteEvidence,
		})
	}
	sort.Slice(plan.Entries, func(i, j int) bool {
		if plan.Entries[i].Path != plan.Entries[j].Path {
			return plan.Entries[i].Path < plan.Entries[j].Path
		}
		return plan.Entries[i].ID < plan.Entries[j].ID
	})
	if len(plan.Entries) > confluencePlanMaxPages {
		return nil, fmt.Errorf("%w: Confluence plan has more than %d update entries; split the target", domain.ErrUsage, confluencePlanMaxPages)
	}
	if err := validateConfluencePlanBuildSnapshot(m, plan); err != nil {
		return nil, err
	}
	plan.ProposalHash = confluencePlanHash(plan)
	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	if len(data) > confluencePlanMaxBytes {
		return nil, fmt.Errorf("%w: Confluence plan exceeds %d bytes; split the target", domain.ErrUsage, confluencePlanMaxBytes)
	}
	dir := filepath.Dir(out)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if err := safepath.WriteFileExclusiveWithin(dir, out, data, 0o600); err != nil {
		if os.IsExist(err) {
			return nil, fmt.Errorf("%w: Confluence plan output %q appeared during creation; no file was replaced", domain.ErrCheckFailed, out)
		}
		return nil, err
	}
	return &ConfluencePlanCreateResult{Path: out, Schema: plan.Schema, ProposalHash: plan.ProposalHash, OperationCount: len(plan.Entries), Summary: plan.Summary}, nil
}

func validateConfluencePlanBuildSnapshot(m *mirror.Mirror, plan *ConfluencePlan) error {
	paths := make([]string, len(plan.Entries))
	for i, entry := range plan.Entries {
		paths[i] = filepath.Join(plan.Root, filepath.FromSlash(entry.Path))
	}
	locals, bodies, err := m.LoadCSFMany(paths)
	if err != nil {
		return err
	}
	for i, entry := range plan.Entries {
		lc, body := locals[i], bodies[i]
		if lc.Synced == nil || lc.TrackedElsewhere || lc.Meta.ID != entry.ID || lc.Meta.Title != entry.Title || lc.Meta.Space != entry.Space || lc.Meta.Version != entry.ExpectedVersion || lc.Synced.Version != entry.ExpectedVersion || lc.Synced.Hash != entry.BaselineSHA256 || mirror.Hash(body) != entry.CandidateSHA256 {
			return fmt.Errorf("%w: page %s changed while its complete plan was being finalized", domain.ErrCheckFailed, entry.ID)
		}
		base, present, err := m.ReadBaseBody(entry.ID)
		if err != nil {
			return err
		}
		if !present || mirror.Hash(base) != entry.BaselineSHA256 {
			return fmt.Errorf("%w: page %s baseline changed while its complete plan was being finalized", domain.ErrCheckFailed, entry.ID)
		}
	}
	return nil
}

func canonicalConfluencePlanPaths(target, into string) (root, canonicalTarget string, err error) {
	if target == "" {
		if into != "" {
			target = into
		} else {
			target = "mirror"
		}
	}
	root = into
	if root == "" {
		root = mirrorRootOf(target)
	}
	root, err = evalSymlinksAbsolute(root)
	if err != nil {
		return "", "", localConfluenceTargetError("plan", root, err)
	}
	canonicalTarget, err = evalSymlinksAbsolute(target)
	if err != nil {
		return "", "", localConfluenceTargetError("plan", target, err)
	}
	if !within(root, canonicalTarget) {
		return "", "", fmt.Errorf("%w: plan target %q is outside mirror root %q", domain.ErrUsage, canonicalTarget, root)
	}
	return root, canonicalTarget, nil
}

func confluencePlanHash(plan *ConfluencePlan) string {
	copy := *plan
	copy.ProposalHash = ""
	canonical, _ := json.Marshal(copy)
	return hashHex(canonical)
}

type ConfluencePlanApplyOpts struct {
	Confirm              string
	ExpectedProposalHash string
}

type ConfluencePlanApplyResult struct {
	Schema       string                     `json:"schema"`
	ProposalHash string                     `json:"proposal_hash"`
	Root         string                     `json:"root"`
	Target       string                     `json:"target"`
	Mode         string                     `json:"mode"`
	Status       string                     `json:"status"`
	Complete     bool                       `json:"complete"`
	Entries      []ConfluencePlanApplyEntry `json:"entries"`
}

type ConfluencePlanApplyEntry struct {
	ID              string                   `json:"id"`
	Type            string                   `json:"type"`
	Title           string                   `json:"title"`
	Space           string                   `json:"space"`
	Path            string                   `json:"path"`
	Status          string                   `json:"status"`
	ExpectedVersion int                      `json:"expected_version"`
	BaselineSHA256  string                   `json:"baseline_sha256"`
	CandidateSHA256 string                   `json:"candidate_sha256"`
	Blocks          []ConfluenceBlockChange  `json:"blocks,omitempty"`
	Features        []ConfluenceFeatureDelta `json:"features,omitempty"`
	ByteEvidence    *ConfluenceByteEvidence  `json:"byte_evidence,omitempty"`
	FinalVersion    int                      `json:"final_version,omitempty"`
	Reconciled      bool                     `json:"reconciled,omitempty"`
	Warning         string                   `json:"warning,omitempty"`
	Failure         string                   `json:"failure,omitempty"`
}

type preparedConfluencePlanEntry struct {
	plan       ConfluencePlanEntry
	path       string
	lc         *mirror.LocalCSF
	body       []byte
	localDone  bool
	remote     *domain.Resource
	remoteDone bool
	refresh    RenderSettings
}

// PreviewConfluencePlan performs the complete local and remote preflight without
// entering the write path.
func (s *ConfluenceService) PreviewConfluencePlan(ctx context.Context, planPath string) (*ConfluencePlanApplyResult, error) {
	return s.runConfluencePlan(ctx, planPath, false, "")
}

// ApplyConfluencePlan requires Confirm=APPLY plus the exact proposal hash.
// Local and remote state for the complete plan is checked before the first PUT;
// exact prior success is resume-safe.
func (s *ConfluenceService) ApplyConfluencePlan(ctx context.Context, planPath string, opts ConfluencePlanApplyOpts) (*ConfluencePlanApplyResult, error) {
	if opts.Confirm != "APPLY" {
		return nil, fmt.Errorf("%w: --confirm must be exactly APPLY", domain.ErrUsage)
	}
	if opts.ExpectedProposalHash == "" {
		return nil, fmt.Errorf("%w: --expected-proposal-hash is required with --confirm APPLY", domain.ErrUsage)
	}
	return s.runConfluencePlan(ctx, planPath, true, opts.ExpectedProposalHash)
}

func (s *ConfluenceService) runConfluencePlan(ctx context.Context, planPath string, apply bool, expectedProposalHash string) (*ConfluencePlanApplyResult, error) {
	plan, err := loadConfluencePlan(planPath)
	if err != nil {
		return nil, err
	}
	if apply && expectedProposalHash != plan.ProposalHash {
		return nil, fmt.Errorf("%w: reviewed proposal hash does not match plan", domain.ErrCheckFailed)
	}
	result := &ConfluencePlanApplyResult{Schema: plan.Schema, ProposalHash: plan.ProposalHash, Root: plan.Root, Target: plan.Target, Mode: "preview", Status: "would_apply", Complete: true, Entries: []ConfluencePlanApplyEntry{}}
	if apply {
		result.Mode = "apply"
	}
	for _, entry := range plan.Entries {
		result.Entries = append(result.Entries, ConfluencePlanApplyEntry{
			ID: entry.ID, Type: entry.Type, Title: entry.Title, Space: entry.Space, Path: entry.Path,
			ExpectedVersion: entry.ExpectedVersion, BaselineSHA256: entry.BaselineSHA256, CandidateSHA256: entry.CandidateSHA256,
			Blocks: entry.Blocks, Features: entry.Features, ByteEvidence: entry.ByteEvidence, Status: "not_checked",
		})
	}
	lock, err := lockConfluenceMutations(plan.Root, false)
	if err != nil {
		result.Status = "blocked"
		result.Complete = false
		return result, err
	}
	defer func() { _ = lock.Unlock() }()
	m := mirror.New(plan.Root)
	prepared, err := s.prepareConfluencePlanLocal(m, plan)
	if err != nil {
		result.Complete = false
		result.Status = "blocked"
		return result, err
	}
	for i, item := range prepared {
		outcome := &result.Entries[i]
		remote, remoteErr := s.store.GetPage(ctx, item.plan.ID, domain.PullOpts{Format: "csf", IncludeRestrictions: confluenceNeedsRestrictions(item.refresh)})
		if remoteErr != nil {
			outcome.Status, outcome.Failure = "blocked", failReason(remoteErr)
			result.Complete = false
			result.Status = "blocked"
			return result, remoteErr
		}
		if bodyErr := requireConfluenceNativeBody(remote, item.plan.ID, "plan preflight"); bodyErr != nil {
			outcome.Status, outcome.Failure = "blocked", "incomplete-body"
			result.Complete = false
			result.Status = "blocked"
			return result, bodyErr
		}
		item.remote = remote
		switch {
		case confluencePlanRemoteIdentity(remote, item.plan) && remote.Version == item.plan.ExpectedVersion && mirror.Hash(remote.Body) == item.plan.BaselineSHA256 && !item.localDone:
			outcome.Status = "would_apply"
		case confluencePlanRemoteIdentity(remote, item.plan) && remote.Version == item.plan.ExpectedVersion+1 && mirror.Hash(remote.Body) == item.plan.CandidateSHA256:
			item.remoteDone = true
			outcome.Status = "already_satisfied"
			outcome.FinalVersion = remote.Version
		default:
			outcome.Status = "stale"
			switch {
			case !confluencePlanRemoteIdentity(remote, item.plan):
				outcome.Failure = "remote-identity-drift"
			case item.localDone && remote.Version == item.plan.ExpectedVersion && mirror.Hash(remote.Body) == item.plan.BaselineSHA256:
				outcome.Failure = "local-ahead-of-remote"
			case remote.Version != item.plan.ExpectedVersion && remote.Version != item.plan.ExpectedVersion+1:
				outcome.Failure = "remote-version-drift"
			default:
				outcome.Failure = "remote-content-drift"
			}
			result.Complete = false
			result.Status = "blocked"
			return result, fmt.Errorf("%w: page %s plan binding failed: %s", domain.ErrCheckFailed, item.plan.ID, outcome.Failure)
		}
	}
	if len(prepared) == 0 {
		result.Status = "already_satisfied"
		return result, nil
	}
	allDone := true
	for _, item := range prepared {
		if !item.remoteDone {
			allDone = false
			break
		}
	}
	if !apply {
		if allDone {
			result.Status = "already_satisfied"
		}
		return result, nil
	}
	wrote := false
	for i, item := range prepared {
		if item.remoteDone {
			result.Entries[i].Warning = s.refreshConfluenceMirror(ctx, m, item.lc, item.path, item.remote, item.refresh, "plan already applied")
			continue
		}
		currentLC, current, readErr := m.LoadCSF(item.path)
		if readErr != nil || currentLC.Synced == nil || currentLC.Meta.ID != item.plan.ID || currentLC.Meta.Title != item.plan.Title || currentLC.Meta.Space != item.plan.Space || currentLC.Synced.Version != item.plan.ExpectedVersion || currentLC.Synced.Hash != item.plan.BaselineSHA256 || mirror.Hash(current) != item.plan.CandidateSHA256 {
			result.Entries[i].Status, result.Entries[i].Failure = "blocked", "local-changed"
			result.Complete = false
			result.Status = "partial"
			return result, fmt.Errorf("%w: planned candidate %s changed before write", domain.ErrCheckFailed, item.path)
		}
		newVersion, updateErr := s.store.UpdatePage(ctx, item.plan.ID, item.plan.ExpectedVersion, item.plan.Title, item.body, false)
		final, getErr := s.store.GetPage(ctx, item.plan.ID, domain.PullOpts{Format: "csf", IncludeRestrictions: confluenceNeedsRestrictions(item.refresh)})
		if getErr == nil {
			getErr = requireConfluenceNativeBody(final, item.plan.ID, "plan reconciliation")
		}
		if getErr == nil && confluencePlanRemoteIdentity(final, item.plan) && final.Version == item.plan.ExpectedVersion+1 && mirror.Hash(final.Body) == item.plan.CandidateSHA256 {
			wrote = true
			result.Entries[i].Status = "applied"
			result.Entries[i].FinalVersion = final.Version
			result.Entries[i].Reconciled = updateErr != nil || newVersion != final.Version
			result.Entries[i].Warning = s.refreshConfluenceMirror(ctx, m, item.lc, item.path, final, item.refresh, "plan applied")
			continue
		}
		result.Complete = false
		result.Status = "partial"
		for j := i + 1; j < len(result.Entries); j++ {
			if result.Entries[j].Status == "would_apply" {
				result.Entries[j].Status = "not_attempted"
			}
		}
		switch {
		case getErr != nil:
			result.Entries[i].Status, result.Entries[i].Failure = "unknown", "reconciliation-failed"
			return result, fmt.Errorf("%w: update outcome for page %s is unknown; do not replay automatically", domain.ErrCheckFailed, item.plan.ID)
		case confluencePlanRemoteIdentity(final, item.plan) && final.Version == item.plan.ExpectedVersion && mirror.Hash(final.Body) == item.plan.BaselineSHA256:
			result.Entries[i].Status, result.Entries[i].Failure = "failed", "not-applied"
			if updateErr != nil {
				return result, updateErr
			}
			return result, fmt.Errorf("%w: page %s update was not applied", domain.ErrCheckFailed, item.plan.ID)
		default:
			result.Entries[i].Status, result.Entries[i].Failure = "unknown", "unexpected-final-state"
			return result, fmt.Errorf("%w: page %s reached an unexpected state; do not replay automatically", domain.ErrCheckFailed, item.plan.ID)
		}
	}
	if wrote {
		result.Status = "applied"
	} else {
		result.Status = "already_satisfied"
	}
	return result, nil
}

func (s *ConfluenceService) prepareConfluencePlanLocal(m *mirror.Mirror, plan *ConfluencePlan) ([]*preparedConfluencePlanEntry, error) {
	prepared := make([]*preparedConfluencePlanEntry, 0, len(plan.Entries))
	paths, ids := make([]string, len(plan.Entries)), make([]string, len(plan.Entries))
	for i, entry := range plan.Entries {
		paths[i], ids[i] = filepath.Join(plan.Root, filepath.FromSlash(entry.Path)), entry.ID
	}
	locals, bodies, err := m.LoadCSFMany(paths)
	if err != nil {
		return nil, err
	}
	views, err := m.ViewStatesOf(ids)
	if err != nil {
		return nil, err
	}
	baseRS, _ := ResolveRender(s.cfg, m.Root, config.RenderService{}, "confluence")
	for i, entry := range plan.Entries {
		path, lc, body := paths[i], locals[i], bodies[i]
		if !within(plan.Root, path) {
			return nil, fmt.Errorf("%w: planned path %q escapes mirror root", domain.ErrCheckFailed, entry.Path)
		}
		if lc.TrackedElsewhere || lc.Meta.ID != entry.ID || lc.Meta.Title != entry.Title || lc.Meta.Space != entry.Space || lc.Synced == nil {
			return nil, fmt.Errorf("%w: planned identity/path binding changed for %s", domain.ErrCheckFailed, entry.ID)
		}
		if mirror.Hash(body) != entry.CandidateSHA256 || csf.HasErrors(csf.Validate(body)) {
			return nil, fmt.Errorf("%w: planned candidate changed or became invalid for %s", domain.ErrCheckFailed, entry.ID)
		}
		base, present, err := m.ReadBaseBody(entry.ID)
		if err != nil {
			return nil, err
		}
		if !present {
			return nil, fmt.Errorf("%w: planned baseline is missing for %s", domain.ErrCheckFailed, entry.ID)
		}
		baseHash := mirror.Hash(base)
		localDone := false
		switch {
		case lc.Meta.Version == entry.ExpectedVersion && lc.Synced.Version == entry.ExpectedVersion && lc.Synced.Hash == entry.BaselineSHA256 && baseHash == entry.BaselineSHA256:
		case lc.Meta.Version == entry.ExpectedVersion+1 && lc.Synced.Version == entry.ExpectedVersion+1 && lc.Synced.Hash == entry.CandidateSHA256 && baseHash == entry.CandidateSHA256 && !lc.Dirty:
			localDone = true
		default:
			return nil, fmt.Errorf("%w: planned sidecar/base binding changed for %s", domain.ErrCheckFailed, entry.ID)
		}
		rs := baseRS
		if view, ok := views[entry.ID]; ok {
			rs = settingsFromViewState(view)
		}
		prepared = append(prepared, &preparedConfluencePlanEntry{plan: entry, path: path, lc: lc, body: body, localDone: localDone, refresh: rs})
	}
	return prepared, nil
}

func loadConfluencePlan(path string) (*ConfluencePlan, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, confluencePlanPathError("plan", path, err)
	}
	defer f.Close()
	limited := io.LimitReader(f, confluencePlanMaxBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("%w: read Confluence plan %q: %v", domain.ErrCheckFailed, path, err)
	}
	if len(data) > confluencePlanMaxBytes {
		return nil, fmt.Errorf("%w: Confluence plan exceeds %d bytes", domain.ErrUsage, confluencePlanMaxBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var plan ConfluencePlan
	if err := dec.Decode(&plan); err != nil {
		return nil, fmt.Errorf("%w: decode Confluence plan: %v", domain.ErrUsage, err)
	}
	if err := ensureJSONEOF(dec); err != nil {
		return nil, err
	}
	if plan.Schema != confluencePlanSchema {
		return nil, fmt.Errorf("%w: unsupported Confluence plan schema %q", domain.ErrUsage, plan.Schema)
	}
	if len(plan.Entries) > confluencePlanMaxPages {
		return nil, fmt.Errorf("%w: Confluence plan has more than %d entries", domain.ErrUsage, confluencePlanMaxPages)
	}
	if !validSHA256Hex(plan.ProposalHash) || confluencePlanHash(&plan) != plan.ProposalHash {
		return nil, fmt.Errorf("%w: Confluence plan proposal hash is missing or does not match its contents", domain.ErrCheckFailed)
	}
	canonical, marshalErr := json.MarshalIndent(&plan, "", "  ")
	if marshalErr != nil {
		return nil, marshalErr
	}
	canonical = append(canonical, '\n')
	if !bytes.Equal(data, canonical) {
		return nil, fmt.Errorf("%w: Confluence plan bytes are not canonical or changed after creation; recreate and review the plan", domain.ErrCheckFailed)
	}
	root, err := filepath.EvalSymlinks(plan.Root)
	if err != nil {
		return nil, confluencePlanPathError("plan root", plan.Root, err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("%w: canonicalize Confluence plan root %q: %v", domain.ErrCheckFailed, plan.Root, err)
	}
	if root != plan.Root {
		return nil, fmt.Errorf("%w: Confluence plan root identity changed", domain.ErrCheckFailed)
	}
	if !filepath.IsAbs(plan.Target) || !within(plan.Root, plan.Target) {
		return nil, fmt.Errorf("%w: Confluence plan target is outside its root", domain.ErrUsage)
	}
	if plan.Summary.Total < 0 || plan.Summary.Unchanged < 0 || plan.Summary.Added != 0 || plan.Summary.Removed != 0 || plan.Summary.Malformed != 0 || plan.Summary.MissingBaseline != 0 || plan.Summary.BaselineMismatch != 0 || plan.Summary.Unreadable != 0 || plan.Summary.Modified != len(plan.Entries) || plan.Summary.Total != plan.Summary.Unchanged+plan.Summary.Modified {
		return nil, fmt.Errorf("%w: Confluence plan summary does not match its supported entry set", domain.ErrUsage)
	}
	seenID, seenPath := map[string]bool{}, map[string]bool{}
	previous := ""
	for _, entry := range plan.Entries {
		if entry.Operation != "update" || entry.ID == "" || entry.Type != "page" || entry.Title == "" || entry.Space == "" || entry.ExpectedVersion <= 0 || !validSHA256Hex(entry.BaselineSHA256) || !validSHA256Hex(entry.CandidateSHA256) || entry.BaselineSHA256 == entry.CandidateSHA256 || csf.HasErrors(entry.Problems) {
			return nil, fmt.Errorf("%w: invalid Confluence plan entry for %q", domain.ErrUsage, entry.ID)
		}
		clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(entry.Path)))
		if clean != entry.Path || clean == "." || strings.HasPrefix(clean, "../") || filepath.IsAbs(filepath.FromSlash(entry.Path)) {
			return nil, fmt.Errorf("%w: invalid Confluence plan path %q", domain.ErrUsage, entry.Path)
		}
		if seenID[entry.ID] || seenPath[entry.Path] {
			return nil, fmt.Errorf("%w: duplicate Confluence plan identity/path", domain.ErrUsage)
		}
		if previous != "" && previous >= entry.Path {
			return nil, fmt.Errorf("%w: Confluence plan entries are not in canonical order", domain.ErrUsage)
		}
		seenID[entry.ID], seenPath[entry.Path], previous = true, true, entry.Path
	}
	return &plan, nil
}

func confluencePlanPathError(kind, path string, err error) error {
	if os.IsNotExist(err) {
		return fmt.Errorf("%w: Confluence %s %q does not exist", domain.ErrNotFound, kind, path)
	}
	return fmt.Errorf("%w: inspect Confluence %s %q: %v", domain.ErrCheckFailed, kind, path, err)
}

func ensureJSONEOF(dec *json.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("%w: trailing JSON value in Confluence plan", domain.ErrUsage)
		}
		return fmt.Errorf("%w: trailing data in Confluence plan: %v", domain.ErrUsage, err)
	}
	return nil
}

func validSHA256Hex(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

func confluencePlanRemoteIdentity(page *domain.Resource, entry ConfluencePlanEntry) bool {
	return page != nil && page.ID == entry.ID && page.Type == entry.Type && page.Title == entry.Title && page.SpaceKey == entry.Space
}

// ConfluencePlanApplyMarkdown renders deterministic per-page outcomes.
func ConfluencePlanApplyMarkdown(result *ConfluencePlanApplyResult) string {
	rows := make([][]string, 0, len(result.Entries))
	for _, entry := range result.Entries {
		final := ""
		if entry.FinalVersion > 0 {
			final = fmt.Sprint(entry.FinalVersion)
		}
		rows = append(rows, []string{entry.Status, entry.ID, entry.Title, entry.Space, entry.Path, fmt.Sprint(entry.ExpectedVersion), final, fmt.Sprint(len(entry.Blocks) + len(entry.Features)), entry.Warning})
	}
	root, target := markdownPlanInline(result.Root), markdownPlanInline(result.Target)
	return fmt.Sprintf("# Confluence plan %s\n\nRoot: %s · target: %s\n\nProposal: `%s` · complete: **%t** · status: **%s**\n\n%s", result.Mode, root, target, result.ProposalHash, result.Complete, result.Status, MarkdownTable([]string{"Status", "Page", "Title", "Space", "Path", "Expected", "Final", "Changes", "Warning"}, rows))
}

func markdownPlanInline(value string) string {
	return strings.ReplaceAll(markdownTableCell(value), "`", "\\`")
}

func ConfluencePlanCreateMarkdown(result *ConfluencePlanCreateResult) string {
	return strings.TrimRight(MarkdownTable([]string{"Plan", "Operations", "Proposal"}, [][]string{{result.Path, fmt.Sprint(result.OperationCount), result.ProposalHash}}), "\n")
}
