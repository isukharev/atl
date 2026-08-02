package jira

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/httpx"
)

const (
	developmentMaxApplications = 8
	developmentMaxSelectors    = 24
	developmentMaxGroups       = 64
	developmentMaxProjects     = 64
	developmentMaxCommits      = 256
	developmentMaxBranches     = 128
	developmentMaxMRs          = 128
	developmentMaxArtifacts    = 512
	developmentMaxURLBytes     = 2048
	developmentMaxPathBytes    = 2048
	developmentMaxBranchBytes  = 512
)

var (
	developmentPositiveID  = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
	developmentApplication = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	developmentGitLabApp   = regexp.MustCompile(`^gitlab(?:selfmanaged|[._-][a-z0-9._-]{1,57})?$`)
	developmentProjectPart = regexp.MustCompile(`^[A-Za-z0-9._-]{1,255}$`)
	developmentFullSHA     = regexp.MustCompile(`^(?:[0-9A-Fa-f]{40}|[0-9A-Fa-f]{64})$`)
)

type developmentArray[T any] struct {
	Present bool
	Null    bool
	Values  []T
}

func (a *developmentArray[T]) UnmarshalJSON(raw []byte) error {
	a.Present = true
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		a.Null = true
		return nil
	}
	var values []T
	if err := json.Unmarshal(raw, &values); err != nil {
		return err
	}
	if values == nil {
		a.Null = true
		return nil
	}
	a.Values = values
	return nil
}

func (a developmentArray[T]) required() bool { return a.Present && !a.Null }
func (a developmentArray[T]) optional() bool { return !a.Present || !a.Null }

type developmentCount struct {
	Count json.Number `json:"count"`
}

type developmentSummaryCategory struct {
	Overall        *developmentCount            `json:"overall"`
	ByInstanceType *map[string]developmentCount `json:"byInstanceType"`
}

type developmentSummaryEnvelope struct {
	Errors       developmentArray[json.RawMessage]      `json:"errors"`
	ConfigErrors developmentArray[json.RawMessage]      `json:"configErrors"`
	Summary      *map[string]developmentSummaryCategory `json:"summary"`
}

type developmentRepositoryRef struct {
	URL string `json:"url"`
}

type developmentCommitDTO struct {
	ID         string                    `json:"id"`
	URL        string                    `json:"url"`
	Repository *developmentRepositoryRef `json:"repository"`
}

type developmentBranchDTO struct {
	Name       string                    `json:"name"`
	URL        string                    `json:"url"`
	Repository *developmentRepositoryRef `json:"repository"`
}

type developmentMRDTO struct {
	ID         json.RawMessage           `json:"id"`
	URL        string                    `json:"url"`
	Status     string                    `json:"status"`
	Repository *developmentRepositoryRef `json:"repository"`
}

type developmentRepositoryDTO struct {
	URL          string                                 `json:"url"`
	Commits      developmentArray[developmentCommitDTO] `json:"commits"`
	Branches     developmentArray[developmentBranchDTO] `json:"branches"`
	PullRequests developmentArray[developmentMRDTO]     `json:"pullRequests"`
}

type developmentDetailGroup struct {
	Repositories developmentArray[developmentRepositoryDTO] `json:"repositories"`
	Commits      developmentArray[developmentCommitDTO]     `json:"commits"`
	Branches     developmentArray[developmentBranchDTO]     `json:"branches"`
	PullRequests developmentArray[developmentMRDTO]         `json:"pullRequests"`
}

type developmentDetailEnvelope struct {
	Errors       developmentArray[json.RawMessage]        `json:"errors"`
	ConfigErrors developmentArray[json.RawMessage]        `json:"configErrors"`
	Detail       developmentArray[developmentDetailGroup] `json:"detail"`
}

type developmentSelector struct {
	RawApplication       string
	CanonicalApplication string
	DataType             string
	Expected             uint64
}

type developmentProjectKey struct{ host, path string }
type developmentCommitKey struct {
	project developmentProjectKey
	sha     string
}
type developmentBranchKey struct {
	project developmentProjectKey
	name    string
}
type developmentMRKey struct {
	project developmentProjectKey
	iid     string
}

