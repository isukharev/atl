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

type profileShowResult struct {
	Exists bool   `json:"exists"`
	Path   string `json:"path"`
	Hash   string `json:"hash"`
	Data   any    `json:"data,omitempty"`
}

type profileGuidanceResult struct {
	Configured    bool     `json:"configured"`
	SchemaVersion int      `json:"schema_version,omitempty"`
	Instructions  []string `json:"instructions"`
}

func newProfileCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "profile",
		Short: "Manage private, structured Atlassian workflow memory",
		Long: "Manage the schema-versioned private workflow profile under the ATL config directory.\n" +
			"Profiles contain no credentials, but may contain private field names and selectors; never commit or publish them.",
	}

	var section, service string
	show := &cobra.Command{
		Use:   "show",
		Short: "Show profile metadata or one explicitly selected section",
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, exists, hash, err := profilepkg.Read(config.Dir())
			if err != nil {
				return err
			}
			data, err := profileSlice(p, exists, section, service)
			if err != nil {
				return err
			}
			out := profileShowResult{Exists: exists, Path: profilepkg.Path(config.Dir()), Hash: hash, Data: data}
			return emit(cmd, out, func() string { return profileShowText(out, section) })
		},
	}
	show.Flags().StringVar(&section, "section", "", "profile data: all|schema|preferences|team_policy|render_defaults|selectors")
	show.Flags().StringVar(&service, "service", "", "narrow schema/selectors to jira|confluence")
	_ = show.RegisterFlagCompletionFunc("section", fixedComp("all", "schema", "preferences", "team_policy", "render_defaults", "selectors"))
	_ = show.RegisterFlagCompletionFunc("service", fixedComp("jira", "confluence"))

	var previewFile string
	preview := &cobra.Command{
		Use:   "preview",
		Short: "Validate and preview a profile candidate without writing",
		RunE: func(cmd *cobra.Command, _ []string) error {
			candidate, err := readProfileCandidate(previewFile)
			if err != nil {
				return err
			}
			result, err := profilepkg.BuildPreview(config.Dir(), candidate)
			if err != nil {
				return err
			}
			return emit(cmd, result, func() string { return profilePreviewText(result) })
		},
	}
	preview.Flags().StringVar(&previewFile, "from-file", "", "profile candidate JSON file, or - for stdin (required)")
	_ = preview.MarkFlagRequired("from-file")

	var applyFile, candidateHash, currentHash string
	apply := &cobra.Command{
		Use:   "apply",
		Short: "Apply the exact candidate/current hashes returned by preview",
		RunE: func(cmd *cobra.Command, _ []string) error {
			candidate, err := readProfileCandidate(applyFile)
			if err != nil {
				return err
			}
			result, err := profilepkg.Apply(config.Dir(), candidate, candidateHash, currentHash)
			if err != nil {
				return err
			}
			return emit(cmd, result, func() string {
				if result.Changed {
					return "profile applied: " + result.ProfileHash
				}
				return "profile already current: " + result.ProfileHash
			})
		},
	}
	apply.Flags().StringVar(&applyFile, "from-file", "", "same profile candidate JSON reviewed by preview (required)")
	apply.Flags().StringVar(&candidateHash, "candidate-hash", "", "candidate_hash returned by preview (required)")
	apply.Flags().StringVar(&currentHash, "expected-current-hash", "", "current_hash returned by preview (required)")
	_ = apply.MarkFlagRequired("from-file")
	_ = apply.MarkFlagRequired("candidate-hash")
	_ = apply.MarkFlagRequired("expected-current-hash")

	guidance := &cobra.Command{
		Use:   "guidance",
		Short: "Emit compact workspace guidance without private profile data",
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, exists, _, err := profilepkg.Read(config.Dir())
			if err != nil {
				return err
			}
			out := profileGuidance(p, exists)
			return emit(cmd, out, func() string { return strings.Join(out.Instructions, "\n") })
		},
	}

	c.AddCommand(show, preview, apply, guidance)
	return c
}

