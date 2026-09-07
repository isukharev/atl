package cli

import (
	"fmt"
	"strings"

	"github.com/isukharev/atl/internal/app"
)

func pushText(res *app.PushResult) string {
	var b strings.Builder
	for _, it := range res.Items {
		state := "ok"
		switch {
		case it.Failed != "":
			state = "FAILED(" + it.Failed + ")"
		case it.Skipped != "":
			state = it.Skipped
		case it.DryRun:
			state = "dry-run"
			if it.Drifted {
				state = "dry-run/DRIFTED"
			}
		case it.Pushed:
			state = fmt.Sprintf("pushed v%d", it.NewVersion)
		case len(it.Problems) > 0 && csfHasErr(it.Problems):
			state = "INVALID"
		}
		fmt.Fprintf(&b, "%s\t%s\n", state, it.Path)
		if it.Warning != "" {
			fmt.Fprintf(&b, "   ⚠ %s\n", it.Warning)
		}
		for _, r := range it.Removed {
			fmt.Fprintf(&b, "   - removes %s %s\n", r.Kind, r.Display)
		}
		for _, r := range it.Added {
			fmt.Fprintf(&b, "   + adds %s %s\n", r.Kind, r.Display)
		}
		for _, p := range it.Problems {
			fmt.Fprintf(&b, "   ! %s:%d:%d %s\n", p.Severity, p.Line, p.Col, p.Message)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
