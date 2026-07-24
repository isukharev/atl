package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
	"github.com/isukharev/atl/internal/safepath"
)

const confluenceMirrorSnapshotSchemaVersion = 1

// ConfluenceMirrorSnapshot is a content-free, deterministic health inventory
// of one durable Confluence mirror. Detailed page identities and bytes remain
// available through status/diff; this contract exists for exact cardinalities.
type ConfluenceMirrorSnapshot struct {
	SchemaVersion   int                               `json:"schema_version"`
	Service         string                            `json:"service"`
	RemoteRequested bool                              `json:"remote_requested"`
	Complete        bool                              `json:"complete"`
	Reconciled      bool                              `json:"reconciled"`
	Local           ConfluenceMirrorLocalSummary      `json:"local"`
	Native          ConfluenceMirrorNativeSummary     `json:"native"`
	Validation      ConfluenceMirrorValidationSummary `json:"validation"`
	Render          ConfluenceMirrorRenderSummary     `json:"render"`
	Remote          ConfluenceMirrorRemoteSummary     `json:"remote"`
}

type ConfluenceMirrorLocalSummary struct {
	Present       int  `json:"present"`
	Clean         int  `json:"clean"`
	LocallyEdited int  `json:"locally_edited"`
	Tracked       int  `json:"tracked"`
	Untracked     int  `json:"untracked"`
	NonCanonical  int  `json:"non_canonical"`
	Reconciled    bool `json:"reconciled"`
}

type ConfluenceMirrorNativeSummary struct {
	Total              int  `json:"total"`
	Unchanged          int  `json:"unchanged"`
	Added              int  `json:"added"`
	Removed            int  `json:"removed"`
	Modified           int  `json:"modified"`
	Malformed          int  `json:"malformed"`
	MissingBaseline    int  `json:"missing_baseline"`
	BaselineMismatch   int  `json:"baseline_mismatch"`
	Unreadable         int  `json:"unreadable"`
	BaselinePresent    int  `json:"baseline_present"`
	BaselineMissing    int  `json:"baseline_missing"`
	BaselineUnreadable int  `json:"baseline_unreadable"`
	BaselineValid      int  `json:"baseline_valid"`
	BaselineInvalid    int  `json:"baseline_invalid"`
	Reconciled         bool `json:"reconciled"`
}

type ConfluenceMirrorValidationSummary struct {
	Total      int  `json:"total"`
	Present    int  `json:"present"`
	Absent     int  `json:"absent"`
	Valid      int  `json:"valid"`
	Invalid    int  `json:"invalid"`
	Unreadable int  `json:"unreadable"`
	Reconciled bool `json:"reconciled"`
}

type ConfluenceMirrorRenderSummary struct {
	Expected           int  `json:"expected"`
	Present            int  `json:"present"`
	Missing            int  `json:"missing"`
	Current            int  `json:"current"`
	Legacy             int  `json:"legacy"`
	MissingMarker      int  `json:"missing_marker"`
	Unsupported        int  `json:"unsupported"`
	Unreadable         int  `json:"unreadable"`
	StateRecorded      int  `json:"state_recorded"`
	StateMissing       int  `json:"state_missing"`
	RendererCompatible bool `json:"renderer_compatible"`
	Reconciled         bool `json:"reconciled"`
}

type ConfluenceMirrorRemoteSummary struct {
	Requested    bool `json:"requested"`
	Eligible     int  `json:"eligible"`
	Attempted    int  `json:"attempted"`
	NotAttempted int  `json:"not_attempted"`
	Checked      int  `json:"checked"`
	InSync       int  `json:"in_sync"`
	Drifted      int  `json:"drifted"`
	Unavailable  int  `json:"unavailable"`
	Reconciled   bool `json:"reconciled"`
}

// SnapshotConfluenceMirror inspects a mirror without config, credentials,
// network access, or writes.
func SnapshotConfluenceMirror(dir string) (*ConfluenceMirrorSnapshot, error) {
	result, _, err := inspectConfluenceMirror(dir)
	return result, err
}

