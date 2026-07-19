// Command check-skill-routing validates the provider-neutral skill registry,
// its synthetic routing corpus, and the source metadata catalog without model
// or backend access. Successful output is aggregate-only.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/isukharev/atl/internal/skillmeta"
	"github.com/isukharev/atl/internal/skillrouting"
)

func main() {
	root := flag.String("root", ".", "repository root")
	flag.Parse()
	if err := run(*root, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "check-skill-routing:", err)
		os.Exit(1)
	}
}

func run(root string, output io.Writer) error {
	summary, err := validateRepository(root)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(summary)
}

func validateRepository(root string) (skillrouting.Summary, error) {
	sourceRoot := filepath.Join(root, "skills-src")
	catalog, err := skillmeta.LoadSource(sourceRoot)
	if err != nil {
		return skillrouting.Summary{}, err
	}
	registry, err := skillrouting.LoadRegistryFile(filepath.Join(sourceRoot, skillmeta.RoutingFileName))
	if err != nil {
		return skillrouting.Summary{}, err
	}
	corpus, err := skillrouting.LoadCorpusFile(filepath.Join(root, "benchmarks", "agent-eval", "skill-routing.v1.json"))
	if err != nil {
		return skillrouting.Summary{}, err
	}
	return validateContracts(catalog, registry, corpus)
}

func validateContracts(catalog skillmeta.Catalog, registry skillrouting.Registry, corpus skillrouting.Corpus) (skillrouting.Summary, error) {
	return skillrouting.ValidateCatalog(catalog, registry, corpus)
}
