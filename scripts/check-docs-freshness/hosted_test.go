package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/isukharev/atl/scripts/check-docs-freshness/hosted"
)

func TestHostedRepositoryModuleSelection(t *testing.T) {
	policy, err := loadImpactManifest(filepath.Join("..", "..", impactManifestPath))
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		path, lanes string
		full        bool
	}{
		{"docs/architecture.md", "contracts", false},
		{"docs/schemas/broker-v1.schema.json", "contracts,eval-compat,product,security", false},
		{"docs/maintainers/agent-eval-distribution.md", "contracts,eval-compat,product,security", false},
		{"skills-src/atl/SKILL.md", "contracts,eval-compat", false},
		{"internal/app/example.go", "contracts,eval-compat,product,security", false},
		{"internal/agenteval/grading/example.go", "contracts,eval-full,platform,security", false},
		{"benchmarks/agent-eval/example.json", "contracts,eval-full,platform,security", false},
		{"examples/corpus-devcontainer/example.json", "contracts,corpus", false},
		{"go.mod", strings.Join(hosted.Lanes, ","), false},
		{".github/workflows/ci.yml", strings.Join(hosted.Lanes, ","), false},
		{"scripts/check-premerge/main.go", strings.Join(hosted.Lanes, ","), false},
		{"unknown/new.file", strings.Join(hosted.Lanes, ","), true},
		{impactManifestPath, strings.Join(hosted.Lanes, ","), true},
	} {
		t.Run(tt.path, func(t *testing.T) {
			plan, err := classifyHosted(policy, &policy, changedPathSet{Paths: []string{tt.path}}, false)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(plan.Lanes, ","); got != tt.lanes || plan.Full != tt.full {
				t.Fatalf("plan=%+v, want lanes=%s full=%v", plan, tt.lanes, tt.full)
			}
		})
	}
}

func hostedPolicy(rules ...impactRule) impactManifest {
	return impactManifest{SchemaVersion: 1, HostedSchemaVersion: 1,
		Checks: []impactCheck{{ID: "docs", MakeTarget: "check-docs-catalog"}}, Rules: rules}
}

func TestHostedUnionIncludesBothPoliciesForModifiedRenamedAndDeletedPaths(t *testing.T) {
	current := hostedPolicy(impactRule{Prefix: "docs/", Checks: []string{"docs"}, HostedLanes: []string{"contracts"}})
	baseline := hostedPolicy(impactRule{Path: "old.txt", Checks: []string{"docs"}, HostedLanes: []string{"product"}},
		impactRule{Prefix: "docs/", Checks: []string{"docs"}, HostedLanes: []string{"eval-full"}})
	for _, status := range []string{"M\x00docs/page.md\x00", "R100\x00old.txt\x00docs/page.md\x00", "D\x00old.txt\x00", "C100\x00old.txt\x00docs/page.md\x00"} {
		changed, err := parseNameStatus([]byte(status))
		if err != nil {
			t.Fatal(err)
		}
		plan, err := classifyHosted(current, &baseline, changed, false)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(status, "M") {
			if !plan.Has("eval-full") || !plan.Has("platform") {
				t.Fatalf("modified path lost baseline impact: %+v", plan)
			}
		} else if !plan.Has("product") || !plan.Has("eval-compat") {
			t.Fatalf("historical path lost baseline impact: %+v", plan)
		}
	}
}

func TestHostedPolicyDriftAndOverrideCannotNarrow(t *testing.T) {
	policy := hostedPolicy(impactRule{Prefix: "docs/", Checks: []string{"docs"}, HostedLanes: []string{"contracts"}})
	changed := changedPathSet{Paths: []string{"docs/page.md"}}
	legacy := policy
	legacy.HostedSchemaVersion = 0
	for _, baseline := range []*impactManifest{nil, &legacy} {
		plan, err := classifyHosted(policy, baseline, changed, false)
		if err != nil || !plan.Full || !slices.Equal(plan.Lanes, hosted.Lanes) {
			t.Fatalf("legacy baseline=%+v plan=%+v error=%v", baseline, plan, err)
		}
	}
	plan, err := classifyHosted(policy, &policy, changed, true)
	if err != nil || !plan.Full || !slices.Equal(plan.Lanes, hosted.Lanes) {
		t.Fatalf("full override: %+v %v", plan, err)
	}
	changed.Paths = append(changed.Paths, impactManifestPath)
	plan, err = classifyHosted(policy, &policy, changed, false)
	if err != nil || !plan.Full {
		t.Fatalf("policy change did not widen: %+v %v", plan, err)
	}
	for _, lanes := range [][]string{nil, {"unknown"}, {"product", "contracts"}, {"contracts", "contracts"}} {
		bad := hostedPolicy(impactRule{Prefix: "docs/", Checks: []string{"docs"}, HostedLanes: lanes})
		if _, err := classifyHosted(bad, &policy, changed, false); err == nil {
			t.Fatal("invalid current policy accepted")
		}
		if _, err := classifyHosted(policy, &bad, changed, false); err == nil {
			t.Fatal("invalid baseline policy accepted")
		}
	}
}

