// Command check-premerge binds hosted checks to one current pull request revision.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/isukharev/atl/scripts/check-docs-freshness/hosted"
)

var shaPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

type revision struct {
	SHA  string `json:"sha"`
	Ref  string `json:"ref"`
	Repo struct {
		FullName string `json:"full_name"`
	} `json:"repo"`
}

type pullRequest struct {
	Number int      `json:"number"`
	State  string   `json:"state"`
	Draft  bool     `json:"draft"`
	Head   revision `json:"head"`
	Base   revision `json:"base"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

type event struct {
	Action      string      `json:"action"`
	Number      int         `json:"number"`
	PullRequest pullRequest `json:"pull_request"`
}

type binding struct {
	Repository string
	Number     int
	Head       string
	Base       string
	Checkout   string
	Ref        string
	HeadRef    string
	BaseRef    string
	Full       bool
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "check-premerge:", err)
		os.Exit(1)
	}
	fmt.Println("premerge binding: ok")
}

func run() error {
	if len(os.Args) != 2 || (os.Args[1] != "bind" && os.Args[1] != "ready") {
		return errors.New("expected bind or ready")
	}
	body, err := os.ReadFile(os.Getenv("GITHUB_EVENT_PATH"))
	if err != nil {
		return errors.New("cannot read workflow event")
	}
	b, err := eventBinding(os.Getenv("GITHUB_EVENT_NAME"), os.Getenv("GITHUB_REPOSITORY"),
		os.Getenv("GITHUB_SHA"), os.Getenv("GITHUB_REF"), body)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := verifyCheckout(ctx, b); err != nil {
		return err
	}
	plan, err := selectPlan(ctx, b)
	if err != nil {
		return err
	}
	if os.Args[1] == "ready" {
		if err := validateNeeds([]byte(os.Getenv("ATL_PREMERGE_NEEDS")), plan); err != nil {
			return err
		}
	}
	// Project at the producer: PR bodies, titles and other unrelated metadata
	// never enter diagnostics. gh retains the workflow's read-only token scope.
	query := `{number,state,draft,head:{sha:.head.sha,ref:.head.ref,repo:{full_name:.head.repo.full_name}},base:{sha:.base.sha,ref:.base.ref,repo:{full_name:.base.repo.full_name}}}`
	endpoint := fmt.Sprintf("repos/%s/pulls/%d", b.Repository, b.Number)
	body, err = exec.CommandContext(ctx, "gh", "api", "--hostname", "github.com", endpoint, "--jq", query).Output()
	if err != nil {
		return errors.New("cannot read current pull request; rerun after resolving API access")
	}
	var current pullRequest
	if err := json.Unmarshal(body, &current); err != nil {
		return errors.New("invalid current pull request response")
	}
	if err := validateCurrent(b, current); err != nil {
		return err
	}
	if os.Args[1] == "bind" {
		return publishPlan(plan, os.Getenv("GITHUB_OUTPUT"))
	}
	return nil
}

func eventBinding(name, repository, sha, ref string, body []byte) (binding, error) {
	b := binding{Repository: repository, Checkout: sha, Ref: ref}
	if !repositoryPattern.MatchString(repository) || !shaPattern.MatchString(sha) {
		return binding{}, errors.New("invalid workflow repository or checkout revision")
	}
	var e event
	if len(body) > 4<<20 || json.Unmarshal(body, &e) != nil {
		return binding{}, errors.New("invalid workflow event")
	}
	if name != "pull_request" || e.Action != "ready_for_review" {
		return binding{}, errors.New("premerge binding requires a ready_for_review pull request event")
	}
	b.Number, b.Head, b.Base = e.Number, e.PullRequest.Head.SHA, e.PullRequest.Base.SHA
	b.HeadRef, b.BaseRef = e.PullRequest.Head.Ref, e.PullRequest.Base.Ref
	if b.Number <= 0 || !shaPattern.MatchString(b.Head) || !shaPattern.MatchString(b.Base) {
		return binding{}, errors.New("missing or invalid PR/head/base binding")
	}
	if e.PullRequest.Number != b.Number || e.PullRequest.State != "open" || e.PullRequest.Draft ||
		e.PullRequest.Head.Repo.FullName != repository || e.PullRequest.Base.Repo.FullName != repository ||
		b.HeadRef == "" || b.BaseRef != "main" || ref != fmt.Sprintf("refs/pull/%d/merge", b.Number) {
		return binding{}, errors.New("pull request event does not identify the expected open same-repository merge ref")
	}
	for _, label := range e.PullRequest.Labels {
		if label.Name == "ci-full" {
			b.Full = true
			break
		}
	}
	return b, nil
}

func validateCurrent(b binding, current pullRequest) error {
	if current.Number != b.Number || current.State != "open" || current.Draft ||
		current.Head.Repo.FullName != b.Repository || current.Base.Repo.FullName != b.Repository ||
		current.Head.SHA != b.Head || current.Base.SHA != b.Base ||
		current.Head.Ref != b.HeadRef || current.Base.Ref != b.BaseRef ||
		b.Ref != fmt.Sprintf("refs/pull/%d/merge", b.Number) {
		return errors.New("pull request is draft, closed, or its head/base changed; review and request checks again")
	}
	return nil
}

func verifyCheckout(ctx context.Context, b binding) error {
	head, err := exec.CommandContext(ctx, "git", "rev-parse", "HEAD").Output()
	if err != nil || strings.TrimSpace(string(head)) != b.Checkout {
		return errors.New("checkout does not match the workflow revision")
	}
	parents, err := exec.CommandContext(ctx, "git", "rev-list", "--parents", "-n", "1", b.Checkout).Output()
	if err != nil || strings.TrimSpace(string(parents)) != b.Checkout+" "+b.Base+" "+b.Head {
		return errors.New("pull request checkout is not the ordered merge of the expected base and head")
	}
	if err := exec.CommandContext(ctx, "git", "merge-base", "--is-ancestor", b.Base, b.Head).Run(); err != nil {
		return errors.New("pull request head does not contain the exact current base")
	}
	mergeTree, mergeErr := exec.CommandContext(ctx, "git", "rev-parse", b.Checkout+"^{tree}").Output()
	headTree, headErr := exec.CommandContext(ctx, "git", "rev-parse", b.Head+"^{tree}").Output()
	if mergeErr != nil || headErr != nil || strings.TrimSpace(string(mergeTree)) != strings.TrimSpace(string(headTree)) {
		return errors.New("pull request merge tree differs from the reviewed head tree")
	}
	return nil
}

func validateNeeds(body []byte, plan hosted.Plan) error {
	var needs map[string]struct {
		Result  string            `json:"result"`
		Outputs map[string]string `json:"outputs"`
	}
	jobs := plan.Jobs()
	if json.Unmarshal(body, &needs) != nil || len(needs) != len(jobs) {
		return errors.New("aggregate requires exactly the complete premerge job set")
	}
	expected, err := planOutputs(plan)
	if err != nil {
		return err
	}
	for key, value := range expected {
		if needs["binding"].Outputs[key] != value {
			return errors.New("binding outputs do not match the recomputed committed impact plan")
		}
	}
	for name, selected := range jobs {
		want := "skipped"
		if selected {
			want = "success"
		}
		if needs[name].Result != want {
			return fmt.Errorf("premerge job %s must report %s for the selected plan", name, want)
		}
	}
	return nil
}