func readProfileCandidate(path string) (profilepkg.Profile, error) {
	var (
		data []byte
		err  error
	)
	if path == "-" {
		if stdinIsTerminal() {
			return profilepkg.Profile{}, usageErr("stdin is a terminal and no profile candidate was piped; pass --from-file FILE or pipe JSON")
		}
		data, err = readBounded(os.Stdin, profilepkg.MaxBytes)
	} else {
		data, err = readFileBounded(path, profilepkg.MaxBytes)
	}
	if err != nil {
		return profilepkg.Profile{}, err
	}
	return profilepkg.DecodeStrict(data)
}

func profileSlice(p profilepkg.Profile, exists bool, section, service string) (any, error) {
	if service != "" && service != "jira" && service != "confluence" {
		return nil, usageErr("invalid --service %q (want jira|confluence)", service)
	}
	if !validProfileSection(section) {
		return nil, usageErr("invalid --section %q (want schema|preferences|team_policy|render_defaults|selectors)", section)
	}
	if service != "" && section != "schema" && section != "selectors" {
		return nil, usageErr("--service is valid only with --section schema or selectors")
	}
	if !exists {
		return nil, nil
	}
	switch section {
	case "":
		return nil, nil
	case "all":
		return p, nil
	case "schema":
		switch service {
		case "jira":
			return map[string]any{"jira_fields": p.Schema.JiraFields}, nil
		case "confluence":
			return map[string]any{"confluence_spaces": p.Schema.ConfluenceSpaces}, nil
		default:
			return p.Schema, nil
		}
	case "preferences":
		return p.Preferences, nil
	case "team_policy":
		return p.TeamPolicy, nil
	case "render_defaults":
		return p.RenderDefaults, nil
	case "selectors":
		switch service {
		case "jira":
			return map[string]any{"jira": p.Selectors.Jira}, nil
		case "confluence":
			return map[string]any{"confluence": p.Selectors.Confluence}, nil
		default:
			return p.Selectors, nil
		}
	default:
		return nil, usageErr("invalid --section %q (want all|schema|preferences|team_policy|render_defaults|selectors)", section)
	}
}

func validProfileSection(section string) bool {
	switch section {
	case "", "all", "schema", "preferences", "team_policy", "render_defaults", "selectors":
		return true
	default:
		return false
	}
}

func profileShowText(out profileShowResult, section string) string {
	if !out.Exists {
		return "profile: not configured\npath: " + out.Path + "\nmissing_hash: " + out.Hash
	}
	if section == "" {
		return fmt.Sprintf("profile: configured\npath: %s\nhash: %s\nuse --section to inspect data", out.Path, out.Hash)
	}
	data, _ := json.MarshalIndent(out.Data, "", "  ")
	return fmt.Sprintf("profile: configured\nsection: %s\nhash: %s\ndata:\n%s", section, out.Hash, data)
}

func profilePreviewText(out profilepkg.Preview) string {
	var b strings.Builder
	fmt.Fprintf(&b, "changed: %t\ncurrent_hash: %s\ncandidate_hash: %s", out.Changed, out.CurrentHash, out.CandidateHash)
	if out.MigrationFromSchemaVersion != nil {
		fmt.Fprintf(&b, "\nmigration_from_schema_version: %d", *out.MigrationFromSchemaVersion)
	}
	for _, section := range out.Sections {
		fmt.Fprintf(&b, "\n%s: %s", section.Section, section.Status)
	}
	candidate, _ := json.MarshalIndent(out.NormalizedCandidate, "", "  ")
	fmt.Fprintf(&b, "\nnormalized_candidate:\n%s", candidate)
	return b.String()
}

func profileGuidance(p profilepkg.Profile, exists bool) profileGuidanceResult {
	if !exists {
		return profileGuidanceResult{
			Instructions: []string{
				"No atl workflow profile is configured; run the onboarding skill before adding workspace guidance.",
			},
		}
	}
	return profileGuidanceResult{
		Configured:    true,
		SchemaVersion: p.SchemaVersion,
		Instructions: []string{
			"Use the private atl profile for Atlassian work; never copy it into the repository.",
			"Load only the needed slice with `atl profile show --section <section> [--service jira|confluence]`.",
			"Treat team_policy as authoritative, preferences as confirmed defaults, and schema facts as time-bounded evidence.",
		},
	}
}
