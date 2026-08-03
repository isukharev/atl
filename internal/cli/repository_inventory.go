package cli

import "sort"

// RepositoryCommand describes one executable CLI leaf for repository
// conformance tools. It is not a user-facing output contract; commandRegistry
// remains the single source of truth.
type RepositoryCommand struct {
	Path            string
	Access          string
	MutationProfile string
	RequiredFlags   []string
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
		})
	}
	sort.Slice(commands, func(i, j int) bool { return commands[i].Path < commands[j].Path })
	return commands, nil
}