type developmentInventory struct {
	projects  map[developmentProjectKey]domain.JiraDevelopmentProject
	commits   map[developmentCommitKey]domain.JiraDevelopmentCommit
	branches  map[developmentBranchKey]domain.JiraDevelopmentBranch
	mrs       map[developmentMRKey]domain.JiraDevelopmentMergeRequest
	groups    int
	artifacts int
}

func newDevelopmentInventory() *developmentInventory {
	return &developmentInventory{
		projects: map[developmentProjectKey]domain.JiraDevelopmentProject{},
		commits:  map[developmentCommitKey]domain.JiraDevelopmentCommit{},
		branches: map[developmentBranchKey]domain.JiraDevelopmentBranch{},
		mrs:      map[developmentMRKey]domain.JiraDevelopmentMergeRequest{},
	}
}

// ReadIssueDevelopment reads Jira's private Development surface. It returns no
// inventory until every selected detail response has been normalized and its
// summary count reconciled.
func (j *Jira) ReadIssueDevelopment(ctx context.Context, numericIssueID string) (domain.JiraDevelopmentInventory, error) {
	if !developmentPositiveID.MatchString(numericIssueID) {
		return domain.JiraDevelopmentInventory{}, fmt.Errorf("%w: Jira Development requires a positive numeric issue id", domain.ErrUsage)
	}
	ctx = domain.WithRedactedHTTPTrace(domain.WithSingleAttempt(ctx))
	summaryPath := "/rest/dev-status/1.0/issue/summary?" + url.Values{"issueId": []string{numericIssueID}}.Encode()
	raw, err := j.c.Do(ctx, http.MethodGet, summaryPath, nil, nil)
	if err != nil {
		return domain.JiraDevelopmentInventory{}, developmentRequestError(err)
	}
	selectors, err := decodeDevelopmentSummary(raw)
	if err != nil {
		return domain.JiraDevelopmentInventory{}, err
	}
	inv := newDevelopmentInventory()
	expected := map[string]uint64{}
	for _, selector := range selectors {
		expected[selector.DataType] += selector.Expected
	}
	for _, selector := range selectors {
		query := url.Values{
			"applicationType": []string{selector.RawApplication},
			"dataType":        []string{selector.DataType},
			"issueId":         []string{numericIssueID},
		}
		raw, requestErr := j.c.Do(ctx, http.MethodGet, "/rest/dev-status/1.0/issue/detail?"+query.Encode(), nil, nil)
		if requestErr != nil {
			return domain.JiraDevelopmentInventory{}, developmentRequestError(requestErr)
		}
		actual, decodeErr := inv.addDetail(raw, selector.DataType)
		if decodeErr != nil {
			return domain.JiraDevelopmentInventory{}, fmt.Errorf("%w: %s detail", decodeErr, selector.DataType)
		}
		if actual != selector.Expected {
			return domain.JiraDevelopmentInventory{}, developmentMalformed()
		}
	}
	if developmentMapCount(inv.commits) != expected["repository"] || developmentMapCount(inv.branches) != expected["branch"] ||
		developmentMapCount(inv.mrs) != expected["pullrequest"] {
		return domain.JiraDevelopmentInventory{}, developmentMalformed()
	}
	return inv.normalized(), nil
}