// PreflightConfluenceMirrorRemoteSnapshot performs the complete local
// inspection and records that remote evidence was requested, without loading
// config/credentials or touching the network. The CLI uses it before wiring a
// backend so a local fail-closed result always wins over setup errors.
func PreflightConfluenceMirrorRemoteSnapshot(dir string) (*ConfluenceMirrorSnapshot, error) {
	result, _, err := inspectConfluenceMirror(dir)
	if result != nil {
		result.RemoteRequested = true
		result.Remote.Requested = true
		finalizeConfluenceMirrorSnapshot(result)
	}
	return result, err
}

// SnapshotMirror optionally adds one bounded remote metadata probe per
// canonical page. Local integrity failures stop before any network request.
func (s *ConfluenceService) SnapshotMirror(ctx context.Context, dir string, checkRemote bool) (*ConfluenceMirrorSnapshot, error) {
	if !checkRemote {
		result, _, err := inspectConfluenceMirror(dir)
		return result, err
	}
	root, guard, result, locals, localErr := beginConfluenceMirrorSnapshot(dir)
	if guard == nil {
		return result, localErr
	}
	finishBeforeRemote := func() (*ConfluenceMirrorSnapshot, error) {
		retry, finishErr := guard.finish()
		if finishErr != nil {
			return nil, contentFreeConfluenceSnapshotError(finishErr)
		}
		if retry {
			return s.SnapshotMirror(ctx, root, true)
		}
		return result, localErr
	}
	if result == nil {
		return finishBeforeRemote()
	}
	result.RemoteRequested = true
	result.Remote.Requested = true
	if localErr != nil || !result.Complete || !result.Reconciled {
		finalizeConfluenceMirrorSnapshot(result)
		return finishBeforeRemote()
	}
	if s.store == nil {
		retry, finishErr := guard.finish()
		if finishErr != nil {
			return nil, contentFreeConfluenceSnapshotError(finishErr)
		}
		if retry {
			return s.SnapshotMirror(ctx, root, true)
		}
		return result, fmt.Errorf("%w: remote mirror snapshot requires a configured Confluence backend", domain.ErrConfig)
	}
	probeContext := domain.WithRedactedHTTPTrace(domain.WithSingleAttempt(ctx))
	for _, local := range locals {
		if local.Meta.ID == "" || local.TrackedElsewhere || local.Synced == nil {
			continue
		}
		result.Remote.Attempted++
		meta, err := s.store.GetMeta(probeContext, local.Meta.ID)
		if err != nil || meta == nil {
			result.Remote.Unavailable++
			continue
		}
		result.Remote.Checked++
		if local.Synced != nil && meta.Version != local.Synced.Version {
			result.Remote.Drifted++
		} else {
			result.Remote.InSync++
		}
	}
	finalizeConfluenceMirrorSnapshot(result)
	retry, finishErr := guard.finish()
	if finishErr != nil {
		return nil, contentFreeConfluenceSnapshotError(finishErr)
	}
	if retry {
		return nil, contentFreeConfluenceSnapshotError(fmt.Errorf("%w: the mirror changed during remote inspection", domain.ErrCheckFailed))
	}
	return result, nil
}

func inspectConfluenceMirror(dir string) (*ConfluenceMirrorSnapshot, []*mirror.LocalCSF, error) {
	root, guard, result, locals, inspectErr := beginConfluenceMirrorSnapshot(dir)
	if guard == nil {
		return result, locals, inspectErr
	}
	retry, finishErr := guard.finish()
	if finishErr != nil {
		return nil, nil, contentFreeConfluenceSnapshotError(finishErr)
	}
	if retry {
		return inspectConfluenceMirror(root)
	}
	return result, locals, inspectErr
}

func beginConfluenceMirrorSnapshot(dir string) (string, *mirrorSnapshotLock, *ConfluenceMirrorSnapshot, []*mirror.LocalCSF, error) {
	if dir == "" {
		dir = "mirror"
	}
	guard, err := beginMirrorSnapshotLock(dir, filepath.Join(dir, ".atl", confluenceMutationLockName))
	if err != nil {
		return dir, nil, nil, nil, contentFreeConfluenceSnapshotError(err)
	}
	result, locals, inspectErr := inspectConfluenceMirrorUnlocked(dir)
	return dir, guard, result, locals, inspectErr
}

