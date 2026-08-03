package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/domain"
)

const (
	textOutputAnnotation = "atl.output.text"
	idOutputAnnotation   = "atl.output.id"
)

func classifyOutputModes(cmd *cobra.Command, modes commandOutputMode) {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	if modes&commandOutputText != 0 {
		cmd.Annotations[textOutputAnnotation] = "supported"
	} else {
		cmd.Annotations[textOutputAnnotation] = "unsupported"
	}
	if modes&commandOutputID != 0 {
		cmd.Annotations[idOutputAnnotation] = "supported"
	} else {
		cmd.Annotations[idOutputAnnotation] = "unsupported"
	}
}

func enforceOutputContract(cmd *cobra.Command) error {
	path := strings.TrimPrefix(cmd.CommandPath(), "atl ")
	switch outputFormat {
	case "text":
		if cmd.Annotations[textOutputAnnotation] != "supported" {
			return usageErr("-o text is not supported for %q; use -o json", path)
		}
	case "id":
		if cmd.Annotations[idOutputAnnotation] != "supported" {
			return usageErr("-o id is not supported for %q; use -o json", path)
		}
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

func confluenceCommentInventoryText(result *app.ConfluenceCommentInventoryResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "page=%s\tversion=%d\tgated=%t\tcomplete=%t\tcomments_complete=%t\tthreads_complete=%t\tanchors_complete=%t\tcount=%d\troots=%d\n",
		textCell(result.PageID), result.PageVersion, result.PageVersionGated, result.Complete,
		result.CommentsComplete, result.ThreadsComplete, result.AnchorsComplete, result.Count, result.RootCount)
	if len(result.PartialReasons) > 0 {
		fmt.Fprintf(&b, "partial_reasons=%s\n", textCell(strings.Join(result.PartialReasons, ",")))
	}
	byID := make(map[string]app.ConfluenceCommentResultRecord, len(result.Comments))
	for _, comment := range result.Comments {
		byID[comment.ID] = comment
	}
	for _, comment := range result.Comments {
		depth := 0
		seen := map[string]struct{}{comment.ID: {}}
		parent := comment.ParentID
		for parent != nil && depth < len(result.Comments) {
			if _, duplicate := seen[*parent]; duplicate {
				break
			}
			seen[*parent] = struct{}{}
			ancestor, ok := byID[*parent]
			if !ok {
				break
			}
			depth++
			parent = ancestor.ParentID
		}
		indent := strings.Repeat("  ", depth)
		fmt.Fprintf(&b, "%s%s\t%s/%s\t%s", indent, textCell(comment.ID), comment.Location, comment.Resolution, comment.Relation)
		if comment.Anchor != nil {
			fmt.Fprintf(&b, "\tanchor=%s", comment.Anchor.Status)
		}
		if comment.Author.DisplayName != "" {
			fmt.Fprintf(&b, "\t%s", textCell(comment.Author.DisplayName))
		}
		if comment.CreatedAt != "" {
			fmt.Fprintf(&b, "\t%s", textCell(comment.CreatedAt))
		}
		b.WriteByte('\n')
		if comment.Body != "" {
			for _, line := range strings.Split(comment.Body, "\n") {
				fmt.Fprintf(&b, "%s  %s\n", indent, line)
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func jiraFieldsText(result *app.JiraFieldCatalogResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "complete=%t\tsource=%s\tcount=%d\ttotal=%d\n",
		result.Complete, textCell(result.Source), result.Count, result.Total)
	if result.PartialReason != "" {
		fmt.Fprintf(&b, "partial_reason=%s\n", textCell(result.PartialReason))
	}
	if result.Projection == "summary" {
		fmt.Fprintf(&b, "projection=summary\tcustom=%d\tsystem=%d\n", result.CustomCount, result.SystemCount)
	}
	for _, field := range result.Fields {
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
