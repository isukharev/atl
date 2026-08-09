package cli

import (
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/csf"
	"github.com/isukharev/atl/internal/domain"
)

func confPageCmd() *cobra.Command {
	c := &cobra.Command{Use: "page", Short: "Page get/view/title/meta/history/create/move/delete"}
	resolve := &cobra.Command{
		Use:   "resolve <ID-OR-URL>",
		Short: "Resolve a safe page reference to its stable content id",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := confService(cmd)
			if err != nil {
				return err
			}
			result, err := svc.ResolvePageReference(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return emitID(cmd, result, func() string { return result.ID }, func() []string { return []string{result.ID} })
		},
	}
	outline := &cobra.Command{
		Use:   "outline <ID-OR-URL>",
		Short: "List structural page headings without rendering the full body",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := confService(cmd)
			if err != nil {
				return err
			}
			result, err := svc.PageOutline(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return emit(cmd, result, func() string { return app.ConfluenceOutlineMarkdown(result) })
		},
	}
	var sectionHeading string
	var sectionOccurrence, sectionMaxBytes, sectionExpectedVersion int
	section := &cobra.Command{
		Use:   "section <ID-OR-URL>",
		Short: "Render one structurally bounded page section as Markdown",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := confService(cmd)
			if err != nil {
				return err
			}
			result, err := svc.PageSection(cmd.Context(), args[0], app.ConfluencePageSectionOpts{
				Heading: sectionHeading, Occurrence: sectionOccurrence, MaxBytes: sectionMaxBytes,
				ExpectedPageVersion: sectionExpectedVersion,
			})
			if err != nil {
				return err
			}
			if invocationRuntimeFor(cmd).outputFormat == "text" {
				_, err := io.WriteString(cmd.OutOrStdout(), result.Markdown)
				return err
			}
			return emit(cmd, result, nil)
		},
	}
	section.Flags().StringVar(&sectionHeading, "heading", "", "exact heading text (case/whitespace normalized)")
	section.Flags().IntVar(&sectionOccurrence, "occurrence", 0, "1-based occurrence when the heading is duplicated")
	section.Flags().IntVar(&sectionMaxBytes, "max-bytes", 256<<10, "maximum Markdown bytes (1..1048576; truncates at block boundary)")
	section.Flags().IntVar(&sectionExpectedVersion, "expected-version", 0, "refuse the read unless the page is at this exact version, e.g. the version `conf page outline` returned (0 leaves the read ungated)")
	var sectionHeadings, sectionOccurrences []string
	var sectionsMaxBytes, sectionsExpectedVersion int
	sections := &cobra.Command{
		Use:   "sections <ID-OR-URL>",
		Short: "Render ordered page sections from one bounded page read",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			selectors, err := confluencePageSectionSelectors(sectionHeadings, sectionOccurrences)
			if err != nil {
				return err
			}
			svc, err := confService(cmd)
			if err != nil {
				return err
			}
			result, err := svc.PageSections(cmd.Context(), args[0], app.ConfluencePageSectionsOpts{
				Selectors: selectors, MaxBytes: sectionsMaxBytes,
				ExpectedPageVersion: sectionsExpectedVersion,
			})
			if err != nil {
				return err
			}
			if invocationRuntimeFor(cmd).outputFormat == "text" {
				for _, selected := range result.Sections {
					if _, err := io.WriteString(cmd.OutOrStdout(), selected.Markdown); err != nil {
						return err
					}
				}
				return nil
			}
			return emit(cmd, result, nil)
		},
	}
	sections.Flags().StringArrayVar(&sectionHeadings, "heading", nil, "exact heading text in output order (repeatable; maximum 32)")
	sections.Flags().StringArrayVar(&sectionOccurrences, "occurrence", nil, "matching 1-based occurrence for each --heading (repeatable; 0 selects a unique heading)")
	sections.Flags().IntVar(&sectionsMaxBytes, "max-bytes", 256<<10, "aggregate maximum Markdown bytes (1..1048576; allocated in request order)")
	sections.Flags().IntVar(&sectionsExpectedVersion, "expected-version", 0, "refuse the read unless the page is at this exact version, e.g. the version `conf page outline` returned (0 leaves the read ungated)")
	var id, format string
	get := &cobra.Command{
		Use:   "get",
		Short: "Print a page body (csf|view)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if id == "" {
				return usageErr("--id is required")
			}
			if format != "csf" && format != "view" {
				return usageErr("--format must be csf or view")
			}
			svc, err := confService(cmd)
			if err != nil {
				return err
			}
			page, err := svc.Get(cmd.Context(), id, format)
			if err != nil {
				return err
			}
			// Body is text; print raw for piping.
			if invocationRuntimeFor(cmd).outputFormat == "text" {
				fmt.Fprintln(cmd.OutOrStdout(), string(page.Body))
				return nil
			}
			return emit(cmd, map[string]any{
				"id": page.ID, "title": page.Title, "space": page.SpaceKey,
				"version": page.Version, "body": string(page.Body), "url": page.URL,
			}, nil)
		},
	}
	get.Flags().StringVar(&id, "id", "", "page id or supported same-origin URL")
	get.Flags().StringVar(&format, "format", "csf", "csf|view")
	_ = get.RegisterFlagCompletionFunc("format", fixedComp("csf", "view"))

	view := confPageViewCmd()
	titleCmd := confPageTitleCmd()
	labelsCmd := confPageLabelsCmd()

	var metaID string
	meta := &cobra.Command{
		Use:   "meta",
		Short: "Print page metadata (version/ancestors/labels/restrictions)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if metaID == "" {
				return usageErr("--id is required")
			}
			svc, err := confService(cmd)
			if err != nil {
				return err
			}
			m, err := svc.Meta(cmd.Context(), metaID)
			if err != nil {
				return err
			}
			return emit(cmd, m, func() string { return confluencePageMetaText(m) })
		},
	}
	meta.Flags().StringVar(&metaID, "id", "", "page id or supported same-origin URL")

	var histID string
	hist := &cobra.Command{
		Use:   "history",
		Short: "List page versions",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if histID == "" {
				return usageErr("--id is required")
			}
			svc, err := confService(cmd)
			if err != nil {
				return err
			}
			result, err := svc.History(cmd.Context(), histID)
			if err != nil {
				return err
			}
			return emit(cmd, result, func() string { return confluenceVersionsText(result.Versions) })
		},
	}
	hist.Flags().StringVar(&histID, "id", "", "page id or supported same-origin URL")

	var space, parent, title, fromFile, fromMD, createInto string
	var createRegister bool
	create := &cobra.Command{
		Use:   "create",
		Short: "Create a page (body = CSF via --from-file -, or markdown via --from-md)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if space == "" || title == "" {
				return usageErr("--space and --title are required")
			}
			if createRegister != (strings.TrimSpace(createInto) != "") {
				return usageErr("--register and a non-empty --into must be used together")
			}
			body, err := createBody(cmd, fromFile, fromMD)
			if err != nil {
				return err
			}
			if probs := csf.Validate(body); csf.HasErrors(probs) {
				// Emit the problems, but exit non-zero so an agent learns the page
				// was NOT created (previously this returned exit 0 — a silent no-op).
				_ = emit(cmd, map[string]any{"problems": probs}, nil)
				return fmt.Errorf("%w: CSF not well-formed (see problems); page not created", domain.ErrCheckFailed)
			}
			svc, err := confService(cmd)
			if err != nil {
				return err
			}
			if !createRegister {
				page, err := svc.Create(cmd.Context(), space, parent, title, body)
				if err != nil {
					return err
				}
				return emit(cmd, map[string]any{"id": page.ID, "title": page.Title, "version": page.Version, "url": page.URL}, nil)
			}
			page, registration, createErr := svc.CreateAndRegister(cmd.Context(), space, parent, title, body, createInto)
			if registration != nil {
				warnRender(cmd.ErrOrStderr(), registration.Warnings)
			}
			var emitErr error
			if page != nil {
				out := map[string]any{"id": page.ID, "title": page.Title, "version": page.Version, "url": page.URL, "registration": registration}
				emitErr = emit(cmd, out, nil)
			}
			return createdRegistrationResultErr(createErr, emitErr)
		},
	}
	create.Flags().StringVar(&space, "space", "", "space key")
	create.Flags().StringVar(&parent, "parent", "", "parent page id")
	create.Flags().StringVar(&title, "title", "", "page title")
	create.Flags().StringVar(&fromFile, "from-file", "-", "CSF body file or - for stdin")
	create.Flags().StringVar(&fromMD, "from-md", "", "markdown body file or - for stdin (converted to CSF; unsupported constructs are refused)")
	create.Flags().BoolVar(&createRegister, "register", false, "register the created page in the mirror named by --into from an authoritative readback")
	create.Flags().StringVar(&createInto, "into", "", "mirror root for explicit post-create registration (requires --register)")

	var moveParent, moveExpectedParent string
	var moveExpectedVersion int
	moveGuard := guardedWriteFlags{profile: guardedWriteMove}
	move := &cobra.Command{
		Use:   "move <ID>",
		Short: "Preview or apply a guarded page move",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(moveParent) == "" {
				return usageErr("--parent is required")
			}
			if moveGuard.apply && moveExpectedVersion <= 0 {
				return usageErr("--expected-version is required with --apply; run the dry-run first")
			}
			if moveGuard.apply && !cmd.Flags().Changed("expected-parent") {
				return usageErr("--expected-parent is required with --apply; use --expected-parent= for a top-level page")
			}
			if err := moveGuard.validate(); err != nil {
				return err
			}
			svc, err := confService(cmd)
			if err != nil {
				return err
			}
			res, moveErr := svc.MoveGuarded(cmd.Context(), args[0], app.ConfluenceMoveOpts{
				Parent: moveParent, ExpectedVersion: moveExpectedVersion,
				ExpectedParent: moveExpectedParent, ExpectedParentSet: cmd.Flags().Changed("expected-parent"),
				ExpectedProposalHash: moveGuard.expectedProposalHash, Apply: moveGuard.apply,
			})
			if res != nil {
				if emitErr := emit(cmd, res, func() string {
					return fmt.Sprintf("%s\t%s\tv%d\t%s\t%s", res.Status, res.ID, res.CurrentVersion, res.ProposalHash, res.Parent)
				}); emitErr != nil {
					return emitErr
				}
			}
			return moveErr
		},
	}
	move.Flags().StringVar(&moveParent, "parent", "", "new parent page id")
	move.Flags().IntVar(&moveExpectedVersion, "expected-version", 0, "reviewed current page version (required with --apply)")
	move.Flags().StringVar(&moveExpectedParent, "expected-parent", "", "reviewed current parent id; use --expected-parent= for top-level (required with --apply)")
	moveGuard.register(move)

	var delID, delConfirm string
	var delExpectedVersion int
	delGuard := guardedWriteFlags{profile: guardedWriteProposal}
	del := &cobra.Command{
		Use:   "delete",
		Short: "Preview or apply one reviewed page trash operation",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			svc, err := confService(cmd)
			if err != nil {
				return err
			}
			result, trashErr := svc.TrashPageGuarded(cmd.Context(), delID, app.ConfluencePageTrashOpts{
				Apply: delGuard.apply, Confirm: delConfirm, ExpectedVersion: delExpectedVersion,
				ExpectedProposalHash: delGuard.expectedProposalHash,
			})
			if result == nil {
				return trashErr
			}
			emitErr := emit(cmd, result, func() string { return app.ConfluencePageTrashText(result) })
			return guardedMutationResultErr(trashErr, emitErr, result.WriteAttempted, "page trash")
		},
	}
	del.Flags().StringVar(&delID, "id", "", "page id")
	del.Flags().IntVar(&delExpectedVersion, "expected-version", 0, "reviewed current page version (required with --apply)")
	del.Flags().StringVar(&delConfirm, "confirm", "", "must be exactly TRASH with --apply")
	delGuard.register(del)

	var listSpace, listStatus, listCursor string
	var listLimit int
	list := &cobra.Command{
		Use:   "list",
		Short: "List pages in a space",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if listSpace == "" {
				return usageErr("--space is required")
			}
			if err := validatePageLimit(listLimit, 100); err != nil {
				return err
			}
			q := buildSearchCQL(listSpace, "", "", "") + ` AND type = page`
			if listStatus != "" {
				q += ` AND status = ` + cqlEscape(listStatus)
			}
			svc, err := confService(cmd)
			if err != nil {
				return err
			}
			hits, next, err := svc.Search(cmd.Context(), q, listLimit, listCursor)
			if err != nil {
				return err
			}
			return emitID(cmd, map[string]any{"results": hits, "next_cursor": next}, func() string {
				var b strings.Builder
				for _, h := range hits {
					fmt.Fprintf(&b, "%s\tv%d\t%s\t%s\n", h.ID, h.Version, h.Space, h.Title)
				}
				return strings.TrimRight(b.String(), "\n")
			}, func() []string {
				ids := make([]string, len(hits))
				for i, h := range hits {
					ids[i] = h.ID
				}
				return ids
			})
		},
	}
	list.Flags().StringVar(&listSpace, "space", "", "space key")
	list.Flags().StringVar(&listStatus, "status", "", "current|archived|trashed")
	_ = list.RegisterFlagCompletionFunc("status", fixedComp("current", "archived", "trashed"))
	list.Flags().IntVar(&listLimit, "limit", 25, "max results (1..100)")
	list.Flags().StringVar(&listCursor, "cursor", "", "pagination cursor (start offset)")

	var openID string
	open := &cobra.Command{
		Use:   "open",
		Short: "Open a page in the system browser",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if openID == "" {
				return usageErr("--id is required")
			}
			svc, err := confService(cmd)
			if err != nil {
				return err
			}
			m, err := svc.Meta(cmd.Context(), openID)
			if err != nil {
				return err
			}
			if m.URL == "" {
				return fmt.Errorf("%w: page %s has no web URL", domain.ErrNotFound, openID)
			}
			if err := defaultBrowserOpener(cmd.Context(), m.URL); err != nil {
				return fmt.Errorf("open browser: %w", err)
			}
			return emit(cmd, map[string]string{"id": m.ID, "url": m.URL}, func() string {
				return m.URL
			})
		},
	}
	open.Flags().StringVar(&openID, "id", "", "page id or supported same-origin URL")

	var copyID, copyTitle, copySpace, copyParent, copyInto string
	var copyExpectedVersion int
	var copyRegister bool
	copyGuard := guardedWriteFlags{profile: guardedWriteProposal}
	cp := &cobra.Command{
		Use:   "copy",
		Short: "Preview or apply one reviewed page copy",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			svc, err := confService(cmd)
			if err != nil {
				return err
			}
			result, copyErr := svc.CopyPageGuarded(cmd.Context(), copyID, app.ConfluencePageCopyOpts{
				Title: copyTitle, Space: copySpace, Parent: copyParent,
				Register: copyRegister, Root: copyInto, Apply: copyGuard.apply,
				ExpectedVersion: copyExpectedVersion, ExpectedProposalHash: copyGuard.expectedProposalHash,
			})
			if result == nil {
				return copyErr
			}
			if result.Registration != nil {
				warnRender(cmd.ErrOrStderr(), result.Registration.Warnings)
			}
			var emitErr error
			if invocationRuntimeFor(cmd).outputFormat == "id" {
				if result.ID == "" {
					if copyErr != nil {
						return copyErr
					}
					return usageErr("-o id is available only with --apply after the created page id is known")
				}
				emitErr = emitID(cmd, result, nil, func() []string { return []string{result.ID} })
			} else {
				emitErr = emit(cmd, result, func() string { return app.ConfluencePageCopyText(result) })
			}
			return guardedMutationResultErr(copyErr, emitErr, result.WriteAttempted, "page copy")
		},
	}
	cp.Flags().StringVar(&copyID, "id", "", "source page id")
	cp.Flags().StringVar(&copyTitle, "title", "", "new page title")
	cp.Flags().StringVar(&copySpace, "space", "", "target space key (default: same as source)")
	cp.Flags().StringVar(&copyParent, "parent", "", "target parent page id (default: same as source)")
	cp.Flags().BoolVar(&copyRegister, "register", false, "register the copied page in the mirror named by --into from an authoritative readback")
	cp.Flags().StringVar(&copyInto, "into", "", "mirror root for explicit post-copy registration (requires --register)")
	cp.Flags().IntVar(&copyExpectedVersion, "expected-version", 0, "reviewed source page version (required with --apply)")
	copyGuard.register(cp)

	c.AddCommand(resolve, outline, section, sections, get, view, titleCmd, labelsCmd, meta, hist, list, open, cp, create, move, del)
	return c
}

