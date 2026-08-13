package app

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/isukharev/atl/internal/corpus"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
)

const (
	corpusCacheStatusHit       = "hit"
	corpusCacheStatusPublished = "published"

	corpusCacheReasonEmpty        = "empty"
	corpusCacheReasonIncompatible = "incompatible"
	corpusCacheReasonChanged      = "changed"
	corpusCacheReasonIneligible   = "ineligible"
	corpusCacheReasonUnqualified  = "unqualified"
)

type corpusCacheProbe struct {
	budget   *domain.ReadBudget
	duration time.Duration
}

func newCorpusCacheProbe(options CorpusBuildOptions) (*corpusCacheProbe, error) {
	budget, err := domain.NewReadBudget(options.CacheMaxRequests, options.CacheMaxResponseBytes)
	if err != nil {
		return nil, err
	}
	return &corpusCacheProbe{budget: budget, duration: options.CacheDeadline}, nil
}

func (probe *corpusCacheProbe) context(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(parent, probe.duration)
	ctx = domain.WithReadBudget(ctx, probe.budget)
	ctx = domain.WithRedactedHTTPTrace(ctx)
	return ctx, cancel
}

func (probe *corpusCacheProbe) usage() corpus.CaptureUsage {
	if probe == nil || probe.budget == nil {
		return corpus.CaptureUsage{}
	}
	return corpusUsage(probe.budget.Usage())
}

type corpusCacheMetadataRow struct {
	ID          string   `json:"id"`
	Type        string   `json:"type"`
	Title       string   `json:"title"`
	Space       string   `json:"space"`
	Version     int      `json:"version"`
	Updated     string   `json:"updated"`
	Parent      string   `json:"parent"`
	Ancestors   []string `json:"ancestors"`
	AncestorIDs []string `json:"ancestor_ids"`
	Labels      []string `json:"labels"`
	Restricted  bool     `json:"restricted"`
	URL         string   `json:"url"`
}

type corpusCacheMetadataEnvelope struct {
	SchemaVersion int                      `json:"schema_version"`
	Rows          []corpusCacheMetadataRow `json:"rows"`
}

