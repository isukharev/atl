package contentpolicy

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

const validPolicyJSON = `{"schema_version":1,"rules":[{"id":"allow-ml","effect":"allow","verbs":["write"],"resource":{"service":"jira","project":"ml"}}]}`

func TestParsePolicyStrictNegativeTable(t *testing.T) {
	tests := map[string]string{
		"unknown top field": `{"schema_version":1,"rules":[{"id":"a","effect":"allow","verbs":["update"],"resource":{"service":"jira"}}],"extra":true}`,
		"unknown selector":  `{"schema_version":1,"rules":[{"id":"a","effect":"allow","verbs":["update"],"resource":{"service":"jira","glob":"*"}}]}`,
		"duplicate key":     `{"schema_version":1,"schema_version":1,"rules":[]}`,
		"trailing JSON":     validPolicyJSON + `{}`,
		"bad schema":        strings.Replace(validPolicyJSON, `"schema_version":1`, `"schema_version":2`, 1),
		"bad rule id":       strings.Replace(validPolicyJSON, `"allow-ml"`, `"Allow_ML"`, 1),
		"bad effect":        strings.Replace(validPolicyJSON, `"allow"`, `"audit"`, 1),
		"read verb":         strings.Replace(validPolicyJSON, `"write"`, `"read"`, 1),
		"missing service":   strings.Replace(validPolicyJSON, `"service":"jira",`, ``, 1),
		"id without kind":   strings.Replace(validPolicyJSON, `"project":"ml"`, `"id":"42"`, 1),
		"wildcard":          strings.Replace(validPolicyJSON, `"project":"ml"`, `"project":"M*"`, 1),
		"null selector":     strings.Replace(validPolicyJSON, `"project":"ml"`, `"project":null`, 1),
		"overlapping verbs": strings.Replace(validPolicyJSON, `"write"`, `"write","update"`, 1),
		"duplicate rule id": `{"schema_version":1,"rules":[{"id":"same","effect":"allow","verbs":["update"],"resource":{"service":"jira"}},{"id":"same","effect":"deny","verbs":["update"],"resource":{"service":"jira"}}]}`,
		"empty rules":       `{"schema_version":1,"rules":[]}`,
		"too many rules":    `{"schema_version":1,"rules":[` + strings.Repeat(`{"id":"a","effect":"allow","verbs":["update"],"resource":{"service":"jira"}},`, 256) + `{"id":"a","effect":"allow","verbs":["update"],"resource":{"service":"jira"}}]}`,
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			if _, _, err := parsePolicy([]byte(input)); err == nil {
				t.Fatal("invalid policy passed")
			}
		})
	}
}

func TestParsePolicyNormalizesExpandsAndWarns(t *testing.T) {
	input := `{"schema_version":1,"rules":[{"id":"create-id","effect":"allow","verbs":["write","delete"],"resource":{"service":["jira"],"kind":"sprint","project":"ml","id":"42"}}]}`
	policy, warnings, err := parsePolicy([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if got := policy.Rules[0]; got.Resource.Projects[0] != "ML" || !domain.ValidWriteVerbSet(got.Verbs) || len(got.Verbs) != 4 {
		t.Fatalf("normalized rule = %+v", got)
	}
	if len(warnings) != 1 || warnings[0].RuleID != "create-id" {
		t.Fatalf("warnings = %+v", warnings)
	}
}

func TestLoadWarningsNameOnlySafeSourceSymbols(t *testing.T) {
	input := `{"schema_version":1,"rules":[{"id":"never","effect":"allow","verbs":["transition"],"resource":{"service":"confluence","kind":"future-kind"}}]}`
	resolved, err := Load("", Environment{Inline: input})
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.Warnings) != 2 {
		t.Fatalf("warnings = %+v", resolved.Warnings)
	}
	for _, warning := range resolved.Warnings {
		if warning.Source != "env_inline" || warning.RuleID != "never" || strings.Contains(warning.Message, "/") {
			t.Fatalf("warning = %+v", warning)
		}
	}
}

func TestLoadLayersDigestModesLinksBoundsAndStickyResolver(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "policy.json")
	if err := os.WriteFile(configPath, []byte(validPolicyJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	managed := strings.Replace(validPolicyJSON, "allow-ml", "allow-ops", 1)
	managedPath := filepath.Join(root, "managed.json")
	if err := os.WriteFile(managedPath, []byte(managed), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := Load(root, Environment{File: managedPath, FileSHA256: Digest([]byte(managed))})
	if err != nil || len(resolved.Layers) != 2 || resolved.Layers[0].Source != "env_file" || resolved.Layers[1].Source != "config_dir" {
		t.Fatalf("resolved=%+v error=%v", resolved, err)
	}
	if _, err := Load(root, Environment{Inline: managed, File: managedPath}); err == nil {
		t.Fatal("mutually exclusive managed sources passed")
	}
	if _, err := Load(root, Environment{FileSHA256: Digest([]byte(managed))}); err == nil {
		t.Fatal("detached managed digest passed")
	}
	if _, err := Load(root, Environment{File: managedPath, FileSHA256: Digest([]byte("wrong"))}); err == nil || !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("digest mismatch error = %v", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(managedPath, 0o640); err != nil {
			t.Fatal(err)
		}
		if _, err := Load("", Environment{File: managedPath}); err == nil {
			t.Fatal("group-readable policy passed")
		}
		if err := os.Chmod(managedPath, 0o600); err != nil {
			t.Fatal(err)
		}
		wrongUID := ^uint32(0)
		if _, err := Load("", Environment{File: managedPath, ExpectedOwnerUID: &wrongUID}); err == nil {
			t.Fatal("wrong policy owner passed")
		}
	}
	link := filepath.Join(root, "link.json")
	if err := os.Symlink(managedPath, link); err == nil {
		if _, err := Load("", Environment{File: link}); err == nil {
			t.Fatal("policy symlink passed")
		}
	}
	oversize := filepath.Join(root, "large.json")
	if err := os.WriteFile(oversize, make([]byte, maxPolicyBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load("", Environment{File: oversize}); err == nil {
		t.Fatal("oversized policy passed")
	}

	resolver := NewResolver(root, Environment{})
	first, err := resolver.Resolve()
	if err != nil || len(first.Layers) != 1 {
		t.Fatalf("first resolve=%+v error=%v", first, err)
	}
	if err := os.WriteFile(configPath, []byte(`bad`), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := resolver.Resolve()
	if err != nil || second == first || len(second.Layers) != 1 || second.Layers[0].Digest != first.Layers[0].Digest {
		t.Fatalf("memoized resolve=%+v error=%v", second, err)
	}
}

func TestLoadMissingOptionalConfigButUnreadablePresentFails(t *testing.T) {
	root := t.TempDir()
	if resolved, err := Load(root, Environment{}); err != nil || len(resolved.Layers) != 0 {
		t.Fatalf("missing optional policy resolved=%+v error=%v", resolved, err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	path := filepath.Join(root, "policy.json")
	if err := os.WriteFile(path, []byte(validPolicyJSON), 0o000); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root, Environment{}); err == nil {
		t.Fatal("unreadable present policy treated as absent")
	}
}

func TestResolverMemoizesFailure(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "policy.json")
	if err := os.WriteFile(path, []byte(`bad`), 0o600); err != nil {
		t.Fatal(err)
	}
	resolver := NewResolver(root, Environment{})
	if _, err := resolver.Resolve(); err == nil {
		t.Fatal("invalid policy resolved")
	}
	if err := os.WriteFile(path, []byte(validPolicyJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve(); err == nil {
		t.Fatal("resolver retried after sticky failure")
	}
}
