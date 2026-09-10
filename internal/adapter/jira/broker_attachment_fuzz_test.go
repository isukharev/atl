package jira

import "testing"

func FuzzBrokerJiraAttachmentContentURI(f *testing.F) {
	for _, seed := range []string{
		"/secure/attachment/7/a%20b.bin",
		"https://backend.example/jira/secure/attachment/7/a%20b.bin",
		"/secure/attachment/7/a%2520b.bin",
		"/secure/attachment/7/a%2Fb.bin",
		"//foreign.example/secure/attachment/7/a%20b.bin",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, candidate string) {
		accepted := validBrokerJiraAttachmentContentURI("https://backend.example/jira", candidate, "7", "a b.bin")
		if accepted && candidate != "/secure/attachment/7/a%20b.bin" && candidate != "https://backend.example/jira/secure/attachment/7/a%20b.bin" {
			t.Fatalf("unexpected URI accepted: %q", candidate)
		}
	})
}
