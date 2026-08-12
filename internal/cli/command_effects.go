package cli

import (
	"fmt"
	"sort"
	"strings"

	capabilitydef "github.com/isukharev/atl/internal/capability"
	"github.com/isukharev/atl/internal/domain"
)

const commandEffectCatalogSchemaVersion = 1

type commandEffectSelection struct {
	Command string `json:"command,omitempty"`
	Count   int    `json:"count"`
}

type commandEffect struct {
	Command         string   `json:"command"`
	EffectProfile   string   `json:"effect_profile"`
	Access          string   `json:"access"`
	OutputModes     []string `json:"output_modes"`
	MutationProfile string   `json:"mutation_profile,omitempty"`
	CapabilityIDs   []string `json:"capability_ids,omitempty"`
}

type commandEffectCatalog struct {
	SchemaVersion int                           `json:"schema_version"`
	Enforcement   string                        `json:"enforcement"`
	Selection     commandEffectSelection        `json:"selection"`
	Profiles      []capabilitydef.EffectProfile `json:"profiles"`
	Commands      []commandEffect               `json:"commands"`
}

func buildCommandEffectCatalog(selection commandEffectSelection) (commandEffectCatalog, error) {
	commands, err := RepositoryCommandInventory()
	if err != nil {
		return commandEffectCatalog{}, err
	}
	capabilityIDs := map[string][]string{}
	for _, definition := range capabilitydef.Definitions() {
		capabilityIDs[definition.CLICommand] = append(capabilityIDs[definition.CLICommand], definition.ID)
	}

	items := make([]commandEffect, 0, len(commands))
	usedProfiles := map[string]bool{}
	for _, command := range commands {
		if selection.Command != "" && command.Path != selection.Command {
			continue
		}
		ids := append([]string(nil), capabilityIDs[command.Path]...)
		sort.Strings(ids)
		items = append(items, commandEffect{
			Command: command.Path, EffectProfile: command.EffectProfile,
			Access: command.Access, OutputModes: append([]string(nil), command.OutputModes...),
			MutationProfile: command.MutationProfile, CapabilityIDs: ids,
		})
		usedProfiles[command.EffectProfile] = true
	}
	if len(items) == 0 && selection.Command != "" {
		return commandEffectCatalog{}, fmt.Errorf("%w: command %q has no executable effect profile", domain.ErrNotFound, selection.Command)
	}
	profiles := make([]capabilitydef.EffectProfile, 0, len(usedProfiles))
	for _, profile := range capabilitydef.EffectProfiles() {
		if usedProfiles[profile.ID] {
			profiles = append(profiles, profile)
		}
	}
	selection.Count = len(items)
	return commandEffectCatalog{
		SchemaVersion: commandEffectCatalogSchemaVersion,
		Enforcement:   "informational",
		Selection:     selection,
		Profiles:      profiles,
		Commands:      items,
	}, nil
}

func commandEffectCatalogText(catalog commandEffectCatalog) string {
	var b strings.Builder
	b.WriteString("| Command | Access | Effect profile | Capability IDs |\n")
	b.WriteString("| --- | --- | --- | --- |\n")
	for _, command := range catalog.Commands {
		fmt.Fprintf(&b, "| `atl %s` | %s | `%s` | %s |\n",
			command.Command, command.Access, command.EffectProfile, strings.Join(command.CapabilityIDs, ", "))
	}
	return strings.TrimRight(b.String(), "\n")
}
