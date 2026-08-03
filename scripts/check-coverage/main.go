// Command check-coverage enforces an exact statement-coverage floor from a Go
// cover profile without floating-point rounding at the policy boundary.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

func main() {
	profile := flag.String("profile", "", "Go cover profile to check")
	minimum := flag.String("minimum", "", "required coverage percentage with one decimal place")
	flag.Parse()
	if flag.NArg() != 0 || *profile == "" || *minimum == "" {
		fmt.Fprintln(os.Stderr, "usage: check-coverage --profile FILE --minimum PERCENT")
		os.Exit(2)
	}
	if err := checkCoverage(*profile, *minimum, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "check-coverage:", err)
		os.Exit(1)
	}
}

func checkCoverage(path, minimum string, output io.Writer) error {
	floor, err := parseTenthsPercent(minimum)
	if err != nil {
		return fmt.Errorf("invalid minimum %q: %w", minimum, err)
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("read coverage profile %q: %w", path, err)
	}
	defer file.Close()

	covered, total, err := readProfile(file)
	if err != nil {
		return fmt.Errorf("malformed coverage profile %q: %w", path, err)
	}
	// floor is tenths of one percent. Comparing cross-products retains the exact
	// reviewed boundary; no float parsing or display rounding can turn a below-
	// floor result into a pass.
	passed := covered*1000 >= floor*total
	fmt.Fprintf(output, "core coverage: %d.%02d%% (%d/%d statements), required >= %s%%\n",
		covered*100/total, (covered*10000/total)%100, covered, total, minimum)
	if !passed {
		return fmt.Errorf("core statement coverage is below the reviewed %s%% floor", minimum)
	}
	return nil
}

func parseTenthsPercent(value string) (uint64, error) {
	whole, decimal, ok := strings.Cut(value, ".")
	if !ok || len(decimal) != 1 || whole == "" {
		return 0, errors.New("want a percentage with exactly one decimal place")
	}
	for _, part := range []string{whole, decimal} {
		if strings.Trim(part, "0123456789") != "" {
			return 0, errors.New("want decimal digits only")
		}
	}
	parsedWhole, err := strconv.ParseUint(whole, 10, 64)
	if err != nil {
		return 0, err
	}
	parsedDecimal := uint64(decimal[0] - '0')
	if parsedWhole > 100 || parsedWhole == 100 && parsedDecimal != 0 {
		return 0, errors.New("percentage must be between 0.0 and 100.0")
	}
	return parsedWhole*10 + parsedDecimal, nil
}

func readProfile(input io.Reader) (covered, total uint64, err error) {
	scanner := bufio.NewScanner(input)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return 0, 0, err
		}
		return 0, 0, errors.New("profile is empty")
	}
	switch scanner.Text() {
	case "mode: atomic", "mode: count", "mode: set":
	default:
		return 0, 0, errors.New("missing or invalid mode header")
	}
	line := 1
	type block struct {
		statements uint64
		covered    bool
	}
	blocks := make(map[string]block)
	for scanner.Scan() {
		line++
		fields := strings.Fields(scanner.Text())
		if len(fields) != 3 || !validPosition(fields[0]) {
			return 0, 0, fmt.Errorf("line %d has invalid record syntax", line)
		}
		statements, parseErr := strconv.ParseUint(fields[1], 10, 64)
		if parseErr != nil {
			return 0, 0, fmt.Errorf("line %d has invalid statement count", line)
		}
		count, parseErr := strconv.ParseUint(fields[2], 10, 64)
		if parseErr != nil {
			return 0, 0, fmt.Errorf("line %d has invalid execution count", line)
		}
		position := fields[0]
		if previous, ok := blocks[position]; ok {
			if previous.statements != statements {
				return 0, 0, fmt.Errorf("line %d repeats block %q with a different statement count", line, position)
			}
			previous.covered = previous.covered || count != 0
			blocks[position] = previous
			continue
		}
		blocks[position] = block{statements: statements, covered: count != 0}
	}
	if err := scanner.Err(); err != nil {
		return 0, 0, err
	}
	for _, item := range blocks {
		if ^uint64(0)-total < item.statements {
			return 0, 0, errors.New("statement count overflow")
		}
		total += item.statements
		if item.covered {
			covered += item.statements
		}
	}
	if total == 0 {
		return 0, 0, errors.New("profile has no statements")
	}
	if total > ^uint64(0)/10000 {
		return 0, 0, errors.New("statement count is too large to calculate safely")
	}
	return covered, total, nil
}

func validPosition(value string) bool {
	colon := strings.LastIndexByte(value, ':')
	if colon <= 0 || colon == len(value)-1 {
		return false
	}
	start, end, ok := strings.Cut(value[colon+1:], ",")
	return ok && validPoint(start) && validPoint(end)
}

func validPoint(value string) bool {
	line, column, ok := strings.Cut(value, ".")
	if !ok || line == "" || column == "" {
		return false
	}
	parsedLine, lineErr := strconv.ParseUint(line, 10, 64)
	parsedColumn, columnErr := strconv.ParseUint(column, 10, 64)
	return lineErr == nil && columnErr == nil && parsedLine != 0 && parsedColumn != 0
}
