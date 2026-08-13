package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"

	"github.com/isukharev/atl/internal/agenteval"
)

type standaloneReferenceRunResult struct {
	ManifestSHA256 string `json:"manifest_sha256"`
	Trials         int    `json:"trials"`
	Succeeded      int    `json:"succeeded"`
	Failed         int    `json:"failed"`
}

func standaloneExecuteReferenceRun(ctx context.Context, args []string) (standaloneOutcome, *standaloneFailure) {
	mode, failure := standalonePeekFlag(args, "mode")
	if failure != nil {
		return standaloneOutcome{}, failure
	}
	if mode != "reference" {
		return standaloneOutcome{}, standaloneFail(standaloneCompatibilityError, "operation_unavailable")
	}
	parsed, failure := parseStandaloneFlags(args, map[string]standaloneFlagSpec{
		"mode": {takesValue: true}, "manifest": {takesValue: true}, "bundle": {takesValue: true},
		"destination": {takesValue: true}, "output": {takesValue: true},
	})
	if failure != nil {
		return standaloneOutcome{}, failure
	}
	if len(parsed.positionals) != 0 || parsed.one("manifest") == "" || parsed.one("bundle") == "" || parsed.one("destination") == "" {
		return standaloneOutcome{}, standaloneFail(standaloneUsageError, "invalid_reference_run_options")
	}
	output, failure := parsed.outputMode()
	if failure != nil {
		return standaloneOutcome{}, failure
	}
	if failure := standaloneContextFailure(ctx); failure != nil {
		return standaloneOutcome{}, failure
	}
	manifestData, failure := standaloneReadStableReferenceArtifact(parsed.one("manifest"), agenteval.ExperimentManifestMaxBytes)
	if failure != nil {
		return standaloneOutcome{}, failure
	}
	bundleData, failure := standaloneReadStableReferenceArtifact(parsed.one("bundle"), agenteval.SequentialReferenceBundleMaxBytes)
	if failure != nil {
		return standaloneOutcome{}, failure
	}
	manifest, err := agenteval.DecodeExperimentManifest(bytes.NewReader(manifestData))
	if err != nil {
		return standaloneOutcome{}, standaloneReferenceInputFailure("invalid_reference_manifest")
	}
	bundle, err := agenteval.DecodeSequentialReferenceBundle(bytes.NewReader(bundleData))
	if err != nil {
		return standaloneOutcome{}, standaloneReferenceInputFailure("invalid_reference_bundle")
	}
	if failure := standaloneContextFailure(ctx); failure != nil {
		return standaloneOutcome{}, failure
	}
	result, err := agenteval.RunSequentialReferenceToNewDestination(ctx, parsed.one("destination"), manifest, bundle)
	if err != nil {
		return standaloneOutcome{}, standaloneReferenceRunFailure(ctx, err)
	}
	summary := standaloneReferenceRunResult{ManifestSHA256: result.ManifestSHA256, Trials: len(result.Trials)}
	for _, trial := range result.Trials {
		switch string(trial.TrialRecord.LifecycleState) {
		case "succeeded":
			summary.Succeeded++
		case "failed":
			summary.Failed++
		default:
			return standaloneOutcome{}, standaloneFail(standaloneInternalError, "reference_result_invalid")
		}
	}
	return standaloneOutcome{command: "run", status: "completed", result: summary, outputMode: output, text: "sequential reference run completed\n"}, nil
}

func standaloneReferenceInputFailure(kind string) *standaloneFailure {
	failure := standaloneFail(standaloneInputError, kind)
	failure.retrySafe = true
	return failure
}

func standaloneReferenceRunFailure(ctx context.Context, err error) *standaloneFailure {
	switch {
	case errors.Is(err, agenteval.ErrSequentialReferenceOutcomeUnknown):
		return standaloneFail(standaloneOutcomeUnknownError, "reference_outcome_unknown")
	case errors.Is(err, agenteval.ErrSequentialReferenceUnsupported):
		failure := standaloneFail(standaloneCompatibilityError, "reference_profile_unsupported")
		failure.retrySafe = true
		return failure
	case ctx != nil && ctx.Err() != nil, errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		failure := standaloneFail(standaloneInterruptedError, "execution_canceled")
		failure.retrySafe = true
		return failure
	default:
		failure := standaloneFail(standaloneInputError, "reference_run_rejected")
		failure.retrySafe = true
		return failure
	}
}

func standaloneReadStableReferenceArtifact(path string, maximum int64) ([]byte, *standaloneFailure) {
	before, err := os.Lstat(path)
	if err != nil || before == nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 ||
		before.Size() < 0 || before.Size() > maximum {
		return nil, standaloneReferenceInputFailure("invalid_reference_input")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, standaloneReferenceInputFailure("invalid_reference_input")
	}
	opened, openedErr := file.Stat()
	primary, primaryErr := io.ReadAll(io.LimitReader(file, maximum+1))
	afterPrimary, primaryStatErr := file.Stat()
	_, seekErr := file.Seek(0, io.SeekStart)
	verification, verificationErr := io.ReadAll(io.LimitReader(file, maximum+1))
	afterVerification, verificationStatErr := file.Stat()
	closeErr := file.Close()
	if openedErr != nil || primaryErr != nil || primaryStatErr != nil || seekErr != nil || verificationErr != nil ||
		verificationStatErr != nil || closeErr != nil || int64(len(primary)) > maximum || !bytes.Equal(primary, verification) ||
		!standaloneStableConfigSnapshots(before, opened, afterPrimary, afterVerification) {
		return nil, standaloneReferenceInputFailure("unstable_reference_input")
	}
	return primary, nil
}
