package cli

import (
	"fmt"
	"sort"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// RepositoryCommand describes one executable CLI leaf for repository
// conformance tools. It is not a user-facing output contract; commandRegistry
// remains the single source of truth.
type RepositoryCommand struct {
	Path            string
	Access          string
	MutationProfile string
	RequiredFlags   []string
	OutputModes     []string
}

const (
	RepositoryFlagVisible    = "visible"
	RepositoryFlagHidden     = "hidden"
	RepositoryFlagDeprecated = "deprecated"
	RepositoryFlagFramework  = "framework"
)

// RepositoryFlag describes one long-form flag on the finalized Cobra tree.
// Command is empty for root/global flags. Inherited root flags are recorded
// once as global flags rather than copied onto every executable leaf.
type RepositoryFlag struct {
	Command string
	Name    string
	Class   string
}

// RepositoryCommandInventory returns a deterministic copy of the reviewed
// command registry. Repository checks use it to prove that documentation and
// safety routes cover every executable leaf without maintaining a second
// command inventory.
func RepositoryCommandInventory() ([]RepositoryCommand, error) {
	if commandRegistryErr != nil {
		return nil, commandRegistryErr
	}
	commands := make([]RepositoryCommand, 0, len(commandRegistry.nodes))
	for path, registration := range commandRegistry.nodes {
		if registration.traits&commandLeaf == 0 {
			continue
		}
		access := "read-only"
		if registration.traits&commandMutating != 0 {
			access = "mutating"
		}
		commands = append(commands, RepositoryCommand{
			Path:            path,
			Access:          access,
			MutationProfile: string(registration.profile),
			RequiredFlags:   append([]string(nil), registration.requiredFlags...),
			OutputModes:     commandOutputModeNames(registration.outputModes),
		})
	}
	sort.Slice(commands, func(i, j int) bool { return commands[i].Path < commands[j].Path })
	return commands, nil
}

// RepositoryFlagInventory returns the deterministic root/global and
// command-local flag surface used by repository conformance checks. It builds
// the finalized Cobra tree without executing a command, loading config, or
// touching credentials or the network.
func RepositoryFlagInventory() ([]RepositoryFlag, error) {
	commands, err := RepositoryCommandInventory()
	if err != nil {
		return nil, err
	}
	wantCommands := make(map[string]bool, len(commands))
	for _, command := range commands {
		wantCommands[command.Path] = true
	}

	root := newRoot()
	root.InitDefaultHelpFlag()
	root.InitDefaultVersionFlag()

	seen := map[string]RepositoryFlag{}
	globalNames := map[string]bool{}
	add := func(command string, flag *pflag.Flag) error {
		entry := RepositoryFlag{Command: command, Name: flag.Name, Class: repositoryFlagClass(flag)}
		key := repositoryFlagKey(entry.Command, entry.Name)
		if prior, ok := seen[key]; ok {
			if prior != entry {
				return fmt.Errorf("flag %q on command %q has inconsistent classification", entry.Name, entry.Command)
			}
			return nil
		}
		seen[key] = entry
		return nil
	}
	var addErr error
	root.NonInheritedFlags().VisitAll(func(flag *pflag.Flag) {
		if addErr != nil {
			return
		}
		globalNames[flag.Name] = true
		addErr = add("", flag)
	})
	if addErr != nil {
		return nil, addErr
	}

	seenCommands := map[string]bool{}
	var walk func(*cobra.Command)
	walk = func(command *cobra.Command) {
		if addErr != nil {
			return
		}
		path := commandRegistryPath(root, command)
		if wantCommands[path] {
			seenCommands[path] = true
			command.InitDefaultHelpFlag()
			command.NonInheritedFlags().VisitAll(func(flag *pflag.Flag) {
				if addErr == nil {
					addErr = add(path, flag)
				}
			})
			command.InheritedFlags().VisitAll(func(flag *pflag.Flag) {
				if addErr == nil && !globalNames[flag.Name] {
					addErr = add(path, flag)
				}
			})
		}
		for _, child := range command.Commands() {
			walk(child)
		}
	}
	walk(root)
	if addErr != nil {
		return nil, addErr
	}
	for path := range wantCommands {
		if !seenCommands[path] {
			return nil, fmt.Errorf("command %q is missing from finalized flag inventory", path)
		}
	}

	flags := make([]RepositoryFlag, 0, len(seen))
	for _, entry := range seen {
		flags = append(flags, entry)
	}
	sort.Slice(flags, func(i, j int) bool {
		return repositoryFlagKey(flags[i].Command, flags[i].Name) < repositoryFlagKey(flags[j].Command, flags[j].Name)
	})
	return flags, nil
}

func repositoryFlagClass(flag *pflag.Flag) string {
	if flag.Name == "help" {
		return RepositoryFlagFramework
	}
	if flag.Deprecated != "" {
		return RepositoryFlagDeprecated
	}
	if flag.Hidden {
		return RepositoryFlagHidden
	}
	return RepositoryFlagVisible
}

func repositoryFlagKey(command, name string) string {
	return command + "\x00" + name
}
