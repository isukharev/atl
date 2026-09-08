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
		{name: "automatic PR", event: "pull_request"},
		{name: "full override", edit: func(e *event) { e.Inputs["full"] = "true" }, ok: true},
		{name: "invalid full override", edit: func(e *event) { e.Inputs["full"] = "skip" }},
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

}

func TestCheckoutProvesExactHeadAndBaseAncestry(t *testing.T) {
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
	b := binding{Base: base, Head: head, Checkout: head}
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

}
