package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/isukharev/atl/internal/agenteval"
)

type standaloneReferenceRunResult struct {
	ManifestSHA256 string `json:"manifest_sha256"`
	Trials         int    `json:"trials"`
	Succeeded      int    `json:"succeeded"`
	Failed         int    `json:"failed"`
	Canceled       int    `json:"canceled"`
	Unknown        int    `json:"unknown"`
	Workers        uint32 `json:"workers"`
	Queued         uint32 `json:"queued"`
	Started        uint32 `json:"started"`
	Completed      uint32 `json:"completed"`
	NeverStarted   uint32 `json:"never_started"`
	Stop           string `json:"stop"`
}

func standaloneReferenceOptions(modes []string, newDestination bool) []standaloneOptionDescriptor {
	destination := "one exact existing incomplete destination"
	if newDestination {
		destination = "one exact clean and previously nonexistent destination"
	}
	return []standaloneOptionDescriptor{
		{Name: "--mode", Value: strings.Join(modes, "|"), Description: "supported execution profile"},
		{Name: "--manifest", Value: "FILE", Description: "compiled immutable experiment manifest"},
		{Name: "--bundle", Value: "FILE", Description: "bounded reference inputs"},
		{Name: "--destination", Value: "ABSOLUTE_DIR", Description: destination},
		{Name: "--workers", Value: "N", Description: "bounded local workers (1-256)"},
		{Name: "--sequential", Description: "force the exact one-worker compatibility schedule"},
		{Name: "--output", Value: "json|text", Description: "select JSON (default) or explicit human output"},
	}
}

func standaloneExecuteReferenceRun(ctx context.Context, args []string) (standaloneOutcome, *standaloneFailure) {
	return standaloneExecuteReference(ctx, args, false)
}

func standaloneExecuteReferenceResume(ctx context.Context, args []string) (standaloneOutcome, *standaloneFailure) {
	return standaloneExecuteReference(ctx, args, true)
}

func standaloneExecuteReference(ctx context.Context, args []string, resume bool) (standaloneOutcome, *standaloneFailure) {
	mode, failure := standalonePeekFlag(args, "mode")
	if failure != nil {
		return standaloneOutcome{}, failure
	}
	if mode != "reference" {
		return standaloneOutcome{}, standaloneFail(standaloneCompatibilityError, "operation_unavailable")
	}
	parsed, failure := parseStandaloneFlags(args, map[string]standaloneFlagSpec{
		"mode": {takesValue: true}, "manifest": {takesValue: true}, "bundle": {takesValue: true},
		"destination": {takesValue: true}, "workers": {takesValue: true}, "sequential": {}, "output": {takesValue: true},
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
	workers, explicitWorkers, failure := standaloneReferenceWorkers(parsed)
	if failure != nil {
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
	var result agenteval.SequentialReferenceResult
	if resume {
		if explicitWorkers {
			result, err = agenteval.ResumeScheduledReferenceAtDestination(ctx, parsed.one("destination"), manifest, bundle,
				agenteval.SequentialReferenceRunOptions{Workers: workers})
		} else {
			result, err = agenteval.ResumeSequentialReferenceAtDestination(ctx, parsed.one("destination"), manifest, bundle)
		}
	} else if explicitWorkers {
		result, err = agenteval.RunScheduledReferenceToNewDestination(ctx, parsed.one("destination"), manifest, bundle,
			agenteval.SequentialReferenceRunOptions{Workers: workers})
	} else {
		result, err = agenteval.RunSequentialReferenceToNewDestination(ctx, parsed.one("destination"), manifest, bundle)
	}
	if err != nil {
		return standaloneOutcome{}, standaloneReferenceRunFailure(ctx, err)
	}
	summary := standaloneReferenceRunResult{ManifestSHA256: result.ManifestSHA256, Trials: len(result.Trials), Workers: workers,
		Queued: result.Scheduler.Queued, Started: result.Scheduler.Started, Completed: result.Scheduler.Completed,
		NeverStarted: result.Scheduler.NeverStarted, Stop: string(result.Scheduler.Stop)}
	for _, trial := range result.Trials {
		switch string(trial.TrialRecord.LifecycleState) {
		case "succeeded":
			summary.Succeeded++
		case "failed":
			summary.Failed++
		case "canceled", "timed_out":
			summary.Canceled++
		case "unknown":
			summary.Unknown++
		default:
			return standaloneOutcome{}, standaloneFail(standaloneInternalError, "reference_result_invalid")
		}
	}
	command, text := "run", "reference run completed\n"
	if resume {
		command, text = "resume", "reference resume completed\n"
	}
	return standaloneOutcome{command: command, status: "completed", result: summary, outputMode: output, text: text}, nil
}

func standaloneReferenceWorkers(parsed standaloneParsedFlags) (uint32, bool, *standaloneFailure) {
	value := parsed.one("workers")
	if parsed.boolean("sequential") && value != "" {
		return 0, false, standaloneFail(standaloneUsageError, "invalid_reference_scheduler_options")
	}
	if value == "" {
		return 1, false, nil
	}
	parsedWorkers, err := strconv.ParseUint(value, 10, 32)
	if err != nil || parsedWorkers == 0 || parsedWorkers > uint64(agenteval.SchedulerMaximumWorkers) {
		return 0, false, standaloneFail(standaloneUsageError, "invalid_reference_scheduler_options")
	}
	return uint32(parsedWorkers), true, nil
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
