package agenteval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/agenteval/lifecycle"
)

const (
	standaloneProductContractFixturePath = "testdata/standalone-product-contract.v1.json"
	standaloneConformanceFixturePath     = "testdata/standalone-conformance.v1.json"
)

type standaloneProductContractFixture struct {
	SchemaVersion        int                           `json:"schema_version"`
	ContractVersion      string                        `json:"contract_version"`
	Status               string                        `json:"status"`
	CompatibilityBegins  string                        `json:"compatibility_begins"`
	MaintainerCommands   []string                      `json:"maintainer_commands"`
	StandaloneOperations []standaloneOperation         `json:"standalone_operations"`
	Configuration        standaloneConfiguration       `json:"configuration"`
	Roles                []standaloneRole              `json:"roles"`
	CapabilityStates     []string                      `json:"capability_states"`
	MetricStates         []string                      `json:"metric_states"`
	ExitClasses          []standaloneExitClass         `json:"exit_classes"`
	ArtifactSchemas      []standaloneArtifactSchema    `json:"artifact_schemas"`
	ArtifactClasses      []standaloneArtifactClass     `json:"artifact_classes"`
	AttemptStates        []standaloneAttemptState      `json:"attempt_states"`
	AttemptProofs        []string                      `json:"attempt_proofs"`
	AttemptTransitions   []standaloneAttemptTransition `json:"attempt_transitions"`
	AttemptRecovery      standaloneAttemptRecovery     `json:"attempt_recovery"`
	CompatibilityPolicy  standaloneCompatibilityPolicy `json:"compatibility_policy"`
}

type standaloneOperation struct {
	ID                     string   `json:"id"`
	Mode                   string   `json:"mode"`
	CurrentStatus          string   `json:"current_status"`
	MaintainerAliases      []string `json:"maintainer_aliases"`
	StandaloneStatus       string   `json:"standalone_status"`
	Authority              string   `json:"authority"`
	LocalRead              *bool    `json:"local_read"`
	LocalWrite             *bool    `json:"local_write"`
	ProcessSpawn           *bool    `json:"process_spawn"`
	ProviderContact        *bool    `json:"provider_contact"`
	BackendContact         *bool    `json:"backend_contact"`
	Network                *bool    `json:"network"`
	CredentialAccess       *bool    `json:"credential_access"`
	PrivateWorkspaceAccess *bool    `json:"private_workspace_access"`
}

type standaloneConfiguration struct {
	Precedence       []string `json:"precedence"`
	UnknownKeys      string   `json:"unknown_keys"`
	AmbientAuthority []string `json:"ambient_authority"`
}

type standaloneRole struct {
	ID         string   `json:"id"`
	Owns       []string `json:"owns"`
	DoesNotOwn []string `json:"does_not_own"`
}

type standaloneExitClass struct {
	Code *int   `json:"code"`
	ID   string `json:"id"`
}

type standaloneArtifactSchema struct {
	Namespace   string `json:"namespace"`
	Kind        string `json:"kind"`
	Current     int    `json:"current"`
	Readable    []int  `json:"readable"`
	Emitted     []int  `json:"emitted"`
	Executable  []int  `json:"executable"`
	Disposition string `json:"disposition"`
	Privacy     string `json:"privacy"`
	Migration   string `json:"migration"`
	MaxBytes    *int64 `json:"max_bytes"`
}

type standaloneArtifactClass struct {
	ID          string `json:"id"`
	Privacy     string `json:"privacy"`
	Disposition string `json:"disposition"`
}

type standaloneAttemptState struct {
	ID                    string   `json:"id"`
	Phase                 string   `json:"phase"`
	Terminal              *bool    `json:"terminal"`
	AutomaticResume       *bool    `json:"automatic_resume"`
	AutomaticResumeProofs []string `json:"automatic_resume_proofs"`
}

type standaloneAttemptTransition struct {
	From      string     `json:"from"`
	To        string     `json:"to"`
	ProofSets [][]string `json:"proof_sets"`
}

type standaloneAttemptRecovery struct {
	UnknownMutable     *bool  `json:"unknown_mutable"`
	SameIdentityReplay *bool  `json:"same_identity_replay"`
	ReconcileMode      string `json:"reconcile_mode"`
}

type standaloneCompatibilityPolicy struct {
	StrictUnknownFields              *bool `json:"strict_unknown_fields"`
	AdditiveMembersRequireSchemaBump *bool `json:"additive_members_require_schema_bump"`
	SourceBytesPreserved             *bool `json:"source_bytes_preserved"`
	MigrationRequiresReviewedPreview *bool `json:"migration_requires_reviewed_preview"`
	MinimumDeprecationReleases       *int  `json:"minimum_deprecation_releases"`
	MinimumDeprecationDays           *int  `json:"minimum_deprecation_days"`
	RootModuleLinkForbidden          *bool `json:"root_module_link_forbidden"`
}

type standaloneConformanceFixture struct {
	SchemaVersion    int                           `json:"schema_version"`
	ContractVersion  string                        `json:"contract_version"`
	GoldenBundle     standaloneGoldenBundle        `json:"golden_bundle"`
	Readability      []standaloneReadabilityVector `json:"readability"`
	ForwardRejection []standaloneForwardVector     `json:"forward_rejection"`
	MetricVectors    []standaloneMetricVector      `json:"metric_vectors"`
}

type standaloneGoldenBundle struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type standaloneReadabilityVector struct {
	Namespace string `json:"namespace"`
	Kind      string `json:"kind"`
	Versions  []int  `json:"versions"`
}

type standaloneForwardVector struct {
	Namespace string `json:"namespace"`
	Kind      string `json:"kind"`
	Version   int    `json:"version"`
}

type standaloneMetricVector struct {
	ID             string `json:"id"`
	Representation string `json:"representation"`
	Present        *bool  `json:"present"`
	Required       *bool  `json:"required"`
	State          string `json:"state,omitempty"`
	Coverage       *bool  `json:"coverage,omitempty"`
	Value          *int64 `json:"value,omitempty"`
	Valid          *bool  `json:"valid"`
}

type standaloneArtifactCompatibility struct {
	current    int
	readable   []int
	emitted    []int
	executable []int
}

type standaloneArtifactPolicy struct {
	disposition string
	privacy     string
	migration   string
	maxBytes    int64
}