func decodeDevelopmentSummary(raw []byte) ([]developmentSelector, error) {
	var envelope developmentSummaryEnvelope
	if err := decodeDevelopmentJSON(raw, &envelope); err != nil || envelope.Summary == nil ||
		!envelope.Errors.required() || !envelope.ConfigErrors.required() {
		return nil, developmentMalformed()
	}
	if len(envelope.Errors.Values) != 0 || len(envelope.ConfigErrors.Values) != 0 {
		return nil, developmentMalformed()
	}
	relevant := map[string]bool{"repository": true, "branch": true, "pullrequest": true}
	ignored := map[string]bool{
		"build": true, "review": true, "deployment": true,
		"deployment-environment": true, "featureflag": true,
	}
	for key := range relevant {
		if _, ok := (*envelope.Summary)[key]; !ok {
			return nil, developmentMalformed()
		}
	}
	rawByCanonical := map[string]string{}
	selectors := []developmentSelector{}
	for dataType, category := range *envelope.Summary {
		overall, byApplication, ok := developmentCategoryCounts(category)
		if !ok {
			return nil, developmentMalformed()
		}
		var sum uint64
		for rawApplication, count := range byApplication {
			if !developmentApplication.MatchString(rawApplication) {
				return nil, developmentMalformed()
			}
			canonical := strings.ToLower(rawApplication)
			if previous, found := rawByCanonical[canonical]; found && previous != rawApplication {
				return nil, developmentMalformed()
			}
			rawByCanonical[canonical] = rawApplication
			if len(rawByCanonical) > developmentMaxApplications {
				return nil, developmentLimit()
			}
			if ^uint64(0)-sum < count {
				return nil, developmentMalformed()
			}
			sum += count
			if relevant[dataType] && count > 0 {
				if !developmentGitLabApp.MatchString(canonical) {
					return nil, developmentMalformed()
				}
				var classLimit uint64
				switch dataType {
				case "repository":
					classLimit = developmentMaxCommits
				case "branch":
					classLimit = developmentMaxBranches
				case "pullrequest":
					classLimit = developmentMaxMRs
				}
				if count > classLimit {
					return nil, developmentLimit()
				}
				selectors = append(selectors, developmentSelector{
					RawApplication: rawApplication, CanonicalApplication: canonical,
					DataType: dataType, Expected: count,
				})
			}
		}
		if sum != overall {
			return nil, developmentMalformed()
		}
		if relevant[dataType] {
			classLimit := uint64(developmentMaxCommits)
			switch dataType {
			case "branch":
				classLimit = developmentMaxBranches
			case "pullrequest":
				classLimit = developmentMaxMRs
			}
			if sum > classLimit {
				return nil, developmentLimit()
			}
		}
		if !relevant[dataType] && !ignored[dataType] && overall != 0 {
			return nil, developmentMalformed()
		}
	}
	if len(selectors) > developmentMaxSelectors {
		return nil, developmentLimit()
	}
	rank := map[string]int{"repository": 0, "branch": 1, "pullrequest": 2}
	sort.Slice(selectors, func(i, k int) bool {
		if rank[selectors[i].DataType] != rank[selectors[k].DataType] {
			return rank[selectors[i].DataType] < rank[selectors[k].DataType]
		}
		return selectors[i].CanonicalApplication < selectors[k].CanonicalApplication
	})
	return selectors, nil
}

func developmentCategoryCounts(category developmentSummaryCategory) (uint64, map[string]uint64, bool) {
	if category.Overall == nil || category.ByInstanceType == nil {
		return 0, nil, false
	}
	overall, ok := developmentCountValue(category.Overall.Count)
	if !ok {
		return 0, nil, false
	}
	by := make(map[string]uint64, len(*category.ByInstanceType))
	for application, raw := range *category.ByInstanceType {
		value, valid := developmentCountValue(raw.Count)
		if !valid {
			return 0, nil, false
		}
		by[application] = value
	}
	return overall, by, true
}

func developmentCountValue(number json.Number) (uint64, bool) {
	text := number.String()
	if text == "" || strings.ContainsAny(text, ".eE+-") {
		return 0, false
	}
	value, err := strconv.ParseUint(text, 10, 63)
	return value, err == nil
}

