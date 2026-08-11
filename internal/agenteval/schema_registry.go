package agenteval

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"slices"

	"github.com/isukharev/atl/internal/agenteval/schemaregistry"
)

const (
	StandaloneSchemaRegistrySchema       = schemaregistry.Schema
	StandaloneSchemaRegistryVersion      = schemaregistry.SchemaVersion
	StandaloneSchemaRegistryMaxBytes     = schemaregistry.MaxBytes
	StandaloneMigrationPreviewSchema     = "agent-eval/migration-preview"
	StandaloneMigrationResultSchema      = "agent-eval/migration-result"
	StandaloneMigrationArtifactVersion   = 1
	StandaloneMigrationArtifactMaxBytes  = 64 << 10
	StandaloneMigrationConfirmation      = "MIGRATE"
	standaloneMigrationPreviewHashDomain = "agent-eval/migration-preview/v1"
)

var (
	ErrStandaloneSchemaRegistry = errors.New("standalone_schema_registry_error")
	ErrStandaloneMigration      = errors.New("standalone_schema_migration_error")
)

type StandaloneSchemaRegistry = schemaregistry.Registry
type StandaloneSchemaDescriptor = schemaregistry.Descriptor
type StandaloneSchemaMigrationEdge = schemaregistry.MigrationEdge

type StandaloneSchemaInspection struct {
	Schema                string                          `json:"schema"`
	SchemaVersion         int                             `json:"schema_version"`
	ContractVersion       string                          `json:"contract_version"`
	Namespace             string                          `json:"namespace"`
	Kind                  string                          `json:"kind"`
	Owner                 string                          `json:"owner"`
	Current               int                             `json:"current"`
	Readable              []int                           `json:"readable"`
	Emitted               []int                           `json:"emitted"`
	Executable            []int                           `json:"executable"`
	Disposition           string                          `json:"disposition"`
	Privacy               string                          `json:"privacy"`
	Migration             string                          `json:"migration"`
	MaxBytes              int64                           `json:"max_bytes"`
	SchemaResource        string                          `json:"schema_resource"`
	SchemaSHA256          string                          `json:"schema_sha256"`
	MigrationGraphSHA256  string                          `json:"migration_graph_sha256"`
	RegistrySHA256        string                          `json:"registry_sha256"`
	SupportedMigrations   int                             `json:"supported_migrations"`
	MigrationUnavailable  bool                            `json:"migration_unavailable"`
	MigrationEdges        []StandaloneSchemaMigrationEdge `json:"migration_edges"`
	ImplementationSHA256s []string                        `json:"implementation_sha256s"`
}

type StandaloneMigrationCount struct {
	ID    string `json:"id"`
	Value int    `json:"value"`
}

type StandaloneMigrationPreview struct {
	Schema               string                     `json:"schema"`
	SchemaVersion        int                        `json:"schema_version"`
	ContractVersion      string                     `json:"contract_version"`
	Status               string                     `json:"status"`
	Namespace            string                     `json:"namespace"`
	Kind                 string                     `json:"kind"`
	From                 int                        `json:"from"`
	To                   int                        `json:"to"`
	Privacy              string                     `json:"privacy"`
	SourceSHA256         string                     `json:"source_sha256"`
	CandidateSHA256      string                     `json:"candidate_sha256"`
	AdapterPreviewSHA256 string                     `json:"adapter_preview_sha256"`
	ImplementationSHA256 string                     `json:"implementation_sha256"`
	MigrationGraphSHA256 string                     `json:"migration_graph_sha256"`
	RegistrySHA256       string                     `json:"registry_sha256"`
	Counts               []StandaloneMigrationCount `json:"counts"`
	PreviewSHA256        string                     `json:"preview_sha256"`
}

type StandaloneMigrationResult struct {
	Schema               string `json:"schema"`
	SchemaVersion        int    `json:"schema_version"`
	ContractVersion      string `json:"contract_version"`
	Status               string `json:"status"`
	Namespace            string `json:"namespace"`
	Kind                 string `json:"kind"`
	From                 int    `json:"from"`
	To                   int    `json:"to"`
	SourceSHA256         string `json:"source_sha256"`
	CandidateSHA256      string `json:"candidate_sha256"`
	ImplementationSHA256 string `json:"implementation_sha256"`
	RegistrySHA256       string `json:"registry_sha256"`
	PreviewSHA256        string `json:"preview_sha256"`
}

type StandaloneMigrationPreviewOptions struct {
	Namespace      string
	Kind           string
	From           int
	To             int
	Root           string
	RepositoryRoot string
}