func inspectConfluenceMirrorUnlocked(dir string) (*ConfluenceMirrorSnapshot, []*mirror.LocalCSF, error) {
	diff, diffErr := DiffConfluenceMirror("", dir)
	if diff == nil {
		return nil, nil, contentFreeConfluenceSnapshotError(diffErr)
	}
	m := mirror.New(diff.Root)
	locals, err := m.ListCSF()
	if err != nil {
		return nil, nil, contentFreeConfluenceSnapshotError(err)
	}
	result := &ConfluenceMirrorSnapshot{
		SchemaVersion: confluenceMirrorSnapshotSchemaVersion,
		Service:       "confluence",
		Native: ConfluenceMirrorNativeSummary{
			Total: diff.Summary.Total, Unchanged: diff.Summary.Unchanged,
			Added: diff.Summary.Added, Removed: diff.Summary.Removed,
			Modified: diff.Summary.Modified, Malformed: diff.Summary.Malformed,
			MissingBaseline:  diff.Summary.MissingBaseline,
			BaselineMismatch: diff.Summary.BaselineMismatch,
			Unreadable:       diff.Summary.Unreadable,
		},
		Validation: ConfluenceMirrorValidationSummary{Total: diff.Summary.Total, Unreadable: diff.Summary.Unreadable},
		Render:     ConfluenceMirrorRenderSummary{Expected: len(locals)},
	}
	for _, page := range diff.Pages {
		if page.ID == "" || !page.tracked {
			result.Native.BaselineMissing++
		} else {
			base, present, baseErr := m.ReadBaseBody(page.ID)
			switch {
			case baseErr != nil:
				result.Native.BaselineUnreadable++
				result.Complete = false
				diffErr = moreSevereErr(diffErr, fmt.Errorf("%w: one or more mirror baselines are unreadable", domain.ErrCheckFailed))
			case present:
				result.Native.BaselinePresent++
				if !csf.HasErrors(csf.Validate(base)) {
					result.Native.BaselineValid++
				} else {
					result.Native.BaselineInvalid++
				}
			default:
				result.Native.BaselineMissing++
			}
		}
		if page.Candidate.Present {
			result.Validation.Present++
			if page.Candidate.Valid {
				result.Validation.Valid++
			} else {
				result.Validation.Invalid++
			}
		} else {
			result.Validation.Absent++
		}
	}
	ids := make([]string, 0, len(locals))
	for _, local := range locals {
		result.Local.Present++
		if local.Dirty {
			result.Local.LocallyEdited++
		} else {
			result.Local.Clean++
		}
		if local.Synced != nil {
			result.Local.Tracked++
		} else {
			result.Local.Untracked++
		}
		if local.TrackedElsewhere {
			result.Local.NonCanonical++
		} else if local.Meta.ID != "" && local.Synced != nil {
			result.Remote.Eligible++
		}
		ids = append(ids, local.Meta.ID)
	}
	views, err := m.ViewStatesOf(ids)
	if err != nil {
		return nil, nil, contentFreeConfluenceSnapshotError(err)
	}
	for _, local := range locals {
		if _, ok := views[local.Meta.ID]; ok && !local.TrackedElsewhere {
			result.Render.StateRecorded++
		} else {
			result.Render.StateMissing++
		}
		mdPath := strings.TrimSuffix(local.Path, ".csf") + ".md"
		body, readErr := safepath.ReadFileWithin(diff.Root, mdPath)
		switch {
		case os.IsNotExist(readErr):
			result.Render.Missing++
		case readErr != nil:
			result.Render.Unreadable++
			diffErr = moreSevereErr(diffErr, fmt.Errorf("%w: one or more Confluence derived views are unreadable", domain.ErrCheckFailed))
		default:
			result.Render.Present++
			switch confluenceViewMarkerClass(body) {
			case "current":
				result.Render.Current++
			case "legacy":
				result.Render.Legacy++
			case "unsupported":
				result.Render.Unsupported++
			default:
				result.Render.MissingMarker++
			}
		}
	}
	result.Complete = diff.Complete && result.Native.BaselineUnreadable == 0 && result.Render.Unreadable == 0
	finalizeConfluenceMirrorSnapshot(result)
	return result, locals, contentFreeConfluenceSnapshotError(diffErr)
}

