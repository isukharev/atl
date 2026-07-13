package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/domain"
)

const textOutputAnnotation = "atl.output.text"

// textOutputCommandPaths is the reviewed inventory of executable commands that
// intentionally support -o text. Commands absent from this set are JSON/id
// only and are rejected by the root preflight before config, stdin, or network
// access. Adding a command to knownCommandPaths therefore cannot accidentally
// inherit the old JSON fallback.
var textOutputCommandPaths = stringSetFromLines(`
auth login
auth status
completion bash
completion fish
completion powershell
completion zsh
conf apply
conf attachment get
conf attachment list
conf blog create
conf comment list
conf diff
conf edit
conf me
conf page get
conf page history
conf page labels add
conf page labels list
conf page labels remove
conf page list
conf page meta
conf page move
conf page open
conf page outline
conf page resolve
conf page section
conf page title set
conf page view
conf plan apply
conf plan create
conf pull
conf push
conf render
conf search
conf space tree
conf status
conf table extract
config show
help
jira apply
jira board backlog
jira board config
jira board export
jira board get
jira board issues
jira board list
jira board view
jira epic digest
jira export
jira export diff
jira field-options
jira fields
jira issue assign
jira issue attachment get
jira issue attachment list
jira issue check
jira issue children
jira issue comment list
jira issue create
jira issue edit
jira issue field set
jira issue fields
jira issue get
jira issue history
jira issue link list
jira issue link suggest
jira issue plan apply
jira issue refs
jira issue search
jira issue tree
jira issue view
jira issue watchers add
jira issue watchers list
jira issue watchers remove
jira issue worklog add
jira issue worklog list
jira link-types
jira me
jira planning report
jira pull
jira push
jira quality-report
jira render
jira sprint current
jira sprint get
jira sprint issues
jira sprint list
jira status
jira structure export
jira structure folders
jira structure forest
jira structure get
jira structure pull-issues
jira structure rows
jira structure view
jira transitions
jira user get
jira user search
manifest create
profile apply
profile guidance
profile preview
profile revalidate
profile revalidation status
profile show
profile suggest
profile suggestion apply
profile suggestion reject
profile suggestion review
version
`)

func classifyTextOutput(cmd *cobra.Command, path string) {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	if textOutputCommandPaths[path] {
		cmd.Annotations[textOutputAnnotation] = "supported"
	} else {
		cmd.Annotations[textOutputAnnotation] = "unsupported"
	}
}

func enforceOutputContract(cmd *cobra.Command) error {
	if outputFormat != "text" {
		return nil
	}
	if cmd.Annotations[textOutputAnnotation] != "supported" {
		return usageErr("-o text is not supported for %q; use -o json", strings.TrimPrefix(cmd.CommandPath(), "atl "))
	}
	return nil
}

func confluencePageMetaText(meta *domain.PageMeta) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\tv%d\t%s\t%s\n", textCell(meta.ID), meta.Version, textCell(meta.Space), textCell(meta.Title))
	if meta.Updated != "" {
		fmt.Fprintf(&b, "updated\t%s\n", textCell(meta.Updated))
	}
	if len(meta.Ancestors) > 0 {
		fmt.Fprintf(&b, "ancestors\t%s\n", textCell(strings.Join(meta.Ancestors, " > ")))
	}
	if len(meta.Labels) > 0 {
		fmt.Fprintf(&b, "labels\t%s\n", textCell(strings.Join(meta.Labels, ", ")))
	}
	restricted := "unknown"
	if meta.Restrictions != nil {
		restricted = fmt.Sprintf("%t", *meta.Restrictions)
	}
	fmt.Fprintf(&b, "restricted\t%s", restricted)
	if meta.URL != "" {
		fmt.Fprintf(&b, "\nurl\t%s", textCell(meta.URL))
	}
	return b.String()
}

func confluenceVersionsText(versions []domain.Version) string {
	var b strings.Builder
	for _, version := range versions {
		fmt.Fprintf(&b, "%d\t%s\t%s", version.Number, textCell(version.When), textCell(version.By))
		if version.Message != "" {
			fmt.Fprintf(&b, "\t%s", textCell(version.Message))
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func commentsText(comments []domain.Comment) string {
	var b strings.Builder
	for _, comment := range comments {
		fmt.Fprintf(&b, "%s\t%s (%s):\n%s\n\n", textCell(comment.ID), textCell(comment.Author), textCell(comment.Created), comment.Body)
	}
	return strings.TrimRight(b.String(), "\n")
}

func jiraFieldsText(fields []domain.FieldDef) string {
	var b strings.Builder
	for _, field := range fields {
		fmt.Fprintf(&b, "%s\t%s\tcustom=%t", textCell(field.ID), textCell(field.Name), field.Custom)
		if field.Schema != "" {
			fmt.Fprintf(&b, "\tschema=%s", textCell(field.Schema))
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func stringLines(values []string) string {
	clean := make([]string, len(values))
	for i, value := range values {
		clean[i] = textCell(value)
	}
	return strings.Join(clean, "\n")
}

func jiraTransitionsText(transitions []domain.TransitionDef) string {
	var b strings.Builder
	for _, transition := range transitions {
		fmt.Fprintf(&b, "%s\t%s\t%s\n", textCell(transition.ID), textCell(transition.To), textCell(transition.Name))
	}
	return strings.TrimRight(b.String(), "\n")
}

func textCell(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
