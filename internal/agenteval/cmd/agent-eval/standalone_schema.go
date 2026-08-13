package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/isukharev/atl/internal/agenteval"
)

var (
	standaloneInspectSchema    = agenteval.InspectStandaloneSchema
	standalonePreviewMigration = agenteval.PreviewStandaloneSchemaMigration
	standaloneApplyMigration   = agenteval.ApplyStandaloneSchemaMigration
)

type standaloneSupportedVersion struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
}

func standaloneExecuteSchemaInspect(ctx context.Context, args []string) (standaloneOutcome, *standaloneFailure) {
	parsed, failure := parseStandaloneFlags(args, map[string]standaloneFlagSpec{
		"namespace": {takesValue: true}, "kind": {takesValue: true}, "output": {takesValue: true},
	})
	if failure != nil {
		return standaloneOutcome{}, failure
	}
	if len(parsed.positionals) != 0 || parsed.one("namespace") == "" || parsed.one("kind") == "" {
		return standaloneOutcome{}, standaloneFail(standaloneUsageError, "invalid_schema_inspect_options")
	}
	if failure := standaloneContextFailure(ctx); failure != nil {
		return standaloneOutcome{}, failure
	}
	inspection, err := standaloneInspectSchema(parsed.one("namespace"), parsed.one("kind"))
	if err != nil {
		return standaloneOutcome{}, standaloneFail(standaloneInputError, "unknown_schema")
	}
	return standaloneOutcome{
		command: "schema inspect", status: "completed", result: inspection, outputMode: parsed.outputModeValue(),
		text: fmt.Sprintf("%s/%s current=%d readable=%v\n", inspection.Namespace, inspection.Kind, inspection.Current, inspection.Readable),
	}, nil
}

func standaloneExecuteMigrationPreview(ctx context.Context, args []string) (standaloneOutcome, *standaloneFailure) {
	parsed, options, failure := standaloneParseMigrationOptions(args, false)
	if failure != nil {
		return standaloneOutcome{}, failure
	}
	if failure := standaloneContextFailure(ctx); failure != nil {
		return standaloneOutcome{}, failure
	}
	preview, err := standalonePreviewMigration(options)
	if err != nil {
		return standaloneOutcome{}, standaloneMigrationFailure(err, false)
	}
	return standaloneOutcome{
		command: "migrate preview", status: preview.Status, result: preview, outputMode: parsed.outputModeValue(),
		text: fmt.Sprintf("migration %s/%s %d->%d %s\n", preview.Namespace, preview.Kind, preview.From, preview.To, preview.Status),
	}, nil
}

func standaloneExecuteMigrationApply(ctx context.Context, args []string) (standaloneOutcome, *standaloneFailure) {
	parsed, options, failure := standaloneParseMigrationOptions(args, true)
	if failure != nil {
		return standaloneOutcome{}, failure
	}
	if parsed.one("confirm") != agenteval.StandaloneMigrationConfirmation {
		return standaloneOutcome{}, standaloneFail(standalonePolicyDeniedError, "migration_confirmation_required")
	}
	if failure := standaloneContextFailure(ctx); failure != nil {
		return standaloneOutcome{}, failure
	}
	result, err := standaloneApplyMigration(agenteval.StandaloneMigrationApplyOptions{
		StandaloneMigrationPreviewOptions: options,
		ExpectedPreviewSHA256:             parsed.one("expected-preview-sha256"),
		Confirm:                           parsed.one("confirm"),
	})
	if err != nil {
		return standaloneOutcome{}, standaloneMigrationFailure(err, true)
	}
	return standaloneOutcome{
		command: "migrate apply", status: result.Status, result: result, outputMode: parsed.outputModeValue(),
		text: fmt.Sprintf("migration %s/%s %d->%d %s\n", result.Namespace, result.Kind, result.From, result.To, result.Status),
	}, nil
}

func standaloneParseMigrationOptions(args []string, apply bool) (standaloneParsedFlags, agenteval.StandaloneMigrationPreviewOptions, *standaloneFailure) {
	specs := map[string]standaloneFlagSpec{
		"namespace": {takesValue: true}, "kind": {takesValue: true}, "from": {takesValue: true}, "to": {takesValue: true},
		"root": {takesValue: true}, "repository-root": {takesValue: true}, "output": {takesValue: true},
	}
	if apply {
		specs["expected-preview-sha256"] = standaloneFlagSpec{takesValue: true}
		specs["confirm"] = standaloneFlagSpec{takesValue: true}
	}
	parsed, failure := parseStandaloneFlags(args, specs)
	if failure != nil {
		return standaloneParsedFlags{}, agenteval.StandaloneMigrationPreviewOptions{}, failure
	}
	from, fromErr := strconv.Atoi(parsed.one("from"))
	to, toErr := strconv.Atoi(parsed.one("to"))
	repositoryRoot := parsed.one("repository-root")
	if repositoryRoot == "" {
		repositoryRoot = "."
	}
	missingApply := apply && (parsed.one("expected-preview-sha256") == "" || parsed.one("confirm") == "")
	if len(parsed.positionals) != 0 || parsed.one("namespace") == "" || parsed.one("kind") == "" || parsed.one("root") == "" ||
		fromErr != nil || toErr != nil || from < 1 || to < 1 || from >= to || missingApply {
		return standaloneParsedFlags{}, agenteval.StandaloneMigrationPreviewOptions{}, standaloneFail(standaloneUsageError, "invalid_migration_options")
	}
	if apply && !standaloneSHA256(parsed.one("expected-preview-sha256")) {
		return standaloneParsedFlags{}, agenteval.StandaloneMigrationPreviewOptions{}, standaloneFail(standaloneInputError, "invalid_preview_digest")
	}
	return parsed, agenteval.StandaloneMigrationPreviewOptions{
		Namespace: parsed.one("namespace"), Kind: parsed.one("kind"), From: from, To: to,
		Root: parsed.one("root"), RepositoryRoot: repositoryRoot,
	}, nil
}

func standaloneMigrationFailure(err error, apply bool) *standaloneFailure {
	if !errors.Is(err, agenteval.ErrStandaloneMigration) {
		return standaloneFail(standaloneInternalError, "migration_internal_failure")
	}
	if !apply {
		return standaloneFail(standaloneCompatibilityError, "migration_unavailable")
	}
	return standaloneFail(standaloneExecutionFailedError, "migration_apply_failed")
}

func standaloneSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}