func (i *developmentInventory) addDetail(raw []byte, selectedType string) (uint64, error) {
	var envelope developmentDetailEnvelope
	if err := decodeDevelopmentJSON(raw, &envelope); err != nil || !envelope.Errors.required() ||
		!envelope.Detail.required() || !envelope.ConfigErrors.optional() {
		return 0, fmt.Errorf("%w: detail envelope", developmentMalformed())
	}
	if len(envelope.Errors.Values) != 0 || envelope.ConfigErrors.Present && len(envelope.ConfigErrors.Values) != 0 {
		return 0, fmt.Errorf("%w: detail plugin errors", developmentMalformed())
	}
	if i.groups+len(envelope.Detail.Values) > developmentMaxGroups {
		return 0, developmentLimit()
	}
	i.groups += len(envelope.Detail.Values)
	matched := map[string]struct{}{}
	var matchedCount uint64
	markMatched := func(key string) {
		if _, found := matched[key]; found {
			return
		}
		matched[key] = struct{}{}
		matchedCount++
	}
	for _, group := range envelope.Detail.Values {
		for _, repository := range group.Repositories.Values {
			project, ok := parseDevelopmentProject(repository.URL)
			if !ok {
				return 0, fmt.Errorf("%w: repository identity", developmentMalformed())
			}
			for _, commit := range repository.Commits.Values {
				key, err := i.addCommit(commit, &project)
				if err != nil {
					return 0, fmt.Errorf("%w: nested commit", err)
				}
				if selectedType == "repository" {
					markMatched(developmentCommitString(key))
				}
			}
			for _, branch := range repository.Branches.Values {
				key, err := i.addBranch(branch, &project)
				if err != nil {
					return 0, fmt.Errorf("%w: nested branch", err)
				}
				if selectedType == "branch" {
					markMatched(developmentBranchString(key))
				}
			}
			for _, mr := range repository.PullRequests.Values {
				key, err := i.addMR(mr, &project)
				if err != nil {
					return 0, fmt.Errorf("%w: nested merge request", err)
				}
				if selectedType == "pullrequest" {
					markMatched(developmentMRString(key))
				}
			}
		}
		for _, commit := range group.Commits.Values {
			key, err := i.addCommit(commit, nil)
			if err != nil {
				return 0, fmt.Errorf("%w: top-level commit", err)
			}
			if selectedType == "repository" {
				markMatched(developmentCommitString(key))
			}
		}
		for _, branch := range group.Branches.Values {
			key, err := i.addBranch(branch, nil)
			if err != nil {
				return 0, fmt.Errorf("%w: top-level branch", err)
			}
			if selectedType == "branch" {
				markMatched(developmentBranchString(key))
			}
		}
		for _, mr := range group.PullRequests.Values {
			key, err := i.addMR(mr, nil)
			if err != nil {
				return 0, fmt.Errorf("%w: top-level merge request", err)
			}
			if selectedType == "pullrequest" {
				markMatched(developmentMRString(key))
			}
		}
	}
	return matchedCount, nil
}

func developmentMapCount[K comparable, V any](values map[K]V) uint64 {
	var count uint64
	for range values {
		count++
	}
	return count
}

func (i *developmentInventory) inspectArtifact() error {
	i.artifacts++
	if i.artifacts > developmentMaxArtifacts {
		return developmentLimit()
	}
	return nil
}

func (i *developmentInventory) addProject(project developmentProjectKey) error {
	if _, found := i.projects[project]; found {
		return nil
	}
	if len(i.projects) >= developmentMaxProjects {
		return developmentLimit()
	}
	i.projects[project] = domain.JiraDevelopmentProject{Host: project.host, ProjectPath: project.path}
	return nil
}

func (i *developmentInventory) addCommit(raw developmentCommitDTO, explicit *developmentProjectKey) (developmentCommitKey, error) {
	if err := i.inspectArtifact(); err != nil {
		return developmentCommitKey{}, err
	}
	if !developmentFullSHA.MatchString(raw.ID) {
		return developmentCommitKey{}, developmentMalformed()
	}
	sha := strings.ToLower(raw.ID)
	project, value, ok := parseDevelopmentArtifact(raw.URL, "commit", sha)
	if !ok || !strings.EqualFold(value, sha) {
		return developmentCommitKey{}, developmentMalformed()
	}
	if raw.Repository != nil {
		nested, valid := parseDevelopmentProject(raw.Repository.URL)
		if !valid || explicit != nil && nested != *explicit {
			return developmentCommitKey{}, developmentMalformed()
		}
		explicit = &nested
	}
	if explicit != nil && project != *explicit {
		return developmentCommitKey{}, developmentMalformed()
	}
	if err := i.addProject(project); err != nil {
		return developmentCommitKey{}, err
	}
	key := developmentCommitKey{project: project, sha: sha}
	if _, found := i.commits[key]; !found {
		if len(i.commits) >= developmentMaxCommits {
			return developmentCommitKey{}, developmentLimit()
		}
		i.commits[key] = domain.JiraDevelopmentCommit{Host: project.host, ProjectPath: project.path, SHA: sha}
	}
	return key, nil
}

