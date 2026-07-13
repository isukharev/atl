package cli

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func blogJSON(id, title, space string, version int, body string, includeBody bool) string {
	response := map[string]any{
		"id": id, "type": "blogpost", "title": title, "space": map[string]any{"key": space},
		"version": map[string]any{"number": version},
	}
	if includeBody {
		response["body"] = map[string]any{"storage": map[string]any{"value": body}}
	}
	encoded, _ := json.Marshal(response)
	return string(encoded)
}

func TestConfBlogCreateNativeCSFAndOutputContracts(t *testing.T) {
	server := newConfServer(t)
	server.writes = []cannedResp{{status: http.StatusOK, body: blogJSON("900", "Release notes", "DOC", 1, "<p>body</p>", true)}}
	path := filepath.Join(t.TempDir(), "body.csf")
	if err := os.WriteFile(path, []byte("<p>body</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := runCLI(t, confEnv(server.srv), "conf", "blog", "create", "--space", " DOC ", "--title", " Release notes ", "--from-file", path)
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	assertGolden(t, "conf_blog_create.json", []byte(out))
	writes := server.writeReqs()
	if len(writes) != 1 || writes[0].method != http.MethodPost || writes[0].path != "/rest/api/content" {
		t.Fatalf("writes=%+v", writes)
	}
	query, err := url.ParseQuery(writes[0].query)
	if err != nil || query.Get("expand") != "body.storage,version,space" {
		t.Fatalf("query=%q parsed=%v err=%v", writes[0].query, query, err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(writes[0].body), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["type"] != "blogpost" || payload["title"] != "Release notes" || putStorageValue(t, writes[0].body) != "<p>body</p>" {
		t.Fatalf("payload=%v", payload)
	}
	if _, exists := payload["ancestors"]; exists {
		t.Fatalf("blog payload contains ancestors: %v", payload)
	}
}

func TestConfBlogCreateStrictMarkdownAndIDProjection(t *testing.T) {
	server := newConfServer(t)
	server.writes = []cannedResp{{status: http.StatusOK, body: blogJSON("901", "Update", "DOC", 1, createMDCSF, true)}}
	path := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(path, []byte(createMD), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := runCLI(t, confEnv(server.srv), "-o", "id", "conf", "blog", "create", "--space", "DOC", "--title", "Update", "--from-md", path)
	if code != exitOK || out != "901\n" {
		t.Fatalf("exit=%d out=%q", code, out)
	}
	if got := putStorageValue(t, server.writeReqs()[0].body); got != createMDCSF {
		t.Fatalf("body=%q", got)
	}
}

func TestConfBlogCreateRejectsUnsafeInputBeforeNetwork(t *testing.T) {
	server := newConfServer(t)
	dir := t.TempDir()
	badCSF := filepath.Join(dir, "bad.csf")
	emptyCSF := filepath.Join(dir, "empty.csf")
	blankCSF := filepath.Join(dir, "blank.csf")
	badMD := filepath.Join(dir, "bad.md")
	for path, body := range map[string]string{
		badCSF: "<p>broken", emptyCSF: "", blankCSF: " \n\t", badMD: "![asset](attachment:image.png)",
	} {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tests := [][]string{
		{"conf", "blog", "create", "--title", "T", "--from-file", badCSF},
		{"conf", "blog", "create", "--space", "DOC", "--from-file", badCSF},
		{"conf", "blog", "create", "--space", "DOC", "--title", "T", "--from-file", badCSF},
		{"conf", "blog", "create", "--space", "DOC", "--title", "T", "--from-file", emptyCSF},
		{"conf", "blog", "create", "--space", "DOC", "--title", "T", "--from-file", blankCSF},
		{"conf", "blog", "create", "--space", "DOC", "--title", "T", "--from-md", badMD},
	}
	for _, args := range tests {
		if _, code := runCLI(t, confEnv(server.srv), args...); code != exitUsage && code != exitCheckFailed {
			t.Fatalf("args=%v exit=%d", args, code)
		}
	}
	if requests := server.requests(); len(requests) != 0 {
		t.Fatalf("unsafe input sent %d requests: %+v", len(requests), requests)
	}
}

func TestConfBlogCreatePartialResponseAndReadOnlyPolicy(t *testing.T) {
	server := newConfServer(t)
	server.writes = []cannedResp{{status: http.StatusOK, body: blogJSON("900", "T", "DOC", 1, "", false)}}
	path := filepath.Join(t.TempDir(), "body.csf")
	if err := os.WriteFile(path, []byte("<p>x</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, code := runCLI(t, confEnv(server.srv), "conf", "blog", "create", "--space", "DOC", "--title", "T", "--from-file", path)
	if code != exitCheckFailed || len(server.writeReqs()) != 1 {
		t.Fatalf("partial exit=%d writes=%d", code, len(server.writeReqs()))
	}
	before := len(server.requests())
	if _, code := runCLI(t, confEnv(server.srv), "--read-only", "conf", "blog", "create", "--space", "DOC", "--title", "T", "--from-file", "/definitely/missing"); code != exitCheckFailed || len(server.requests()) != before {
		t.Fatalf("read-only exit=%d requests=%d->%d", code, before, len(server.requests()))
	}
}
