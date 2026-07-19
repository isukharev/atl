// Package skillrouting validates the provider-neutral routing policy for the
// shipped agent skills. It deliberately evaluates reviewed task-class
// annotations, not natural-language prompts: provider NLP behavior is measured
// separately with model-in-the-loop benchmarks.
package skillrouting

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/skillmeta"
)

const (
	SchemaVersion = 1
	maxInputBytes = 1 << 20
	maxSkills     = 64
	maxCases      = 4096
	maxBoundaries = 256
)

var (
	skillNameRE = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)
	caseIDRE    = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)
	taskClassRE = regexp.MustCompile(`^[a-z][a-z0-9-]*(?:/[a-z][a-z0-9-]*)+$`)
)

// Registry is the source-independent routing policy. Name is the logical
// skill name below the plugin namespace; clients may project it differently.
type Registry struct {
	SchemaVersion int     `json:"schema_version"`
	Skills        []Skill `json:"skills"`
}

type Skill struct {
	Name                string   `json:"name"`
	Implicit            bool     `json:"implicit"`
	PositiveTaskClasses []string `json:"owned_task_classes"`
	NegativeTaskClasses []string `json:"excluded_task_classes"`

	implicitPresent bool
}

func (s *Skill) UnmarshalJSON(data []byte) error {
	type wireSkill struct {
		Name                string   `json:"name"`
		Implicit            *bool    `json:"implicit"`
		PositiveTaskClasses []string `json:"owned_task_classes"`
		NegativeTaskClasses []string `json:"excluded_task_classes"`
	}
	var wire wireSkill
	if err := decodeStrict(bytes.NewReader(data), &wire); err != nil {
		return err
	}
	if wire.Implicit == nil {
		return fmt.Errorf("implicit is required")
	}
	*s = Skill{
		Name: wire.Name, Implicit: *wire.Implicit,
		PositiveTaskClasses: wire.PositiveTaskClasses,
		NegativeTaskClasses: wire.NegativeTaskClasses,
		implicitPresent:     true,
	}
	return nil
}

type Corpus struct {
	SchemaVersion int           `json:"schema_version"`
	Cases         []RoutingCase `json:"cases"`
}

// RoutingCase retains the human-readable prompt for later model-in-the-loop
// use, while the deterministic oracle consumes only TaskClass and Invocation.
// ExpectedSkill must be present in JSON and may be null.
type RoutingCase struct {
	ID              string   `json:"id"`
	Prompt          string   `json:"prompt"`
	TaskClass       string   `json:"task_class"`
	Invocation      string   `json:"invocation"`
	InvokedSkill    *string  `json:"invoked_skill,omitempty"`
	ExpectedSkill   *string  `json:"expected_skill"`
	ForbiddenSkills []string `json:"forbidden_skills,omitempty"`

	expectedSkillPresent bool
}

func (c *RoutingCase) UnmarshalJSON(data []byte) error {
	type wireCase struct {
		ID              string          `json:"id"`
		Prompt          string          `json:"prompt"`
		TaskClass       string          `json:"task_class"`
		Invocation      string          `json:"invocation"`
		InvokedSkill    *string         `json:"invoked_skill,omitempty"`
		ExpectedSkill   json.RawMessage `json:"expected_skill"`
		ForbiddenSkills []string        `json:"forbidden_skills,omitempty"`
	}
	var wire wireCase
	if err := decodeStrict(bytes.NewReader(data), &wire); err != nil {
		return err
	}
	if wire.ExpectedSkill == nil {
		return fmt.Errorf("expected_skill is required")
	}
	var expected *string
	if !bytes.Equal(bytes.TrimSpace(wire.ExpectedSkill), []byte("null")) {
		var value string
		if err := json.Unmarshal(wire.ExpectedSkill, &value); err != nil {
			return fmt.Errorf("expected_skill must be a string or null")
		}
		expected = &value
	}
	*c = RoutingCase{
		ID: wire.ID, Prompt: wire.Prompt, TaskClass: wire.TaskClass,
		Invocation: wire.Invocation, InvokedSkill: wire.InvokedSkill, ExpectedSkill: expected,
		ForbiddenSkills: wire.ForbiddenSkills, expectedSkillPresent: true,
	}
	return nil
}

