package cli

import (
	"fmt"
	"strings"

	"github.com/isukharev/atl/internal/app"
)

func appendPullLocalSafetyText(b *strings.Builder, safety *app.PullLocalSafety) {
	if safety == nil {
		return
	}
	fmt.Fprintf(b, "local-safety: complete=%t dry_run=%t blocked=%d actions=%d\n", safety.Complete, safety.DryRun, safety.Blocked, safety.ActionCount)
	for _, action := range safety.Actions {
		fmt.Fprintf(b, "  %s  %s  %s  %s", action.Status, action.ID, action.Path, action.Reason)
		if action.StashPath != "" {
			fmt.Fprintf(b, "  stash=%s", action.StashPath)
		}
		b.WriteByte('\n')
	}
}