func TestStandaloneProductContractV1IsClosedAndSelfConsistent(t *testing.T) {
	data := standaloneReadFixture(t, standaloneProductContractFixturePath)
	contract, err := decodeStandaloneProductContractFixture(data)
	if err != nil {
		t.Fatal(err)
	}

	mutations := map[string][]byte{
		"unknown top-level member": bytes.Replace(data, []byte("{"), []byte(`{"unknown":true,`), 1),
		"duplicate member":         bytes.Replace(data, []byte(`"schema_version": 1`), []byte(`"schema_version": 1, "schema_version": 1`), 1),
		"trailing value":           append(slices.Clone(data), []byte(` {}`)...),
	}
	for name, mutation := range mutations {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeStandaloneProductContractFixture(mutation); err == nil {
				t.Fatal("non-canonical standalone product contract passed")
			}
		})
	}
	semanticMutations := []struct {
		name   string
		mutate func(*standaloneProductContractFixture)
	}{
		{name: "unknown operation status", mutate: func(value *standaloneProductContractFixture) {
			value.StandaloneOperations[0].CurrentStatus = "available"
		}},
		{name: "implemented operation left reserved", mutate: func(value *standaloneProductContractFixture) {
			value.StandaloneOperations[0].StandaloneStatus = "reserved"
		}},
		{name: "pre-release status without implementation", mutate: func(value *standaloneProductContractFixture) {
			value.StandaloneOperations[0].CurrentStatus = "maintainer_compat"
		}},
		{name: "unknown operation authority", mutate: func(value *standaloneProductContractFixture) { value.StandaloneOperations[0].Authority = "ambient" }},
		{name: "unknown capability state", mutate: func(value *standaloneProductContractFixture) { value.CapabilityStates[0] = "accepted" }},
		{name: "unknown metric state", mutate: func(value *standaloneProductContractFixture) { value.MetricStates[0] = "measured" }},
		{name: "unknown exit class", mutate: func(value *standaloneProductContractFixture) { value.ExitClasses[1].ID = "failure" }},
		{name: "unknown privacy class", mutate: func(value *standaloneProductContractFixture) { value.ArtifactSchemas[0].Privacy = "secret" }},
		{name: "unknown migration policy", mutate: func(value *standaloneProductContractFixture) { value.ArtifactSchemas[0].Migration = "automatic" }},
		{name: "unknown attempt phase", mutate: func(value *standaloneProductContractFixture) { value.AttemptStates[0].Phase = "maybe" }},
		{name: "unknown attempt proof", mutate: func(value *standaloneProductContractFixture) { value.AttemptTransitions[0].ProofSets[0][0] = "assumed" }},
		{name: "unknown attempt state", mutate: func(value *standaloneProductContractFixture) { value.AttemptTransitions[0].To = "missing" }},
	}
	for _, mutation := range semanticMutations {
		t.Run(mutation.name, func(t *testing.T) {
			value := loadStandaloneProductContractFixture(t)
			mutation.mutate(&value)
			data, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeStandaloneProductContractFixture(data); err == nil {
				t.Fatal("unknown standalone product-contract vocabulary passed")
			}
		})
	}

	wantRoles := []string{"agent-adapter", "atl-profile", "execution-backend", "grader", "reporter", "standalone-core"}
	gotRoles := make([]string, 0, len(contract.Roles))
	for _, role := range contract.Roles {
		gotRoles = append(gotRoles, role.ID)
	}
	if !slices.Equal(gotRoles, wantRoles) {
		t.Fatalf("standalone role ownership matrix=%v, want %v", gotRoles, wantRoles)
	}

	wantSchemas := map[string]standaloneArtifactCompatibility{
		standaloneContractKey("atl-profile", "activation-reference"):        {current: PrivateActivationReferenceSchemaVersion, readable: []int{LegacyPrivateActivationReferenceSchemaVersion, PrivateActivationReferenceSchemaVersion}, emitted: []int{PrivateActivationReferenceSchemaVersion}},
		standaloneContractKey("atl-profile", "activation-report"):           {current: PrivateActivationReportSchemaVersion, readable: []int{LegacyPrivateActivationReportSchemaVersion, PrivateActivationReportSchemaVersion}, emitted: []int{LegacyPrivateActivationReportSchemaVersion, PrivateActivationReportSchemaVersion}},
		standaloneContractKey("atl-profile", "aggregate"):                   {current: AggregateSchemaVersion, emitted: []int{AggregateSchemaVersion}},
		standaloneContractKey("atl-profile", "capability-catalog"):          {current: CapabilityCatalogSchemaVersion, readable: []int{CapabilityCatalogSchemaVersion}, emitted: []int{CapabilityCatalogSchemaVersion}, executable: []int{CapabilityCatalogSchemaVersion}},
		standaloneContractKey("atl-profile", "observation"):                 {current: ObservationSchemaVersion, readable: []int{ObservationSchemaVersion}, emitted: []int{ObservationSchemaVersion}, executable: []int{ObservationSchemaVersion}},
		standaloneContractKey("atl-profile", "private-plan"):                {current: PrivatePlanSchemaVersion, readable: []int{LegacyPrivatePlanSchemaVersion, LegacyPromptBoundPrivatePlanSchemaVersion, LegacyCompleteActivationPrivatePlanSchemaVersion, LegacyActivationStudyPrivatePlanSchemaVersion, LegacyCalibratedPrivatePlanSchemaVersion, LegacyToolQualifiedPrivatePlanSchemaVersion, LegacyExecutableReviewPrivatePlanSchemaVersion, LegacyLiveWritePrivatePlanSchemaVersion, PrivatePlanSchemaVersion}, emitted: []int{PrivatePlanSchemaVersion}, executable: []int{PrivatePlanSchemaVersion}},
		standaloneContractKey("atl-profile", "private-review-attempt"):      {current: privateReviewAttemptSchemaVersion, readable: []int{privateReviewLegacySchemaVersion, privateReviewAttemptSchemaVersion}, emitted: []int{privateReviewAttemptSchemaVersion}, executable: []int{privateReviewLegacySchemaVersion, privateReviewAttemptSchemaVersion}},
		standaloneContractKey("atl-profile", "private-review-receipt"):      {current: privateReviewReceiptSchemaVersion, readable: []int{privateReviewLegacySchemaVersion, privateReviewReceiptSchemaVersion}, emitted: []int{privateReviewReceiptSchemaVersion}, executable: []int{privateReviewLegacySchemaVersion, privateReviewReceiptSchemaVersion}},
		standaloneContractKey("atl-profile", "private-workspace"):           {current: PrivateWorkspaceSchemaVersion, readable: []int{LegacyPrivateWorkspaceSchemaVersion, LegacyActivationWorkspaceSchemaVersion, LegacyCalibratedWorkspaceSchemaVersion, PrivateWorkspaceSchemaVersion}, emitted: []int{PrivateWorkspaceSchemaVersion}, executable: []int{PrivateWorkspaceSchemaVersion}},
		standaloneContractKey("atl-profile", "qualitative-panel"):           {current: QualitativePanelSchemaVersion, readable: []int{QualitativePanelSchemaVersion}, emitted: []int{QualitativePanelSchemaVersion}, executable: []int{QualitativePanelSchemaVersion}},
		standaloneContractKey("atl-profile", "result"):                      {current: ResultSchemaVersion, readable: []int{LegacyResultSchemaVersion, PanelResultSchemaVersion, LegacyPromptBoundResultSchemaVersion, LegacyAttemptlessResultSchemaVersion, LegacyEvidenceResultSchemaVersion, ResultSchemaVersion}, emitted: []int{ResultSchemaVersion}, executable: []int{LegacyResultSchemaVersion, PanelResultSchemaVersion, LegacyPromptBoundResultSchemaVersion, LegacyAttemptlessResultSchemaVersion, LegacyEvidenceResultSchemaVersion, ResultSchemaVersion}},
		standaloneContractKey("atl-profile", "review"):                      {current: ReviewSchemaVersion, readable: []int{LegacyReviewSchemaVersion, ReviewSchemaVersion}, emitted: []int{ReviewSchemaVersion}, executable: []int{LegacyReviewSchemaVersion, ReviewSchemaVersion}},
		standaloneContractKey("atl-profile", "rubric"):                      {current: RubricSchemaVersion, readable: []int{RubricSchemaVersion}, executable: []int{RubricSchemaVersion}},
		standaloneContractKey("atl-profile", "run-spec"):                    {current: RunSpecSchemaVersion, readable: []int{LegacyPromptChannelRunSpecVersion, LegacyRunSpecSchemaVersion, RunSpecSchemaVersion}, emitted: []int{RunSpecSchemaVersion}, executable: []int{LegacyPromptChannelRunSpecVersion, LegacyRunSpecSchemaVersion, RunSpecSchemaVersion}},
		standaloneContractKey("atl-profile", "scenario"):                    {current: ScenarioSchemaVersion, readable: []int{ScenarioSchemaVersion}, emitted: []int{ScenarioSchemaVersion}, executable: []int{ScenarioSchemaVersion}},
		standaloneContractKey("atl-profile", "synthetic-root-aggregate"):    {current: SyntheticRootAggregateSchemaVersion, emitted: []int{SyntheticRootAggregateSchemaVersion}},
		standaloneContractKey("atl-profile", "synthetic-run-receipt"):       {current: SyntheticRunReceiptSchemaVersion, readable: []int{SyntheticRunReceiptLegacySchemaVersion, SyntheticRunReceiptSchemaVersion}, emitted: []int{SyntheticRunReceiptSchemaVersion}, executable: []int{SyntheticRunReceiptLegacySchemaVersion, SyntheticRunReceiptSchemaVersion}},
		standaloneContractKey("standalone", "adapter-manifest"):             {current: 1, readable: []int{1}, emitted: []int{1}, executable: []int{1}},
		standaloneContractKey("standalone", "adapter-message"):              {current: 1, readable: []int{1}, emitted: []int{1}, executable: []int{1}},
		standaloneContractKey("standalone", "agent-adapter-contract"):       {current: AgentAdapterSchemaVersion, readable: []int{AgentAdapterSchemaVersion}, emitted: []int{AgentAdapterSchemaVersion}, executable: []int{AgentAdapterSchemaVersion}},
		standaloneContractKey("standalone", "agent-observation"):            {current: AgentAdapterSchemaVersion, readable: []int{AgentAdapterSchemaVersion}, emitted: []int{AgentAdapterSchemaVersion}},
		standaloneContractKey("standalone", "attempt-event"):                {current: lifecycle.SchemaVersion, readable: []int{lifecycle.SchemaVersion}, emitted: []int{lifecycle.SchemaVersion}, executable: []int{lifecycle.SchemaVersion}},
		standaloneContractKey("standalone", "attempt-ledger"):               {current: lifecycle.SchemaVersion, readable: []int{lifecycle.SchemaVersion}, emitted: []int{lifecycle.SchemaVersion}, executable: []int{lifecycle.SchemaVersion}},
		standaloneContractKey("standalone", "attempt-plan"):                 {current: lifecycle.SchemaVersion, readable: []int{lifecycle.SchemaVersion}, emitted: []int{lifecycle.SchemaVersion}, executable: []int{lifecycle.SchemaVersion}},
		standaloneContractKey("standalone", "extension-conformance-bundle"): {current: 1, readable: []int{1}, emitted: []int{1}, executable: []int{1}},
		standaloneContractKey("standalone", "extension-conformance-report"): {current: 1, readable: []int{1}, emitted: []int{1}},
		standaloneContractKey("standalone", "migration-preview"):            {current: StandaloneMigrationArtifactVersion, readable: []int{StandaloneMigrationArtifactVersion}, emitted: []int{StandaloneMigrationArtifactVersion}},
		standaloneContractKey("standalone", "migration-result"):             {current: StandaloneMigrationArtifactVersion, readable: []int{StandaloneMigrationArtifactVersion}, emitted: []int{StandaloneMigrationArtifactVersion}},
		standaloneContractKey("standalone", "project-config"):               {current: StandaloneProjectConfigVersion, readable: []int{StandaloneProjectConfigVersion}, emitted: []int{StandaloneProjectConfigVersion}, executable: []int{StandaloneProjectConfigVersion}},
		standaloneContractKey("standalone", "schema-registry"):              {current: StandaloneSchemaRegistryVersion, readable: []int{StandaloneSchemaRegistryVersion}, emitted: []int{StandaloneSchemaRegistryVersion}, executable: []int{StandaloneSchemaRegistryVersion}},
	}
	wantSchemaPolicies := map[string]standaloneArtifactPolicy{
		standaloneContractKey("atl-profile", "activation-reference"):        {disposition: "preserve", privacy: "owner_private", migration: "compare_only", maxBytes: 1 << 20},
		standaloneContractKey("atl-profile", "activation-report"):           {disposition: "preserve", privacy: "content_minimized", migration: "compare_only", maxBytes: PrivateActivationReportMaxBytes},
		standaloneContractKey("atl-profile", "aggregate"):                   {disposition: "write_only_projection", privacy: "content_minimized", migration: "compare_only", maxBytes: 1 << 20},
		standaloneContractKey("atl-profile", "capability-catalog"):          {disposition: "preserve", privacy: "public", migration: "explicit", maxBytes: 1 << 20},
		standaloneContractKey("atl-profile", "observation"):                 {disposition: "preserve", privacy: "content_minimized", migration: "explicit", maxBytes: 1 << 20},
		standaloneContractKey("atl-profile", "private-plan"):                {disposition: "preserve", privacy: "owner_private", migration: "compare_only", maxBytes: 4 << 20},
		standaloneContractKey("atl-profile", "private-review-attempt"):      {disposition: "preserve", privacy: "owner_private", migration: "compare_only", maxBytes: maxReviewBytes},
		standaloneContractKey("atl-profile", "private-review-receipt"):      {disposition: "preserve", privacy: "owner_private", migration: "compare_only", maxBytes: maxReviewBytes},
		standaloneContractKey("atl-profile", "private-workspace"):           {disposition: "preserve", privacy: "owner_private", migration: "partial_explicit", maxBytes: 1 << 20},
		standaloneContractKey("atl-profile", "qualitative-panel"):           {disposition: "preserve", privacy: "owner_private", migration: "explicit", maxBytes: 1 << 20},
		standaloneContractKey("atl-profile", "result"):                      {disposition: "preserve", privacy: "content_minimized", migration: "explicit", maxBytes: 1 << 20},
		standaloneContractKey("atl-profile", "review"):                      {disposition: "preserve", privacy: "owner_private", migration: "explicit", maxBytes: 1 << 20},
		standaloneContractKey("atl-profile", "rubric"):                      {disposition: "preserve", privacy: "public_or_private", migration: "explicit", maxBytes: 1 << 20},
		standaloneContractKey("atl-profile", "run-spec"):                    {disposition: "preserve", privacy: "public_or_private", migration: "explicit", maxBytes: 1 << 20},
		standaloneContractKey("atl-profile", "scenario"):                    {disposition: "preserve", privacy: "public_or_private", migration: "explicit", maxBytes: 1 << 20},
		standaloneContractKey("atl-profile", "synthetic-root-aggregate"):    {disposition: "write_only_projection", privacy: "content_minimized", migration: "compare_only", maxBytes: 1 << 20},
		standaloneContractKey("atl-profile", "synthetic-run-receipt"):       {disposition: "preserve", privacy: "content_minimized", migration: "explicit", maxBytes: 16 << 10},
		standaloneContractKey("standalone", "adapter-manifest"):             {disposition: "preserve", privacy: "public", migration: "explicit", maxBytes: 64 << 10},
		standaloneContractKey("standalone", "adapter-message"):              {disposition: "preserve", privacy: "public_or_private", migration: "explicit", maxBytes: 1 << 20},
		standaloneContractKey("standalone", "agent-adapter-contract"):       {disposition: "preserve", privacy: "content_minimized", migration: "explicit", maxBytes: AgentAdapterContractMaxBytes},
		standaloneContractKey("standalone", "agent-observation"):            {disposition: "preserve", privacy: "content_minimized", migration: "explicit", maxBytes: AgentAdapterObservationMaxBytes},
		standaloneContractKey("standalone", "attempt-event"):                {disposition: "preserve", privacy: "content_minimized", migration: "explicit", maxBytes: lifecycle.MaxEventBytes},
		standaloneContractKey("standalone", "attempt-ledger"):               {disposition: "preserve", privacy: "content_minimized", migration: "explicit", maxBytes: lifecycle.MaxHeaderBytes},
		standaloneContractKey("standalone", "attempt-plan"):                 {disposition: "preserve", privacy: "content_minimized", migration: "explicit", maxBytes: lifecycle.MaxPlanBytes},
		standaloneContractKey("standalone", "extension-conformance-bundle"): {disposition: "preserve", privacy: "public", migration: "explicit", maxBytes: 1 << 20},
		standaloneContractKey("standalone", "extension-conformance-report"): {disposition: "preserve", privacy: "content_minimized", migration: "explicit", maxBytes: 1 << 20},
		standaloneContractKey("standalone", "migration-preview"):            {disposition: "preserve", privacy: "content_minimized", migration: "explicit", maxBytes: StandaloneMigrationArtifactMaxBytes},
		standaloneContractKey("standalone", "migration-result"):             {disposition: "preserve", privacy: "content_minimized", migration: "explicit", maxBytes: StandaloneMigrationArtifactMaxBytes},
		standaloneContractKey("standalone", "project-config"):               {disposition: "preserve", privacy: "public_or_private", migration: "explicit", maxBytes: StandaloneProjectConfigMaxBytes},
		standaloneContractKey("standalone", "schema-registry"):              {disposition: "preserve", privacy: "public", migration: "explicit", maxBytes: StandaloneSchemaRegistryMaxBytes},
	}
	for _, schema := range contract.ArtifactSchemas {
		key := standaloneContractKey(schema.Namespace, schema.Kind)
		want, ok := wantSchemas[key]
		if !ok {
			t.Fatalf("unclassified artifact schema %q", key)
		}
		if schema.Current != want.current || !slices.Equal(schema.Readable, want.readable) ||
			!slices.Equal(schema.Emitted, want.emitted) || !slices.Equal(schema.Executable, want.executable) {
			t.Fatalf("artifact schema %q compatibility=%+v, want %+v", schema.Kind, schema, want)
		}
		wantPolicy, ok := wantSchemaPolicies[key]
		if !ok || schema.MaxBytes == nil || schema.Disposition != wantPolicy.disposition || schema.Privacy != wantPolicy.privacy ||
			schema.Migration != wantPolicy.migration || *schema.MaxBytes != wantPolicy.maxBytes {
			t.Fatalf("artifact schema %q policy=%+v, want %+v", schema.Kind, schema, wantPolicy)
		}
		delete(wantSchemas, key)
		delete(wantSchemaPolicies, key)
	}
	if len(wantSchemas) != 0 {
		t.Fatalf("standalone contract omitted artifact schemas: %v", wantSchemas)
	}
	if len(wantSchemaPolicies) != 0 {
		t.Fatalf("standalone contract omitted artifact policies: %v", wantSchemaPolicies)
	}

	if want := []string{"not_applicable", "supported", "unknown", "unsupported"}; !slices.Equal(contract.CapabilityStates, want) {
		t.Fatalf("capability states=%v, want %v", contract.CapabilityStates, want)
	}
	if want := []string{"not_applicable", "observed", "unknown", "unsupported"}; !slices.Equal(contract.MetricStates, want) {
		t.Fatalf("metric states=%v, want %v", contract.MetricStates, want)
	}
	wantExitClasses := []standaloneExitClass{
		{Code: standaloneInt(0), ID: "success"},
		{Code: standaloneInt(1), ID: "internal_error"},
		{Code: standaloneInt(2), ID: "usage_error"},
		{Code: standaloneInt(3), ID: "configuration_error"},
		{Code: standaloneInt(4), ID: "input_error"},
		{Code: standaloneInt(5), ID: "compatibility_error"},
		{Code: standaloneInt(6), ID: "policy_denied"},
		{Code: standaloneInt(7), ID: "authentication_failed"},
		{Code: standaloneInt(8), ID: "execution_failed"},
		{Code: standaloneInt(9), ID: "check_failed"},
		{Code: standaloneInt(10), ID: "outcome_unknown"},
		{Code: standaloneInt(11), ID: "interrupted"},
	}
	for index, want := range wantExitClasses {
		if len(contract.ExitClasses) != len(wantExitClasses) || contract.ExitClasses[index].Code == nil ||
			*contract.ExitClasses[index].Code != *want.Code || contract.ExitClasses[index].ID != want.ID {
			t.Fatalf("exit class registry=%+v, want %+v", contract.ExitClasses, wantExitClasses)
		}
	}
	policy := contract.CompatibilityPolicy
	if !standaloneTrue(policy.StrictUnknownFields) || !standaloneTrue(policy.AdditiveMembersRequireSchemaBump) ||
		!standaloneTrue(policy.SourceBytesPreserved) || !standaloneTrue(policy.MigrationRequiresReviewedPreview) ||
		!standaloneTrue(policy.RootModuleLinkForbidden) || policy.MinimumDeprecationReleases == nil || *policy.MinimumDeprecationReleases != 2 ||
		policy.MinimumDeprecationDays == nil || *policy.MinimumDeprecationDays != 180 ||
		contract.CompatibilityBegins != "first-conforming-signed-standalone-release" {
		t.Fatalf("standalone compatibility policy is incomplete: %+v", policy)
	}
}

