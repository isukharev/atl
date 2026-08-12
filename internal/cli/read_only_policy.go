package cli

import (
	"os"
	"strings"
)

const explicitReadOnlyAnnotation = "atl.explicit.read_only"

func envReadOnly() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("ATL_READ_ONLY"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