type Summary struct {
	SchemaVersion      int `json:"schema_version"`
	Skills             int `json:"skills"`
	ImplicitSkills     int `json:"implicit_skills"`
	ExplicitOnlySkills int `json:"explicit_only_skills"`
	Cases              int `json:"cases"`
	RoutedCases        int `json:"routed_cases"`
	NoActivationCases  int `json:"no_activation_cases"`
}

func LoadRegistry(reader io.Reader) (Registry, error) {
	var registry Registry
	if err := decodeBoundedStrict(reader, &registry); err != nil {
		return Registry{}, fmt.Errorf("invalid skill routing registry: %w", err)
	}
	if err := validateRegistry(registry); err != nil {
		return Registry{}, err
	}
	return registry, nil
}

func LoadCorpus(reader io.Reader) (Corpus, error) {
	var corpus Corpus
	if err := decodeBoundedStrict(reader, &corpus); err != nil {
		return Corpus{}, fmt.Errorf("invalid skill routing corpus: %w", err)
	}
	if err := validateCorpus(corpus); err != nil {
		return Corpus{}, err
	}
	return corpus, nil
}

func LoadRegistryFile(path string) (Registry, error) {
	file, err := openRegularFile(path)
	if err != nil {
		return Registry{}, fmt.Errorf("open skill routing registry: %w", err)
	}
	defer file.Close()
	return LoadRegistry(file)
}

func LoadCorpusFile(path string) (Corpus, error) {
	file, err := openRegularFile(path)
	if err != nil {
		return Corpus{}, fmt.Errorf("open skill routing corpus: %w", err)
	}
	defer file.Close()
	return LoadCorpus(file)
}

// ValidateCatalog binds the provider-neutral routing policy to the complete
// reviewed source metadata catalog before routing cases are evaluated.
func ValidateCatalog(catalog skillmeta.Catalog, registry Registry, corpus Corpus) (Summary, error) {
	if len(catalog.Skills) != len(registry.Skills) {
		return Summary{}, fmt.Errorf("source metadata and routing registry skill inventories differ")
	}
	for index, metadata := range catalog.Skills {
		policy := registry.Skills[index]
		if metadata.Name != policy.Name {
			return Summary{}, fmt.Errorf("source metadata and routing registry skill inventories differ")
		}
		if metadata.OpenAI.AllowImplicitInvocation != policy.Implicit || metadata.DisableModelInvocation == policy.Implicit {
			return Summary{}, fmt.Errorf("skill %q implicit policy differs between metadata and routing registry", metadata.Name)
		}
	}
	return Validate(registry, corpus)
}

// openRegularFile rejects symlinks and special files before reading routing
// policy. Comparing the opened descriptor with the lstat result also closes
// the check/open replacement window without following a substituted symlink.
func openRegularFile(path string) (*os.File, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return nil, fmt.Errorf("routing document is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	after, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) {
		file.Close()
		return nil, fmt.Errorf("routing document changed while it was opened")
	}
	return file, nil
}

