// Package mcpserver exposes a deliberately small, read-only MCP transport over
// atl's application services. It never shells back into the CLI and registers
// no mutation or arbitrary filesystem tool.
package mcpserver

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/isukharev/atl/internal/app"
	"github.com/isukharev/atl/internal/compose"
	"github.com/isukharev/atl/internal/domain"
)

type JiraReader interface {
	FieldCatalog(context.Context, app.JiraFieldCatalogOpts) (*app.JiraFieldCatalogResult, error)
	IssueFieldEvidence(context.Context, string, app.JiraIssueFieldEvidenceOpts) (*app.JiraIssueFieldEvidenceResult, error)
	IssueGraphWithOptions(context.Context, string, app.JiraIssueGraphOptions) (*app.JiraIssueGraphResult, error)
	IssueRefs(context.Context, app.JiraIssueRefsOpts) (*app.JiraIssueRefsResult, error)
	HistoryFiltered(context.Context, string, app.JiraHistoryOpts) (*app.JiraHistoryResult, error)
	SearchIssueListView(context.Context, string, []string, string, int, string) (*app.IssueList, error)
	EpicDigest(context.Context, string, app.JiraEpicDigestOpts) (*app.JiraEpicDigestResult, error)
	BoardSnapshot(context.Context, int, app.BoardSnapshotOpts) (*app.BoardSnapshot, error)
	Structure(context.Context, int64) (*domain.Structure, error)
	StructureSnapshot(context.Context, int64, app.StructureSnapshotOpts) (*app.StructureSnapshot, error)
}

type ConfluenceReader interface {
	SearchQualified(context.Context, string, int, string) (*app.ConfluenceSearchResult, error)
	ResolvePageReference(context.Context, string) (*app.ConfluencePageResolution, error)
	PageMetadata(context.Context, string) (*app.ConfluencePageMetadataResult, error)
	PageOutline(context.Context, string) (*app.ConfluencePageOutlineResult, error)
	PageSection(context.Context, string, app.ConfluencePageSectionOpts) (*app.ConfluencePageSectionResult, error)
	PageSections(context.Context, string, app.ConfluencePageSectionsOpts) (*app.ConfluencePageSectionsResult, error)
	SummarizeTablesWithOptions(context.Context, string, int, app.ConfluenceTableReadOpts) (*app.ConfluenceTableSummary, error)
	ExtractTablesWithOptions(context.Context, string, int, app.ConfluenceTableReadOpts) (*app.ConfluenceTableExtract, error)
	AttachmentInventory(context.Context, string, app.ConfluenceAttachmentInventoryOpts) (*app.ConfluenceAttachmentInventoryResult, error)
	CommentInventory(context.Context, string, app.ConfluenceCommentInventoryOpts) (*app.ConfluenceCommentInventoryResult, error)
	CommentThreadWithOptions(context.Context, string, string, app.ConfluenceCommentThreadOpts) (*app.ConfluenceCommentInventoryResult, error)
}

// Dependencies are lazy so one unconfigured backend does not prevent MCP
// initialization or use of the configured sibling backend.
type Dependencies struct {
	Jira       func() (JiraReader, error)
	Confluence func() (ConfluenceReader, error)
	MirrorRoot func() (string, error)
}

func ProductionDependencies(version string) Dependencies {
	return Dependencies{
		Jira: func() (JiraReader, error) {
			return compose.LoadJira(version)
		},
		Confluence: func() (ConfluenceReader, error) {
			return compose.LoadConfluence(version)
		},
		MirrorRoot: func() (string, error) {
			root := strings.TrimSpace(os.Getenv("ATL_MIRROR_ROOT"))
			if root == "" {
				return "", fmt.Errorf("%w: ATL_MIRROR_ROOT is required for mirror snapshot tools", domain.ErrConfig)
			}
			return root, nil
		},
	}
}

// New constructs a protocol server. Every tool is added explicitly: the list
// itself is the security boundary, not a string filter over CLI commands.
func New(version string, deps Dependencies) *mcp.Server {
	return NewForService(version, deps, ServiceDefault)
}

// NewForService constructs one of the closed MCP service profiles. The
// explicit switch is the capability boundary: callers cannot turn an arbitrary
// list of names into registered tools.
func NewForService(version string, deps Dependencies, profile ServiceProfile) *mcp.Server {
	return NewForServiceWithRuntime(version, deps, profile, defaultRuntimeSnapshot())
}

// NewForServiceWithRuntime constructs a closed service profile with the
// invocation's already-captured runtime safety projection.
func NewForServiceWithRuntime(version string, deps Dependencies, profile ServiceProfile, runtime RuntimeSnapshot) *mcp.Server {
	if !runtime.valid() {
		panic("invalid MCP runtime snapshot")
	}
	if strings.TrimSpace(version) == "" {
		version = "dev"
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "atl", Version: version}, &mcp.ServerOptions{
		Instructions: instructionsForService(profile),
		Capabilities: &mcp.ServerCapabilities{},
	})
	server.AddReceivingMiddleware(normalizeSDKSchemaValidationErrors)
	server.AddReceivingMiddleware(privateRuntimeResourceCache)
	registerCapabilitiesResource(server)
	registerRuntimeResource(server, profile, runtime)
	switch profile {
	case ServiceDefault:
		registerJiraTools(server, deps)
		registerConfluenceTools(server, deps)
		registerMirrorTools(server, deps)
	case ServiceJira:
		registerJiraTools(server, deps)
		registerJiraMirrorTool(server, deps)
	case ServiceConfluence:
		registerConfluenceTools(server, deps)
		registerConfluenceMirrorTool(server, deps)
	case ServiceOffline:
		registerMirrorTools(server, deps)
	default:
		panic(fmt.Sprintf("unsupported MCP service profile %q", profile))
	}
	return server
}

func jiraReader(deps Dependencies) (JiraReader, error) {
	if deps.Jira == nil {
		return nil, fmt.Errorf("%w: Jira is unavailable in this MCP server", domain.ErrConfig)
	}
	return deps.Jira()
}

func confluenceReader(deps Dependencies) (ConfluenceReader, error) {
	if deps.Confluence == nil {
		return nil, fmt.Errorf("%w: Confluence is unavailable in this MCP server", domain.ErrConfig)
	}
	return deps.Confluence()
}

func mirrorRoot(deps Dependencies) (string, error) {
	if deps.MirrorRoot == nil {
		return "", fmt.Errorf("%w: local mirror snapshots are unavailable in this MCP server", domain.ErrConfig)
	}
	configured, err := deps.MirrorRoot()
	if err != nil {
		return "", err
	}
	configured = strings.TrimSpace(configured)
	if configured == "" {
		return "", fmt.Errorf("%w: a configured mirror root is required", domain.ErrConfig)
	}
	abs, err := filepath.Abs(configured)
	if err != nil {
		return "", fmt.Errorf("%w: configured mirror root is invalid", domain.ErrConfig)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("%w: configured mirror root is unavailable", domain.ErrConfig)
	}
	info, err := os.Stat(real)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("%w: configured mirror root is not a directory", domain.ErrConfig)
	}
	marker, err := os.Lstat(filepath.Join(real, ".atl"))
	if err != nil || !marker.IsDir() || marker.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%w: configured mirror root has no valid .atl directory", domain.ErrConfig)
	}
	return real, nil
}
