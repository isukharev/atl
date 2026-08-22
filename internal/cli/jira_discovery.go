package cli

import (
	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
)

func jiraProjectCmd() *cobra.Command {
	c := &cobra.Command{Use: "project", Short: "Project discovery"}
	var includeArchived bool
	var limit int
	list := &cobra.Command{
		Use:   "list",
		Short: "List Jira projects visible to the authenticated user",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := validatePageLimit(limit, 1000); err != nil {
				return err
			}
			svc, err := jiraService(cmd)
			if err != nil {
				return err
			}
			result, err := svc.ListProjects(cmd.Context(), includeArchived, limit)
			if err != nil {
				return err
			}
			return emitID(cmd, result, func() string { return app.JiraProjectsMarkdown(result) }, func() []string {
				ids := make([]string, len(result.Projects))
				for i := range result.Projects {
					ids[i] = result.Projects[i].Key
				}
				return ids
			})
		},
	}
	list.Flags().BoolVar(&includeArchived, "include-archived", false, "include archived projects")
	list.Flags().IntVar(&limit, "limit", 200, "maximum projects to emit (1..1000)")
	c.AddCommand(list)
	return c
}

func jiraIssueTypesCmd() *cobra.Command {
	var project string
	c := &cobra.Command{
		Use:   "types",
		Short: "List issue types available for issue creation",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if project == "" {
				return usageErr("--project is required")
			}
			svc, err := jiraService(cmd)
			if err != nil {
				return err
			}
			result, err := svc.ListCreateIssueTypes(cmd.Context(), project)
			if err != nil {
				return err
			}
			return emitID(cmd, result, func() string { return app.JiraIssueTypesMarkdown(result) }, func() []string {
				ids := make([]string, len(result.IssueTypes))
				for i := range result.IssueTypes {
					ids[i] = result.IssueTypes[i].ID
				}
				return ids
			})
		},
	}
	c.Flags().StringVar(&project, "project", "", "Jira project key or id")
	return c
}

func jiraIssueCreateCheckCmd() *cobra.Command {
	var project, issueType string
	c := &cobra.Command{
		Use:   "create-check",
		Short: "Inspect create-screen fields for a project and issue type",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if project == "" || issueType == "" {
				return usageErr("--project and --type are required")
			}
			svc, err := jiraService(cmd)
			if err != nil {
				return err
			}
			result, err := svc.CheckCreateMetadata(cmd.Context(), project, issueType)
			if err != nil {
				return err
			}
			return emit(cmd, result, func() string { return app.JiraCreateCheckMarkdown(result) })
		},
	}
	c.Flags().StringVar(&project, "project", "", "Jira project key or id")
	c.Flags().StringVar(&issueType, "type", "", "exact issue type id or name")
	return c
}

func jiraIssueCreateMetadataCmd() *cobra.Command {
	var project, issueType string
	c := &cobra.Command{
		Use:   "create-metadata",
		Short: "Inspect bounded, qualified create-field metadata",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if project == "" || issueType == "" {
				return usageErr("--project and --type are required")
			}
			svc, err := jiraService(cmd)
			if err != nil {
				return err
			}
			result, err := svc.InspectCreateMetadata(cmd.Context(), project, issueType)
			if err != nil {
				return err
			}
			return emit(cmd, result, func() string { return app.JiraQualifiedCreateMetadataMarkdown(result) })
		},
	}
	c.Flags().StringVar(&project, "project", "", "Jira project key or id")
	c.Flags().StringVar(&issueType, "type", "", "exact issue type id or name")
	return c
}