func confluencePageSectionSelectors(headings, occurrences []string) ([]app.ConfluencePageSectionSelector, error) {
	if len(headings) == 0 {
		return nil, usageErr("at least one --heading is required")
	}
	if len(occurrences) != 0 && len(occurrences) != len(headings) {
		return nil, usageErr("when --occurrence is used, provide one value for every --heading")
	}
	selectors := make([]app.ConfluencePageSectionSelector, len(headings))
	for index, heading := range headings {
		selectors[index].Heading = heading
		if len(occurrences) == 0 {
			continue
		}
		occurrence, err := strconv.Atoi(occurrences[index])
		if err != nil || occurrence < 0 {
			return nil, usageErr("--occurrence values must be non-negative integers")
		}
		selectors[index].Occurrence = occurrence
	}
	return selectors, nil
}

func confPageViewCmd() *cobra.Command {
	var root, jiraView string
	var rf renderFlags
	cmd := &cobra.Command{
		Use:   "view <ID-OR-URL>",
		Short: "Render one page as configured Markdown without writing a mirror",
		Long: "Fetch native CSF and render one Confluence page through the configured Markdown view without writing mirror artifacts. " +
			"Default JSON contains stable page identity, version, and markdown; -o text emits raw Markdown. " +
			"Every region is read-only because this creates no writeback baseline: pull the page before editing it.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			override, err := rf.override()
			if err != nil {
				return err
			}
			configRoot, err := filepath.Abs(root)
			if err != nil {
				return err
			}
			if detected, ok := app.MirrorRootOf(configRoot); ok {
				configRoot = detected
			}
			svc, err := confService(cmd)
			if err != nil {
				return err
			}
			res, err := svc.ViewPage(cmd.Context(), args[0], app.ConfluencePageViewOpts{Root: configRoot, Render: override, JiraView: jiraView})
			if err != nil {
				return err
			}
			warnRender(cmd.ErrOrStderr(), res.Warnings)
			if invocationRuntimeFor(cmd).outputFormat == "text" {
				_, err := io.WriteString(cmd.OutOrStdout(), res.Markdown)
				return err
			}
			return emit(cmd, res, nil)
		},
	}
	cmd.Flags().StringVar(&root, "render-root", mirrorRootDefault("."), "root whose .atl/config.json supplies local render settings (never written)")
	cmd.Flags().StringVar(&jiraView, "jira-view", "", "named Jira list view for JQL macros (default: default; macro columns win)")
	rf.register(cmd)
	rf.registerConfluenceJiraMacros(cmd)
	return cmd
}
