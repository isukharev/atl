package scmref

import "testing"

func TestParseGitLabProjectCanonicalizesOffline(t *testing.T) {
	for _, test := range []struct {
		raw  string
		want GitLabProject
	}{
		{"https://SCM.example.test/group/sub/project.git", GitLabProject{Host: "scm.example.test", ProjectPath: "group/sub/project"}},
		{"https://scm.example.test:443/group/project", GitLabProject{Host: "scm.example.test", ProjectPath: "group/project"}},
		{"https://scm.example.test:0443/group/project/", GitLabProject{Host: "scm.example.test", ProjectPath: "group/project"}},
		{"https://scm.example.test:8443/Group/Project", GitLabProject{Host: "scm.example.test:8443", ProjectPath: "Group/Project"}},
	} {
		got, ok := ParseGitLabProject(test.raw)
		if !ok || got != test.want {
			t.Errorf("ParseGitLabProject(%q) = %+v, %t, want %+v, true", test.raw, got, ok, test.want)
		}
	}
}

func TestParseGitLabReferenceUsesModernArtifactBoundary(t *testing.T) {
	for _, raw := range []string{
		"https://scm.example.test/group/project/-/commit/0123456789abcdef",
		"https://scm.example.test/group/project/-/merge_requests/42",
		"https://scm.example.test/group/project/-/blob/main/README.md",
	} {
		got, ok := ParseGitLabReference(raw)
		if !ok || got != (GitLabProject{Host: "scm.example.test", ProjectPath: "group/project"}) {
			t.Errorf("ParseGitLabReference(%q) = %+v, %t", raw, got, ok)
		}
	}
	if _, ok := ParseGitLabProject("https://scm.example.test/group/project/-/commit/0123456789abcdef"); ok {
		t.Fatal("exact project parser accepted an artifact URL")
	}
}

func TestParseGitLabProjectRejectsUnsafeOrAmbiguousReferences(t *testing.T) {
	for _, raw := range []string{
		"http://scm.example.test/group/project",
		"https://user@scm.example.test/group/project",
		"https://scm.example.test/group/project?token=secret",
		"https://scm.example.test/group/project#fragment",
		"https://scm.example.test/group",
		"https://scm.example.test/group/%2Fproject",
		"https://scm.example.test/group/../project",
		"https://scm.example.test/group/project/-/commit/1",
		"https://scm.example.test:/group/project",
		"https://scm.\uFFFD.example.test/group/project",
		"https://scm." + string([]byte{0xff}) + ".example.test/group/project",
	} {
		if _, ok := ParseGitLabProject(raw); ok {
			t.Errorf("ParseGitLabProject(%q) unexpectedly succeeded", raw)
		}
	}
	if _, ok := ParseGitLabReference("https://scm.example.test/group/project/-/"); ok {
		t.Fatal("artifact reference without a suffix unexpectedly succeeded")
	}
}

func TestValidateGitLabProjectRequiresCanonicalCoordinates(t *testing.T) {
	for _, test := range []struct {
		host, path string
		want       bool
	}{
		{"scm.example.test", "group/project", true},
		{"scm.example.test:8443", "Group/Project", true},
		{"SCM.example.test", "group/project", false},
		{"scm.example.test:443", "group/project", false},
		{"scm.example.test", "group/project.git", false},
		{"scm.example.test", "group/%2Fproject", false},
	} {
		_, got := ValidateGitLabProject(test.host, test.path)
		if got != test.want {
			t.Errorf("ValidateGitLabProject(%q, %q) = %t, want %t", test.host, test.path, got, test.want)
		}
	}
}
