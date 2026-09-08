package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

const testHead = "1111111111111111111111111111111111111111"
const testBase = "2222222222222222222222222222222222222222"
const testMerge = "3333333333333333333333333333333333333333"

func manualEvent() event {
	return event{Inputs: map[string]string{"pr": "42", "head_sha": testHead, "base_sha": testBase}}
}

func encode(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func currentPR() pullRequest {
	p := pullRequest{Number: 42, State: "open", Head: revision{SHA: testHead, Ref: "topic"}, Base: revision{SHA: testBase, Ref: "main"}}
	p.Head.Repo.FullName, p.Base.Repo.FullName = "owner/repo", "owner/repo"
	return p
}

func TestManualBindingRejectsWrongEventAndRef(t *testing.T) {
	tests := []struct {
		name, event, repository, sha, ref string
		edit                              func(*event)
		ok                                bool
	}{
		{name: "exact branch", ok: true},
		{name: "wrong event", event: "push"},
		{name: "wrong checkout", sha: testMerge},
		{name: "tag", ref: "refs/tags/topic"},
		{name: "merge ref", ref: "refs/pull/42/merge"},
		{name: "repository injection", repository: "owner/repo/../../other"},
		{name: "missing PR", edit: func(e *event) { delete(e.Inputs, "pr") }},
		{name: "negative PR", edit: func(e *event) { e.Inputs["pr"] = "-42" }},
		{name: "noncanonical PR", edit: func(e *event) { e.Inputs["pr"] = "042" }},
		{name: "missing base", edit: func(e *event) { delete(e.Inputs, "base_sha") }},
		{name: "symbolic base", edit: func(e *event) { e.Inputs["base_sha"] = "main" }},
		{name: "missing head", edit: func(e *event) { delete(e.Inputs, "head_sha") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := manualEvent()
			if tt.edit != nil {
				tt.edit(&e)
			}
			if tt.event == "" {
				tt.event = "workflow_dispatch"
			}
			if tt.repository == "" {
				tt.repository = "owner/repo"
			}
			if tt.sha == "" {
				tt.sha = testHead
			}
			if tt.ref == "" {
				tt.ref = "refs/heads/topic"
			}
			_, err := eventBinding(tt.event, tt.repository, tt.sha, tt.ref, encode(t, e))
			if (err == nil) != tt.ok {
				t.Fatalf("binding error = %v, want success %v", err, tt.ok)
			}
		})
	}
}

func TestPullRequestBindingUsesMergeCheckout(t *testing.T) {
	e := event{Number: 42, PullRequest: currentPR()}
	b, err := eventBinding("pull_request", "owner/repo", testMerge, "refs/pull/42/merge", encode(t, e))
	if err != nil || b.Head != testHead || b.Checkout != testMerge || b.Manual {
		t.Fatalf("binding = %+v, error = %v", b, err)
	}
	for _, ref := range []string{"refs/heads/topic", "refs/pull/43/merge"} {
		if _, err := eventBinding("pull_request", "owner/repo", testMerge, ref, encode(t, e)); err == nil {
			t.Fatalf("accepted wrong PR ref %q", ref)
		}
	}
	e.PullRequest.Base.Repo.FullName = "other/repo"
	if _, err := eventBinding("pull_request", "owner/repo", testMerge, "refs/pull/42/merge", encode(t, e)); err == nil {
		t.Fatal("accepted wrong base repository")
	}
}

func TestCurrentBindingRejectsStaleOrRetargetedPullRequest(t *testing.T) {
	b, err := eventBinding("workflow_dispatch", "owner/repo", testHead, "refs/heads/topic", encode(t, manualEvent()))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateCurrent(b, currentPR()); err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func(*pullRequest){
		"head advanced":                func(p *pullRequest) { p.Head.SHA = testMerge },
		"base advanced":                func(p *pullRequest) { p.Base.SHA = testMerge },
		"closed":                       func(p *pullRequest) { p.State = "closed" },
		"wrong PR":                     func(p *pullRequest) { p.Number++ },
		"different base branch":        func(p *pullRequest) { p.Base.Ref = "other" },
		"different repository":         func(p *pullRequest) { p.Base.Repo.FullName = "other/repo" },
		"same commit different branch": func(p *pullRequest) { p.Head.Ref = "other" },
		"fork dispatch":                func(p *pullRequest) { p.Head.Repo.FullName = "other/repo" },
	} {
		t.Run(name, func(t *testing.T) {
			p := currentPR()
			edit(&p)
			if err := validateCurrent(b, p); err == nil {
				t.Fatal("accepted stale or mismatched PR")
			}
		})
	}
	// Automatic PR runs still support fork heads through GitHub's merge ref.
	b.Manual = false
	p := currentPR()
	p.Head.Repo.FullName = "fork/repo"
	if err := validateCurrent(b, p); err != nil {
		t.Fatal(err)
	}
}

func TestAggregateRequiresEveryJobToSucceed(t *testing.T) {
	names := []string{"binding", "test", "corpus-devcontainer", "agent-eval", "agent-eval-extension-windows", "lint", "govulncheck", "codeql"}
	all := func() map[string]map[string]string {
		jobs := map[string]map[string]string{}
		for _, name := range names {
			jobs[name] = map[string]string{"result": "success"}
		}
		return jobs
	}
	if err := validateNeeds(encode(t, all())); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		for _, result := range []string{"failure", "cancelled", "skipped", "", "neutral"} { //nolint:misspell // GitHub's job-result spelling.
			t.Run(name+"/"+result, func(t *testing.T) {
				jobs := all()
				jobs[name]["result"] = result
				if err := validateNeeds(encode(t, jobs)); err == nil {
					t.Fatal("aggregate accepted unsuccessful dependency")
				}
			})
		}
		jobs := all()
		delete(jobs, name)
		if err := validateNeeds(encode(t, jobs)); err == nil {
			t.Fatalf("accepted missing dependency %s", name)
		}
		jobs["unexpected"] = map[string]string{"result": "success"}
		if err := validateNeeds(encode(t, jobs)); err == nil {
			t.Fatalf("accepted substituted dependency %s", name)
		}
	}
	if err := validateNeeds([]byte(`null`)); err == nil {
		t.Fatal("accepted absent job results")
	}
}

