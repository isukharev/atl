package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
	"time"
)

type testTiming struct {
	Package string  `json:"package"`
	Name    string  `json:"name"`
	Seconds float64 `json:"seconds"`
}

type executionSummary struct {
	Packages        int          `json:"packages"`
	InventoryCount  int          `json:"inventory_count"`
	InventorySHA256 string       `json:"inventory_sha256"`
	SelectedCount   int          `json:"selected_count"`
	SelectedSHA256  string       `json:"selected_sha256"`
	Tests           int          `json:"tests"`
	FuzzTargets     int          `json:"fuzz_targets"`
	Examples        int          `json:"examples"`
	ObservedSeeds   int          `json:"observed_seed_events"`
	Skipped         int          `json:"go_skips"`
	Seconds         float64      `json:"seconds"`
	Slowest         []testTiming `json:"slowest"`
}

type executionState struct {
	inventory testInventory
	want      map[string]map[string]testItem
	runs      map[string]int
	terminals map[string]int
	starts    map[string]int
	packages  map[string]int
	seeds     map[string]seedState
	timings   []testTiming
	skipped   int
	events    int
}

type seedState struct {
	runs      int
	terminals int
}

func executeInventory(ctx context.Context, evaluator string, packages []string, selector string, inventory testInventory, env []string, failure io.Writer) (executionSummary, error) {
	args := raceCommandArgs(packages, selector)
	testCtx, cancel := context.WithTimeout(ctx, testTimeout)
	defer cancel()
	state := newExecutionState(inventory)
	started := time.Now()
	stdoutTail := newTailBuffer(maxFailureOutput)
	stderrTail := newTailBuffer(maxFailureOutput)
	err := runJSONProcess(testCtx, commandSpec{dir: evaluator, name: "go", args: args, env: env}, state.consume, stdoutTail, stderrTail)
	elapsed := time.Since(started).Seconds()
	if err == nil {
		err = state.verify()
	}
	if err != nil {
		writeFailureTail(failure, stdoutTail, stderrTail)
		if testCtx.Err() != nil {
			return executionSummary{}, errors.New("evaluator race command exceeded its 47-minute process bound")
		}
		return executionSummary{}, fmt.Errorf("evaluator race command failed: %w", err)
	}
	return state.summary(elapsed), nil
}

func raceCommandArgs(packages []string, selector string) []string {
	args := []string{"test", "-json", "-race"}
	args = append(args, packages...)
	if selector != "" {
		args = append(args, "-run", selector)
	}
	return append(args, "-count=1", "-timeout="+goTestTimeout)
}

func newExecutionState(inventory testInventory) *executionState {
	state := &executionState{
		inventory: inventory, want: map[string]map[string]testItem{}, runs: map[string]int{},
		terminals: map[string]int{}, starts: map[string]int{}, packages: map[string]int{}, seeds: map[string]seedState{},
	}
	for _, pkg := range inventory.Packages {
		state.want[pkg] = map[string]testItem{}
		for _, item := range inventory.Items[pkg] {
			state.want[pkg][item.Name] = item
		}
	}
	return state
}

func (s *executionState) consume(event goEvent) error {
	s.events++
	if s.events > maxJSONEvents {
		return errors.New("execution event count exceeds its reviewed bound")
	}
	switch event.Action {
	case "start", "run", "pause", "cont", "pass", "bench", "fail", "output", "skip", "build-output", "build-fail":
	default:
		return errors.New("race execution contains an unknown event action")
	}
	if event.Action == "build-output" {
		return nil
	}
	if event.Action == "build-fail" {
		return errors.New("race build failed")
	}
	wantPackage, ok := s.want[event.Package]
	if !ok {
		return errors.New("execution event names an unexpected package")
	}
	if event.Test == "" {
		if event.Action == "start" {
			s.starts[event.Package]++
		}
		if event.Action == "pass" || event.Action == "skip" || event.Action == "fail" {
			s.packages[event.Package]++
			if event.Action == "fail" {
				return errors.New("race package failed")
			}
		}
		return nil
	}
	top := strings.SplitN(event.Test, "/", 2)[0]
	item, selected := wantPackage[top]
	if !selected {
		return errors.New("execution event names an unselected top-level runnable")
	}
	if event.Action == "skip" {
		s.skipped++
	}
	key := event.Package + "\x00" + top
	if event.Test == top {
		switch event.Action {
		case "run":
			s.runs[key]++
		case "pass", "skip", "fail":
			s.terminals[key]++
			s.timings = append(s.timings, testTiming{Package: event.Package, Name: top, Seconds: event.Elapsed})
			if event.Action == "fail" {
				return errors.New("race runnable failed")
			}
		}
		return nil
	}
	suffix := strings.TrimPrefix(event.Test, top+"/")
	if item.Kind == "fuzz" && suffix != "" && !strings.Contains(suffix, "/") {
		seedKey := event.Package + "\x00" + top + "/" + suffix
		seed := s.seeds[seedKey]
		switch event.Action {
		case "run":
			seed.runs++
		case "pass", "skip", "fail":
			seed.terminals++
			if event.Action == "fail" {
				return errors.New("race fuzz seed failed")
			}
		}
		s.seeds[seedKey] = seed
	}
	return nil
}

