package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	capabilitydef "github.com/isukharev/atl/internal/capability"
	"github.com/isukharev/atl/internal/domain"
)

const capabilityCatalogSchemaVersion = 1

type capability struct {
	ID           string   `json:"id"`
	TaskClass    string   `json:"task_class"`
	Service      string   `json:"service"`
	Role         string   `json:"role"`
	Priority     int      `json:"priority"`
	Summary      string   `json:"summary"`
	Command      string   `json:"command"`
	CLICommand   string   `json:"cli_command"`
	MCPTool      string   `json:"mcp_tool,omitempty"`
	MCPScope     string   `json:"mcp_scope,omitempty"`
	CLIOnly      bool     `json:"cli_only"`
	Access       string   `json:"access"`
	OutputModes  []string `json:"output_modes"`
	Evidence     string   `json:"evidence"`
	Completeness string   `json:"completeness"`
	Skill        string   `json:"skill"`
	Reference    string   `json:"reference"`
}

type capabilitySelection struct {
	Task    string `json:"task,omitempty"`
	Service string `json:"service,omitempty"`
	Access  string `json:"access,omitempty"`
	ID      string `json:"id,omitempty"`
	Count   int    `json:"count"`
}

type capabilityCatalog struct {
	SchemaVersion int                 `json:"schema_version"`
	Routing       capabilityRouting   `json:"routing"`
	Selection     capabilitySelection `json:"selection"`
	Capabilities  []capability        `json:"capabilities"`
}

type capabilityRouting struct {
	Match         string `json:"match"`
	ReferenceLoad string `json:"reference_load"`
	Stop          string `json:"stop"`
}

func newCapabilitiesCmd() *cobra.Command {
	var task, service, access, id string
	taskClasses := capabilitydef.TaskClasses()
	c := &cobra.Command{
		Use:   "capabilities",
		Short: "Query the versioned offline agent capability catalog",
		Long: "Query exact task-to-command routes without loading config, credentials, or network state.\n" +
			"The catalog is deterministic and derives access/output facts from the registered CLI tree.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			catalog, err := buildCapabilityCatalog(cmd.Root(), capabilitySelection{
				Task: strings.TrimSpace(task), Service: strings.TrimSpace(service),
				Access: strings.TrimSpace(access), ID: strings.TrimSpace(id),
			})
			if err != nil {
				return err
			}
			return emitID(cmd, catalog, func() string { return capabilityCatalogMarkdown(catalog) }, func() []string {
				ids := make([]string, len(catalog.Capabilities))
				for i := range catalog.Capabilities {
					ids[i] = catalog.Capabilities[i].ID
				}
				return ids
			})
		},
	}
	c.Flags().StringVar(&task, "task", "", fmt.Sprintf("exact task class (%s)", strings.Join(taskClasses, ", ")))
	c.Flags().StringVar(&service, "service", "", "exact service: jira|confluence")
	c.Flags().StringVar(&access, "access", "", "exact access class: read-only|mutating")
	c.Flags().StringVar(&id, "id", "", "exact capability id")
	_ = c.RegisterFlagCompletionFunc("task", fixedComp(taskClasses...))
	_ = c.RegisterFlagCompletionFunc("service", fixedComp("jira", "confluence"))
	_ = c.RegisterFlagCompletionFunc("access", fixedComp("read-only", "mutating"))
	return c
}

func buildCapabilityCatalog(root *cobra.Command, selection capabilitySelection) (capabilityCatalog, error) {
	if selection.Service != "" && selection.Service != "jira" && selection.Service != "confluence" {
		return capabilityCatalog{}, usageErr("invalid capability service %q (want jira|confluence)", selection.Service)
	}
	if selection.Access != "" && selection.Access != "read-only" && selection.Access != "mutating" {
		return capabilityCatalog{}, usageErr("invalid capability access %q (want read-only|mutating)", selection.Access)
	}

	definitions := capabilitydef.Definitions()
	items := make([]capability, 0, len(definitions))
	for _, definition := range definitions {
		if selection.Task != "" && definition.TaskClass != selection.Task {
			continue
		}
		if selection.Service != "" && definition.Service != selection.Service {
			continue
		}
		if selection.ID != "" && definition.ID != selection.ID {
			continue
		}
		command, remaining, err := root.Find(strings.Fields(definition.CLICommand))
		if err != nil || len(remaining) != 0 || command == nil || (command.Run == nil && command.RunE == nil) {
			return capabilityCatalog{}, fmt.Errorf("%w: capability %q references unregistered command %q", domain.ErrCheckFailed, definition.ID, definition.CLICommand)
		}
		commandAccess := command.Annotations[accessAnnotation]
		if commandAccess != "read-only" && commandAccess != "mutating" {
			return capabilityCatalog{}, fmt.Errorf("%w: capability %q command %q has invalid access metadata", domain.ErrCheckFailed, definition.ID, definition.CLICommand)
		}
		if selection.Access != "" && commandAccess != selection.Access {
			continue
		}
		modes := []string{"json"}
		if command.Annotations[textOutputAnnotation] == "supported" {
			modes = append(modes, "text")
		}
		if command.Annotations[idOutputAnnotation] == "supported" {
			modes = append(modes, "id")
		}
		items = append(items, capability{
			ID: definition.ID, TaskClass: definition.TaskClass, Service: definition.Service,
			Role: definition.Role, Priority: definition.Priority, Summary: definition.Summary,
			Command: definition.CLICommand, CLICommand: definition.CLICommand,
			MCPTool: definition.MCPTool, MCPScope: definition.MCPScope, CLIOnly: definition.MCPTool == "",
			Access: commandAccess, OutputModes: modes,
			Evidence: definition.Evidence, Completeness: definition.Completeness,
			Skill: definition.Skill, Reference: definition.Reference,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].TaskClass != items[j].TaskClass {
			return items[i].TaskClass < items[j].TaskClass
		}
		if items[i].Priority != items[j].Priority {
			return items[i].Priority < items[j].Priority
		}
		return items[i].ID < items[j].ID
	})
	if len(items) == 0 && (selection.Task != "" || selection.ID != "") {
		return capabilityCatalog{}, fmt.Errorf("%w: no capability matches the exact selection", domain.ErrNotFound)
	}
	selection.Count = len(items)
	return capabilityCatalog{
		SchemaVersion: capabilityCatalogSchemaVersion,
		Routing: capabilityRouting{
			Match:         "exact",
			ReferenceLoad: "invoke capability.skill, then open capability.reference relative to that skill; do not search the filesystem",
			Stop:          "stop expanding the route when sufficient complete evidence is available",
		},
		Selection: selection, Capabilities: items,
	}, nil
}

func capabilityCatalogMarkdown(catalog capabilityCatalog) string {
	var b strings.Builder
	b.WriteString("| Capability | Role | Access | Command | Evidence | Reference |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- |\n")
	for _, item := range catalog.Capabilities {
		fmt.Fprintf(&b, "| `%s` | %s | %s | `atl %s` | %s/%s | `%s/%s` |\n",
			item.ID, item.Role, item.Access, item.Command, item.Evidence, item.Completeness, item.Skill, item.Reference)
	}
	return strings.TrimRight(b.String(), "\n")
}