func TestStandaloneContractClassifiesCurrentCommandsAndArtifacts(t *testing.T) {
	contract := loadStandaloneProductContractFixture(t)
	commands := standaloneCoordinatorCommands(t, filepath.Join("cmd", "agent-eval", "main.go"))
	if len(commands) != 17 || !slices.Equal(commands, contract.MaintainerCommands) {
		t.Fatalf("coordinator commands=%v, contract=%v", commands, contract.MaintainerCommands)
	}

	operationByID := make(map[string]standaloneOperation, len(contract.StandaloneOperations))
	aliasOwners := make(map[string][]string, len(commands))
	for _, operation := range contract.StandaloneOperations {
		key := standaloneOperationKey(operation.ID, operation.Mode)
		operationByID[key] = operation
		for _, alias := range operation.MaintainerAliases {
			aliasOwners[alias] = append(aliasOwners[alias], key)
		}
	}
	for command, want := range map[string]struct{ current, standalone string }{
		"run":      {current: "maintainer_compat", standalone: "reserved"},
		"validate": {current: "implemented_pre_release", standalone: "pre_release"},
	} {
		operation, ok := operationByID[standaloneOperationKey(command, "default")]
		if !ok || operation.CurrentStatus != want.current || operation.StandaloneStatus != want.standalone {
			t.Fatalf("current command %q classification=%+v, want %+v", command, operation, want)
		}
	}
	wantAliasOwners := map[string][]string{
		"aggregate":                  {standaloneOperationKey("compare", "default")},
		"aggregate-root":             {standaloneOperationKey("compare", "default")},
		"assess":                     {standaloneOperationKey("grade", "deterministic")},
		"attempt-ledger":             {standaloneOperationKey("reconcile", "evidence-only")},
		"evaluate":                   {standaloneOperationKey("grade", "deterministic")},
		"inventory":                  {standaloneOperationKey("inspect", "default")},
		"private":                    {standaloneOperationKey("init", "default"), standaloneOperationKey("migrate apply", "default"), standaloneOperationKey("migrate preview", "default"), standaloneOperationKey("plan", "default"), standaloneOperationKey("resume", "default")},
		"review-template":            {standaloneOperationKey("report", "default")},
		"run":                        {standaloneOperationKey("run", "default")},
		"validate":                   {standaloneOperationKey("validate", "default")},
		"validate-comparison-set":    {standaloneOperationKey("compare", "default")},
		"validate-pair":              {standaloneOperationKey("compare", "default")},
		"validate-run":               {standaloneOperationKey("validate", "default")},
		"verify-atl-capabilities":    {standaloneOperationKey("compat verify", "provider-free")},
		"verify-codex-skill-package": {standaloneOperationKey("compat verify", "provider-free")},
	}
	for _, owners := range aliasOwners {
		slices.Sort(owners)
	}
	for _, owners := range wantAliasOwners {
		slices.Sort(owners)
	}
	if !standaloneStringSliceMapEqual(aliasOwners, wantAliasOwners) {
		t.Fatalf("maintainer alias routes=%v, want %v", aliasOwners, wantAliasOwners)
	}

	behavior := loadEvaluatorBehaviorContract(t)
	publicClasses := make(map[string]bool, len(behavior.Artifacts.PublicTrackedClasses))
	privateClasses := make(map[string]bool, len(behavior.Artifacts.PrivateOnlyClasses))
	wantClasses := make([]string, 0, len(behavior.Artifacts.PublicTrackedClasses)+len(behavior.Artifacts.PrivateOnlyClasses))
	for _, class := range behavior.Artifacts.PublicTrackedClasses {
		publicClasses[class.Name] = true
		wantClasses = append(wantClasses, class.Name)
	}
	for _, class := range behavior.Artifacts.PrivateOnlyClasses {
		privateClasses[class.Name] = true
		wantClasses = append(wantClasses, class.Name)
	}
	slices.Sort(wantClasses)
	gotClasses := make([]string, 0, len(contract.ArtifactClasses))
	for _, class := range contract.ArtifactClasses {
		gotClasses = append(gotClasses, class.ID)
		if publicClasses[class.ID] && class.Privacy == "owner_private" {
			t.Fatalf("public artifact class %q became owner-private", class.ID)
		}
		if privateClasses[class.ID] && class.Privacy != "owner_private" {
			t.Fatalf("private artifact class %q has privacy %q", class.ID, class.Privacy)
		}
	}
	if !slices.Equal(gotClasses, wantClasses) {
		t.Fatalf("artifact class inventory=%v, behavior contract=%v", gotClasses, wantClasses)
	}

	schemas := make(map[string]standaloneArtifactSchema, len(contract.ArtifactSchemas))
	for _, schema := range contract.ArtifactSchemas {
		if schema.Namespace != "atl-profile" {
			continue
		}
		schemas[schema.Kind] = schema
	}
	for _, behaviorSchema := range behavior.Schemas {
		kind := strings.ReplaceAll(behaviorSchema.Name, "_", "-")
		schema, ok := schemas[kind]
		if !ok || !slices.Equal(schema.Readable, behaviorSchema.Readable) ||
			!slices.Equal(schema.Emitted, behaviorSchema.Emitted) || !slices.Equal(schema.Executable, behaviorSchema.Executable) {
			t.Fatalf("behavior schema %q is not reconciled: behavior=%+v standalone=%+v", kind, behaviorSchema, schema)
		}
	}
}