func normalizeCorpusCacheMetadata(inventory domain.ConfluenceCorpusMetadataInventory, space string, maxPages int) ([]corpusCacheMetadataRow, string, string, error) {
	if !inventory.Complete || inventory.Rows == nil || len(inventory.Rows) > maxPages {
		return nil, "", "", corpus.ErrIntegrity
	}
	rows := make([]corpusCacheMetadataRow, 0, len(inventory.Rows))
	ids := make([]string, 0, len(inventory.Rows))
	for _, source := range inventory.Rows {
		if source.ID == "" || source.Type != "page" || source.Title == "" || source.Space != space ||
			source.Version < 1 || source.Updated == "" || source.URL == "" ||
			len(source.Ancestors) != len(source.AncestorIDs) ||
			(len(source.AncestorIDs) == 0 && source.Parent != "") ||
			(len(source.AncestorIDs) > 0 && source.Parent != source.AncestorIDs[len(source.AncestorIDs)-1]) {
			return nil, "", "", corpus.ErrIntegrity
		}
		labels := append([]string(nil), source.Labels...)
		sort.Strings(labels)
		for index, label := range labels {
			if label == "" || index > 0 && labels[index-1] == label {
				return nil, "", "", corpus.ErrIntegrity
			}
		}
		rows = append(rows, corpusCacheMetadataRow{
			ID: source.ID, Type: source.Type, Title: source.Title, Space: source.Space,
			Version: source.Version, Updated: source.Updated, Parent: source.Parent,
			Ancestors: append([]string{}, source.Ancestors...), AncestorIDs: append([]string{}, source.AncestorIDs...),
			Labels: labels, Restricted: source.Restricted, URL: source.URL,
		})
		ids = append(ids, source.ID)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	for index := 1; index < len(rows); index++ {
		if rows[index-1].ID == rows[index].ID {
			return nil, "", "", corpus.ErrIntegrity
		}
	}
	selectionDigest, err := corpus.CaptureSelectionDigest(corpus.ServiceConfluence, ids)
	if err != nil {
		return nil, "", "", err
	}
	data, err := json.Marshal(corpusCacheMetadataEnvelope{SchemaVersion: 1, Rows: rows})
	if err != nil {
		return nil, "", "", corpus.ErrIntegrity
	}
	metadataDigest, err := confluenceCompleteHashJSON(struct {
		Domain string          `json:"domain"`
		Data   json.RawMessage `json:"data"`
	}{Domain: "atl.corpus.cache-metadata.v1", Data: data})
	if err != nil {
		return nil, "", "", corpus.ErrIntegrity
	}
	return rows, selectionDigest, metadataDigest, nil
}

func readCorpusCacheMetadata(ctx context.Context, service *ConfluenceService, space string, maxPages int) ([]corpusCacheMetadataRow, string, string, error) {
	if service == nil || service.corpusMetadata == nil {
		return nil, "", "", domain.ErrCheckFailed
	}
	inventory, err := service.corpusMetadata.ReadConfluenceCorpusMetadata(ctx, space, maxPages)
	if err != nil {
		return nil, "", "", err
	}
	return normalizeCorpusCacheMetadata(inventory, space, maxPages)
}

func validateCorpusCacheSnapshot(root string, rows []corpusCacheMetadataRow, selectionDigest string, options CorpusBuildOptions) error {
	snapshot, err := mirror.New(root).BeginCorpusSnapshot(mirror.CorpusSnapshotConfluence, mirror.CorpusSnapshotOptions{
		Limits: corpusBuildSnapshotLimits(options),
	})
	if err != nil || snapshot.Len() != len(rows) {
		return corpus.ErrIntegrity
	}
	ids := make([]string, 0, snapshot.Len())
	remote := make(map[string]corpusCacheMetadataRow, len(rows))
	for _, row := range rows {
		remote[row.ID] = row
	}
	for index := 0; index < snapshot.Len(); index++ {
		item, readErr := snapshot.ReadItem(index)
		if readErr != nil {
			return readErr
		}
		var metadata mirror.Meta
		if json.Unmarshal(item.Metadata.Data, &metadata) != nil || metadata.Restricted == nil {
			return corpus.ErrIntegrity
		}
		row, found := remote[metadata.ID]
		labels := append([]string(nil), metadata.Labels...)
		sort.Strings(labels)
		// Reconcile every metadata field retained by the projection. Canonical URL
		// and the full ancestor-ID chain are probe-only freshness evidence: the
		// projection retains neither, while Parent binds its one hierarchy edge.
		if !found || row.Type != "page" || row.Title != metadata.Title || row.Space != metadata.Space ||
			row.Version != metadata.Version || row.Updated != metadata.Updated || row.Parent != metadata.Parent ||
			!equalCorpusCacheStrings(row.Ancestors, metadata.Ancestors) ||
			!equalCorpusCacheStrings(row.Labels, labels) || row.Restricted != *metadata.Restricted {
			return corpus.ErrIntegrity
		}
		ids = append(ids, metadata.ID)
	}
	localSelection, err := corpus.CaptureSelectionDigest(corpus.ServiceConfluence, ids)
	if err != nil || localSelection != selectionDigest || snapshot.Revalidate() != nil {
		return corpus.ErrIntegrity
	}
	return nil
}

func equalCorpusCacheStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func (service *CorpusBuildService) tryCorpusCacheHit(ctx context.Context, store *corpus.Store, options CorpusBuildOptions, probe *corpusCacheProbe, limits corpus.Limits) (*CorpusBuildResult, string, error) {
	current, err := store.SelectCurrent(ctx)
	if errors.Is(err, corpus.ErrNoCurrent) {
		return nil, corpusCacheReasonEmpty, nil
	}
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = current.Close() }()
	manifest := current.Manifest()
	if len(manifest.Qualifications) != 1 || manifest.Qualifications[0].Service != corpus.ServiceConfluence {
		return nil, corpusCacheReasonIncompatible, nil
	}
	bindingSpec := corpus.CacheBindingMemberSpec()
	bindingMembers := 0
	for _, member := range manifest.Members {
		if member.Service == bindingSpec.Service && member.StableID == bindingSpec.StableID && member.Role == bindingSpec.Role {
			bindingMembers++
			if member.Path != bindingSpec.Path {
				return nil, "", corpus.ErrIntegrity
			}
		}
	}
	if bindingMembers == 0 {
		return nil, corpusCacheReasonIncompatible, nil
	}
	if bindingMembers != 1 {
		return nil, "", corpus.ErrIntegrity
	}
	binding, err := corpus.LoadCacheBindingV1(ctx, current)
	if err != nil {
		return nil, "", err
	}
	if err := corpus.VerifyCacheBindingV1(binding, current); err != nil {
		return nil, "", err
	}
	if !binding.Reusable {
		return nil, corpusCacheReasonIneligible, nil
	}
	generatorDigest, err := corpus.GeneratorIdentityDigest(service.generatorVersion, service.generatorCommit, service.buildState)
	if err != nil || binding.GeneratorDigest != generatorDigest || binding.TrustDigest != service.confluenceTrustDigest {
		return nil, corpusCacheReasonIncompatible, nil
	}
	_, states, err := corpusBuildServices(options)
	if err != nil || len(states) != 1 || states[0].Service != corpus.ServiceConfluence || binding.SelectorDigest != states[0].SelectorDigest {
		return nil, corpusCacheReasonIncompatible, nil
	}
	optionsDigest, err := service.captureOptionsDigest(corpus.ServiceConfluence, options)
	if err != nil || binding.OptionsDigest != optionsDigest {
		return nil, corpusCacheReasonIncompatible, nil
	}
	projection, qualified, err := loadCorpusDeltaGeneration(ctx, current, limits)
	if err != nil {
		return nil, "", err
	}
	capture := projection.captures[corpus.ServiceConfluence]
	if !qualified || capture.SelectionDigest != binding.SelectionDigest || capture.Total != binding.Total ||
		projection.receipt.ProjectionDigest != manifest.Qualifications[0].ProjectionDigest {
		return nil, "", corpus.ErrIntegrity
	}
	probeCtx, cancel := probe.context(ctx)
	defer cancel()
	scope, err := service.currentScope(probeCtx, corpus.ServiceConfluence)
	if err != nil {
		return nil, "", err
	}
	if scope != binding.ScopeDigest {
		return nil, corpusCacheReasonIncompatible, nil
	}
	first, firstSelection, firstDigest, err := readCorpusCacheMetadata(probeCtx, service.confluence, options.ConfluenceSpace, options.MaxConfluencePages)
	if err != nil {
		if corpusCacheMetadataUnqualified(err) {
			return nil, corpusCacheReasonUnqualified, nil
		}
		return nil, "", err
	}
	second, secondSelection, secondDigest, err := readCorpusCacheMetadata(probeCtx, service.confluence, options.ConfluenceSpace, options.MaxConfluencePages)
	if err != nil {
		if corpusCacheMetadataUnqualified(err) {
			return nil, corpusCacheReasonUnqualified, nil
		}
		return nil, "", err
	}
	if firstSelection != secondSelection || firstDigest != secondDigest || !equalCorpusCacheRows(first, second) ||
		firstSelection != binding.SelectionDigest || firstDigest != binding.MetadataDigest {
		return nil, corpusCacheReasonChanged, nil
	}
	if err := verifyCorpusGenerationTombstoneState(ctx, store, current, limits); err != nil {
		return nil, "", err
	}
	if err := store.ConfirmCurrent(ctx, current); err != nil {
		return nil, "", err
	}
	usage := probe.usage()
	return &CorpusBuildResult{
		SchemaVersion: CorpusBuildSchemaV1, Source: "cache", Reused: true,
		Usage: usage, Projection: projection.receipt, Generation: current.Summary(),
		Cache: &CorpusBuildCacheResult{Status: corpusCacheStatusHit, Reason: "exact", ProbeUsage: usage},
		Services: []CorpusBuildServiceResult{{
			Service: corpus.ServiceConfluence, Status: "reused", Count: capture.Total,
			StartedAt: capture.StartedAt, CompletedAt: capture.CompletedAt,
			Usage: corpus.CaptureUsage{}, Dimensions: append([]corpus.CaptureDimensionEvidence(nil), capture.Dimensions...),
		}},
	}, "", nil
}

