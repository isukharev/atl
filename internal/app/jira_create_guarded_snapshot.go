package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/backendid"
	"github.com/isukharev/atl/internal/config"
	"github.com/isukharev/atl/internal/domain"
)

func (s *JiraService) buildGuardedCreateSnapshot(ctx context.Context, port domain.JiraGuardedCreatePort, opts JiraGuardedCreateOpts) (*jiraGuardedCreateSnapshot, error) {
	project, err := s.qualifyGuardedCreateProject(ctx, opts.Project)
	if err != nil {
		return nil, err
	}
	reader, ok := s.tr.(domain.JiraQualifiedCreateMetadataReader)
	if !ok {
		return nil, fmt.Errorf("%w: qualified Jira create metadata is unavailable", domain.ErrConfig)
	}
	metadata, err := reader.ReadQualifiedCreateMetadata(ctx, project.Key, opts.IssueType)
	if err != nil {
		return nil, err
	}
	metadataDigest, schemas, err := qualifyGuardedCreateMetadata(metadata, project, opts)
	if err != nil {
		return nil, err
	}
	prepared, err := port.PrepareGuardedCreate(domain.JiraGuardedCreatePreparationRequest{
		ProjectKey: project.Key, IssueTypeID: metadata.IssueType.ID, Summary: opts.Summary,
		Description: opts.Description, DescriptionPresent: len(opts.Description) > 0, Fields: opts.Fields,
	})
	if err != nil {
		return nil, err
	}
	if len(prepared.Payload) == 0 || len(prepared.Payload) > jiraGuardedCreateMaxPayloadBytes || !json.Valid(prepared.Payload) {
		return nil, fmt.Errorf("%w: Jira create preparer returned invalid payload bytes", domain.ErrCheckFailed)
	}
	fields, err := guardedCreateProposalFields(prepared, opts.Fields, schemas)
	if err != nil {
		return nil, err
	}
	backendHash, err := backendid.OriginSHA256(s.baseURL)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid Jira backend identity", domain.ErrCheckFailed)
	}
	result := newGuardedCreateResult(opts)
	result.BackendSHA256 = backendHash
	result.Project = JiraGuardedCreateProject{ID: project.ID, Key: project.Key, Archived: false}
	result.TypeSelector = guardedCreateDigest([]byte(opts.IssueType))
	result.IssueType = metadata.IssueType
	result.Summary = guardedCreateDigest([]byte(opts.Summary))
	result.Description = JiraGuardedCreateDescription{Source: opts.DescriptionSource, Present: len(opts.Description) > 0, Bytes: len(opts.Description)}
	if len(opts.Description) > 0 {
		result.Description.SHA256 = sha256Hex(opts.Description)
	}
	result.Fields = fields
	result.MetadataCount = len(metadata.Fields)
	result.MetadataSHA256 = metadataDigest
	result.RequestSHA256 = sha256Hex(prepared.Payload)
	result.RequestBytes = len(prepared.Payload)

	render := RenderSettings{}
	if opts.Register {
		root, rootErr := createdRegistrationRoot(opts.Into)
		if rootErr != nil {
			return nil, rootErr
		}
		result.RegistrationRootSHA256 = sha256Hex([]byte(root))
		render, err = s.resolveGuardedCreateRender(ctx, root)
		if err != nil {
			return nil, err
		}
		renderBytes, marshalErr := json.Marshal(render)
		if marshalErr != nil {
			return nil, marshalErr
		}
		result.RenderProjectionSHA256 = sha256Hex(renderBytes)
	}
	readFields := guardedCreateReadbackProjection(opts.Fields, render, opts.Register)
	if err := validateGuardedCreateReadbackProjection(readFields); err != nil {
		return nil, err
	}
	result.ProposalHash = guardedCreateProposalHash(result)
	return &jiraGuardedCreateSnapshot{result: result, prepared: cloneGuardedPreparation(prepared), project: project, metadata: metadata, render: render, readFields: readFields}, nil
}

