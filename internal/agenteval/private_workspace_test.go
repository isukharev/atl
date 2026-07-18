package agenteval

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestPrivateWorkspacePublicExampleMatchesStrictManifestContract(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "benchmarks", "agent-eval", "private-workspace.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := DecodePrivateWorkspaceManifest(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.RunSets) != 1 || len(manifest.RunSets[0].SpecPaths) != 3 || manifest.Execution.MaxEstimatedCostMicroUSD != 10_000_000 {
		t.Fatalf("manifest=%+v", manifest)
	}
}

func TestInitPrivateWorkspaceCreatesFixedOwnerOnlyLayout(t *testing.T) {
	repository := t.TempDir()
	root := filepath.Join(t.TempDir(), "private-evaluations")
	manifest := DefaultPrivateWorkspaceManifest()

	report, err := InitPrivateWorkspace(root, repository, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Healthy || report.Counts.FixedDirectories != len(privateWorkspaceFixedDirectories) || report.Counts.RunSets != 0 {
		t.Fatalf("report=%+v", report)
	}
	for _, path := range append([]string{root}, privateWorkspacePaths(root, privateWorkspaceFixedDirectories)...) {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
			t.Fatalf("directory mode=%v", info.Mode())
		}
	}
	for _, name := range []string{privateOutputRootMarker, PrivateWorkspaceManifestName} {
		info, err := os.Stat(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode=%v", name, info.Mode())
		}
	}

	second, err := InitPrivateWorkspace(root, repository, manifest)
	if err != nil || !second.Healthy {
		t.Fatalf("idempotent init report=%+v err=%v", second, err)
	}
}

func TestInitPrivateWorkspaceRequiresIgnoredRootInsideRepository(t *testing.T) {
	repository := newPrivateWorkspaceGitRepository(t)
	root := filepath.Join(repository, "private")
	if _, err := InitPrivateWorkspace(root, repository, DefaultPrivateWorkspaceManifest()); !errors.Is(err, ErrPrivateWorkspaceUnhealthy) {
		t.Fatalf("unignored root err=%v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("unignored init created root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repository, ".gitignore"), []byte("private/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := InitPrivateWorkspace(root, repository, DefaultPrivateWorkspaceManifest())
	if err != nil || !report.Healthy {
		t.Fatalf("ignored init report=%+v err=%v", report, err)
	}

	tracked := filepath.Join(root, "cases", "tracked.json")
	if err := os.WriteFile(tracked, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("git", "-C", repository, "add", "-f", "private/cases/tracked.json").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, output)
	}
	report = InspectPrivateWorkspace(root, repository)
	if report.Healthy || privateWorkspaceCheckStatus(report, PrivateWorkspaceCheckGitBoundary) != "fail" {
		t.Fatalf("tracked private file passed: %+v", report)
	}
}

func TestInitPrivateWorkspaceRefusesUnmarkedNonemptyAndSymlinkRoots(t *testing.T) {
	repository := t.TempDir()
	t.Run("nonempty", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "private")
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "existing"), []byte("private"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := InitPrivateWorkspace(root, repository, DefaultPrivateWorkspaceManifest()); !errors.Is(err, ErrPrivateWorkspaceUnhealthy) {
			t.Fatalf("err=%v", err)
		}
		if _, err := os.Stat(filepath.Join(root, privateOutputRootMarker)); !os.IsNotExist(err) {
			t.Fatalf("marker unexpectedly created: %v", err)
		}
	})
	t.Run("symlink", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink permissions vary on Windows")
		}
		target := filepath.Join(t.TempDir(), "target")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
		root := filepath.Join(t.TempDir(), "private")
		if err := os.Symlink(target, root); err != nil {
			t.Fatal(err)
		}
		if _, err := InitPrivateWorkspace(root, repository, DefaultPrivateWorkspaceManifest()); !errors.Is(err, ErrPrivateWorkspaceUnhealthy) {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestPrivateWorkspaceManifestStrictSchemaAndContainedSpecs(t *testing.T) {
	valid := DefaultPrivateWorkspaceManifest()
	valid.RunSets = []PrivateWorkspaceRunSet{{Alias: "portfolio-01", SpecPaths: []string{"cases/portfolio-01/run.cli.json"}}}
	data, err := EncodePrivateWorkspaceManifest(valid)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodePrivateWorkspaceManifest(bytes.NewReader(data))
	if err != nil || len(decoded.RunSets) != 1 {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}
	if decoded.Retention.MaxCandidateAgeDays != 14 || decoded.Retention.MaxCandidateBytes != 2<<30 || !decoded.Retention.RetainBaselineTranscripts {
		t.Fatalf("retention=%+v", decoded.Retention)
	}

	privateMarker := "PRIVATE_MARKER_SHOULD_NOT_BE_ECHOED"
	unknown := strings.TrimSuffix(string(data), "}\n") + `,"` + privateMarker + `":true}`
	if _, err := DecodePrivateWorkspaceManifest(strings.NewReader(unknown)); err == nil || strings.Contains(err.Error(), privateMarker) {
		t.Fatalf("unknown-field err=%v", err)
	}
	duplicate := strings.Replace(string(data), `"schema_version": 1`, `"schema_version": 1, "schema_version": 1`, 1)
	if _, err := DecodePrivateWorkspaceManifest(strings.NewReader(duplicate)); err == nil {
		t.Fatal("duplicate manifest key passed")
	}

	for name, mutate := range map[string]func(*PrivateWorkspaceManifest){
		"version":        func(m *PrivateWorkspaceManifest) { m.SchemaVersion = 2 },
		"env":            func(m *PrivateWorkspaceManifest) { m.LiveConfigEnv = "TOKEN=value" },
		"alias":          func(m *PrivateWorkspaceManifest) { m.RunSets[0].Alias = "Private Project" },
		"traversal":      func(m *PrivateWorkspaceManifest) { m.RunSets[0].SpecPaths[0] = "cases/../outside.json" },
		"absolute":       func(m *PrivateWorkspaceManifest) { m.RunSets[0].SpecPaths[0] = "/outside.json" },
		"outside cases":  func(m *PrivateWorkspaceManifest) { m.RunSets[0].SpecPaths[0] = "runs/result.json" },
		"age retention":  func(m *PrivateWorkspaceManifest) { m.Retention.MaxCandidateAgeDays = 0 },
		"byte retention": func(m *PrivateWorkspaceManifest) { m.Retention.MaxCandidateBytes = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			candidate.RunSets = []PrivateWorkspaceRunSet{{Alias: valid.RunSets[0].Alias, SpecPaths: append([]string(nil), valid.RunSets[0].SpecPaths...)}}
			mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("invalid manifest passed")
			}
		})
	}
}

