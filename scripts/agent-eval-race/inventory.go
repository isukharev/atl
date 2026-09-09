package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"
)

type testItem struct {
	Name string
	Kind string
}

type testInventory struct {
	Packages []string
	Items    map[string][]testItem
}

type goEvent struct {
	Time        string  `json:"Time"`
	Action      string  `json:"Action"`
	Package     string  `json:"Package"`
	Test        string  `json:"Test"`
	Elapsed     float64 `json:"Elapsed"`
	Output      string  `json:"Output"`
	FailedBuild string  `json:"FailedBuild"`
	ImportPath  string  `json:"ImportPath"`
}

var runnableName = regexp.MustCompile(`^(Test|Fuzz|Example)[[:alnum:]_]*$`)

func discoverPackages(ctx context.Context, evaluator string, env []string) ([]string, error) {
	listCtx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()
	body, _, err := captureCommand(listCtx, commandSpec{
		dir: evaluator, name: "go", args: []string{"list", "-race", "-f", "{{.ImportPath}}", "./..."}, env: env,
	}, maxCommandOutput)
	if err != nil {
		return nil, errors.New("cannot discover evaluator race packages")
	}
	seen := map[string]bool{}
	packages := []string{}
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		if line == "" || seen[line] || strings.ContainsAny(line, " \t\r") {
			return nil, errors.New("evaluator race package inventory is malformed")
		}
		seen[line] = true
		packages = append(packages, line)
	}
	if len(packages) < 2 || len(packages) > maxInventory {
		return nil, errors.New("evaluator race package inventory is outside its reviewed bound")
	}
	sort.Strings(packages)
	return packages, nil
}

func discoverInventory(ctx context.Context, evaluator string, args, expected []string, env []string) (testInventory, error) {
	listCtx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()
	commandArgs := []string{"test", "-json", "-race"}
	commandArgs = append(commandArgs, args...)
	commandArgs = append(commandArgs, "-list", "^(Test|Fuzz|Example)", "-count=1")
	body, _, err := captureCommand(listCtx, commandSpec{dir: evaluator, name: "go", args: commandArgs, env: env}, maxCommandOutput)
	if err != nil {
		return testInventory{}, errors.New("cannot discover runnable evaluator race inventory")
	}
	return parseListEvents(body, expected)
}

func parseListEvents(body []byte, expected []string) (testInventory, error) {
	inventory := testInventory{Packages: append([]string(nil), expected...), Items: map[string][]testItem{}}
	want := map[string]bool{}
	starts := map[string]int{}
	terminal := map[string]int{}
	seen := map[string]map[string]bool{}
	for _, pkg := range expected {
		if pkg == "" || want[pkg] {
			return testInventory{}, errors.New("expected package inventory is malformed")
		}
		want[pkg] = true
		seen[pkg] = map[string]bool{}
		inventory.Items[pkg] = nil
	}
	if len(want) == 0 {
		return testInventory{}, errors.New("expected package inventory is empty")
	}
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 4096), maxJSONLine)
	events := 0
	for scanner.Scan() {
		events++
		if events > maxJSONEvents {
			return testInventory{}, errors.New("test list event count exceeds its reviewed bound")
		}
		var event goEvent
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			return testInventory{}, errors.New("test list contains malformed JSON")
		}
		if event.Action == "build-output" || event.Action == "build-fail" {
			return testInventory{}, errors.New("test list contains unexpected build diagnostics")
		}
		if event.Package == "" || !want[event.Package] {
			return testInventory{}, errors.New("test list contains an unexpected package")
		}
		if event.Test != "" {
			return testInventory{}, errors.New("test list unexpectedly executed a runnable")
		}
		switch event.Action {
		case "start":
			starts[event.Package]++
		case "output":
			for _, line := range strings.Split(strings.TrimSuffix(event.Output, "\n"), "\n") {
				if runnableName.MatchString(line) {
					if seen[event.Package][line] {
						return testInventory{}, errors.New("test list contains a duplicate runnable")
					}
					seen[event.Package][line] = true
					inventory.Items[event.Package] = append(inventory.Items[event.Package], testItem{Name: line, Kind: itemKind(line)})
					if inventorySize(inventory) > maxInventory {
						return testInventory{}, errors.New("runnable inventory exceeds its reviewed bound")
					}
					continue
				}
				if line != "" && !strings.HasPrefix(line, "ok  \t") && !strings.HasPrefix(line, "?   \t") {
					return testInventory{}, errors.New("test list contains unexpected output")
				}
			}
		case "pass", "skip":
			terminal[event.Package]++
		default:
			return testInventory{}, errors.New("test list contains an unexpected event")
		}
	}
	if scanner.Err() != nil || events == 0 {
		return testInventory{}, errors.New("test list event stream is incomplete")
	}
	for _, pkg := range expected {
		if starts[pkg] != 1 || terminal[pkg] != 1 {
			return testInventory{}, errors.New("test list lacks one successful package terminal")
		}
		sort.Slice(inventory.Items[pkg], func(i, j int) bool { return inventory.Items[pkg][i].Name < inventory.Items[pkg][j].Name })
	}
	return inventory, nil
}

func itemKind(name string) string {
	for _, kind := range []string{"Test", "Fuzz", "Example"} {
		if strings.HasPrefix(name, kind) {
			return strings.ToLower(kind)
		}
	}
	return ""
}

func inventorySize(inventory testInventory) int {
	total := 0
	for _, items := range inventory.Items {
		total += len(items)
	}
	return total
}

func partitionInventory(inventory testInventory, root string) ([][]string, error) {
	items, ok := inventory.Items[root]
	if !ok || len(inventory.Packages) != 1 || inventory.Packages[0] != root || len(items) < shardCount {
		return nil, errors.New("root runnable inventory cannot fill every shard")
	}
	partitions := make([][]string, shardCount)
	union := map[string]bool{}
	for index, item := range items {
		if item.Name == "" || union[item.Name] {
			return nil, errors.New("root runnable inventory is empty or duplicated")
		}
		if index > 0 && items[index-1].Name >= item.Name {
			return nil, errors.New("root runnable inventory is not strictly sorted")
		}
		union[item.Name] = true
		shard := index % shardCount
		partitions[shard] = append(partitions[shard], item.Name)
	}
	total := 0
	selected := map[string]bool{}
	for _, partition := range partitions {
		if len(partition) == 0 {
			return nil, errors.New("root runnable partition contains an empty shard")
		}
		for _, name := range partition {
			if selected[name] {
				return nil, errors.New("root runnable partitions overlap")
			}
			selected[name] = true
			total++
		}
	}
	if total != len(items) || len(selected) != len(union) {
		return nil, errors.New("root runnable partitions omit inventory")
	}
	return partitions, nil
}

func inventoryDigest(inventory testInventory) string {
	hash := sha256.New()
	packages := append([]string(nil), inventory.Packages...)
	sort.Strings(packages)
	for _, pkg := range packages {
		_, _ = hash.Write([]byte("package"))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(pkg))
		_, _ = hash.Write([]byte{'\n'})
		items := append([]testItem(nil), inventory.Items[pkg]...)
		sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
		for _, item := range items {
			_, _ = hash.Write([]byte(pkg))
			_, _ = hash.Write([]byte{0})
			_, _ = hash.Write([]byte(item.Kind))
			_, _ = hash.Write([]byte{0})
			_, _ = hash.Write([]byte(item.Name))
			_, _ = hash.Write([]byte{'\n'})
		}
	}
	return hex.EncodeToString(hash.Sum(nil))
}
