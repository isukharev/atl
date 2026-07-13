package cli

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/csf"
)

func confBlogCmd() *cobra.Command {
	group := &cobra.Command{Use: "blog", Short: "Native Confluence blog-post operations"}
	group.AddCommand(confBlogCreateCmd())
	return group
}

func confBlogCreateCmd() *cobra.Command {
	var space, title, fromFile, fromMD string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a native blog post from CSF or strict Markdown",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			space = strings.TrimSpace(space)
			title = strings.TrimSpace(title)
			if space == "" || title == "" {
				return usageErr("--space and --title are required")
			}
			body, err := createBody(cmd, fromFile, fromMD)
			if err != nil {
				return err
			}
			if len(bytes.TrimSpace(body)) == 0 {
				return usageErr("blog post body must not be empty")
			}
			if problems := csf.Validate(body); csf.HasErrors(problems) {
				_ = emit(cmd, map[string]any{"problems": problems}, nil)
				return usageErr("CSF not well-formed (see problems); blog post not created")
			}
			svc, err := confService()
			if err != nil {
				return err
			}
			post, err := svc.CreateBlogPost(cmd.Context(), space, title, body)
			if err != nil {
				return err
			}
			result := map[string]any{
				"id": post.ID, "type": post.Type, "title": post.Title, "space": post.SpaceKey,
				"version": post.Version, "body_present": post.BodyPresent, "url": post.URL,
			}
			return emitID(cmd, result, func() string {
				return fmt.Sprintf("%s\tv%d\t%s\t%s\t%s", post.ID, post.Version, post.SpaceKey, post.Title, post.URL)
			}, func() []string { return []string{post.ID} })
		},
	}
	cmd.Flags().StringVar(&space, "space", "", "space key (required)")
	cmd.Flags().StringVar(&title, "title", "", "blog-post title (required)")
	cmd.Flags().StringVar(&fromFile, "from-file", "-", "CSF body file or - for stdin")
	cmd.Flags().StringVar(&fromMD, "from-md", "", "markdown body file or - for stdin (converted to CSF; unsupported constructs are refused)")
	return cmd
}
