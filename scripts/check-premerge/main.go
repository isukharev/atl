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
	Number      int               `json:"number"`
	PullRequest pullRequest       `json:"pull_request"`
	Inputs      map[string]string `json:"inputs"`
}

type binding struct {
	Repository string
	Number     int
	Head       string
	Base       string
	Checkout   string
	Ref        string
	Manual     bool
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
	if os.Args[1] == "ready" {
		if err := validateNeeds([]byte(os.Getenv("ATL_PREMERGE_NEEDS"))); err != nil {
			return err
		}
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
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := verifyCheckout(ctx, b); err != nil {
		return err
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
	return validateCurrent(b, current)
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
	switch name {
	case "workflow_dispatch":
		b.Manual = true
		var err error
		b.Number, err = strconv.Atoi(e.Inputs["pr"])
		if err != nil || strconv.Itoa(b.Number) != e.Inputs["pr"] {
			return binding{}, errors.New("manual dispatch requires a positive PR number")
		}
		b.Head, b.Base = e.Inputs["head_sha"], e.Inputs["base_sha"]
		if b.Checkout != b.Head || !strings.HasPrefix(ref, "refs/heads/") {
			return binding{}, errors.New("dispatch the PR branch at the expected head revision")
		}
	case "pull_request":
		b.Number, b.Head, b.Base = e.Number, e.PullRequest.Head.SHA, e.PullRequest.Base.SHA
		if e.PullRequest.Number != b.Number || e.PullRequest.Base.Repo.FullName != repository ||
			e.PullRequest.Base.Ref != "main" || ref != fmt.Sprintf("refs/pull/%d/merge", b.Number) {
			return binding{}, errors.New("pull request event does not identify the expected merge ref and base")
		}
	default:
		return binding{}, errors.New("premerge binding requires a pull_request or workflow_dispatch event")
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
	if b.Manual && (current.Head.Repo.FullName != b.Repository || b.Ref != "refs/heads/"+current.Head.Ref) {
		return errors.New("manual dispatch must use this repository's PR head branch")
	}
	return nil
}

func verifyCheckout(ctx context.Context, b binding) error {
	head, err := exec.CommandContext(ctx, "git", "rev-parse", "HEAD").Output()
	if err != nil || strings.TrimSpace(string(head)) != b.Checkout {
		return errors.New("checkout does not match the workflow revision")
	}
	if b.Manual {
		if err := exec.CommandContext(ctx, "git", "merge-base", "--is-ancestor", b.Base, b.Head).Run(); err != nil {
			return errors.New("manual head must contain the exact current base; update the branch and dispatch again")
		}
		return nil
	}
	parents, err := exec.CommandContext(ctx, "git", "rev-list", "--parents", "-n", "1", b.Checkout).Output()
	if err != nil || strings.TrimSpace(string(parents)) != b.Checkout+" "+b.Base+" "+b.Head {
		return errors.New("pull request checkout is not the merge of the expected base and head")
	}
	return nil
}

func validateNeeds(body []byte) error {
	var needs map[string]struct {
		Result string `json:"result"`
	}
	if json.Unmarshal(body, &needs) != nil || len(needs) != 8 {
		return errors.New("aggregate requires exactly the complete premerge job set")
	}
	for _, name := range []string{"binding", "test", "corpus-devcontainer", "agent-eval", "agent-eval-extension-windows", "lint", "govulncheck", "codeql"} {
		if needs[name].Result != "success" {
			return fmt.Errorf("required premerge job %s did not succeed", name)
		}
	}
	return nil
}