func TestPrivateWorkspaceReportGuidesTheNextSafeAction(t *testing.T) {
	repository := t.TempDir()
	root := filepath.Join(t.TempDir(), "private")
	report, err := InitPrivateWorkspace(root, repository, DefaultPrivateWorkspaceManifest())
	if err != nil {
		t.Fatal(err)
	}
	if report.State != "needs_configuration" || !reflect.DeepEqual(report.NextActions, []string{"configure_run_sets"}) {
		t.Fatalf("report=%+v", report)
	}
	if strings.Contains(strings.Join(report.NextActions, "\n"), root) {
		t.Fatal("next actions leaked the private root")
	}
}

func TestPrivateWorkspaceDoctorDetectsModeAndSymlinkControls(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission and symlink controls")
	}
	repository := t.TempDir()

	t.Run("mode", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "private")
		if _, err := InitPrivateWorkspace(root, repository, DefaultPrivateWorkspaceManifest()); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(root, PrivateWorkspaceManifestName), 0o644); err != nil {
			t.Fatal(err)
		}
		report, err := DoctorPrivateWorkspace(root, repository)
		if !errors.Is(err, ErrPrivateWorkspaceUnhealthy) || report.Healthy || privateWorkspaceCheckStatus(report, PrivateWorkspaceCheckManifestMode) != "fail" || privateWorkspaceCheckStatus(report, PrivateWorkspaceCheckTreeOwnerOnly) != "fail" {
			t.Fatalf("report=%+v err=%v", report, err)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "private")
		if _, err := InitPrivateWorkspace(root, repository, DefaultPrivateWorkspaceManifest()); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(root, PrivateWorkspaceManifestName), filepath.Join(root, "cases", "link.json")); err != nil {
			t.Fatal(err)
		}
		report, err := DoctorPrivateWorkspace(root, repository)
		if !errors.Is(err, ErrPrivateWorkspaceUnhealthy) || report.Healthy || privateWorkspaceCheckStatus(report, PrivateWorkspaceCheckTreeNoSymlinks) != "fail" {
			t.Fatalf("report=%+v err=%v", report, err)
		}
	})
}