func TestStandaloneContractCompatibilityVectors(t *testing.T) {
	contract := loadStandaloneProductContractFixture(t)
	conformance := loadStandaloneConformanceFixture(t)
	if conformance.ContractVersion != contract.ContractVersion {
		t.Fatalf("conformance contract_version=%q, product contract=%q", conformance.ContractVersion, contract.ContractVersion)
	}
	goldens := loadStandaloneReadabilityGoldenFixture(t, conformance.GoldenBundle)
	goldenByVersion := make(map[string]standaloneReadabilityGoldenEntry, len(goldens.Entries))
	for _, entry := range goldens.Entries {
		key := standaloneVersionedContractKey(entry.Namespace, entry.Kind, entry.Version)
		goldenByVersion[key] = entry
	}

	schemaByKey := make(map[string]standaloneArtifactSchema, len(contract.ArtifactSchemas))
	for _, schema := range contract.ArtifactSchemas {
		schemaByKey[standaloneContractKey(schema.Namespace, schema.Kind)] = schema
	}
	readabilityByKey := make(map[string][]int, len(conformance.Readability))
	for _, vector := range conformance.Readability {
		key := standaloneContractKey(vector.Namespace, vector.Kind)
		readabilityByKey[key] = vector.Versions
		schema, ok := schemaByKey[key]
		if !ok || !slices.Equal(vector.Versions, schema.Readable) {
			t.Fatalf("readability vector %q=%v, schema=%+v", key, vector.Versions, schema)
		}
		for _, version := range vector.Versions {
			goldenKey := standaloneVersionedContractKey(vector.Namespace, vector.Kind, version)
			entry, ok := goldenByVersion[goldenKey]
			if !ok {
				t.Fatalf("%s v%d has no checked-in readability golden", vector.Kind, version)
			}
			if err := standaloneValidateReadabilityGolden(t, entry); err != nil {
				t.Fatalf("%s v%d declared readable but rejected: %v", vector.Kind, version, err)
			}
			delete(goldenByVersion, goldenKey)
		}
	}
	if len(goldenByVersion) != 0 {
		t.Fatalf("readability golden bundle contains unclaimed entries: %v", goldenByVersion)
	}
	for key, schema := range schemaByKey {
		if len(schema.Readable) == 0 {
			continue
		}
		if versions, ok := readabilityByKey[key]; !ok || !slices.Equal(versions, schema.Readable) {
			t.Fatalf("readable artifact %q has no exact conformance vector", key)
		}
	}

	forwardByKey := make(map[string]int, len(conformance.ForwardRejection))
	for _, vector := range conformance.ForwardRejection {
		key := standaloneContractKey(vector.Namespace, vector.Kind)
		if _, exists := forwardByKey[key]; exists {
			t.Fatalf("duplicate forward rejection vector %q", key)
		}
		forwardByKey[key] = vector.Version
		if vector.Namespace == "standalone" && vector.Kind == "product-contract" {
			data := standaloneReadFixture(t, standaloneProductContractFixturePath)
			future := bytes.Replace(data, []byte(`"schema_version": 1`), []byte(fmt.Sprintf(`"schema_version": %d`, vector.Version)), 1)
			if _, err := decodeStandaloneProductContractFixture(future); err == nil {
				t.Fatalf("future standalone product contract v%d passed", vector.Version)
			}
			continue
		}
		schema, ok := schemaByKey[key]
		if !ok || vector.Version != schema.Current+1 {
			t.Fatalf("forward vector %q=%d, schema=%+v", key, vector.Version, schema)
		}
		currentKey := standaloneVersionedContractKey(vector.Namespace, vector.Kind, schema.Current)
		currentEntry, ok := standaloneReadabilityGoldenEntryFor(goldens, currentKey)
		if !ok {
			t.Fatalf("forward vector %q lacks a current golden", key)
		}
		if err := standaloneDecodeFutureReadabilityGolden(t, currentEntry, vector.Version); err == nil {
			t.Fatalf("future %s v%d passed", vector.Kind, vector.Version)
		}
	}
	for key, schema := range schemaByKey {
		if len(schema.Readable) == 0 {
			continue
		}
		if version, ok := forwardByKey[key]; !ok || version != schema.Current+1 {
			t.Fatalf("readable artifact %q lacks its next-version rejection", key)
		}
	}
	if version := forwardByKey[standaloneContractKey("standalone", "product-contract")]; version != 2 {
		t.Fatalf("standalone product-contract future vector=%d, want 2", version)
	}

	metricStates := make(map[string]bool, len(contract.MetricStates))
	for _, state := range contract.MetricStates {
		metricStates[state] = true
	}
	wantMetricVectors := map[string]bool{
		"legacy-not-applicable-zero": false,
		"legacy-unknown-zero":        true,
		"legacy-unsupported-zero":    true,
		"missing-optional-entry":     true,
		"missing-required-entry":     false,
		"not-applicable-absent":      true,
		"not-applicable-zero":        false,
		"observed-zero":              true,
		"uncovered-nonzero":          false,
		"unknown-absent":             true,
		"unknown-covered":            false,
		"unsupported-absent":         true,
	}
	for _, vector := range conformance.MetricVectors {
		valid := standaloneMetricVectorValid(vector, metricStates)
		if vector.Valid == nil || valid != *vector.Valid {
			t.Fatalf("metric vector %q validity=%t, fixture=%v", vector.ID, valid, vector.Valid)
		}
		want, ok := wantMetricVectors[vector.ID]
		if !ok || want != *vector.Valid {
			t.Fatalf("metric vector %q has unexpected contract: %+v", vector.ID, vector)
		}
		delete(wantMetricVectors, vector.ID)
	}
	if len(wantMetricVectors) != 0 {
		t.Fatalf("metric conformance vectors are missing: %v", wantMetricVectors)
	}
}