// Validate evaluates the complete corpus and proves that every declared
// owned route and declared exclusion is witnessed by at least one case.
func Validate(registry Registry, corpus Corpus) (Summary, error) {
	if err := validateRegistry(registry); err != nil {
		return Summary{}, err
	}
	if err := validateCorpus(corpus); err != nil {
		return Summary{}, err
	}

	byName := make(map[string]Skill, len(registry.Skills))
	positiveWitnesses := make(map[string]map[string]bool, len(registry.Skills))
	negativeWitnesses := make(map[string]map[string]bool, len(registry.Skills))
	expectedWitnesses := make(map[string]bool, len(registry.Skills))
	implicitBlockedWitnesses := make(map[string]bool, len(registry.Skills))
	for _, skill := range registry.Skills {
		byName[skill.Name] = skill
		positiveWitnesses[skill.Name] = make(map[string]bool, len(skill.PositiveTaskClasses))
		negativeWitnesses[skill.Name] = make(map[string]bool, len(skill.NegativeTaskClasses))
	}

	summary := Summary{SchemaVersion: SchemaVersion, Skills: len(registry.Skills), Cases: len(corpus.Cases)}
	for _, skill := range registry.Skills {
		if skill.Implicit {
			summary.ImplicitSkills++
		} else {
			summary.ExplicitOnlySkills++
		}
	}

	for _, testCase := range corpus.Cases {
		selected, eligible, err := selectSkill(registry.Skills, testCase)
		if err != nil {
			return Summary{}, fmt.Errorf("routing case %q: %w", testCase.ID, err)
		}
		if selected == "" {
			summary.NoActivationCases++
		} else {
			summary.RoutedCases++
			expectedWitnesses[selected] = true
		}
		want := ""
		if testCase.ExpectedSkill != nil {
			want = *testCase.ExpectedSkill
			if _, ok := byName[want]; !ok {
				return Summary{}, fmt.Errorf("routing case %q references unknown expected skill", testCase.ID)
			}
		}
		if selected != want {
			return Summary{}, fmt.Errorf("routing case %q selected %q, want %q", testCase.ID, selected, want)
		}

		for _, forbidden := range testCase.ForbiddenSkills {
			skill, ok := byName[forbidden]
			if !ok {
				return Summary{}, fmt.Errorf("routing case %q references unknown forbidden skill", testCase.ID)
			}
			if eligible[forbidden] {
				return Summary{}, fmt.Errorf("routing case %q leaves forbidden skill %q eligible", testCase.ID, forbidden)
			}
			switch {
			case contains(skill.NegativeTaskClasses, testCase.TaskClass):
				negativeWitnesses[forbidden][testCase.TaskClass] = true
			case !skill.Implicit && testCase.Invocation == "implicit" && contains(skill.PositiveTaskClasses, testCase.TaskClass):
				// The explicit-only invocation policy is itself the exclusion.
			default:
				return Summary{}, fmt.Errorf("routing case %q forbidden skill %q does not declare an exclusion", testCase.ID, forbidden)
			}
		}
		classified := false
		for _, skill := range registry.Skills {
			if contains(skill.PositiveTaskClasses, testCase.TaskClass) {
				positiveWitnesses[skill.Name][testCase.TaskClass] = true
				classified = true
				if !skill.Implicit && testCase.Invocation == "implicit" && !eligible[skill.Name] {
					implicitBlockedWitnesses[skill.Name] = true
				}
			}
			if contains(skill.NegativeTaskClasses, testCase.TaskClass) {
				classified = true
			}
		}
		if !classified {
			return Summary{}, fmt.Errorf("routing case %q uses an unclassified task_class", testCase.ID)
		}
	}

	for _, skill := range registry.Skills {
		if !expectedWitnesses[skill.Name] {
			return Summary{}, fmt.Errorf("skill %q is never the expected route", skill.Name)
		}
		if !skill.Implicit && !implicitBlockedWitnesses[skill.Name] {
			return Summary{}, fmt.Errorf("explicit-only skill %q has no implicit refusal case", skill.Name)
		}
		for _, taskClass := range skill.PositiveTaskClasses {
			if !positiveWitnesses[skill.Name][taskClass] {
				return Summary{}, fmt.Errorf("skill %q has an unexercised owned route", skill.Name)
			}
		}
		for _, taskClass := range skill.NegativeTaskClasses {
			if !negativeWitnesses[skill.Name][taskClass] {
				return Summary{}, fmt.Errorf("skill %q has an unexercised exclusion", skill.Name)
			}
		}
	}
	return summary, nil
}

