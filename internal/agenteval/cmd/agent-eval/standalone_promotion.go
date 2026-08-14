package main

import (
	"context"
	"fmt"
	"os"

	"github.com/isukharev/atl/internal/agenteval"
)

func standalonePromotionFailure(err error) *standaloneFailure {
	if code, ok := agenteval.PromotionCodeOf(err); ok {
		switch string(code) {
		case "promotion_conflict":
			return standaloneFail(standalonePolicyDeniedError, "promotion_conflict")
		case "promotion_refused":
			return standaloneFail(standaloneExitClass{code: 9, id: "check_failed", message: "promotion checks failed", recovery: "review_failed_check"}, "promotion_refused")
		case "promotion_limit_exceeded":
			return standaloneFail(standaloneInputError, "promotion_limit_exceeded")
		case "invalid_promotion_identity", "invalid_promotion_review", "invalid_promotion_axis", "invalid_promotion_receipt", "invalid_rollback_receipt":
			return standaloneFail(standaloneInputError, "invalid_promotion_input")
		}
	}
	return standaloneFail(standaloneInternalError, "promotion_internal")
}

func standaloneExecutePromotion(ctx context.Context, args []string) (standaloneOutcome, *standaloneFailure) {
	parsed, failure := parseStandaloneFlags(args, standaloneCommandFlagSpecs(map[string]standaloneFlagSpec{
		"comparison": {takesValue: true}, "store": {takesValue: true}, "confirm": {takesValue: true},
		"dry-run": {}, "explain": {},
	}))
	if failure != nil {
		return standaloneOutcome{}, failure
	}
	if len(parsed.positionals) != 0 || parsed.one("comparison") == "" || parsed.one("store") == "" {
		return standaloneOutcome{}, standaloneFail(standaloneUsageError, "invalid_promotion_options")
	}
	if failure := standaloneContextFailure(ctx); failure != nil {
		return standaloneOutcome{}, failure
	}
	comparisonFile, err := os.Open(parsed.one("comparison"))
	if err != nil {
		return standaloneOutcome{}, standaloneFail(standaloneInputError, "invalid_promotion_input")
	}
	comparison, decodeErr := agenteval.DecodePromotionComparison(comparisonFile)
	closeErr := comparisonFile.Close()
	if decodeErr != nil || closeErr != nil {
		if decodeErr != nil {
			return standaloneOutcome{}, standalonePromotionFailure(decodeErr)
		}
		return standaloneOutcome{}, standaloneFail(standaloneInputError, "invalid_promotion_input")
	}
	receipt, err := agenteval.EvaluatePromotion(comparison)
	if err != nil {
		return standaloneOutcome{}, standalonePromotionFailure(err)
	}
	if parsed.boolean("dry-run") || parsed.boolean("explain") {
		status := "completed"
		if receipt.Decision == agenteval.PromotionDecisionRefuse {
			status = "refused"
		}
		return standaloneOutcome{command: "promote", status: status, result: receipt, outputMode: parsed.outputModeValue(), text: fmt.Sprintf("promotion %s\n", receipt.Decision)}, nil
	}
	if parsed.one("confirm") != "PROMOTE" {
		return standaloneOutcome{}, standaloneFail(standalonePolicyDeniedError, "promotion_confirmation")
	}
	store, err := agenteval.NewPromotionStore(parsed.one("store"))
	if err != nil {
		return standaloneOutcome{}, standalonePromotionFailure(err)
	}
	if receipt.Decision == agenteval.PromotionDecisionRefuse {
		if err := store.RecordDecision(receipt); err != nil {
			return standaloneOutcome{}, standalonePromotionFailure(err)
		}
		return standaloneOutcome{}, standaloneFail(standaloneExitClass{code: 9, id: "check_failed", message: "promotion checks failed", recovery: "review_failed_check"}, "promotion_refused")
	}
	current, present, err := store.Current()
	if err != nil {
		return standaloneOutcome{}, standalonePromotionFailure(err)
	}
	var expected *agenteval.PromotionIdentity
	if present {
		expected = &current
	}
	if err := store.ApplyPromotion(receipt, expected); err != nil {
		return standaloneOutcome{}, standalonePromotionFailure(err)
	}
	return standaloneOutcome{command: "promote", status: "completed", result: receipt, outputMode: parsed.outputModeValue(), text: "promotion applied\n"}, nil
}

func standaloneExecuteRollback(ctx context.Context, args []string) (standaloneOutcome, *standaloneFailure) {
	parsed, failure := parseStandaloneFlags(args, standaloneCommandFlagSpecs(map[string]standaloneFlagSpec{
		"receipt": {takesValue: true}, "store": {takesValue: true}, "confirm": {takesValue: true},
	}))
	if failure != nil {
		return standaloneOutcome{}, failure
	}
	if len(parsed.positionals) != 0 || parsed.one("receipt") == "" || parsed.one("store") == "" || parsed.one("confirm") != "ROLLBACK" {
		return standaloneOutcome{}, standaloneFail(standaloneUsageError, "invalid_rollback_options")
	}
	if failure := standaloneContextFailure(ctx); failure != nil {
		return standaloneOutcome{}, failure
	}
	receiptFile, err := os.Open(parsed.one("receipt"))
	if err != nil {
		return standaloneOutcome{}, standaloneFail(standaloneInputError, "invalid_rollback_input")
	}
	receipt, decodeErr := agenteval.DecodePromotionRollback(receiptFile)
	closeErr := receiptFile.Close()
	if decodeErr != nil || closeErr != nil {
		if decodeErr != nil {
			return standaloneOutcome{}, standalonePromotionFailure(decodeErr)
		}
		return standaloneOutcome{}, standaloneFail(standaloneInputError, "invalid_rollback_input")
	}
	store, err := agenteval.NewPromotionStore(parsed.one("store"))
	if err != nil {
		return standaloneOutcome{}, standalonePromotionFailure(err)
	}
	if err := store.ApplyRollback(receipt); err != nil {
		return standaloneOutcome{}, standalonePromotionFailure(err)
	}
	return standaloneOutcome{command: "rollback", status: "completed", result: receipt, outputMode: parsed.outputModeValue(), text: "rollback applied\n"}, nil
}