func (s *JiraService) qualifyGuardedCreateProject(ctx context.Context, selector string) (domain.JiraProject, error) {
	reader, ok := s.tr.(domain.JiraProjectReader)
	if !ok {
		return domain.JiraProject{}, fmt.Errorf("%w: complete Jira project inventory is unavailable", domain.ErrConfig)
	}
	projects, err := reader.ReadProjects(ctx, true)
	if err != nil {
		return domain.JiraProject{}, err
	}
	if len(projects) == 0 || len(projects) > jiraGuardedCreateMaxInventoryRows {
		return domain.JiraProject{}, fmt.Errorf("%w: Jira project inventory is empty or exceeds 1000 rows", domain.ErrCheckFailed)
	}
	seenID, seenKey := map[string]bool{}, map[string]bool{}
	var match *domain.JiraProject
	for index := range projects {
		project := projects[index]
		if !guardedCreatePositiveID(project.ID) || !guardedCreateProjectKey(project.Key) || project.Name == "" || !guardedCreateMetadataString(project.Name) || project.Archived == nil || seenID[project.ID] || seenKey[project.Key] {
			return domain.JiraProject{}, fmt.Errorf("%w: Jira project inventory is malformed, incomplete, or duplicate", domain.ErrCheckFailed)
		}
		seenID[project.ID], seenKey[project.Key] = true, true
		if project.Key == selector {
			if match != nil {
				return domain.JiraProject{}, fmt.Errorf("%w: Jira project selector is ambiguous", domain.ErrCheckFailed)
			}
			copy := project
			match = &copy
		}
	}
	if match == nil {
		return domain.JiraProject{}, fmt.Errorf("%w: Jira project was not found", domain.ErrNotFound)
	}
	if *match.Archived {
		return domain.JiraProject{}, fmt.Errorf("%w: Jira project is archived", domain.ErrCheckFailed)
	}
	return *match, nil
}

type guardedCreateMetadataHashField struct {
	ID                   string                       `json:"id"`
	Name                 string                       `json:"name"`
	Required             bool                         `json:"required"`
	Schema               domain.JiraCreateFieldSchema `json:"schema"`
	HasDefault           bool                         `json:"has_default"`
	AllowedValuesPresent bool                         `json:"allowed_values_present"`
	AllowedValuesCount   int                          `json:"allowed_values_count"`
	AutocompletePresent  bool                         `json:"autocomplete_present"`
	HasAutocomplete      bool                         `json:"has_autocomplete"`
}