func TestHostedBaselineUsesSharedStructuralValidation(t *testing.T) {
	valid := func() impactManifest {
		return hostedPolicy(impactRule{Prefix: "docs/", Checks: []string{"docs"}, HostedLanes: []string{"contracts"}},
			impactRule{Prefix: "retired/", Checks: []string{"docs"}, HostedLanes: []string{"product"}})
	}
	current := valid()
	changed := changedPathSet{Paths: []string{"retired/deleted.txt"}, Historical: map[string]bool{"retired/deleted.txt": true}}
	baseline := valid()
	baseline.Checks[0].MakeTarget = "retired-target"
	if plan, err := classifyHosted(current, &baseline, changed, false); err != nil || !plan.Has("product") {
		t.Fatalf("historical paths/targets must not require current-tree presence: %+v %v", plan, err)
	}
	for name, edit := range map[string]func(*impactManifest){
		"invalid schema":      func(p *impactManifest) { p.SchemaVersion = 2 },
		"empty checks":        func(p *impactManifest) { p.Checks = nil },
		"duplicate checks":    func(p *impactManifest) { p.Checks = append(p.Checks, p.Checks[0]) },
		"malformed target":    func(p *impactManifest) { p.Checks[0].MakeTarget = "bad target" },
		"duplicate selector":  func(p *impactManifest) { p.Rules = append(p.Rules, p.Rules[1]) },
		"unsorted selectors":  func(p *impactManifest) { p.Rules[0], p.Rules[1] = p.Rules[1], p.Rules[0] },
		"escaping exclusion":  func(p *impactManifest) { p.Rules[0].ExcludePrefixes = []string{"retired/"} },
		"unsorted exclusions": func(p *impactManifest) { p.Rules[0].ExcludePrefixes = []string{"docs/z/", "docs/a/"} },
		"empty rule checks":   func(p *impactManifest) { p.Rules[0].Checks = nil },
		"unknown check":       func(p *impactManifest) { p.Rules[0].Checks = []string{"unknown"} },
		"malformed legacy":    func(p *impactManifest) { p.HostedSchemaVersion = 0; p.Rules[0].ExcludePrefixes = []string{"../"} },
	} {
		t.Run(name, func(t *testing.T) {
			bad := valid()
			edit(&bad)
			for _, full := range []bool{false, true} {
				if _, err := classifyHosted(current, &bad, changed, full); err == nil {
					t.Fatal("malformed baseline was usable")
				}
				if _, err := classifyHosted(bad, &current, changed, full); err == nil {
					t.Fatal("malformed current policy was usable")
				}
			}
		})
	}
}

func TestHostedPlanReadsCommittedPolicyAndEndpoints(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root, "-c", "user.name=Fixture", "-c", "user.email=ivan7654@gmail.com", "-c", "commit.gpgSign=false", "-c", "core.hooksPath=/dev/null"}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if err := os.Mkdir(filepath.Join(root, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	policy := hostedPolicy(impactRule{Prefix: "docs/", Checks: []string{"docs"}, HostedLanes: []string{"contracts"}})
	writePolicy := func() {
		t.Helper()
		body, err := json.Marshal(policy)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, impactManifestPath), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writePolicy()
	git("init", "--initial-branch=main")
	git("add", ".")
	git("commit", "-m", "base")
	base := git("rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(root, "docs", "page.md"), []byte("synthetic\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "head")
	head := git("rev-parse", "HEAD")
	policy.Rules[0].HostedLanes = []string{"product"}
	writePolicy()
	var output bytes.Buffer
	if err := runHostedPlan(root, base, head, false, &output); err != nil {
		t.Fatal(err)
	}
	var plan hosted.Plan
	if err := json.Unmarshal(output.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.BaseSHA != base || plan.HeadSHA != head || !slices.Equal(plan.Lanes, []string{"contracts"}) {
		t.Fatalf("working policy affected committed selection: %+v", plan)
	}
	if err := runHostedPlan(root, "main", head, false, &output); err == nil {
		t.Fatal("accepted symbolic base")
	}
	if err := runHostedPlan(root, strings.Repeat("f", 40), head, false, &output); err == nil {
		t.Fatal("accepted missing base")
	}
}
