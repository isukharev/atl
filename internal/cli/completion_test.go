package cli

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestFixedComp(t *testing.T) {
	f := fixedComp("json", "text", "id")
	got, dir := f(nil, nil, "")
	if len(got) != 3 || got[0] != "json" || got[2] != "id" {
		t.Errorf("values = %v, want [json text id]", got)
	}
	if dir != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("directive = %v, want NoFileComp", dir)
	}
}

// The root command registers value completion for -o/--output so a shell
// completes json/text/id rather than filenames.
func TestRootRegistersOutputCompletion(t *testing.T) {
	root := newRoot()
	f := root.GetFlagCompletionFunc
	comp, ok := f("output")
	if !ok || comp == nil {
		t.Fatal("no completion registered for --output")
	}
	got, _ := comp(root, nil, "")
	if len(got) != 3 {
		t.Errorf("output completion = %v, want 3 values", got)
	}
}