func TestPrivateWorkspaceDoctorNeverEchoesPrivateInputs(t *testing.T) {
	privateMarker := "PRIVATE_BACKEND_MARKER_7XQ"
	for _, test := range []struct {
		name    string
		prepare func(*testing.T, string)
	}{
		{
			name: "manifest decoder",
			prepare: func(t *testing.T, root string) {
				malformed := `{"schema_version":1,"unknown_` + privateMarker + `":true}`
				if err := os.WriteFile(filepath.Join(root, PrivateWorkspaceManifestName), []byte(malformed), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "spec decoder",
			prepare: func(t *testing.T, root string) {
				manifest := DefaultPrivateWorkspaceManifest()
				manifest.RunSets = []PrivateWorkspaceRunSet{{Alias: "case-01", SpecPaths: []string{"cases/case-01/run.json"}}}
				data, err := EncodePrivateWorkspaceManifest(manifest)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, PrivateWorkspaceManifestName), data, 0o600); err != nil {
					t.Fatal(err)
				}
				directory := filepath.Join(root, "cases", "case-01")
				if err := os.Mkdir(directory, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(directory, "run.json"), []byte(`{"private":"`+privateMarker+`"}`), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := t.TempDir()
			root := filepath.Join(t.TempDir(), "private")
			if _, err := InitPrivateWorkspace(root, repository, DefaultPrivateWorkspaceManifest()); err != nil {
				t.Fatal(err)
			}
			test.prepare(t, root)
			report, err := DoctorPrivateWorkspace(root, repository)
			if !errors.Is(err, ErrPrivateWorkspaceUnhealthy) || report.Healthy {
				t.Fatalf("report=%+v err=%v", report, err)
			}
			encoded, marshalErr := json.Marshal(report)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			combined := string(encoded) + "\n" + err.Error()
			if strings.Contains(combined, privateMarker) || strings.Contains(combined, root) {
				t.Fatalf("private input leaked: %s", combined)
			}
			for _, check := range report.Checks {
				if !privateWorkspaceDoctorCode(check.Code) || (check.Status != "pass" && check.Status != "fail") {
					t.Fatalf("unbounded check=%+v", check)
				}
			}
		})
	}
}

func TestPrivateWorkspaceDoctorDetectsStaleCredentialScratch(t *testing.T) {
	repository := t.TempDir()
	root := filepath.Join(t.TempDir(), "private")
	if _, err := InitPrivateWorkspace(root, repository, DefaultPrivateWorkspaceManifest()); err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(root, ".ephemeral", "atl-agent-eval-live-config-stale")
	if err := os.Mkdir(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scratch, "credentials.json"), []byte(`{"secret":"synthetic"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := DoctorPrivateWorkspace(root, repository)
	if !errors.Is(err, ErrPrivateWorkspaceUnhealthy) || report.Healthy {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	found := false
	for _, check := range report.Checks {
		if check.Code == PrivateWorkspaceCheckScratchClean {
			found = check.Status == "fail"
		}
	}
	if !found {
		t.Fatalf("scratch check did not fail: %+v", report.Checks)
	}
}

func newPrivateWorkspaceGitRepository(t *testing.T) string {
	t.Helper()
	repository := t.TempDir()
	if output, err := exec.Command("git", "-C", repository, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	return repository
}

func privateWorkspacePaths(root string, names []string) []string {
	paths := make([]string, 0, len(names))
	for _, name := range names {
		paths = append(paths, filepath.Join(root, name))
	}
	return paths
}

func privateWorkspaceCheckStatus(report PrivateWorkspaceReport, code string) string {
	for _, check := range report.Checks {
		if check.Code == code {
			return check.Status
		}
	}
	return ""
}

func privateWorkspaceDoctorCode(code string) bool {
	for _, allowed := range []string{
		PrivateWorkspaceCheckRootExists, PrivateWorkspaceCheckRootOwnerOnly,
		PrivateWorkspaceCheckRootMarker, PrivateWorkspaceCheckGitBoundary,
		PrivateWorkspaceCheckManifestMode, PrivateWorkspaceCheckManifestValid,
		PrivateWorkspaceCheckFixedLayout, PrivateWorkspaceCheckTreeOwnerOnly,
		PrivateWorkspaceCheckTreeNoSymlinks, PrivateWorkspaceCheckSpecsContained,
		PrivateWorkspaceCheckSpecsValid, PrivateWorkspaceCheckScratchClean,
		PrivateWorkspaceCheckLifecycleValid,
	} {
		if code == allowed {
			return true
		}
	}
	return false
}