type StandaloneMigrationApplyOptions struct {
	StandaloneMigrationPreviewOptions
	ExpectedPreviewSHA256 string
	Confirm               string
}

func DecodeStandaloneSchemaRegistry(reader io.Reader) (StandaloneSchemaRegistry, error) {
	registry, err := schemaregistry.Decode(reader)
	if err != nil {
		return StandaloneSchemaRegistry{}, ErrStandaloneSchemaRegistry
	}
	return registry, nil
}

func EncodeStandaloneSchemaRegistry(registry StandaloneSchemaRegistry) ([]byte, error) {
	data, err := schemaregistry.Encode(registry)
	if err != nil {
		return nil, ErrStandaloneSchemaRegistry
	}
	return data, nil
}

func BuiltInStandaloneSchemaRegistry() (StandaloneSchemaRegistry, error) {
	registry, err := schemaregistry.BuiltIn()
	if err != nil {
		return StandaloneSchemaRegistry{}, ErrStandaloneSchemaRegistry
	}
	return registry, nil
}

func BuiltInStandaloneSchemaRegistryBytes() []byte {
	return schemaregistry.BuiltInBytes()
}

func InspectStandaloneSchema(namespace, kind string) (StandaloneSchemaInspection, error) {
	registry, err := schemaregistry.BuiltIn()
	if err != nil {
		return StandaloneSchemaInspection{}, ErrStandaloneSchemaRegistry
	}
	inspection, err := registry.Inspect(namespace, kind)
	if err != nil {
		return StandaloneSchemaInspection{}, ErrStandaloneSchemaRegistry
	}
	descriptor := inspection.Descriptor
	implementationDigests := make([]string, len(descriptor.MigrationEdges))
	for index, edge := range descriptor.MigrationEdges {
		implementationDigests[index] = schemaregistry.ImplementationSHA256(edge)
	}
	return StandaloneSchemaInspection{
		Schema: StandaloneSchemaRegistrySchema, SchemaVersion: StandaloneSchemaRegistryVersion,
		ContractVersion: StandaloneContractVersion, Namespace: descriptor.Namespace, Kind: descriptor.Kind,
		Owner: descriptor.Owner, Current: descriptor.Current, Readable: slices.Clone(descriptor.Readable),
		Emitted: slices.Clone(descriptor.Emitted), Executable: slices.Clone(descriptor.Executable),
		Disposition: descriptor.Disposition, Privacy: descriptor.Privacy, Migration: descriptor.Migration,
		MaxBytes: descriptor.MaxBytes, SchemaResource: descriptor.SchemaResource, SchemaSHA256: inspection.SchemaSHA256,
		MigrationGraphSHA256: inspection.MigrationSHA256, RegistrySHA256: inspection.RegistrySHA256,
		SupportedMigrations: inspection.SupportedMigrations, MigrationUnavailable: inspection.MigrationUnavailable,
		MigrationEdges: slices.Clone(descriptor.MigrationEdges), ImplementationSHA256s: implementationDigests,
	}, nil
}

func PreviewStandaloneSchemaMigration(options StandaloneMigrationPreviewOptions) (StandaloneMigrationPreview, error) {
	registry, edge, inspection, err := resolveStandaloneMigration(options.Namespace, options.Kind, options.From, options.To)
	if err != nil {
		return StandaloneMigrationPreview{}, err
	}
	_ = registry
	if edge.ID != "atl-profile/private-workspace/v3-to-v4" {
		return StandaloneMigrationPreview{}, standaloneMigrationError("implementation_unavailable")
	}
	privatePreview, err := PreviewPrivateWorkspaceMigration(options.Root, options.RepositoryRoot)
	if err != nil {
		return StandaloneMigrationPreview{}, standaloneMigrationError("preview", err)
	}
	return buildStandaloneMigrationPreview(privatePreview, edge, inspection)
}

