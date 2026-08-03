// Command check-docs-catalog validates the maintained public Markdown catalog.
package main

import (
	"flag"
	"fmt"
	"os"
)

type report struct {
	Documents int
	Excluded  int
	Links     int
}

func main() {
	root := flag.String("root", ".", "repository root")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "check-docs-catalog does not accept positional arguments")
		os.Exit(2)
	}
	result, err := validateRepository(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("documentation catalog: %d maintained documents, %d explicit exclusions, %d validated local links\n",
		result.Documents, result.Excluded, result.Links)
}