func TestStandaloneContractAuthorityMatrix(t *testing.T) {
	contract := loadStandaloneProductContractFixture(t)
	configuration := contract.Configuration
	if !slices.Equal(configuration.Precedence, []string{"flags", "project_file", "opt_in_environment"}) ||
		configuration.UnknownKeys != "reject" || configuration.AmbientAuthority == nil || len(configuration.AmbientAuthority) != 0 {
		t.Fatalf("configuration authority contract=%+v", configuration)
	}

	type operationAuthority struct {
		current, standalone, authority                             string
		localRead, localWrite, processSpawn                        bool
		providerContact, backendContact, network, credentialAccess bool
		privateWorkspaceAccess                                     bool
	}
	wantOperations := map[string]operationAuthority{
		standaloneOperationKey("capabilities", "default"):        {current: "implemented_pre_release", standalone: "pre_release", authority: "none"},
		standaloneOperationKey("compare", "default"):             {current: "implemented_pre_release", standalone: "pre_release", authority: "local_read", localRead: true},
		standaloneOperationKey("compat verify", "provider-free"): {current: "maintainer_compat", standalone: "reserved", authority: "verifier_execution", localRead: true, processSpawn: true},
		standaloneOperationKey("export", "agent-skills"):         {current: "implemented_pre_release", standalone: "pre_release", authority: "local_write", localRead: true, localWrite: true},
		standaloneOperationKey("grade", "deterministic"):         {current: "implemented_pre_release", standalone: "pre_release", authority: "verifier_execution", localRead: true, processSpawn: true},
		standaloneOperationKey("grade", "judge"):                 {current: "absent", standalone: "reserved", authority: "provider_execution", localRead: true, processSpawn: true, providerContact: true, network: true, credentialAccess: true},
		standaloneOperationKey("import", "agent-skills"):         {current: "implemented_pre_release", standalone: "pre_release", authority: "local_read", localRead: true},
		standaloneOperationKey("import", "default"):              {current: "absent", standalone: "reserved", authority: "local_write", localRead: true, localWrite: true},
		standaloneOperationKey("init", "default"):                {current: "private_maintainer_only", standalone: "reserved", authority: "local_write", localWrite: true, privateWorkspaceAccess: true},
		standaloneOperationKey("inspect", "default"):             {current: "implemented_pre_release", standalone: "pre_release", authority: "local_read", localRead: true},
		standaloneOperationKey("migrate apply", "default"):       {current: "implemented_pre_release", standalone: "pre_release", authority: "local_write", localRead: true, localWrite: true, privateWorkspaceAccess: true},
		standaloneOperationKey("migrate preview", "default"):     {current: "implemented_pre_release", standalone: "pre_release", authority: "local_read", localRead: true, privateWorkspaceAccess: true},
		standaloneOperationKey("plan", "default"):                {current: "private_maintainer_only", standalone: "reserved", authority: "local_write", localRead: true, localWrite: true, privateWorkspaceAccess: true},
		standaloneOperationKey("reconcile", "evidence-only"):     {current: "maintainer_compat", standalone: "reserved", authority: "local_write", localRead: true, localWrite: true, privateWorkspaceAccess: true},
		standaloneOperationKey("report", "default"):              {current: "maintainer_compat", standalone: "reserved", authority: "local_read", localRead: true},
		standaloneOperationKey("resume", "default"):              {current: "private_maintainer_only", standalone: "reserved", authority: "agent_execution", localRead: true, localWrite: true, processSpawn: true, providerContact: true, backendContact: true, network: true, credentialAccess: true, privateWorkspaceAccess: true},
		standaloneOperationKey("run", "default"):                 {current: "maintainer_compat", standalone: "reserved", authority: "agent_execution", localRead: true, localWrite: true, processSpawn: true, providerContact: true, backendContact: true, network: true, credentialAccess: true},
		standaloneOperationKey("schema inspect", "default"):      {current: "implemented_pre_release", standalone: "pre_release", authority: "local_read", localRead: true},
		standaloneOperationKey("validate", "default"):            {current: "implemented_pre_release", standalone: "pre_release", authority: "local_read", localRead: true},
		standaloneOperationKey("version", "default"):             {current: "implemented_pre_release", standalone: "pre_release", authority: "none"},
	}
	for _, operation := range contract.StandaloneOperations {
		key := standaloneOperationKey(operation.ID, operation.Mode)
		want, ok := wantOperations[key]
		if !ok {
			t.Fatalf("operation %q has no reviewed authority classification", key)
		}
		if operation.CurrentStatus != want.current || operation.StandaloneStatus != want.standalone || operation.Authority != want.authority ||
			standaloneBool(operation.LocalRead) != want.localRead || standaloneBool(operation.LocalWrite) != want.localWrite ||
			standaloneBool(operation.ProcessSpawn) != want.processSpawn || standaloneBool(operation.ProviderContact) != want.providerContact ||
			standaloneBool(operation.BackendContact) != want.backendContact || standaloneBool(operation.Network) != want.network ||
			standaloneBool(operation.CredentialAccess) != want.credentialAccess ||
			standaloneBool(operation.PrivateWorkspaceAccess) != want.privateWorkspaceAccess {
			t.Fatalf("operation %q authority=%+v, want %+v", key, operation, want)
		}
		if (standaloneBool(operation.ProviderContact) || standaloneBool(operation.BackendContact)) &&
			(!standaloneBool(operation.Network) || !standaloneBool(operation.CredentialAccess)) {
			t.Fatalf("operation %q permits contact without network and credential authority", key)
		}
		delete(wantOperations, key)
	}
	if len(wantOperations) != 0 {
		t.Fatalf("authority matrix omitted operations: %v", wantOperations)
	}

	wantAttempts := map[string]struct {
		phase           string
		terminal        bool
		automaticResume bool
		proofs          []string
	}{
		"canceled":      {phase: "derived", terminal: true, proofs: []string{}},
		"committed":     {phase: "postcommit", proofs: []string{}},
		"failed":        {phase: "postcommit", terminal: true, proofs: []string{}},
		"planned":       {phase: "precommit", automaticResume: true, proofs: []string{"complete_ledger", "immutable_plan", "no_commit"}},
		"policy_denied": {phase: "precommit", terminal: true, proofs: []string{}},
		"running":       {phase: "postcommit", proofs: []string{}},
		"spawning":      {phase: "postcommit", proofs: []string{}},
		"succeeded":     {phase: "postcommit", terminal: true, proofs: []string{}},
		"timed_out":     {phase: "derived", terminal: true, proofs: []string{}},
		"unknown":       {phase: "derived", terminal: true, proofs: []string{}},
		"unsupported":   {phase: "precommit", terminal: true, proofs: []string{}},
	}
	stateTerminal := make(map[string]bool, len(contract.AttemptStates))
	for _, state := range contract.AttemptStates {
		want, ok := wantAttempts[state.ID]
		if !ok || state.Terminal == nil || state.AutomaticResume == nil ||
			state.Phase != want.phase || *state.Terminal != want.terminal || *state.AutomaticResume != want.automaticResume ||
			!slices.Equal(state.AutomaticResumeProofs, want.proofs) {
			t.Fatalf("attempt state %q violates no-replay semantics: %+v", state.ID, state)
		}
		if *state.AutomaticResume && state.ID != "planned" {
			t.Fatalf("started attempt state %q permits automatic replay", state.ID)
		}
		stateTerminal[state.ID] = *state.Terminal
		delete(wantAttempts, state.ID)
	}
	if len(wantAttempts) != 0 {
		t.Fatalf("attempt lifecycle states are missing: %v", wantAttempts)
	}
	wantProofs := []string{"complete_ledger", "definitive_spawn_failure", "durable_cancel", "durable_capability_refusal", "durable_commit", "durable_deadline", "durable_policy_refusal", "durable_process_identity", "durable_spawn_intent", "immutable_plan", "incomplete_terminal_evidence", "no_commit", "non_execution_proof", "terminal_receipt", "termination_proof"}
	if !slices.Equal(contract.AttemptProofs, wantProofs) {
		t.Fatalf("attempt proof registry=%v, want %v", contract.AttemptProofs, wantProofs)
	}
	wantTransitions := standaloneExpectedAttemptTransitions()
	gotTransitions := make(map[string][][]string, len(contract.AttemptTransitions))
	for _, transition := range contract.AttemptTransitions {
		gotTransitions[standaloneTransitionKey(transition.From, transition.To)] = transition.ProofSets
	}
	for _, from := range contract.AttemptStates {
		for _, to := range contract.AttemptStates {
			key := standaloneTransitionKey(from.ID, to.ID)
			got, gotOK := gotTransitions[key]
			want, wantOK := wantTransitions[key]
			if gotOK != wantOK || gotOK && !standaloneProofSetsEqual(got, want) {
				t.Fatalf("attempt transition %s proof sets=%v, want %v present=%t", key, got, want, wantOK)
			}
			if stateTerminal[from.ID] && gotOK {
				t.Fatalf("terminal attempt state %q has outgoing transition to %q", from.ID, to.ID)
			}
		}
	}
	if len(gotTransitions) != len(wantTransitions) || len(gotTransitions) != 22 {
		t.Fatalf("attempt transition count=%d, want %d", len(gotTransitions), len(wantTransitions))
	}
	if contract.AttemptRecovery.UnknownMutable == nil || *contract.AttemptRecovery.UnknownMutable ||
		contract.AttemptRecovery.SameIdentityReplay == nil || *contract.AttemptRecovery.SameIdentityReplay ||
		contract.AttemptRecovery.ReconcileMode != "append_evidence_and_authorize_new_identity_only" {
		t.Fatalf("attempt recovery permits replay or mutation: %+v", contract.AttemptRecovery)
	}
}

