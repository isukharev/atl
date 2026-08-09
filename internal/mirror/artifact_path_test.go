package mirror

import (
	"errors"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func mustPublicArtifactPath(t testing.TB, value string) ArtifactPath {
	t.Helper()
	path, err := NewPublicArtifactPath(value)
	if err != nil {
		t.Fatalf("NewPublicArtifactPath(%q): %v", value, err)
	}
	return path
}

func mustPrivateArtifactPath(t testing.TB, value string) ArtifactPath {
	t.Helper()
	path, err := NewPrivateArtifactPath(value)
	if err != nil {
		t.Fatalf("NewPrivateArtifactPath(%q): %v", value, err)
	}
	return path
}

func artifactPathStringForTest(t testing.TB, path ArtifactPath) string {
	t.Helper()
	value, err := artifactPathDurableString(path)
	if err != nil {
		t.Fatalf("artifactPathDurableString: %v", err)
	}
	return value
}

func TestArtifactPathConstructorsEnforceClosedClasses(t *testing.T) {
	exactASCII := "D/" + strings.Repeat("a", maxArtifactPathBytes-2)
	exactMultibyte := "D/" + strings.Repeat("é", (maxArtifactPathBytes-2)/2)
	publicTests := []struct {
		name  string
		value string
		ok    bool
	}{
		{name: "file", value: "DOC/page/page.csf", ok: true},
		{name: "unicode", value: "文档/页.csf", ok: true},
		{name: "exact ASCII bound", value: exactASCII, ok: true},
		{name: "exact multibyte bound", value: exactMultibyte, ok: true},
		{name: "empty"},
		{name: "root", value: "."},
		{name: "parent", value: ".."},
		{name: "traversal", value: "../page.csf"},
		{name: "embedded traversal", value: "DOC/../page.csf"},
		{name: "absolute", value: "/DOC/page.csf"},
		{name: "backslash", value: `DOC\page.csf`},
		{name: "colon", value: "DOC/page:copy.csf"},
		{name: "drive", value: "C:/DOC/page.csf"},
		{name: "NUL", value: "DOC/page\x00.csf"},
		{name: "double separator", value: "DOC//page.csf"},
		{name: "trailing separator", value: "DOC/page/"},
		{name: "overlong ASCII", value: exactASCII + "x"},
		{name: "overlong multibyte", value: exactMultibyte + "é"},
		{name: "exact reserved root", value: ".atl"},
		{name: "reserved child", value: ".atl/cache/file"},
		{name: "reserved base", value: ".atl/base/10.csf"},
		{name: "uppercase reserved root", value: ".ATL"},
		{name: "uppercase reserved child", value: ".ATL/base/10.csf"},
		{name: "mixed-case reserved child", value: ".AtL/base/10.csf"},
	}
	for _, tc := range publicTests {
		t.Run("public/"+tc.name, func(t *testing.T) {
			got, err := NewPublicArtifactPath(tc.value)
			if tc.ok {
				if err != nil || got.class != artifactPathPublic || got.value != tc.value {
					t.Fatalf("path=%+v err=%v", got, err)
				}
				return
			}
			if !errors.Is(err, domain.ErrCheckFailed) || got != (ArtifactPath{}) {
				t.Fatalf("path=%+v err=%v, want zero check failure", got, err)
			}
		})
	}

	exactPrivate := ".atl/base/" + strings.Repeat("a", maxArtifactPathBytes-len(".atl/base/"))
	privateTests := []struct {
		name  string
		value string
		ok    bool
	}{
		{name: "file", value: ".atl/base/10.csf", ok: true},
		{name: "nested", value: ".atl/base/service/10.wiki", ok: true},
		{name: "exact bound", value: exactPrivate, ok: true},
		{name: "empty"},
		{name: "public", value: "DOC/page.csf"},
		{name: "reserved root", value: ".atl"},
		{name: "base root", value: ".atl/base"},
		{name: "base trailing separator", value: ".atl/base/"},
		{name: "base sibling", value: ".atl/basement/10.csf"},
		{name: "other private subtree", value: ".atl/cache/10.csf"},
		{name: "uppercase alias", value: ".ATL/base/10.csf"},
		{name: "mixed-case alias", value: ".AtL/base/10.csf"},
		{name: "traversal", value: ".atl/base/../state.json"},
		{name: "overlong", value: exactPrivate + "x"},
	}
	for _, tc := range privateTests {
		t.Run("private/"+tc.name, func(t *testing.T) {
			got, err := NewPrivateArtifactPath(tc.value)
			if tc.ok {
				if err != nil || got.class != artifactPathPrivateBase || got.value != tc.value {
					t.Fatalf("path=%+v err=%v", got, err)
				}
				return
			}
			if !errors.Is(err, domain.ErrCheckFailed) || got != (ArtifactPath{}) {
				t.Fatalf("path=%+v err=%v, want zero check failure", got, err)
			}
		})
	}
}

func TestArtifactPathDurableReparseInfersAndPreservesClass(t *testing.T) {
	for _, tc := range []struct {
		value string
		class artifactPathClass
	}{
		{value: "DOC/page.csf", class: artifactPathPublic},
		{value: ".atl/base/10.csf", class: artifactPathPrivateBase},
	} {
		path, err := artifactPathFromDurable(tc.value)
		if err != nil || path.class != tc.class {
			t.Fatalf("reparse %q: path=%+v err=%v", tc.value, path, err)
		}
		durable, err := artifactPathDurableString(path)
		if err != nil || durable != tc.value {
			t.Fatalf("bridge %q: durable=%q err=%v", tc.value, durable, err)
		}
	}
	for _, value := range []string{"", ".atl", ".ATL/base/10.csf", ".atl/cache/10.csf", "../escape"} {
		if path, err := artifactPathFromDurable(value); !errors.Is(err, domain.ErrCheckFailed) || path != (ArtifactPath{}) {
			t.Fatalf("reparse %q: path=%+v err=%v", value, path, err)
		}
	}
}

func TestArtifactPathZeroValueFailsEveryPublicationConsumer(t *testing.T) {
	zero := ArtifactPath{}
	if err := validateArtifactPath(zero); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("validate zero error=%v", err)
	}
	if _, err := artifactPathTarget(t.TempDir(), zero); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("target zero error=%v", err)
	}
	if _, err := artifactPathDurableString(zero); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("bridge zero error=%v", err)
	}
	m, checkpoint, entry, artifacts := completePullPublicationFixture(t)
	artifacts[0].Path = zero
	if err := m.PrepareCompletePullPublication(checkpoint, 0, entry, true, artifacts, nil); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("complete-pull zero error=%v", err)
	}
}

