// Command atl is the agent-native CLI for Confluence/Jira document access:
// mirror spaces to disk, edit native storage format with diffs, validate, and
// push under an optimistic version gate. See docs/architecture.md for the
// design.
package main

import "github.com/isukharev/atl/internal/cli"

func main() {
	cli.Execute()
}