func loadStandaloneProductContractFixture(t *testing.T) standaloneProductContractFixture {
	t.Helper()
	contract, err := decodeStandaloneProductContractFixture(standaloneReadFixture(t, standaloneProductContractFixturePath))
	if err != nil {
		t.Fatal(err)
	}
	return contract
}

func loadStandaloneConformanceFixture(t *testing.T) standaloneConformanceFixture {
	t.Helper()
	fixture, err := decodeStandaloneConformanceFixture(standaloneReadFixture(t, standaloneConformanceFixturePath))
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}

func standaloneReadFixture(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func decodeStandaloneProductContractFixture(data []byte) (standaloneProductContractFixture, error) {
	var contract standaloneProductContractFixture
	if err := standaloneDecodeClosedJSON(data, &contract); err != nil {
		return standaloneProductContractFixture{}, fmt.Errorf("decode standalone product contract: %w", err)
	}
	if err := validateStandaloneProductContractFixture(contract); err != nil {
		return standaloneProductContractFixture{}, err
	}
	return contract, nil
}

func decodeStandaloneConformanceFixture(data []byte) (standaloneConformanceFixture, error) {
	var fixture standaloneConformanceFixture
	if err := standaloneDecodeClosedJSON(data, &fixture); err != nil {
		return standaloneConformanceFixture{}, fmt.Errorf("decode standalone conformance fixture: %w", err)
	}
	if fixture.SchemaVersion != 1 || fixture.ContractVersion == "" || fixture.GoldenBundle.Path != "testdata/standalone-readability-golden.v1.json" ||
		!standaloneValidSHA256(fixture.GoldenBundle.SHA256) || len(fixture.Readability) == 0 ||
		len(fixture.ForwardRejection) == 0 || len(fixture.MetricVectors) == 0 {
		return standaloneConformanceFixture{}, fmt.Errorf("standalone conformance fixture is incomplete")
	}
	previous := ""
	seenReadability := map[string]bool{}
	for _, vector := range fixture.Readability {
		key := standaloneContractKey(vector.Namespace, vector.Kind)
		if vector.Namespace == "" || vector.Kind == "" || key <= previous || seenReadability[key] || standaloneValidateVersions(vector.Versions, false) != nil {
			return standaloneConformanceFixture{}, fmt.Errorf("invalid readability vector %q", key)
		}
		previous = key
		seenReadability[key] = true
	}
	previous = ""
	seenForward := map[string]bool{}
	for _, vector := range fixture.ForwardRejection {
		key := standaloneContractKey(vector.Namespace, vector.Kind)
		if vector.Namespace == "" || vector.Kind == "" || vector.Version < 1 || key <= previous || seenForward[key] {
			return standaloneConformanceFixture{}, fmt.Errorf("invalid forward rejection vector %q", key)
		}
		previous = key
		seenForward[key] = true
	}
	previous = ""
	seenMetrics := map[string]bool{}
	for _, vector := range fixture.MetricVectors {
		if vector.ID == "" || vector.ID <= previous || (vector.Representation != "atl-profile-legacy" && vector.Representation != "standalone") ||
			vector.Present == nil || vector.Required == nil || vector.Valid == nil || seenMetrics[vector.ID] {
			return standaloneConformanceFixture{}, fmt.Errorf("invalid metric vector %q", vector.ID)
		}
		previous = vector.ID
		seenMetrics[vector.ID] = true
	}
	return fixture, nil
}

func standaloneDecodeClosedJSON(data []byte, target any) error {
	if err := validateJSONNoDuplicateKeys(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err == nil {
			return fmt.Errorf("contains multiple JSON values")
		}
		return fmt.Errorf("contains trailing JSON data: %w", err)
	}
	return nil
}

func validateStandaloneProductContractFixture(contract standaloneProductContractFixture) error {
	if contract.SchemaVersion != 1 {
		return fmt.Errorf("unsupported standalone product contract schema_version %d", contract.SchemaVersion)
	}
	if contract.ContractVersion != "0.1.0-pre-release" || contract.Status != "pre-release" ||
		contract.CompatibilityBegins != "first-conforming-signed-standalone-release" {
		return fmt.Errorf("standalone product contract identity is incomplete")
	}
	if err := standaloneValidateSortedUniqueStrings("maintainer commands", contract.MaintainerCommands, false); err != nil {
		return err
	}
	previous := ""
	seenOperations := map[string]bool{}
	for _, operation := range contract.StandaloneOperations {
		key := standaloneOperationKey(operation.ID, operation.Mode)
		if operation.ID == "" || operation.Mode == "" || key <= previous || seenOperations[key] ||
			!standaloneOneOf(operation.CurrentStatus, "absent", "implemented_pre_release", "maintainer_compat", "private_maintainer_only") ||
			!standaloneOneOf(operation.StandaloneStatus, "pre_release", "reserved") ||
			(operation.CurrentStatus == "implemented_pre_release") != (operation.StandaloneStatus == "pre_release") ||
			!standaloneOneOf(operation.Authority, "agent_execution", "local_read", "local_write", "none", "provider_execution", "verifier_execution") ||
			standaloneValidateSortedUniqueStrings("maintainer aliases", operation.MaintainerAliases, true) != nil ||
			operation.LocalRead == nil || operation.LocalWrite == nil || operation.ProcessSpawn == nil || operation.ProviderContact == nil ||
			operation.BackendContact == nil || operation.Network == nil || operation.CredentialAccess == nil || operation.PrivateWorkspaceAccess == nil {
			return fmt.Errorf("invalid standalone operation %q", key)
		}
		previous = key
		seenOperations[key] = true
	}
	if contract.Configuration.Precedence == nil || contract.Configuration.AmbientAuthority == nil || contract.Configuration.UnknownKeys == "" {
		return fmt.Errorf("standalone configuration contract is incomplete")
	}
	previous = ""
	seenRoles := map[string]bool{}
	for _, role := range contract.Roles {
		if role.ID == "" || role.ID <= previous || seenRoles[role.ID] ||
			standaloneValidateDistinctNonemptyStrings(role.Owns) != nil || standaloneValidateDistinctNonemptyStrings(role.DoesNotOwn) != nil {
			return fmt.Errorf("invalid standalone role %q", role.ID)
		}
		previous = role.ID
		seenRoles[role.ID] = true
	}
	for owner, states := range map[string][]string{"capability states": contract.CapabilityStates, "metric states": contract.MetricStates} {
		if err := standaloneValidateSortedUniqueStrings(owner, states, false); err != nil {
			return err
		}
	}
	if !slices.Equal(contract.CapabilityStates, []string{"not_applicable", "supported", "unknown", "unsupported"}) ||
		!slices.Equal(contract.MetricStates, []string{"not_applicable", "observed", "unknown", "unsupported"}) {
		return fmt.Errorf("standalone capability or metric state vocabulary is unknown")
	}
	previousCode := -1
	seenExitIDs := map[string]bool{}
	wantExitIDs := []string{"success", "internal_error", "usage_error", "configuration_error", "input_error", "compatibility_error", "policy_denied", "authentication_failed", "execution_failed", "check_failed", "outcome_unknown", "interrupted"}
	for index, exitClass := range contract.ExitClasses {
		if exitClass.Code == nil || index >= len(wantExitIDs) || *exitClass.Code != index || *exitClass.Code <= previousCode ||
			exitClass.ID != wantExitIDs[index] || seenExitIDs[exitClass.ID] {
			return fmt.Errorf("invalid standalone exit class %+v", exitClass)
		}
		previousCode = *exitClass.Code
		seenExitIDs[exitClass.ID] = true
	}
	if len(contract.ExitClasses) != len(wantExitIDs) {
		return fmt.Errorf("standalone exit class registry is incomplete")
	}
	previous = ""
	seenSchemas := map[string]bool{}
	for _, schema := range contract.ArtifactSchemas {
		key := standaloneContractKey(schema.Namespace, schema.Kind)
		if schema.Namespace == "" || schema.Kind == "" || key <= previous || seenSchemas[key] || schema.Current < 1 ||
			schema.Readable == nil || schema.Emitted == nil || schema.Executable == nil ||
			!standaloneOneOf(schema.Disposition, "preserve", "write_only_projection") ||
			!standaloneOneOf(schema.Privacy, "content_minimized", "owner_private", "public", "public_or_private") ||
			!standaloneOneOf(schema.Migration, "compare_only", "explicit", "partial_explicit") ||
			schema.MaxBytes == nil || *schema.MaxBytes < 0 || (*schema.MaxBytes == 0 && (len(schema.Readable) != 0 || schema.Disposition != "write_only_projection")) {
			return fmt.Errorf("invalid standalone artifact schema %q", key)
		}
		if err := standaloneValidateVersions(schema.Readable, true); err != nil {
			return fmt.Errorf("artifact schema %q readable versions: %w", key, err)
		}
		if err := standaloneValidateVersions(schema.Emitted, true); err != nil {
			return fmt.Errorf("artifact schema %q emitted versions: %w", key, err)
		}
		if err := standaloneValidateVersions(schema.Executable, true); err != nil {
			return fmt.Errorf("artifact schema %q executable versions: %w", key, err)
		}
		if len(schema.Emitted) != 0 && !slices.Contains(schema.Emitted, schema.Current) {
			return fmt.Errorf("artifact schema %q does not emit its current version", key)
		}
		previous = key
		seenSchemas[key] = true
	}
	previous = ""
	seenClasses := map[string]bool{}
	for _, class := range contract.ArtifactClasses {
		if class.ID == "" || class.ID <= previous || seenClasses[class.ID] ||
			!standaloneOneOf(class.Privacy, "content_minimized", "owner_private", "public", "public_or_private") ||
			!standaloneOneOf(class.Disposition, "compare_only", "experimental", "preserve") {
			return fmt.Errorf("invalid standalone artifact class %q", class.ID)
		}
		previous = class.ID
		seenClasses[class.ID] = true
	}
	previous = ""
	seenAttempts := map[string]bool{}
	for _, state := range contract.AttemptStates {
		if state.ID == "" || state.ID <= previous || seenAttempts[state.ID] ||
			!standaloneOneOf(state.Phase, "derived", "postcommit", "precommit") || state.Terminal == nil || state.AutomaticResume == nil ||
			standaloneValidateSortedUniqueStrings("automatic resume proofs", state.AutomaticResumeProofs, true) != nil {
			return fmt.Errorf("invalid standalone attempt state %q", state.ID)
		}
		previous = state.ID
		seenAttempts[state.ID] = true
	}
	wantAttemptStates := []string{"canceled", "committed", "failed", "planned", "policy_denied", "running", "spawning", "succeeded", "timed_out", "unknown", "unsupported"}
	gotAttemptStates := make([]string, 0, len(contract.AttemptStates))
	for _, state := range contract.AttemptStates {
		gotAttemptStates = append(gotAttemptStates, state.ID)
	}
	if !slices.Equal(gotAttemptStates, wantAttemptStates) {
		return fmt.Errorf("standalone attempt state vocabulary is unknown")
	}
	if err := standaloneValidateSortedUniqueStrings("attempt proofs", contract.AttemptProofs, false); err != nil {
		return err
	}
	wantAttemptProofs := []string{"complete_ledger", "definitive_spawn_failure", "durable_cancel", "durable_capability_refusal", "durable_commit", "durable_deadline", "durable_policy_refusal", "durable_process_identity", "durable_spawn_intent", "immutable_plan", "incomplete_terminal_evidence", "no_commit", "non_execution_proof", "terminal_receipt", "termination_proof"}
	if !slices.Equal(contract.AttemptProofs, wantAttemptProofs) {
		return fmt.Errorf("standalone attempt proof vocabulary is unknown")
	}
	knownProofs := make(map[string]bool, len(contract.AttemptProofs))
	for _, proof := range contract.AttemptProofs {
		knownProofs[proof] = true
	}
	for _, state := range contract.AttemptStates {
		for _, proof := range state.AutomaticResumeProofs {
			if !knownProofs[proof] {
				return fmt.Errorf("attempt state %q references unknown proof %q", state.ID, proof)
			}
		}
	}
	previous = ""
	seenTransitions := map[string]bool{}
	gotTransitions := make(map[string][][]string, len(contract.AttemptTransitions))
	for _, transition := range contract.AttemptTransitions {
		key := standaloneTransitionKey(transition.From, transition.To)
		if !seenAttempts[transition.From] || !seenAttempts[transition.To] || key <= previous || seenTransitions[key] || len(transition.ProofSets) == 0 {
			return fmt.Errorf("invalid attempt transition %q", key)
		}
		previousProofSet := ""
		for _, proofSet := range transition.ProofSets {
			if err := standaloneValidateSortedUniqueStrings("transition proof set", proofSet, false); err != nil {
				return fmt.Errorf("attempt transition %q: %w", key, err)
			}
			proofSetKey := strings.Join(proofSet, "\x00")
			if proofSetKey <= previousProofSet {
				return fmt.Errorf("attempt transition %q proof sets are not sorted and unique", key)
			}
			previousProofSet = proofSetKey
			for _, proof := range proofSet {
				if !knownProofs[proof] {
					return fmt.Errorf("attempt transition %q references unknown proof %q", key, proof)
				}
			}
		}
		previous = key
		seenTransitions[key] = true
		gotTransitions[key] = transition.ProofSets
	}
	wantTransitions := standaloneExpectedAttemptTransitions()
	if len(gotTransitions) != len(wantTransitions) {
		return fmt.Errorf("standalone attempt transition registry is incomplete")
	}
	for key, want := range wantTransitions {
		if got, ok := gotTransitions[key]; !ok || !standaloneProofSetsEqual(got, want) {
			return fmt.Errorf("standalone attempt transition %q is unknown", key)
		}
	}
	if contract.AttemptRecovery.UnknownMutable == nil || contract.AttemptRecovery.SameIdentityReplay == nil ||
		*contract.AttemptRecovery.UnknownMutable || *contract.AttemptRecovery.SameIdentityReplay ||
		contract.AttemptRecovery.ReconcileMode != "append_evidence_and_authorize_new_identity_only" {
		return fmt.Errorf("standalone attempt recovery is incomplete")
	}
	policy := contract.CompatibilityPolicy
	if policy.StrictUnknownFields == nil || policy.AdditiveMembersRequireSchemaBump == nil || policy.SourceBytesPreserved == nil ||
		policy.MigrationRequiresReviewedPreview == nil || policy.MinimumDeprecationReleases == nil || policy.MinimumDeprecationDays == nil ||
		policy.RootModuleLinkForbidden == nil {
		return fmt.Errorf("standalone compatibility policy is incomplete")
	}
	return nil
}

func standaloneCoordinatorCommands(t *testing.T, path string) []string {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var runFunction *ast.FuncDecl
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Recv == nil && function.Name.Name == "run" {
			runFunction = function
			break
		}
	}
	if runFunction == nil {
		t.Fatal("coordinator run function is absent")
	}
	var commands []string
	var commandSwitches int
	ast.Inspect(runFunction.Body, func(node ast.Node) bool {
		switchStatement, ok := node.(*ast.SwitchStmt)
		if !ok || !standaloneArgsZeroExpression(switchStatement.Tag) {
			return true
		}
		commandSwitches++
		for _, statement := range switchStatement.Body.List {
			clause, ok := statement.(*ast.CaseClause)
			if !ok {
				t.Fatal("coordinator command switch contains a non-case statement")
			}
			for _, expression := range clause.List {
				literal, ok := expression.(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					t.Fatal("coordinator command switch has a non-literal command")
				}
				value, err := strconv.Unquote(literal.Value)
				if err != nil {
					t.Fatal(err)
				}
				commands = append(commands, value)
			}
		}
		return false
	})
	if commandSwitches != 1 {
		t.Fatalf("coordinator command switch count=%d, want 1", commandSwitches)
	}
	slices.Sort(commands)
	return commands
}