func qualifyGuardedCreateMetadata(metadata *domain.JiraQualifiedCreateMetadata, project domain.JiraProject, opts JiraGuardedCreateOpts) (string, map[string]string, error) {
	if metadata == nil || metadata.Project != project.Key || !guardedCreatePositiveID(metadata.IssueType.ID) || !guardedCreateMetadataString(metadata.IssueType.Name) || len(metadata.Fields) == 0 || len(metadata.Fields) > jiraGuardedCreateMaxInventoryRows {
		return "", nil, fmt.Errorf("%w: Jira create metadata identity is incomplete or oversized", domain.ErrCheckFailed)
	}
	seen := map[string]bool{}
	rows := make([]guardedCreateMetadataHashField, 0, len(metadata.Fields))
	schemas := make(map[string]string, len(metadata.Fields))
	provided := map[string]bool{"project": true, "issuetype": true, "summary": true}
	if len(opts.Description) > 0 {
		provided["description"] = true
	}
	for key := range opts.Fields {
		provided[key] = true
	}
	for _, field := range metadata.Fields {
		if field.FieldID == "" || !guardedCreateMetadataString(field.FieldID) || field.Name == "" || !guardedCreateMetadataString(field.Name) || seen[field.FieldID] || field.Required == nil || field.Schema == nil || field.HasDefaultValue == nil || field.AllowedValuesCount < 0 || field.AllowedValuesCount > jiraGuardedCreateMaxInventoryRows || field.HasAutocomplete && !field.AutocompletePresent {
			return "", nil, fmt.Errorf("%w: Jira create-screen metadata is malformed, incomplete, duplicate, or oversized", domain.ErrCheckFailed)
		}
		if !guardedCreateMetadataString(field.Schema.Type) || field.Schema.Items != "" && !guardedCreateMetadataString(field.Schema.Items) || field.Schema.System != "" && !guardedCreateMetadataString(field.Schema.System) || field.Schema.Custom != "" && !guardedCreateMetadataString(field.Schema.Custom) || field.Schema.CustomID != nil && *field.Schema.CustomID <= 0 {
			return "", nil, fmt.Errorf("%w: Jira create-screen schema is malformed or oversized", domain.ErrCheckFailed)
		}
		seen[field.FieldID] = true
		schemaBytes, _ := json.Marshal(field.Schema)
		schemas[field.FieldID] = sha256Hex(schemaBytes)
		rows = append(rows, guardedCreateMetadataHashField{
			ID: field.FieldID, Name: field.Name, Required: *field.Required, Schema: *field.Schema,
			HasDefault: *field.HasDefaultValue, AllowedValuesPresent: field.AllowedValuesPresent,
			AllowedValuesCount: field.AllowedValuesCount, AutocompletePresent: field.AutocompletePresent,
			HasAutocomplete: field.HasAutocomplete,
		})
		if *field.Required && !*field.HasDefaultValue && !provided[field.FieldID] {
			return "", nil, fmt.Errorf("%w: a required Jira create-screen field was omitted", domain.ErrCheckFailed)
		}
	}
	for field := range provided {
		if !seen[field] {
			return "", nil, fmt.Errorf("%w: a supplied Jira create field is not on the qualified create screen", domain.ErrCheckFailed)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	encoded, _ := json.Marshal(rows)
	return sha256Hex(encoded), schemas, nil
}

func guardedCreateProposalFields(prepared domain.JiraGuardedCreatePreparation, inputs map[string]domain.JiraFieldInput, schemas map[string]string) ([]JiraGuardedCreateField, error) {
	if len(prepared.Fields) != len(inputs) {
		return nil, fmt.Errorf("%w: Jira create preparation projection is incomplete", domain.ErrCheckFailed)
	}
	seen := map[string]bool{}
	out := make([]JiraGuardedCreateField, 0, len(prepared.Fields))
	for _, field := range prepared.Fields {
		if seen[field.FieldID] || schemas[field.FieldID] == "" || field.Bytes <= 0 || !guardedCreateSHA256(field.SHA256) || field.JSONKind == "unknown" || field.InputKind != "legacy" && field.InputKind != "explicit_json" {
			return nil, fmt.Errorf("%w: Jira create preparation projection is malformed", domain.ErrCheckFailed)
		}
		seen[field.FieldID] = true
		out = append(out, JiraGuardedCreateField{
			FieldID: field.FieldID, InputKind: field.InputKind, JSONKind: field.JSONKind,
			NormalizedSHA: field.SHA256, NormalizedBytes: field.Bytes, SchemaSHA256: schemas[field.FieldID],
		})
	}
	for key := range inputs {
		if !seen[key] {
			return nil, fmt.Errorf("%w: Jira create preparation omitted a supplied field", domain.ErrCheckFailed)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FieldID < out[j].FieldID })
	return out, nil
}

func (s *JiraService) resolveGuardedCreateRender(ctx context.Context, root string) (RenderSettings, error) {
	settings, _ := ResolveRender(s.cfg, root, config.RenderService{}, "jira")
	selectors := append([]string(nil), settings.CustomFields...)
	for _, view := range settings.FieldViews {
		selectors = append(selectors, view.ID)
	}
	if len(selectors) == 0 {
		return settings, nil
	}
	if _, ok := jiraTechnicalFieldDefs(selectors); ok {
		return settings, nil
	}
	reader, ok := s.tr.(domain.QualifiedFieldCatalogReader)
	if !ok {
		return RenderSettings{}, fmt.Errorf("%w: complete Jira field catalog is unavailable", domain.ErrConfig)
	}
	catalog, err := reader.ReadFieldCatalog(ctx)
	if err != nil {
		return RenderSettings{}, err
	}
	if !catalog.Complete || catalog.PartialReason != "" || len(catalog.Fields) == 0 || len(catalog.Fields) > jiraGuardedCreateMaxInventoryRows {
		return RenderSettings{}, fmt.Errorf("%w: Jira field catalog is incomplete or oversized", domain.ErrCheckFailed)
	}
	seen := map[string]bool{}
	for _, field := range catalog.Fields {
		if field.ID == "" || field.Name == "" || !guardedCreateMetadataString(field.ID) || !guardedCreateMetadataString(field.Name) || field.Schema != "" && !guardedCreateMetadataString(field.Schema) || seen[field.ID] {
			return RenderSettings{}, fmt.Errorf("%w: Jira field catalog is malformed or duplicate", domain.ErrCheckFailed)
		}
		seen[field.ID] = true
	}
	return resolveRenderFieldSelectorsFromCatalog(settings, catalog.Fields)
}

func guardedCreateReadbackProjection(inputs map[string]domain.JiraFieldInput, settings RenderSettings, register bool) []string {
	fields := make([]string, 0, len(inputs))
	for field := range inputs {
		fields = append(fields, field)
	}
	if register {
		fields = jiraPullFields(fields, settings)
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(fields)+6)
	for _, field := range append([]string{"project", "issuetype", "summary", "description", "created", "updated"}, fields...) {
		if !seen[field] {
			seen[field] = true
			out = append(out, field)
		}
	}
	sort.Strings(out)
	return out
}

func validateGuardedCreateReadbackProjection(fields []string) error {
	if len(fields) > jiraGuardedCreateMaxReadbackFields {
		return fmt.Errorf("%w: guarded Jira create readback exceeds 1024 fields", domain.ErrCheckFailed)
	}
	for _, field := range fields {
		if field == "" || len(field) > jiraGuardedCreateMaxStringBytes || !utf8.ValidString(field) || strings.ContainsAny(field, ",\x00\r\n") {
			return fmt.Errorf("%w: guarded Jira create readback field is malformed", domain.ErrCheckFailed)
		}
	}
	query := url.Values{"fields": []string{strings.Join(fields, ",")}}.Encode()
	if len(query) > jiraGuardedCreateMaxReadbackQueryBytes || len("/rest/api/2/issue/"+strings.Repeat("9", 64)+"?"+query) > jiraGuardedCreateMaxReadbackQueryBytes {
		return fmt.Errorf("%w: guarded Jira create readback query exceeds 64 KiB", domain.ErrCheckFailed)
	}
	return nil
}

func guardedCreateProposalHash(result *JiraGuardedCreateResult) string {
	type proposalBounds struct {
		MaxFields             int    `json:"max_fields"`
		MaxInventoryRows      int    `json:"max_inventory_rows"`
		MaxStringBytes        int    `json:"max_string_bytes"`
		MaxPayloadBytes       int    `json:"max_payload_bytes"`
		MaxReadbackFields     int    `json:"max_readback_fields"`
		MaxReadbackQueryBytes int    `json:"max_readback_query_bytes"`
		RequestMaxima         [4]int `json:"request_maxima"`
		MaxResponseBytes      int64  `json:"max_response_bytes"`
		DeadlineMillis        int64  `json:"deadline_millis"`
	}
	projection := struct {
		SchemaVersion          int                          `json:"schema_version"`
		Operation              string                       `json:"operation"`
		BackendSHA256          string                       `json:"backend_sha256"`
		RequestedProject       string                       `json:"requested_project"`
		Project                JiraGuardedCreateProject     `json:"project"`
		TypeSelector           JiraGuardedCreateDigest      `json:"type_selector"`
		IssueType              domain.JiraIssueType         `json:"issue_type"`
		Summary                JiraGuardedCreateDigest      `json:"summary"`
		Description            JiraGuardedCreateDescription `json:"description"`
		Fields                 []JiraGuardedCreateField     `json:"fields"`
		MetadataCount          int                          `json:"metadata_count"`
		MetadataSHA256         string                       `json:"metadata_sha256"`
		RequestSHA256          string                       `json:"request_sha256"`
		RequestBytes           int                          `json:"request_bytes"`
		RegistrationRequested  bool                         `json:"registration_requested"`
		RegistrationRootSHA256 string                       `json:"registration_root_sha256,omitempty"`
		RenderSHA256           string                       `json:"render_projection_sha256,omitempty"`
		Bounds                 proposalBounds               `json:"bounds"`
	}{
		result.SchemaVersion, result.Operation, result.BackendSHA256, result.RequestedProject,
		result.Project, result.TypeSelector, result.IssueType, result.Summary, result.Description,
		result.Fields, result.MetadataCount, result.MetadataSHA256, result.RequestSHA256,
		result.RequestBytes, result.RegistrationRequested, result.RegistrationRootSHA256,
		result.RenderProjectionSHA256, proposalBounds{
			MaxFields: result.Bounds.MaxFields, MaxInventoryRows: result.Bounds.MaxInventoryRows,
			MaxStringBytes: result.Bounds.MaxStringBytes, MaxPayloadBytes: result.Bounds.MaxPayloadBytes,
			MaxReadbackFields: result.Bounds.MaxReadbackFields, MaxReadbackQueryBytes: result.Bounds.MaxReadbackQueryBytes,
			RequestMaxima:    [4]int{jiraGuardedCreatePreviewRequests, jiraGuardedCreatePreviewRegisterRequests, jiraGuardedCreateApplyRequests, jiraGuardedCreateApplyRegisterRequests},
			MaxResponseBytes: result.Bounds.MaxResponseBytes, DeadlineMillis: result.Bounds.DeadlineMillis,
		},
	}
	canonical, _ := json.Marshal(projection)
	return guardedProposalDigest(canonical)
}

func guardedCreateDigest(value []byte) JiraGuardedCreateDigest {
	return JiraGuardedCreateDigest{SHA256: sha256Hex(value), Bytes: len(value)}
}

func sha256Hex(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func guardedCreateSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}

func guardedCreatePositiveID(value string) bool {
	return len(value) <= jiraGuardedCreateMaxStringBytes && domain.ValidConfluenceContentID(value)
}

func guardedCreateProjectKey(value string) bool {
	return len(value) <= jiraGuardedCreateMaxStringBytes && domain.ValidJiraIssueKey(value+"-1")
}

func guardedCreateMetadataString(value string) bool {
	return value != "" && len(value) <= jiraGuardedCreateMaxStringBytes && utf8.ValidString(value)
}

func cloneGuardedPreparation(value domain.JiraGuardedCreatePreparation) domain.JiraGuardedCreatePreparation {
	return domain.JiraGuardedCreatePreparation{Payload: append([]byte(nil), value.Payload...), Fields: append([]domain.JiraGuardedCreatePreparedField(nil), value.Fields...)}
}
