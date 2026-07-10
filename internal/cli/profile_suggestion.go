package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/config"
	profilepkg "github.com/isukharev/atl/internal/profile"
)

func newProfileSuggestCmd() *cobra.Command {
	var fromFile, out string
	cmd := &cobra.Command{
		Use:   "suggest",
		Short: "Build a deterministic private suggestion from explicit observations",
		Long: "Build a versioned suggestion artifact without changing profile.json. The output parent\n" +
			"directory must already be private (mode 0700 or stricter).",
		RunE: func(cmd *cobra.Command, _ []string) error {
			data, err := readProfileJSON(fromFile)
			if err != nil {
				return err
			}
			observations, err := profilepkg.DecodeObservationsStrict(data)
			if err != nil {
				return err
			}
			suggestion, rejected, err := profilepkg.BuildSuggestion(config.Dir(), observations)
			if err != nil {
				return err
			}
			if samePath(fromFile, out) {
				return usageErr("--out must differ from --from-file")
			}
			if err := profilepkg.WriteSuggestion(out, suggestion); err != nil {
				return err
			}
			result := profilepkg.SuggestResult{
				Path: out, SuggestionHash: suggestion.SuggestionHash,
				BaseProfileHash: suggestion.BaseProfileHash, PreviouslyRejected: rejected,
			}
			return emit(cmd, result, func() string {
				return fmt.Sprintf("suggestion: %s\npath: %s\npreviously_rejected: %t", result.SuggestionHash, result.Path, result.PreviouslyRejected)
			})
		},
	}
	cmd.Flags().StringVar(&fromFile, "from-file", "", "observations JSON file, or - for stdin (required)")
	cmd.Flags().StringVar(&out, "out", "", "private *.atl-suggestion.json path (required; parent mode 0700)")
	_ = cmd.MarkFlagRequired("from-file")
	_ = cmd.MarkFlagRequired("out")
	return cmd
}

func newProfileSuggestionCmd() *cobra.Command {
	group := &cobra.Command{Use: "suggestion", Short: "Review, apply, or reject a private suggestion"}

	var reviewFile string
	review := &cobra.Command{
		Use:   "review",
		Short: "Review evidence and the exact profile candidate without writing",
		RunE: func(cmd *cobra.Command, _ []string) error {
			suggestion, err := readSuggestion(reviewFile)
			if err != nil {
				return err
			}
			result, err := profilepkg.ReviewSuggestion(config.Dir(), suggestion)
			if err != nil {
				return err
			}
			return emit(cmd, result, func() string {
				return profileSuggestionReviewText(result)
			})
		},
	}
	review.Flags().StringVar(&reviewFile, "from-file", "", "suggestion JSON file (required)")
	_ = review.MarkFlagRequired("from-file")

	var applyFile, suggestionHash, candidateHash, currentHash string
	apply := &cobra.Command{
		Use:   "apply",
		Short: "Apply the exact suggestion/profile hashes returned by review",
		RunE: func(cmd *cobra.Command, _ []string) error {
			suggestion, err := readSuggestion(applyFile)
			if err != nil {
				return err
			}
			result, err := profilepkg.ApplySuggestion(config.Dir(), suggestion, suggestionHash, candidateHash, currentHash)
			if err != nil {
				return err
			}
			return emit(cmd, result, func() string {
				return "suggestion applied: " + result.SuggestionHash
			})
		},
	}
	apply.Flags().StringVar(&applyFile, "from-file", "", "same suggestion JSON reviewed earlier (required)")
	apply.Flags().StringVar(&suggestionHash, "suggestion-hash", "", "suggestion_hash returned by review (required)")
	apply.Flags().StringVar(&candidateHash, "candidate-hash", "", "preview.candidate_hash returned by review (required)")
	apply.Flags().StringVar(&currentHash, "expected-current-hash", "", "preview.current_hash returned by review (required)")
	_ = apply.MarkFlagRequired("from-file")
	_ = apply.MarkFlagRequired("suggestion-hash")
	_ = apply.MarkFlagRequired("candidate-hash")
	_ = apply.MarkFlagRequired("expected-current-hash")

	var rejectFile, rejectHash string
	reject := &cobra.Command{
		Use:   "reject",
		Short: "Remember rejection of the exact suggestion hash without storing evidence",
		RunE: func(cmd *cobra.Command, _ []string) error {
			suggestion, err := readSuggestion(rejectFile)
			if err != nil {
				return err
			}
			result, err := profilepkg.RejectSuggestion(config.Dir(), suggestion, rejectHash)
			if err != nil {
				return err
			}
			return emit(cmd, result, func() string {
				return fmt.Sprintf("suggestion rejected: %s\nchanged: %t", result.SuggestionHash, result.Changed)
			})
		},
	}
	reject.Flags().StringVar(&rejectFile, "from-file", "", "same suggestion JSON reviewed earlier (required)")
	reject.Flags().StringVar(&rejectHash, "suggestion-hash", "", "suggestion_hash returned by review (required)")
	_ = reject.MarkFlagRequired("from-file")
	_ = reject.MarkFlagRequired("suggestion-hash")

	group.AddCommand(review, apply, reject)
	return group
}

func readSuggestion(path string) (profilepkg.Suggestion, error) {
	data, err := readProfileJSON(path)
	if err != nil {
		return profilepkg.Suggestion{}, err
	}
	return profilepkg.DecodeSuggestionStrict(data)
}

func readProfileJSON(path string) ([]byte, error) {
	if path == "-" {
		if stdinIsTerminal() {
			return nil, usageErr("stdin is a terminal and no JSON was piped; pass --from-file FILE or pipe JSON")
		}
		return readBounded(os.Stdin, profilepkg.MaxBytes)
	}
	return readFileBounded(path, profilepkg.MaxBytes)
}

func profileSuggestionReviewText(result profilepkg.SuggestionReview) string {
	var b strings.Builder
	fmt.Fprintf(&b, "suggestion_hash: %s\npreviously_rejected: %t\ncurrent_hash: %s\ncandidate_hash: %s",
		result.SuggestionHash, result.PreviouslyRejected, result.Preview.CurrentHash, result.Preview.CandidateHash)
	for _, evidence := range result.Evidence {
		fmt.Fprintf(&b, "\nevidence: %s — %s", evidence.Source, evidence.Reason)
	}
	candidate, _ := json.MarshalIndent(result.Preview.NormalizedCandidate, "", "  ")
	fmt.Fprintf(&b, "\nnormalized_candidate:\n%s", candidate)
	return b.String()
}