func standaloneArgsZeroExpression(expression ast.Expr) bool {
	index, ok := expression.(*ast.IndexExpr)
	if !ok {
		return false
	}
	identifier, ok := index.X.(*ast.Ident)
	if !ok || identifier.Name != "args" {
		return false
	}
	literal, ok := index.Index.(*ast.BasicLit)
	return ok && literal.Kind == token.INT && literal.Value == "0"
}

func standaloneMetricVectorValid(vector standaloneMetricVector, states map[string]bool) bool {
	if vector.Present == nil || vector.Required == nil || vector.Valid == nil {
		return false
	}
	if !*vector.Present {
		return !*vector.Required && vector.State == "" && vector.Coverage == nil && vector.Value == nil
	}
	if !states[vector.State] {
		return false
	}
	if vector.Representation == "standalone" {
		if vector.State == "observed" {
			return vector.Coverage != nil && *vector.Coverage && vector.Value != nil && *vector.Value >= 0
		}
		return vector.Coverage == nil && vector.Value == nil
	}
	if vector.Representation != "atl-profile-legacy" || vector.Coverage == nil || vector.Value == nil || *vector.Value < 0 {
		return false
	}
	if vector.State == "observed" {
		return *vector.Coverage
	}
	if vector.State != "unknown" && vector.State != "unsupported" {
		return false
	}
	return !*vector.Coverage && *vector.Value == 0
}