func (i *developmentInventory) addBranch(raw developmentBranchDTO, explicit *developmentProjectKey) (developmentBranchKey, error) {
	if err := i.inspectArtifact(); err != nil {
		return developmentBranchKey{}, err
	}
	if !validDevelopmentBranch(raw.Name) {
		return developmentBranchKey{}, fmt.Errorf("%w: branch name", developmentMalformed())
	}
	project, value, ok := parseDevelopmentArtifact(raw.URL, "tree", raw.Name)
	if !ok || value != raw.Name {
		return developmentBranchKey{}, fmt.Errorf("%w: branch URL", developmentMalformed())
	}
	if raw.Repository != nil {
		nested, valid := parseDevelopmentProject(raw.Repository.URL)
		if !valid || explicit != nil && nested != *explicit {
			return developmentBranchKey{}, fmt.Errorf("%w: branch repository", developmentMalformed())
		}
		explicit = &nested
	}
	if explicit != nil && project != *explicit {
		return developmentBranchKey{}, fmt.Errorf("%w: branch project mismatch", developmentMalformed())
	}
	if err := i.addProject(project); err != nil {
		return developmentBranchKey{}, err
	}
	key := developmentBranchKey{project: project, name: raw.Name}
	if _, found := i.branches[key]; !found {
		if len(i.branches) >= developmentMaxBranches {
			return developmentBranchKey{}, developmentLimit()
		}
		i.branches[key] = domain.JiraDevelopmentBranch{Host: project.host, ProjectPath: project.path, Name: raw.Name}
	}
	return key, nil
}

func (i *developmentInventory) addMR(raw developmentMRDTO, explicit *developmentProjectKey) (developmentMRKey, error) {
	if err := i.inspectArtifact(); err != nil {
		return developmentMRKey{}, err
	}
	rawIID, rawPresent, rawOK := parseDevelopmentRawID(raw.ID)
	if !rawOK {
		return developmentMRKey{}, fmt.Errorf("%w: merge-request id", developmentMalformed())
	}
	project, urlIID, ok := parseDevelopmentArtifact(raw.URL, "merge_requests", "")
	if !ok || !developmentPositiveID.MatchString(urlIID) || rawPresent && rawIID != urlIID {
		return developmentMRKey{}, fmt.Errorf("%w: merge-request URL", developmentMalformed())
	}
	if raw.Repository != nil {
		nested, valid := parseDevelopmentProject(raw.Repository.URL)
		if !valid || explicit != nil && nested != *explicit {
			return developmentMRKey{}, fmt.Errorf("%w: merge-request repository", developmentMalformed())
		}
		explicit = &nested
	}
	if explicit != nil && project != *explicit {
		return developmentMRKey{}, fmt.Errorf("%w: merge-request project mismatch", developmentMalformed())
	}
	if err := i.addProject(project); err != nil {
		return developmentMRKey{}, err
	}
	key := developmentMRKey{project: project, iid: urlIID}
	state := normalizeDevelopmentMRState(raw.Status)
	if existing, found := i.mrs[key]; found {
		merged, valid := mergeDevelopmentMRState(existing.State, state)
		if !valid {
			return developmentMRKey{}, fmt.Errorf("%w: merge-request state conflict", developmentMalformed())
		}
		existing.State = merged
		i.mrs[key] = existing
	} else {
		if len(i.mrs) >= developmentMaxMRs {
			return developmentMRKey{}, developmentLimit()
		}
		i.mrs[key] = domain.JiraDevelopmentMergeRequest{Host: project.host, ProjectPath: project.path, IID: urlIID, State: state}
	}
	return key, nil
}

func parseDevelopmentProject(raw string) (developmentProjectKey, bool) {
	u, escaped, ok := parseDevelopmentURL(raw)
	if !ok {
		return developmentProjectKey{}, false
	}
	return developmentProjectFromEscaped(u, escaped)
}

func parseDevelopmentURL(raw string) (*url.URL, string, bool) {
	if raw == "" || len(raw) > developmentMaxURLBytes {
		return nil, "", false
	}
	u, err := url.Parse(raw)
	if err != nil || strings.ToLower(u.Scheme) != "https" || u.Opaque != "" || u.User != nil ||
		u.Hostname() == "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") {
		return nil, "", false
	}
	escaped := strings.Trim(u.EscapedPath(), "/")
	if escaped == "" {
		return nil, "", false
	}
	return u, escaped, true
}