func (s *executionState) verify() error {
	if s.events == 0 {
		return errors.New("race execution emitted no JSON events")
	}
	for _, pkg := range s.inventory.Packages {
		if s.starts[pkg] != 1 || s.packages[pkg] != 1 {
			return errors.New("race execution lacks one successful package terminal")
		}
		for _, item := range s.inventory.Items[pkg] {
			key := pkg + "\x00" + item.Name
			if s.runs[key] != 1 || s.terminals[key] != 1 {
				return errors.New("race execution lacks one runnable run and terminal event")
			}
		}
	}
	for _, seed := range s.seeds {
		if seed.runs != 1 || seed.terminals != 1 {
			return errors.New("race execution has incomplete or duplicate observed fuzz seed events")
		}
	}
	return nil
}

func (s *executionState) summary(elapsed float64) executionSummary {
	summary := executionSummary{
		Packages: len(s.inventory.Packages), InventoryCount: inventorySize(s.inventory),
		InventorySHA256: inventoryDigest(s.inventory), SelectedCount: inventorySize(s.inventory),
		SelectedSHA256: inventoryDigest(s.inventory), Seconds: elapsed, Skipped: s.skipped,
	}
	for _, items := range s.inventory.Items {
		for _, item := range items {
			switch item.Kind {
			case "test":
				summary.Tests++
			case "fuzz":
				summary.FuzzTargets++
			case "example":
				summary.Examples++
			}
		}
	}
	for _, seed := range s.seeds {
		if seed.runs == 1 && seed.terminals == 1 {
			summary.ObservedSeeds++
		}
	}
	sort.Slice(s.timings, func(i, j int) bool {
		if s.timings[i].Seconds != s.timings[j].Seconds {
			return s.timings[i].Seconds > s.timings[j].Seconds
		}
		if s.timings[i].Package != s.timings[j].Package {
			return s.timings[i].Package < s.timings[j].Package
		}
		return s.timings[i].Name < s.timings[j].Name
	})
	if len(s.timings) > maxSlowTests {
		s.timings = s.timings[:maxSlowTests]
	}
	summary.Slowest = append([]testTiming(nil), s.timings...)
	return summary
}

func runJSONProcess(ctx context.Context, spec commandSpec, consume func(goEvent) error, stdoutTail, stderrTail *tailBuffer) error {
	command := exec.CommandContext(ctx, spec.name, spec.args...)
	command.Dir = spec.dir
	command.Env = spec.env
	stdout, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	command.Stderr = stderrTail
	if err := configureCommand(command); err != nil {
		return err
	}
	if err := command.Start(); err != nil {
		return err
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), maxJSONLine)
	parseErr := error(nil)
	totalBytes := 0
	for scanner.Scan() {
		totalBytes += len(scanner.Bytes()) + 1
		if totalBytes > maxCommandOutput {
			parseErr = errors.New("race execution output exceeds its reviewed bound")
			break
		}
		_, _ = stdoutTail.Write(append(append([]byte(nil), scanner.Bytes()...), '\n'))
		var event goEvent
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			parseErr = errors.New("race execution contains malformed JSON")
			break
		}
		if err := consume(event); err != nil {
			parseErr = err
			break
		}
	}
	if scanErr := scanner.Err(); parseErr == nil && scanErr != nil {
		parseErr = errors.New("race execution JSON line exceeds its reviewed bound")
	}
	if parseErr != nil {
		_ = command.Cancel()
	}
	waitErr := command.Wait()
	cleanupCommand(command)
	if parseErr != nil {
		return parseErr
	}
	if waitErr != nil {
		return waitErr
	}
	return nil
}