func selectSkill(skills []Skill, testCase RoutingCase) (string, map[string]bool, error) {
	eligible := make(map[string]bool, len(skills))
	candidates := make([]string, 0, 2)
	for _, skill := range skills {
		positive := contains(skill.PositiveTaskClasses, testCase.TaskClass)
		negative := contains(skill.NegativeTaskClasses, testCase.TaskClass)
		if positive && negative {
			return "", nil, fmt.Errorf("skill %q has conflicting boundaries", skill.Name)
		}
		allowed := positive && !negative && (testCase.Invocation == "explicit" || skill.Implicit)
		eligible[skill.Name] = allowed
		if !allowed {
			continue
		}
		candidates = append(candidates, skill.Name)
	}
	if len(candidates) > 1 {
		return "", eligible, fmt.Errorf("route is ambiguous")
	}
	if len(candidates) == 0 {
		return "", eligible, nil
	}
	return candidates[0], eligible, nil
}

func validateRegistry(registry Registry) error {
	if registry.SchemaVersion != SchemaVersion {
		return fmt.Errorf("skill routing registry schema_version must be %d", SchemaVersion)
	}
	if len(registry.Skills) == 0 || len(registry.Skills) > maxSkills {
		return fmt.Errorf("skill routing registry must contain 1..%d skills", maxSkills)
	}
	seen := make(map[string]struct{}, len(registry.Skills))
	for index, skill := range registry.Skills {
		if !skillNameRE.MatchString(skill.Name) {
			return fmt.Errorf("skill routing registry contains an invalid skill name")
		}
		if _, exists := seen[skill.Name]; exists {
			return fmt.Errorf("skill routing registry contains a duplicate skill")
		}
		seen[skill.Name] = struct{}{}
		if !skill.implicitPresent {
			return fmt.Errorf("skill %q is missing implicit", skill.Name)
		}
		if index > 0 && registry.Skills[index-1].Name >= skill.Name {
			return fmt.Errorf("skill routing registry skills must be sorted by name")
		}
		if err := validateTaskClasses(skill.PositiveTaskClasses, true); err != nil {
			return fmt.Errorf("skill %q owned routes: %w", skill.Name, err)
		}
		if err := validateTaskClasses(skill.NegativeTaskClasses, true); err != nil {
			return fmt.Errorf("skill %q exclusions: %w", skill.Name, err)
		}
		for _, taskClass := range skill.PositiveTaskClasses {
			if contains(skill.NegativeTaskClasses, taskClass) {
				return fmt.Errorf("skill %q has conflicting boundaries", skill.Name)
			}
		}
	}
	return nil
}

func validateCorpus(corpus Corpus) error {
	if corpus.SchemaVersion != SchemaVersion {
		return fmt.Errorf("skill routing corpus schema_version must be %d", SchemaVersion)
	}
	if len(corpus.Cases) == 0 || len(corpus.Cases) > maxCases {
		return fmt.Errorf("skill routing corpus must contain 1..%d cases", maxCases)
	}
	seenIDs := make(map[string]struct{}, len(corpus.Cases))
	seenPrompts := make(map[string]struct{}, len(corpus.Cases))
	for _, testCase := range corpus.Cases {
		if !caseIDRE.MatchString(testCase.ID) {
			return fmt.Errorf("skill routing corpus contains an invalid case id")
		}
		if _, exists := seenIDs[testCase.ID]; exists {
			return fmt.Errorf("skill routing corpus contains a duplicate case id")
		}
		seenIDs[testCase.ID] = struct{}{}
		if testCase.Prompt != strings.TrimSpace(testCase.Prompt) || testCase.Prompt == "" || len(testCase.Prompt) > 2_000 {
			return fmt.Errorf("routing case %q has an invalid prompt", testCase.ID)
		}
		if _, exists := seenPrompts[testCase.Prompt]; exists {
			return fmt.Errorf("skill routing corpus contains a duplicate prompt")
		}
		seenPrompts[testCase.Prompt] = struct{}{}
		if !taskClassRE.MatchString(testCase.TaskClass) {
			return fmt.Errorf("routing case %q has an invalid task_class", testCase.ID)
		}
		if testCase.Invocation != "implicit" && testCase.Invocation != "explicit" {
			return fmt.Errorf("routing case %q has an invalid invocation", testCase.ID)
		}
		switch testCase.Invocation {
		case "explicit":
			if testCase.InvokedSkill == nil || !skillNameRE.MatchString(*testCase.InvokedSkill) {
				return fmt.Errorf("routing case %q must name its explicitly invoked skill", testCase.ID)
			}
			if !hasBareSkillInvocation(testCase.Prompt, *testCase.InvokedSkill) {
				return fmt.Errorf("routing case %q prompt does not invoke its declared skill", testCase.ID)
			}
		case "implicit":
			if testCase.InvokedSkill != nil {
				return fmt.Errorf("routing case %q must not name an invoked skill for implicit routing", testCase.ID)
			}
		}
		if !testCase.expectedSkillPresent {
			return fmt.Errorf("routing case %q is missing expected_skill", testCase.ID)
		}
		if testCase.ExpectedSkill != nil && !skillNameRE.MatchString(*testCase.ExpectedSkill) {
			return fmt.Errorf("routing case %q has an invalid expected_skill", testCase.ID)
		}
		if err := validateSkillNames(testCase.ForbiddenSkills); err != nil {
			return fmt.Errorf("routing case %q forbidden_skills: %w", testCase.ID, err)
		}
		if testCase.ExpectedSkill != nil && contains(testCase.ForbiddenSkills, *testCase.ExpectedSkill) {
			return fmt.Errorf("routing case %q forbids its expected skill", testCase.ID)
		}
		if testCase.InvokedSkill != nil && (testCase.ExpectedSkill == nil || *testCase.InvokedSkill != *testCase.ExpectedSkill) {
			return fmt.Errorf("routing case %q explicit invocation differs from expected skill", testCase.ID)
		}
	}
	return nil
}