func FuzzArtifactPathConstructors(f *testing.F) {
	for _, value := range []string{
		"DOC/page.csf", ".atl/base/10.csf", "", ".", "..", "../escape",
		"DOC/../page.csf", "/absolute", `DOC\page`, "C:/page", "page:name",
		"page\x00name", ".atl", ".ATL/base/10.csf", ".atl/cache/file",
	} {
		f.Add(value, false)
		f.Add(value, true)
	}
	f.Fuzz(func(t *testing.T, value string, private bool) {
		var path ArtifactPath
		var err error
		if private {
			path, err = NewPrivateArtifactPath(value)
		} else {
			path, err = NewPublicArtifactPath(value)
		}
		if err != nil {
			if path != (ArtifactPath{}) {
				t.Fatalf("rejected value returned non-zero path: %+v", path)
			}
			return
		}
		if len(value) > maxArtifactPathBytes || validateArtifactPath(path) != nil {
			t.Fatalf("accepted invalid value %q as %+v", value, path)
		}
		if private && !strings.HasPrefix(value, ".atl/base/") {
			t.Fatalf("private constructor accepted %q", value)
		}
		first := value
		if slash := strings.IndexByte(first, '/'); slash >= 0 {
			first = first[:slash]
		}
		if !private && asciiEqualFold(first, ".atl") {
			t.Fatalf("public constructor accepted reserved alias %q", value)
		}
		durable, err := artifactPathDurableString(path)
		if err != nil || durable != value {
			t.Fatalf("bridge=%q err=%v for %q", durable, err, value)
		}
		reparsed, err := artifactPathFromDurable(durable)
		if err != nil || reparsed != path {
			t.Fatalf("reparse=%+v err=%v, want %+v", reparsed, err, path)
		}
	})
}