func standaloneValidateSortedUniqueStrings(owner string, values []string, allowEmpty bool) error {
	if values == nil || !allowEmpty && len(values) == 0 {
		return fmt.Errorf("%s is missing", owner)
	}
	previous := ""
	for _, value := range values {
		if value == "" || strings.TrimSpace(value) != value || value <= previous {
			return fmt.Errorf("%s is not sorted, unique, and non-empty", owner)
		}
		previous = value
	}
	return nil
}

func standaloneValidateDistinctNonemptyStrings(values []string) error {
	if len(values) == 0 {
		return fmt.Errorf("list is empty")
	}
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if value == "" || strings.TrimSpace(value) != value || seen[value] {
			return fmt.Errorf("list contains an invalid value")
		}
		seen[value] = true
	}
	return nil
}

func standaloneValidateVersions(versions []int, allowEmpty bool) error {
	if versions == nil || !allowEmpty && len(versions) == 0 {
		return fmt.Errorf("version list is missing")
	}
	previous := 0
	for _, version := range versions {
		if version < 1 || version <= previous {
			return fmt.Errorf("versions are not positive, sorted, and unique")
		}
		previous = version
	}
	return nil
}

func standaloneContractKey(namespace, kind string) string {
	return namespace + "\x00" + kind
}

func standaloneTrue(value *bool) bool {
	return value != nil && *value
}

func standaloneBool(value *bool) bool {
	return value != nil && *value
}
