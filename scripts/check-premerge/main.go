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
	"strconv"
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
	Head   revision `json:"head"`
	Base   revision `json:"base"`
}

type event struct {
	Inputs map[string]string `json:"inputs"`
}

type binding struct {
	Repository string
	Number     int
	Head       string
	Base       string
	Checkout   string
	Ref        string
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
	query := `{number,state,head:{sha:.head.sha,ref:.head.ref,repo:{full_name:.head.repo.full_name}},base:{sha:.base.sha,ref:.base.ref,repo:{full_name:.base.repo.full_name}}}`
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
	if name != "workflow_dispatch" {
		return binding{}, errors.New("premerge binding requires manual workflow_dispatch")
	}
	var err error
	b.Number, err = strconv.Atoi(e.Inputs["pr"])
	if err != nil || strconv.Itoa(b.Number) != e.Inputs["pr"] {
		return binding{}, errors.New("manual dispatch requires a positive PR number")
	}
	b.Head, b.Base = e.Inputs["head_sha"], e.Inputs["base_sha"]
	if b.Checkout != b.Head || !strings.HasPrefix(ref, "refs/heads/") {
		return binding{}, errors.New("dispatch the PR branch at the expected head revision")
	}
	switch e.Inputs["full"] {
	case "true":
		b.Full = true
	case "", "false":
	default:
		return binding{}, errors.New("full override must be true or false")
	}
	if b.Number <= 0 || !shaPattern.MatchString(b.Head) || !shaPattern.MatchString(b.Base) {
		return binding{}, errors.New("missing or invalid PR/head/base binding")
	}
	return b, nil
}

func validateCurrent(b binding, current pullRequest) error {
	if current.Number != b.Number || current.State != "open" || current.Base.Ref != "main" ||
		current.Base.Repo.FullName != b.Repository || current.Head.SHA != b.Head || current.Base.SHA != b.Base {
		return errors.New("pull request is closed or its head/base changed; update the branch and dispatch again")
	}
	if current.Head.Repo.FullName != b.Repository || b.Ref != "refs/heads/"+current.Head.Ref {
		return errors.New("manual dispatch must use this repository's PR head branch")
	}
	return nil
}

func verifyCheckout(ctx context.Context, b binding) error {
	head, err := exec.CommandContext(ctx, "git", "rev-parse", "HEAD").Output()
	if err != nil || strings.TrimSpace(string(head)) != b.Checkout {
		return errors.New("checkout does not match the workflow revision")
	}
	if err := exec.CommandContext(ctx, "git", "merge-base", "--is-ancestor", b.Base, b.Head).Run(); err != nil {
		return errors.New("manual head must contain the exact current base; update the branch and dispatch again")
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