func equalCorpusCacheRows(left, right []corpusCacheMetadataRow) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}

func (service *CorpusBuildService) buildCorpusCacheBinding(ctx context.Context, root string, receipt corpus.CaptureReceipt, options CorpusBuildOptions, probe *corpusCacheProbe, limits corpus.Limits) (*corpus.CacheBindingV1, error) {
	if receipt.Service != corpus.ServiceConfluence || receipt.Completed != receipt.Total {
		return nil, corpus.ErrIntegrity
	}
	input := corpus.CacheBindingInput{
		Service: corpus.ServiceConfluence, ScopeDigest: receipt.ScopeDigest,
		SelectorDigest: receipt.SelectorDigest, OptionsDigest: receipt.OptionsDigest,
		BuildState: service.buildState, ManifestSchema: corpus.ManifestSchemaV1,
		ReceiptSchema: corpus.ReceiptSchemaV1, ProjectionSchema: corpus.IndexerSchemaV2,
		CaptureSchema: corpus.CaptureReceiptSchemaV1, SelectionDigest: receipt.SelectionDigest,
		Total: receipt.Total, UserReferencesDeterministic: true, Reusable: true,
	}
	generatorDigest, generatorErr := corpus.GeneratorIdentityDigest(service.generatorVersion, service.generatorCommit, service.buildState)
	switch {
	case service.buildState != corpus.BuildStateClean:
		input.Reusable = false
		input.IneligibleReason = corpus.CacheIneligibleBuildNotClean
	case generatorErr != nil:
		input.Reusable = false
		input.IneligibleReason = corpus.CacheIneligibleGeneratorUnbound
	default:
		input.GeneratorDigest = generatorDigest
	}
	if input.Reusable && service.confluenceTrustDigest == "" {
		input.Reusable = false
		input.IneligibleReason = corpus.CacheIneligibleTrustUnbound
	} else if service.confluenceTrustDigest != "" {
		input.TrustDigest = service.confluenceTrustDigest
	}
	if input.Reusable && service.confluence.corpusMetadata == nil {
		input.Reusable = false
		input.IneligibleReason = corpus.CacheIneligibleMetadataUnbound
	}
	if input.Reusable && !completeCorpusDeltaCapture(receipt, limits) {
		input.Reusable = false
		input.IneligibleReason = corpus.CacheIneligibleEvidenceIncomplete
	}
	if input.Reusable {
		probeCtx, cancel := probe.context(ctx)
		defer cancel()
		rows, selectionDigest, metadataDigest, err := readCorpusCacheMetadata(probeCtx, service.confluence, options.ConfluenceSpace, options.MaxConfluencePages)
		if err != nil {
			if corpusCacheMetadataUnqualified(err) {
				input.Reusable = false
				input.IneligibleReason = corpus.CacheIneligibleEvidenceIncomplete
			} else {
				return nil, err
			}
		}
		if input.Reusable {
			confirmedRows, confirmedSelection, confirmedDigest, confirmErr := readCorpusCacheMetadata(probeCtx, service.confluence, options.ConfluenceSpace, options.MaxConfluencePages)
			if confirmErr != nil {
				if corpusCacheMetadataUnqualified(confirmErr) {
					input.Reusable = false
					input.IneligibleReason = corpus.CacheIneligibleEvidenceIncomplete
				} else {
					return nil, confirmErr
				}
			} else if selectionDigest != confirmedSelection || metadataDigest != confirmedDigest ||
				!equalCorpusCacheRows(rows, confirmedRows) || selectionDigest != receipt.SelectionDigest || len(rows) != receipt.Total {
				input.Reusable = false
				input.IneligibleReason = corpus.CacheIneligibleEvidenceIncomplete
			}
		}
		if input.Reusable {
			if err := validateCorpusCacheSnapshot(root, rows, selectionDigest, options); err != nil {
				if errors.Is(err, corpus.ErrIntegrity) {
					input.Reusable = false
					input.IneligibleReason = corpus.CacheIneligibleEvidenceIncomplete
				} else {
					return nil, err
				}
			}
		}
		if input.Reusable {
			input.MetadataDigest = metadataDigest
		}
	}
	binding, err := corpus.BuildCacheBindingV1(input, limits)
	if err != nil {
		return nil, err
	}
	return &binding, nil
}

func corpusCacheMetadataUnqualified(err error) bool {
	return errors.Is(err, domain.ErrCheckFailed) || errors.Is(err, domain.ErrForbidden) || errors.Is(err, corpus.ErrIntegrity)
}

func corpusCacheEnabled(options CorpusBuildOptions) bool {
	return strings.TrimSpace(options.CacheRoot) != ""
}

// CorpusBuildCacheFastPathEligible is the one static policy shared by the app
// and composition owners for selecting exact qualified cache trust.
func CorpusBuildCacheFastPathEligible(options CorpusBuildOptions) bool {
	return corpusCacheEnabled(options) && options.ConfluenceSpace != "" && options.JiraProject == "" &&
		!options.Comments && !options.Attachments && !options.AttachmentBodies
}

func openCorpusBuildCache(options CorpusBuildOptions, limits corpus.Limits) (*corpus.Store, error) {
	storeOptions := corpus.Options{Limits: limits}
	if options.InitializeCache {
		return corpus.Initialize(options.CacheRoot, storeOptions)
	}
	return corpus.Open(options.CacheRoot, storeOptions)
}