func validateTaskClasses(values []string, required bool) error {
	if required && len(values) == 0 || len(values) > maxBoundaries {
		return fmt.Errorf("must contain 1..%d task classes", maxBoundaries)
	}
	for index, value := range values {
		if !taskClassRE.MatchString(value) {
			return fmt.Errorf("contains an invalid task class")
		}
		if index > 0 && values[index-1] >= value {
			return fmt.Errorf("must be sorted and unique")
		}
	}
	return nil
}

func validateSkillNames(values []string) error {
	for index, value := range values {
		if !skillNameRE.MatchString(value) {
			return fmt.Errorf("contains an invalid skill name")
		}
		if index > 0 && values[index-1] >= value {
			return fmt.Errorf("must be sorted and unique")
		}
	}
	return nil
}

func decodeBoundedStrict(reader io.Reader, target any) error {
	data, err := io.ReadAll(io.LimitReader(reader, maxInputBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxInputBytes {
		return fmt.Errorf("document exceeds %d bytes", maxInputBytes)
	}
	return decodeStrict(bytes.NewReader(data), target)
}

func decodeStrict(reader io.Reader, target any) error {
	data, err := io.ReadAll(io.LimitReader(reader, maxInputBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxInputBytes {
		return fmt.Errorf("document exceeds %d bytes", maxInputBytes)
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("document contains multiple JSON values")
		}
		return err
	}
	return nil
}

func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return fmt.Errorf("object key is not a string")
				}
				if _, exists := seen[key]; exists {
					return fmt.Errorf("duplicate JSON key %q", key)
				}
				seen[key] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return fmt.Errorf("unexpected JSON delimiter")
		}
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("document contains multiple JSON values")
		}
		return err
	}
	return nil
}

func hasBareSkillInvocation(prompt, skillName string) bool {
	token := "$" + skillName
	if strings.Count(prompt, token) != 1 {
		return false
	}
	index := strings.Index(prompt, token)
	if index < 0 {
		return false
	}
	after := index + len(token)
	if after == len(prompt) {
		return true
	}
	next := prompt[after]
	switch {
	case next >= 'a' && next <= 'z', next >= 'A' && next <= 'Z',
		next >= '0' && next <= '9', next == '_', next == '-', next == ':':
		return false
	default:
		return true
	}
}

func contains(values []string, target string) bool {
	index := sort.SearchStrings(values, target)
	return index < len(values) && values[index] == target
}
