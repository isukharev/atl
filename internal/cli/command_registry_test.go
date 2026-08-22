package cli

import (
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// These reviewed path sets are deliberately independent of commandRegistry.
// They are behavioral oracles for the output contract, not production metadata.
var reviewedTextOutputCommandPaths = reviewedOutputPathSet(`
auth login
auth status
capabilities
compatibility status
completion bash
completion fish
completion powershell
completion zsh
conf apply
conf attachment get
conf attachment list
conf attachment search
conf blog create
conf comment add
conf comment list
conf comment preview
conf comment thread
conf diff
conf edit
conf me
conf page get
conf page history
conf page labels add
conf page labels list
conf page labels remove
conf page list
conf page meta
conf page move
conf page open
conf page outline
conf page resolve
conf page section
conf page sections
conf page title set
conf page view
conf plan apply
conf plan create
conf plan preview
conf pull
conf push
conf reconcile preview
conf reconcile stage
conf render
conf search
conf snapshot
conf space tree
conf status
conf table extract
conf table summary
config show
corpus build
corpus cache retention apply
corpus cache retention preview
corpus cache status
corpus diff
corpus export
corpus handoff
doctor
environment inspect
help
jira apply
jira attachment-bodies
jira board backlog
jira board config
jira board export
jira board get
jira board issues
jira board list
jira board view
jira epic digest
jira export
jira export diff
jira field-options
jira fields
jira issue assign
jira issue attachment get
jira issue attachment list
jira issue check
jira issue children
jira issue comment add
jira issue comment list
jira issue comment preview
jira issue create-check
jira issue create-metadata
jira issue edit
jira issue edit preview
jira issue field get
jira issue field preview
jira issue field set
jira issue fields
jira issue get
jira issue graph
jira issue history
jira issue link list
jira issue link suggest
jira issue plan apply
jira issue refs
jira issue reference search
jira issue search
jira issue tree
jira issue types
jira issue transition
jira issue transition preview
jira issue view
jira issue watchers add
jira issue watchers list
jira issue watchers remove
jira issue worklog add
jira issue worklog list
jira link-types
jira me
jira planning report
jira project list
jira pull
jira push
jira reconcile preview
jira reconcile stage
jira quality-report
jira render
jira sprint current
jira sprint get
jira sprint issues
jira sprint list
jira snapshot
jira status
jira structure export
jira structure folders
jira structure forest
jira structure get
jira structure pull-issues
jira structure rows
jira structure view
jira transitions
jira user get
jira user search
manifest create
mirror backend bind
mirror backend status
policy explain
policy show
profile apply
profile guidance
profile preview
profile revalidate
profile revalidation status
profile show
profile suggest
profile suggestion apply
profile suggestion reject
profile suggestion review
version
`)

var reviewedIDOutputCommandPaths = reviewedOutputPathSet(`
capabilities
conf attachment list
conf attachment search
conf blog create
conf page copy
conf page list
conf page resolve
conf search
jira board backlog
jira board config
jira board get
jira board issues
jira board list
jira board view
jira issue attachment list
jira issue children
jira issue comment list
jira issue create
jira issue link list
jira issue search
jira issue types
jira issue worklog list
jira me
jira project list
jira sprint current
jira sprint get
jira sprint issues
jira sprint list
jira structure folders
jira structure get
jira structure pull-issues
jira structure rows
jira structure view
jira user get
jira user search
`)

func reviewedOutputPathSet(value string) map[string]bool {
	out := map[string]bool{}
	for _, line := range strings.Split(value, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out[line] = true
		}
	}
	return out
}

func reviewedOutputModes(path string) []string {
	modes := []string{"json"}
	if reviewedTextOutputCommandPaths[path] {
		modes = append(modes, "text")
	}
	if reviewedIDOutputCommandPaths[path] {
		modes = append(modes, "id")
	}
	return modes
}

func TestCommandRegistryOutputModeShapesAreEnforced(t *testing.T) {
	for name, row := range map[string]string{
		"read missing modes":     "R unsafe",
		"mutation missing modes": "M remote-write remote-direct - unsafe",
		"missing effect":         "R json safe",
		"unknown effect":         "R guessed json safe",
		"missing json":           "R pure text unsafe",
		"noncanonical order":     "R pure json,id,text unsafe",
		"duplicate mode":         "R pure json,text,text unsafe",
		"unknown mode":           "R pure json,xml unsafe",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseCommandRegistry(row); err == nil {
				t.Fatalf("parseCommandRegistry(%q) succeeded", row)
			}
		})
	}

	registry, err := parseCommandRegistry("R pure json,text,id safe")
	if err != nil {
		t.Fatal(err)
	}
	registration := registry.nodes["safe"]
	want := commandOutputJSON | commandOutputText | commandOutputID
	if registration.outputModes != want {
		t.Fatalf("output modes=%d want=%d", registration.outputModes, want)
	}
}

func TestCommandRegistryPreservesReviewedOutputModes(t *testing.T) {
	root := newRoot()
	seen := map[string]bool{}
	leafCount, textCount, idCount := 0, 0, 0
	var walk func(*cobra.Command)
	walk = func(command *cobra.Command) {
		role := command.Annotations[commandRoleAnnotation]
		if role == commandRoleLeaf || role == commandRoleHybrid {
			path := commandRegistryPath(root, command)
			seen[path] = true
			leafCount++
			textSupported := command.Annotations[textOutputAnnotation] == "supported"
			idSupported := command.Annotations[idOutputAnnotation] == "supported"
			if textSupported {
				textCount++
			}
			if idSupported {
				idCount++
			}
			if textSupported != reviewedTextOutputCommandPaths[path] {
				t.Errorf("%q text support=%t want=%t", path, textSupported, reviewedTextOutputCommandPaths[path])
			}
			if idSupported != reviewedIDOutputCommandPaths[path] {
				t.Errorf("%q id support=%t want=%t", path, idSupported, reviewedIDOutputCommandPaths[path])
			}
		}
		for _, child := range command.Commands() {
			walk(child)
		}
	}
	walk(root)

	if leafCount != 180 || textCount != 151 || idCount != 35 {
		t.Fatalf("leaves/text/id=%d/%d/%d want=180/151/35", leafCount, textCount, idCount)
	}
	for path := range reviewedTextOutputCommandPaths {
		if !seen[path] {
			t.Errorf("reviewed text-output command %q is not constructed", path)
		}
	}
	for path := range reviewedIDOutputCommandPaths {
		if !seen[path] {
			t.Errorf("reviewed id-output command %q is not constructed", path)
		}
	}
}

func TestCapabilityCatalogOutputModesMatchIndependentOracle(t *testing.T) {
	catalog, err := buildCapabilityCatalog(newRoot(), capabilitySelection{})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range catalog.Capabilities {
		want := reviewedOutputModes(item.CLICommand)
		if !reflect.DeepEqual(item.OutputModes, want) {
			t.Errorf("%s output modes=%v want=%v", item.ID, item.OutputModes, want)
		}
	}
}
