// Command check-reference-split generates and validates permanent compatibility
// indexes for Markdown references whose canonical prose has moved to split files.
package main

import (
	"flag"
	"fmt"
	"os"
)

type report struct {
	Indexes int
	Routes  int
	Written int
}

func main() {
	root := flag.String("root", ".", "repository root")
	manifestPath := flag.String("manifest", "docs/reference/split-map.v1.json", "reference split manifest path")
	write := flag.Bool("write", false, "write deterministic compatibility indexes")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "check-reference-split does not accept positional arguments")
		os.Exit(2)
	}

	result, err := checkReferenceSplit(*root, *manifestPath, *write)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if *write {
		fmt.Printf("reference split: %d compatibility indexes, %d routes, %d files written\n",
			result.Indexes, result.Routes, result.Written)
		return
	}
	if result.Written != 0 {
		panic("validation unexpectedly wrote compatibility indexes")
	}
	fmt.Printf("reference split: %d compatibility indexes, %d routes validated\n", result.Indexes, result.Routes)
}
