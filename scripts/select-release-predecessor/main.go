// Command select-release-predecessor chooses the highest stable semantic
// release that a new tag must advance beyond. It consumes GitHub's paginated
// releases JSON on stdin and prints the predecessor tag on stdout.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

type releaseRecord struct {
	Tag        string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

type stableVersion struct {
	Tag                 string
	Major, Minor, Patch uint64
}

func main() {
	current := flag.String("current", "", "new stable release tag (vMAJOR.MINOR.PATCH)")
	bootstrap := flag.String("bootstrap-tag", "", "explicit first-release bootstrap tag")
	flag.Parse()
	previous, err := selectPredecessor(os.Stdin, *current, *bootstrap)
	if err != nil {
		fmt.Fprintln(os.Stderr, "select-release-predecessor:", err)
		os.Exit(1)
	}
	fmt.Println(previous)
}

func selectPredecessor(input io.Reader, currentTag, bootstrapTag string) (string, error) {
	current, err := parseStableVersion(currentTag)
	if err != nil {
		return "", fmt.Errorf("current tag: %w", err)
	}
	var payload any
	decoder := json.NewDecoder(input)
	if err := decoder.Decode(&payload); err != nil {
		return "", fmt.Errorf("decode release listing: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return "", errors.New("release listing must contain exactly one JSON value")
	}
	releases, err := flattenReleases(payload)
	if err != nil {
		return "", err
	}
	var highest *stableVersion
	seen := map[string]bool{}
	for _, release := range releases {
		if release.Draft || release.Prerelease {
			continue
		}
		version, err := parseStableVersion(release.Tag)
		if err != nil {
			return "", fmt.Errorf("published stable release %q is not a canonical semantic version: %w", release.Tag, err)
		}
		if seen[version.Tag] {
			return "", fmt.Errorf("release listing repeats stable tag %q", version.Tag)
		}
		seen[version.Tag] = true
		if highest == nil || compareStableVersion(version, *highest) > 0 {
			copy := version
			highest = &copy
		}
	}
	if highest == nil {
		if strings.TrimSpace(bootstrapTag) != current.Tag {
			return "", fmt.Errorf("no published stable release found; set protected ATL_RELEASE_TRUST_BOOTSTRAP_TAG=%s for this first release only", current.Tag)
		}
		return current.Tag, nil
	}
	if strings.TrimSpace(bootstrapTag) != "" {
		return "", errors.New("ATL_RELEASE_TRUST_BOOTSTRAP_TAG must be unset after the first stable release")
	}
	if compareStableVersion(current, *highest) <= 0 {
		return "", fmt.Errorf("new release %s must be greater than highest published stable release %s", current.Tag, highest.Tag)
	}
	return highest.Tag, nil
}

func flattenReleases(value any) ([]releaseRecord, error) {
	var out []releaseRecord
	var visit func(any) error
	visit = func(value any) error {
		switch typed := value.(type) {
		case []any:
			for _, child := range typed {
				if err := visit(child); err != nil {
					return err
				}
			}
		case map[string]any:
			encoded, err := json.Marshal(typed)
			if err != nil {
				return err
			}
			var release releaseRecord
			if err := json.Unmarshal(encoded, &release); err != nil {
				return fmt.Errorf("invalid release entry: %w", err)
			}
			if strings.TrimSpace(release.Tag) == "" {
				return errors.New("release entry has an empty tag_name")
			}
			out = append(out, release)
		default:
			return fmt.Errorf("release listing contains unexpected %T value", value)
		}
		return nil
	}
	if err := visit(value); err != nil {
		return nil, err
	}
	return out, nil
}

func parseStableVersion(tag string) (stableVersion, error) {
	tag = strings.TrimSpace(tag)
	parts := strings.Split(strings.TrimPrefix(tag, "v"), ".")
	if !strings.HasPrefix(tag, "v") || len(parts) != 3 {
		return stableVersion{}, errors.New("expected vMAJOR.MINOR.PATCH")
	}
	values := [3]uint64{}
	for index, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return stableVersion{}, errors.New("version components must be canonical unsigned integers")
		}
		value, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return stableVersion{}, errors.New("version components must be canonical unsigned integers")
		}
		values[index] = value
	}
	return stableVersion{Tag: tag, Major: values[0], Minor: values[1], Patch: values[2]}, nil
}

func compareStableVersion(left, right stableVersion) int {
	for _, pair := range [][2]uint64{{left.Major, right.Major}, {left.Minor, right.Minor}, {left.Patch, right.Patch}} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	return 0
}
