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

func readyEvent() event {
	return event{Action: "ready_for_review", Number: 42, PullRequest: currentPR()}
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

func TestReadyBindingRejectsWrongEventAndRef(t *testing.T) {
	tests := []struct {
		name, event, repository, sha, ref string
		edit                              func(*event)
		ok                                bool
	}{
		{name: "exact ready event", ok: true},
		{name: "wrong event", event: "push"},
		{name: "manual dispatch", event: "workflow_dispatch"},
		{name: "opened", edit: func(e *event) { e.Action = "opened" }},
		{name: "reopened", edit: func(e *event) { e.Action = "reopened" }},
		{name: "synchronize", edit: func(e *event) { e.Action = "synchronize" }},
		{name: "labeled", edit: func(e *event) { e.Action = "labeled" }},
		{name: "exact full label", edit: func(e *event) {
			e.PullRequest.Labels = append(e.PullRequest.Labels, struct {
				Name string `json:"name"`
			}{Name: "ci-full"})
		}, ok: true},
		{name: "similar full label", edit: func(e *event) {
			e.PullRequest.Labels = append(e.PullRequest.Labels, struct {
				Name string `json:"name"`
			}{Name: "CI-FULL"})
		}, ok: true},
		{name: "tag", ref: "refs/tags/topic"},
		{name: "branch ref", ref: "refs/heads/topic"},
		{name: "wrong merge ref", ref: "refs/pull/43/merge"},
		{name: "repository injection", repository: "owner/repo/../../other"},
		{name: "missing PR", edit: func(e *event) { e.Number = 0; e.PullRequest.Number = 0 }},
		{name: "mismatched PR", edit: func(e *event) { e.PullRequest.Number++ }},
		{name: "missing base", edit: func(e *event) { e.PullRequest.Base.SHA = "" }},
		{name: "symbolic base", edit: func(e *event) { e.PullRequest.Base.SHA = "main" }},
		{name: "missing head", edit: func(e *event) { e.PullRequest.Head.SHA = "" }},
		{name: "closed", edit: func(e *event) { e.PullRequest.State = "closed" }},
		{name: "draft", edit: func(e *event) { e.PullRequest.Draft = true }},
		{name: "fork head", edit: func(e *event) { e.PullRequest.Head.Repo.FullName = "fork/repo" }},
		{name: "wrong base repository", edit: func(e *event) { e.PullRequest.Base.Repo.FullName = "other/repo" }},
		{name: "missing head branch", edit: func(e *event) { e.PullRequest.Head.Ref = "" }},
		{name: "wrong base branch", edit: func(e *event) { e.PullRequest.Base.Ref = "other" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := readyEvent()
			if tt.edit != nil {
				tt.edit(&e)
			}
			if tt.event == "" {
				tt.event = "pull_request"
			}
			if tt.repository == "" {
				tt.repository = "owner/repo"
			}
			if tt.sha == "" {
				tt.sha = testMerge
			}
			if tt.ref == "" {
				tt.ref = "refs/pull/42/merge"
			}
			binding, err := eventBinding(tt.event, tt.repository, tt.sha, tt.ref, encode(t, e))
			if (err == nil) != tt.ok {
				t.Fatalf("binding error = %v, want success %v", err, tt.ok)
			}
			if err == nil && binding.Full != (tt.name == "exact full label") {
				t.Fatalf("full = %v", binding.Full)
			}
		})
	}
}

func TestCurrentBindingRejectsStaleOrRetargetedPullRequest(t *testing.T) {
	b, err := eventBinding("pull_request", "owner/repo", testMerge, "refs/pull/42/merge", encode(t, readyEvent()))
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
		"draft":                        func(p *pullRequest) { p.Draft = true },
		"wrong PR":                     func(p *pullRequest) { p.Number++ },
		"different base branch":        func(p *pullRequest) { p.Base.Ref = "other" },
		"different repository":         func(p *pullRequest) { p.Base.Repo.FullName = "other/repo" },
		"same commit different branch": func(p *pullRequest) { p.Head.Ref = "other" },
		"fork head":                    func(p *pullRequest) { p.Head.Repo.FullName = "other/repo" },
	} {
		t.Run(name, func(t *testing.T) {
			p := currentPR()
			edit(&p)
			if err := validateCurrent(b, p); err == nil {
				t.Fatal("accepted stale or mismatched PR")
			}
		})
	}

}

func TestCheckoutProvesExactMergeParentsAncestryAndTree(t *testing.T) {
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
	tree := git("rev-parse", head+"^{tree}")
	merge := git("commit-tree", tree, "-p", base, "-p", head, "-m", "merge")
	git("checkout", "--detach", merge)
	b := binding{Base: base, Head: head, Checkout: merge}
	if err := verifyCheckout(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	git("checkout", "--detach", head)
	b.Checkout = head
	if err := verifyCheckout(context.Background(), b); err == nil {
		t.Fatal("accepted head checkout instead of synthetic merge")
	}
	git("checkout", "--detach", merge)
	b.Checkout = base
	if err := verifyCheckout(context.Background(), b); err == nil {
		t.Fatal("accepted wrong checkout")
	}
	b.Checkout = merge
	b.Base = testMerge
	if err := verifyCheckout(context.Background(), b); err == nil {
		t.Fatal("accepted unavailable base")
	}
	git("checkout", "--detach", base)
	git("commit", "--allow-empty", "-m", "base advanced")
	advanced := git("rev-parse", "HEAD")
	outside := git("commit-tree", tree, "-p", advanced, "-p", head, "-m", "outside ancestry")
	git("checkout", "--detach", outside)
	b.Base, b.Checkout = advanced, outside
	if err := verifyCheckout(context.Background(), b); err == nil {
		t.Fatal("accepted a base outside head ancestry")
	}
	b.Base = base
	reversed := git("commit-tree", tree, "-p", head, "-p", base, "-m", "reversed")
	git("checkout", "--detach", reversed)
	b.Checkout = reversed
	if err := verifyCheckout(context.Background(), b); err == nil {
		t.Fatal("accepted reversed merge parents")
	}
	wrongTree := git("rev-parse", base+"^{tree}")
	changed := git("commit-tree", wrongTree, "-p", base, "-p", head, "-m", "changed tree")
	git("checkout", "--detach", changed)
	b.Checkout = changed
	if err := verifyCheckout(context.Background(), b); err == nil {
		t.Fatal("accepted merge tree different from head")
	}

}