func contentFreeConfluenceSnapshotError(err error) error {
	if err == nil {
		return nil
	}
	kind := domain.ErrCheckFailed
	switch {
	case errors.Is(err, domain.ErrUsage):
		kind = domain.ErrUsage
	case errors.Is(err, domain.ErrNotFound):
		kind = domain.ErrNotFound
	case errors.Is(err, domain.ErrConfig):
		kind = domain.ErrConfig
	}
	return fmt.Errorf("%w: content-free mirror snapshot could not be completed; preserve the mirror and use an approved page-level command for details", kind)
}

func confluenceViewMarkerClass(body []byte) string {
	marker := mirror.ConfluenceDocumentMarkerLine(string(body))
	switch marker {
	case mirror.ConfluenceDocumentMarker:
		return "current"
	case "<!-- atl:document confluence-page v3 -->",
		"<!-- atl:document confluence-page v2 -->",
		"<!-- atl:document confluence-page v1 -->",
		"<!-- atl:document confluence-page -->":
		return "legacy"
	default:
		if strings.HasPrefix(marker, "<!-- atl:document confluence-page") {
			return "unsupported"
		}
		return "missing"
	}
}

func finalizeConfluenceMirrorSnapshot(result *ConfluenceMirrorSnapshot) {
	stateTotal := result.Native.Unchanged + result.Native.Added + result.Native.Removed + result.Native.Modified +
		result.Native.Malformed + result.Native.MissingBaseline + result.Native.BaselineMismatch + result.Native.Unreadable
	result.Local.Reconciled = result.Local.Present == result.Local.Clean+result.Local.LocallyEdited &&
		result.Local.Present == result.Local.Tracked+result.Local.Untracked && result.Local.NonCanonical <= result.Local.Untracked
	result.Native.Reconciled = result.Native.Total == stateTotal &&
		result.Native.Total == result.Native.BaselinePresent+result.Native.BaselineMissing+result.Native.BaselineUnreadable &&
		result.Native.BaselinePresent == result.Native.BaselineValid+result.Native.BaselineInvalid
	result.Validation.Reconciled = result.Validation.Total == result.Validation.Present+result.Validation.Absent &&
		result.Validation.Present == result.Validation.Valid+result.Validation.Invalid &&
		result.Validation.Unreadable <= result.Validation.Total
	markerTotal := result.Render.Current + result.Render.Legacy + result.Render.MissingMarker + result.Render.Unsupported
	result.Render.Reconciled = result.Render.Expected == result.Render.Present+result.Render.Missing+result.Render.Unreadable &&
		result.Render.Present == markerTotal &&
		result.Render.Expected == result.Render.StateRecorded+result.Render.StateMissing
	result.Render.RendererCompatible = result.Render.Unsupported == 0 && result.Render.Unreadable == 0
	result.Remote.NotAttempted = result.Local.Present - result.Remote.Attempted
	result.Remote.Reconciled = result.Remote.Eligible <= result.Local.Present &&
		result.Remote.Attempted <= result.Remote.Eligible && result.Remote.Attempted+result.Remote.NotAttempted == result.Local.Present &&
		result.Remote.Attempted == result.Remote.Checked+result.Remote.Unavailable &&
		result.Remote.Checked == result.Remote.InSync+result.Remote.Drifted
	if result.Remote.Requested && result.Remote.Unavailable > 0 {
		result.Complete = false
	}
	result.Reconciled = result.Local.Reconciled && result.Native.Reconciled && result.Validation.Reconciled &&
		result.Render.Reconciled && result.Remote.Reconciled
}
