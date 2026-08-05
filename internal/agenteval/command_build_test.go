package agenteval

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	evaluatorCommandDirectory = "cmd/agent-eval"
	evaluatorCommandPackage   = "./cmd/agent-eval"
)

// buildEvaluatorCommand compiles the co-located command from its own module.
// Callers pass the repository root explicitly so their test working directory
// remains independent from the command build directory.
func buildEvaluatorCommand(t *testing.T, repositoryRoot, outputPath string) {
	t.Helper()
	moduleRoot := filepath.Join(repositoryRoot, "internal", "agenteval")
	command := exec.Command("go", "build", "-buildvcs=false", "-o", outputPath, evaluatorCommandPackage)
	command.Dir = moduleRoot
	command.Env = evaluatorCommandBuildEnvironment()
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build evaluator command in %s: %v\n%s", moduleRoot, err, output)
	}
}

func evaluatorCommandBuildEnvironment() []string {
	environment := make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		switch {
		case strings.HasPrefix(entry, "GOROOT="),
			strings.HasPrefix(entry, "GOTOOLCHAIN="),
			strings.HasPrefix(entry, "GOWORK="):
			continue
		default:
			environment = append(environment, entry)
		}
	}
	return append(environment, "GOTOOLCHAIN=auto", "GOWORK=off")
}

func TestEvaluatorCommandBuildEnvironmentIsModuleIsolated(t *testing.T) {
	t.Setenv("GOROOT", "/unexpected/goroot")
	t.Setenv("GOTOOLCHAIN", "local")
	t.Setenv("GOWORK", "/unexpected/go.work")

	values := map[string]string{}
	for _, entry := range evaluatorCommandBuildEnvironment() {
		name, value, found := strings.Cut(entry, "=")
		if found {
			values[name] = value
		}
	}
	if _, found := values["GOROOT"]; found {
		t.Fatal("evaluator command build retained GOROOT")
	}
	if got := values["GOTOOLCHAIN"]; got != "auto" {
		t.Fatalf("GOTOOLCHAIN=%q, want auto", got)
	}
	if got := values["GOWORK"]; got != "off" {
		t.Fatalf("GOWORK=%q, want off", got)
	}
}