func ApplyStandaloneSchemaMigration(options StandaloneMigrationApplyOptions) (StandaloneMigrationResult, error) {
	if options.Confirm != StandaloneMigrationConfirmation || !validSHA256(options.ExpectedPreviewSHA256) {
		return StandaloneMigrationResult{}, standaloneMigrationError("confirmation")
	}
	preview, completed, err := loadStandaloneMigrationPreviewForApply(options.StandaloneMigrationPreviewOptions)
	if err != nil {
		return StandaloneMigrationResult{}, err
	}
	if !constantTimeStringEqual(preview.PreviewSHA256, options.ExpectedPreviewSHA256) {
		return StandaloneMigrationResult{}, standaloneMigrationError("reviewed_digest")
	}
	status := "already_applied"
	if !completed {
		summary, err := ApplyPrivateWorkspaceMigration(PrivateWorkspaceMigrationOptions{
			Root: options.Root, RepositoryRoot: options.RepositoryRoot,
			ExpectedMigrationSHA256: preview.AdapterPreviewSHA256, Confirm: PrivateWorkspaceMigrationConfirmation,
		})
		if err != nil {
			return StandaloneMigrationResult{}, standaloneMigrationError("apply", err)
		}
		status = summary.Status
	}
	result := StandaloneMigrationResult{
		Schema: StandaloneMigrationResultSchema, SchemaVersion: StandaloneMigrationArtifactVersion,
		ContractVersion: StandaloneContractVersion, Status: status, Namespace: preview.Namespace, Kind: preview.Kind,
		From: preview.From, To: preview.To, SourceSHA256: preview.SourceSHA256, CandidateSHA256: preview.CandidateSHA256,
		ImplementationSHA256: preview.ImplementationSHA256, RegistrySHA256: preview.RegistrySHA256,
		PreviewSHA256: preview.PreviewSHA256,
	}
	if _, err := EncodeStandaloneMigrationResult(result); err != nil {
		return StandaloneMigrationResult{}, err
	}
	result, err = preserveStandaloneMigrationResult(options.Root, result)
	if err != nil {
		return StandaloneMigrationResult{}, err
	}
	return result, nil
}

func EncodeStandaloneMigrationPreview(preview StandaloneMigrationPreview) ([]byte, error) {
	if validateStandaloneMigrationPreview(preview) != nil {
		return nil, ErrStandaloneMigration
	}
	data, err := json.Marshal(preview)
	if err != nil || len(data)+1 > StandaloneMigrationArtifactMaxBytes {
		return nil, ErrStandaloneMigration
	}
	return append(data, '\n'), nil
}

func DecodeStandaloneMigrationPreview(reader io.Reader) (StandaloneMigrationPreview, error) {
	var preview StandaloneMigrationPreview
	if decodeStandaloneMigrationArtifact(reader, &preview) != nil || validateStandaloneMigrationPreview(preview) != nil {
		return StandaloneMigrationPreview{}, ErrStandaloneMigration
	}
	return preview, nil
}

func EncodeStandaloneMigrationResult(result StandaloneMigrationResult) ([]byte, error) {
	if validateStandaloneMigrationResult(result) != nil {
		return nil, ErrStandaloneMigration
	}
	data, err := json.Marshal(result)
	if err != nil || len(data)+1 > StandaloneMigrationArtifactMaxBytes {
		return nil, ErrStandaloneMigration
	}
	return append(data, '\n'), nil
}

func DecodeStandaloneMigrationResult(reader io.Reader) (StandaloneMigrationResult, error) {
	var result StandaloneMigrationResult
	if decodeStandaloneMigrationArtifact(reader, &result) != nil || validateStandaloneMigrationResult(result) != nil {
		return StandaloneMigrationResult{}, ErrStandaloneMigration
	}
	return result, nil
}

func resolveStandaloneMigration(namespace, kind string, from, to int) (schemaregistry.Registry, schemaregistry.MigrationEdge, schemaregistry.Inspection, error) {
	registry, err := schemaregistry.BuiltIn()
	if err != nil {
		return schemaregistry.Registry{}, schemaregistry.MigrationEdge{}, schemaregistry.Inspection{}, standaloneMigrationError("registry", err)
	}
	path, err := registry.MigrationPath(namespace, kind, from, to)
	if err != nil || len(path) != 1 {
		return schemaregistry.Registry{}, schemaregistry.MigrationEdge{}, schemaregistry.Inspection{}, standaloneMigrationError("path", err)
	}
	inspection, err := registry.Inspect(namespace, kind)
	if err != nil {
		return schemaregistry.Registry{}, schemaregistry.MigrationEdge{}, schemaregistry.Inspection{}, standaloneMigrationError("registry", err)
	}
	return registry, path[0], inspection, nil
}