func developmentProjectFromEscaped(u *url.URL, escaped string) (developmentProjectKey, bool) {
	parts := strings.Split(strings.Trim(escaped, "/"), "/")
	if len(parts) < 2 || len(parts) > 32 {
		return developmentProjectKey{}, false
	}
	decoded := make([]string, len(parts))
	for index, part := range parts {
		value, err := url.PathUnescape(part)
		if err != nil || value == "" || !developmentProjectPart.MatchString(value) || value == "." || value == ".." {
			return developmentProjectKey{}, false
		}
		decoded[index] = value
	}
	decoded[len(decoded)-1] = strings.TrimSuffix(decoded[len(decoded)-1], ".git")
	if decoded[len(decoded)-1] == "" || !developmentProjectPart.MatchString(decoded[len(decoded)-1]) {
		return developmentProjectKey{}, false
	}
	path := strings.Join(decoded, "/")
	if len(path) > developmentMaxPathBytes {
		return developmentProjectKey{}, false
	}
	hostname := strings.ToLower(u.Hostname())
	if strings.Contains(hostname, ":") {
		hostname = "[" + hostname + "]"
	}
	host := hostname
	if port := u.Port(); port != "" {
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 1 || portNumber > 65535 {
			return developmentProjectKey{}, false
		}
		if portNumber != 443 {
			host += ":" + strconv.Itoa(portNumber)
		}
	}
	return developmentProjectKey{host: host, path: path}, true
}

func parseDevelopmentArtifact(raw, kind, expected string) (developmentProjectKey, string, bool) {
	u, escaped, ok := parseDevelopmentURL(raw)
	if !ok {
		return developmentProjectKey{}, "", false
	}
	markers := []string{"/-/" + kind + "/", "/" + kind + "/"}
	for markerIndex, marker := range markers {
		matches := []struct {
			project developmentProjectKey
			value   string
		}{}
		for offset := 0; ; {
			index := strings.Index(escaped[offset:], marker)
			if index < 0 {
				break
			}
			index += offset
			// The legacy marker inside /-/kind/ is not a second candidate.
			if markerIndex == 1 && index >= 2 && escaped[index-2:index] == "/-" {
				offset = index + 1
				continue
			}
			project, valid := developmentProjectFromEscaped(u, escaped[:index])
			value, decodeErr := url.PathUnescape(escaped[index+len(marker):])
			if valid && decodeErr == nil && value != "" && (expected == "" || value == expected || kind == "commit" && strings.EqualFold(value, expected)) {
				matches = append(matches, struct {
					project developmentProjectKey
					value   string
				}{project, value})
			}
			offset = index + 1
		}
		if len(matches) > 0 {
			first := matches[0]
			for _, match := range matches[1:] {
				if match != first {
					return developmentProjectKey{}, "", false
				}
			}
			return first.project, first.value, true
		}
	}
	return developmentProjectKey{}, "", false
}

func validDevelopmentBranch(value string) bool {
	if value == "" || len(value) > developmentMaxBranchBytes || !utf8.ValidString(value) {
		return false
	}
	for _, char := range value {
		if char == 0 || unicode.IsControl(char) {
			return false
		}
	}
	return true
}

func parseDevelopmentRawID(raw json.RawMessage) (string, bool, bool) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", false, true
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if developmentPositiveID.MatchString(text) {
			return text, true, true
		}
		if text != "" && strings.IndexFunc(text, func(current rune) bool { return current < '0' || current > '9' }) >= 0 {
			// Some Jira Development providers expose an opaque global id here.
			// The project-local IID remains authoritative from the canonical URL.
			return "", false, true
		}
		return "", true, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var number json.Number
	if decoder.Decode(&number) != nil {
		return "", true, false
	}
	text = number.String()
	return text, true, developmentPositiveID.MatchString(text)
}

func normalizeDevelopmentMRState(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "open", "opened":
		return "open"
	case "merged":
		return "merged"
	case "closed", "declined":
		return "closed"
	default:
		return "unknown"
	}
}

