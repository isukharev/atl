package app

import (
	"fmt"
	"strings"
)

// JiraIssueGraphMarkdown renders the same qualified facts as compact Markdown.
// Dynamic cells pass through the shared table escaper.
func JiraIssueGraphMarkdown(result *JiraIssueGraphResult) string {
	if result == nil {
		return ""
	}
	var out strings.Builder
	fmt.Fprintf(&out, "# Jira work-artifact graph\n\n")
	fmt.Fprintf(&out, "- Root: `%s`\n", result.RootID)
	fmt.Fprintf(&out, "- Complete: `%t`\n", result.Complete)
	fmt.Fprintf(&out, "- Depth: `%d` (expanded `%d`, followed `%d`)\n", result.Bounds.RequestedDepth, result.Bounds.ExpandedNodes, result.Bounds.FollowedNodes)
	fmt.Fprintf(&out, "- Transport: `%d/%d` attempts; `%d/%d` buffered response bytes\n",
		result.Bounds.RequestsUsed, result.Bounds.MaxRequests,
		result.Bounds.ResponseBytesUsed, result.Bounds.MaxResponseBytes)
	fmt.Fprintf(&out, "- Nodes: `%d`; edges: `%d`; evidence: `%d`; sources: `%d`\n\n",
		result.Summary.NodeCount, result.Summary.EdgeCount, result.Summary.EvidenceCount, result.Summary.SourceCount)
	if selection := result.SourceSelection; selection != nil {
		fmt.Fprintf(&out, "- Selected sources: `%s`\n- Omitted sources: `%s`\n", strings.Join(selection.Selected, ","), strings.Join(selection.Omitted, ","))
		fmt.Fprintf(&out, "- Snapshot fields: `%s`; properties: `%t`\n", strings.Join(selection.Snapshot.Fields, ","), selection.Snapshot.Properties)
		if selection.Snapshot.SupportingFieldsReason != "" {
			fmt.Fprintf(&out, "- Supporting fields: `%s`\n", selection.Snapshot.SupportingFieldsReason)
		}
		out.WriteString("\n")
	}

	includeFailure := false
	for _, source := range result.Sources {
		includeFailure = includeFailure || source.Failure != nil
	}
	sourceRows := make([][]string, 0, len(result.Sources))
	for _, source := range result.Sources {
		row := []string{
			source.Kind, string(source.Status), fmt.Sprint(source.Complete),
			fmt.Sprint(source.Count), fmt.Sprint(source.Truncated),
			string(source.Stability), source.PartialReason,
		}
		if includeFailure {
			class, status := "", ""
			if source.Failure != nil {
				class = string(source.Failure.Class)
				if source.Failure.HTTPStatus != nil {
					status = fmt.Sprint(*source.Failure.HTTPStatus)
				}
			}
			row = append(row, class, status)
		}
		depth := ""
		if source.NodeDepth != nil {
			depth = fmt.Sprint(*source.NodeDepth)
		}
		row = append([]string{source.NodeID, depth}, row...)
		sourceRows = append(sourceRows, row)
	}
	out.WriteString("## Sources\n\n")
	sourceHeader := []string{"Source", "Status", "Complete", "Count", "Truncated", "Stability", "Reason"}
	if includeFailure {
		sourceHeader = append(sourceHeader, "Failure", "HTTP status")
	}
	sourceHeader = append([]string{"Node", "Depth"}, sourceHeader...)
	out.WriteString(MarkdownTable(sourceHeader, sourceRows))
	if len(result.Frontier) > 0 {
		frontierRows := make([][]string, 0, len(result.Frontier))
		for _, item := range result.Frontier {
			frontierRows = append(frontierRows, []string{item.NodeID, fmt.Sprint(item.Depth), item.Reason})
		}
		out.WriteString("\n## Frontier\n\n")
		out.WriteString(MarkdownTable([]string{"Node", "Depth", "Reason"}, frontierRows))
	}
	out.WriteString("\n## Nodes\n\n")
	includeSCM := false
	for _, node := range result.Nodes {
		if node.SCM != nil {
			includeSCM = true
			break
		}
	}
	nodeRows := make([][]string, 0, len(result.Nodes))
	for _, node := range result.Nodes {
		row := []string{node.ID, node.Kind, string(node.State), fmt.Sprint(node.Depth), fmt.Sprint(node.Expanded), node.Label, ""}
		if node.Kind == "url" {
			row[len(row)-1] = node.URL
		}
		if includeSCM {
			host, project, selector, artifactState := "", "", "", ""
			if node.SCM != nil {
				host, project = node.SCM.Host, node.SCM.ProjectPath
				switch {
				case node.SCM.CommitSHA != "":
					selector = "commit:" + node.SCM.CommitSHA
				case node.SCM.BranchName != "":
					selector = "branch:" + node.SCM.BranchName
				case node.SCM.MergeRequestIID != "":
					selector = "merge_request:" + node.SCM.MergeRequestIID
					artifactState = node.SCM.MergeRequestState
				case node.Kind == "gitlab_project":
					selector = "project"
				}
			}
			row = append(row, host, project, selector, artifactState)
		}
		nodeRows = append(nodeRows, row)
	}
	nodeHeader := []string{"ID", "Kind", "State", "Depth", "Expanded", "Label", "URL"}
	if includeSCM {
		nodeHeader = append(nodeHeader, "Host", "Project", "Selector", "Artifact State")
	}
	out.WriteString(MarkdownTable(nodeHeader, nodeRows))
	out.WriteString("\n## Edges\n\n")
	edgeRows := make([][]string, 0, len(result.Edges))
	for _, edge := range result.Edges {
		edgeRows = append(edgeRows, []string{
			edge.From, edge.Kind, edge.RelationType, edge.Relation, edge.To,
			edge.Direction, edge.Confidence, fmt.Sprint(len(edge.Evidence)),
		})
	}
	out.WriteString(MarkdownTable(
		[]string{"From", "Kind", "Type", "Relation", "To", "Direction", "Confidence", "Evidence"},
		edgeRows,
	))
	return strings.TrimRight(out.String(), "\n")
}