func TestCheckoutProvesAncestryAndExactMergeParents(t *testing.T) {
	t.Chdir(t.TempDir())
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=Fixture", "-c", "user.email=ivan7654@gmail.com", "-c", "commit.gpgSign=false", "-c", "core.hooksPath=/dev/null"}, args...)...)
		body, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, body)
		}
		return strings.TrimSpace(string(body))
	}
	git("init", "--initial-branch=main")
	git("commit", "--allow-empty", "-m", "base")
	base := git("rev-parse", "HEAD")
	if err := os.WriteFile("fixture.txt", []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "fixture.txt")
	git("commit", "-m", "head")
	head := git("rev-parse", "HEAD")
	b := binding{Manual: true, Base: base, Head: head, Checkout: head}
	if err := verifyCheckout(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	b.Checkout = base
	if err := verifyCheckout(context.Background(), b); err == nil {
		t.Fatal("accepted wrong checkout")
	}
	b.Checkout, b.Base = head, testMerge
	if err := verifyCheckout(context.Background(), b); err == nil {
		t.Fatal("accepted unavailable base")
	}
	git("checkout", "--detach", base)
	git("commit", "--allow-empty", "-m", "base advanced")
	advanced := git("rev-parse", "HEAD")
	git("checkout", "--detach", head)
	b.Base = advanced
	if err := verifyCheckout(context.Background(), b); err == nil {
		t.Fatal("accepted a base outside head ancestry")
	}
	tree := git("rev-parse", "HEAD^{tree}")
	merge := git("commit-tree", tree, "-p", base, "-p", head, "-m", "merge")
	git("checkout", "--detach", merge)
	b = binding{Base: base, Head: head, Checkout: merge}
	if err := verifyCheckout(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	b.Base = advanced
	if err := verifyCheckout(context.Background(), b); err == nil {
		t.Fatal("accepted wrong merge parents")
	}
}