func mergeDevelopmentMRState(left, right string) (string, bool) {
	switch {
	case left == right:
		return left, true
	case left == "unknown":
		return right, true
	case right == "unknown":
		return left, true
	default:
		return "", false
	}
}

func (i *developmentInventory) normalized() domain.JiraDevelopmentInventory {
	out := domain.JiraDevelopmentInventory{
		Projects:      make([]domain.JiraDevelopmentProject, 0, len(i.projects)),
		Commits:       make([]domain.JiraDevelopmentCommit, 0, len(i.commits)),
		Branches:      make([]domain.JiraDevelopmentBranch, 0, len(i.branches)),
		MergeRequests: make([]domain.JiraDevelopmentMergeRequest, 0, len(i.mrs)),
	}
	for _, value := range i.projects {
		out.Projects = append(out.Projects, value)
	}
	for _, value := range i.commits {
		out.Commits = append(out.Commits, value)
	}
	for _, value := range i.branches {
		out.Branches = append(out.Branches, value)
	}
	for _, value := range i.mrs {
		out.MergeRequests = append(out.MergeRequests, value)
	}
	sort.Slice(out.Projects, func(a, b int) bool {
		return out.Projects[a].Host+"\x00"+out.Projects[a].ProjectPath < out.Projects[b].Host+"\x00"+out.Projects[b].ProjectPath
	})
	sort.Slice(out.Commits, func(a, b int) bool {
		return developmentCommitValueString(out.Commits[a]) < developmentCommitValueString(out.Commits[b])
	})
	sort.Slice(out.Branches, func(a, b int) bool {
		return developmentBranchValueString(out.Branches[a]) < developmentBranchValueString(out.Branches[b])
	})
	sort.Slice(out.MergeRequests, func(a, b int) bool {
		return developmentMRValueString(out.MergeRequests[a]) < developmentMRValueString(out.MergeRequests[b])
	})
	return out
}

func developmentCommitString(v developmentCommitKey) string {
	return v.project.host + "\x00" + v.project.path + "\x00" + v.sha
}
func developmentBranchString(v developmentBranchKey) string {
	return v.project.host + "\x00" + v.project.path + "\x00" + v.name
}
func developmentMRString(v developmentMRKey) string {
	return v.project.host + "\x00" + v.project.path + "\x00" + v.iid
}
func developmentCommitValueString(v domain.JiraDevelopmentCommit) string {
	return v.Host + "\x00" + v.ProjectPath + "\x00" + v.SHA
}
func developmentBranchValueString(v domain.JiraDevelopmentBranch) string {
	return v.Host + "\x00" + v.ProjectPath + "\x00" + v.Name
}
func developmentMRValueString(v domain.JiraDevelopmentMergeRequest) string {
	return v.Host + "\x00" + v.ProjectPath + "\x00" + v.IID
}

func decodeDevelopmentJSON(raw []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("trailing JSON")
	}
	return nil
}

func developmentRequestError(err error) error {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, domain.ErrReadAttemptBudgetExhausted), errors.Is(err, domain.ErrReadResponseBudgetExhausted):
		return err
	case errors.Is(err, domain.ErrAuth):
		return fmt.Errorf("%w: Jira Development request was not authenticated", domain.ErrAuth)
	case errors.Is(err, domain.ErrForbidden):
		return fmt.Errorf("%w: Jira Development request was forbidden", domain.ErrForbidden)
	case errors.Is(err, domain.ErrNotFound):
		return fmt.Errorf("%w: Jira Development endpoint is unavailable", domain.ErrNotFound)
	}
	var apiErr *httpx.APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusMethodNotAllowed {
		return fmt.Errorf("%w: Jira Development endpoint is unavailable", domain.ErrNotFound)
	}
	return errors.New("Jira Development request failed")
}

func developmentMalformed() error {
	return fmt.Errorf("%w: Jira Development response is malformed", domain.ErrCheckFailed)
}

func developmentLimit() error {
	return fmt.Errorf("%w: %w: Jira Development response exceeded a safety limit", domain.ErrCheckFailed, domain.ErrOutputLimit)
}