func buildStandaloneMigrationPreview(privatePreview PrivateWorkspaceMigrationPreview, edge schemaregistry.MigrationEdge, inspection schemaregistry.Inspection) (StandaloneMigrationPreview, error) {
	preview := StandaloneMigrationPreview{
		Schema: StandaloneMigrationPreviewSchema, SchemaVersion: StandaloneMigrationArtifactVersion,
		ContractVersion: StandaloneContractVersion, Status: privatePreview.Status,
		Namespace: "atl-profile", Kind: "private-workspace", From: edge.From, To: edge.To, Privacy: "owner_private",
		SourceSHA256: privatePreview.SourceSHA256, CandidateSHA256: privatePreview.CandidateSHA256,
		AdapterPreviewSHA256: privatePreview.MigrationSHA256,
		ImplementationSHA256: schemaregistry.ImplementationSHA256(edge),
		MigrationGraphSHA256: inspection.MigrationSHA256, RegistrySHA256: inspection.RegistrySHA256,
		Counts: []StandaloneMigrationCount{
			{ID: "preserved_run_records", Value: privatePreview.PreservedRunRecords},
			{ID: "preserved_run_sets", Value: privatePreview.PreservedRunSets},
			{ID: "preserved_spec_references", Value: privatePreview.PreservedSpecRefs},
		},
	}
	preview.PreviewSHA256 = standaloneMigrationPreviewSHA256(preview)
	if validateStandaloneMigrationPreview(preview) != nil {
		return StandaloneMigrationPreview{}, standaloneMigrationError("preview_invalid")
	}
	return preview, nil
}

func standaloneMigrationPreviewSHA256(preview StandaloneMigrationPreview) string {
	preview.Status = ""
	preview.PreviewSHA256 = ""
	data, _ := json.Marshal(preview)
	digest := sha256.Sum256(append([]byte(standaloneMigrationPreviewHashDomain+"\x00"), data...))
	return hex.EncodeToString(digest[:])
}

func validateStandaloneMigrationPreview(preview StandaloneMigrationPreview) error {
	if preview.Schema != StandaloneMigrationPreviewSchema || preview.SchemaVersion != StandaloneMigrationArtifactVersion ||
		preview.ContractVersion != StandaloneContractVersion || (preview.Status != "ready" && preview.Status != "recoverable" && preview.Status != "completed") ||
		preview.Namespace != "atl-profile" || preview.Kind != "private-workspace" || preview.From != 3 || preview.To != 4 || preview.Privacy != "owner_private" ||
		!validSHA256(preview.SourceSHA256) || !validSHA256(preview.CandidateSHA256) || !validSHA256(preview.AdapterPreviewSHA256) ||
		!validSHA256(preview.ImplementationSHA256) || !validSHA256(preview.MigrationGraphSHA256) || !validSHA256(preview.RegistrySHA256) ||
		!validSHA256(preview.PreviewSHA256) || !constantTimeStringEqual(preview.PreviewSHA256, standaloneMigrationPreviewSHA256(preview)) ||
		len(preview.Counts) != 3 {
		return ErrStandaloneMigration
	}
	for index, count := range preview.Counts {
		want := []string{"preserved_run_records", "preserved_run_sets", "preserved_spec_references"}[index]
		if count.ID != want || count.Value < 0 {
			return ErrStandaloneMigration
		}
	}
	return nil
}

func validateStandaloneMigrationResult(result StandaloneMigrationResult) error {
	if result.Schema != StandaloneMigrationResultSchema || result.SchemaVersion != StandaloneMigrationArtifactVersion ||
		result.ContractVersion != StandaloneContractVersion ||
		(result.Status != "migrated" && result.Status != "recovered" && result.Status != "already_applied") ||
		result.Namespace != "atl-profile" || result.Kind != "private-workspace" || result.From != 3 || result.To != 4 ||
		!validSHA256(result.SourceSHA256) || !validSHA256(result.CandidateSHA256) || !validSHA256(result.ImplementationSHA256) ||
		!validSHA256(result.RegistrySHA256) || !validSHA256(result.PreviewSHA256) {
		return ErrStandaloneMigration
	}
	return nil
}

func decodeStandaloneMigrationArtifact(reader io.Reader, target any) error {
	if reader == nil {
		return ErrStandaloneMigration
	}
	limited := &io.LimitedReader{R: reader, N: StandaloneMigrationArtifactMaxBytes + 1}
	data, err := io.ReadAll(limited)
	if err != nil || limited.N == 0 || len(data) == 0 || validateJSONNoDuplicateKeys(data) != nil || decodeStrictJSONObject(data, target) != nil {
		return ErrStandaloneMigration
	}
	var canonical []byte
	switch value := target.(type) {
	case *StandaloneMigrationPreview:
		canonical, err = EncodeStandaloneMigrationPreview(*value)
	case *StandaloneMigrationResult:
		canonical, err = EncodeStandaloneMigrationResult(*value)
	default:
		return ErrStandaloneMigration
	}
	if err != nil || !bytes.Equal(data, canonical) {
		return ErrStandaloneMigration
	}
	return nil
}

func standaloneMigrationError(code string, causes ...error) error {
	return codedError(ErrStandaloneMigration, code, causes...)
}
