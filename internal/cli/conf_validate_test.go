package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cloudCompatCSF trips every v1 Cloud-compat rule while staying well-formed, so
// a run over it must still exit 0.
const cloudCompatCSF = `<ac:structured-macro ac:name="chart"/>` + "\n" +
	`<ac:structured-macro ac:name="span"/>` + "\n" +
	`<ac:structured-macro ac:name="info"><ac:rich-text-body>` +
	`<ac:structured-macro ac:name="code"><ac:plain-text-body><![CDATA[x]]></ac:plain-text-body></ac:structured-macro>` +
	`</ac:rich-text-body></ac:structured-macro>` + "\n" +
	`<table><tbody><tr><td><table><tbody><tr><td>x</td></tr></tbody></table></td></tr></tbody></table>`

// writeCSF drops a fixture body in a temp dir and returns its path.
func writeCSF(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "page.csf")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// normalizeValidateGolden hides the temp path so the golden pins only the
// output shape.
func normalizeValidateGolden(out, path string) []byte {
	return []byte(strings.ReplaceAll(out, path, "<FILE>"))
}

// Without the flag, a page full of Cloud-compat hits validates clean: the
// default output contract is untouched.
func TestConfValidateDefaultOutputUnchanged(t *testing.T) {
	path := writeCSF(t, cloudCompatCSF)
	out, code := runCLI(t, nil, "conf", "validate", path)
	if code != exitOK {
		t.Fatalf("exit=%d out=%q", code, out)
	}
	if strings.Contains(out, "cloud-compat") || strings.Contains(out, "cloud_compat") {
		t.Fatalf("default validate leaked cloud-compat output: %s", out)
	}
	assertGolden(t, "conf_validate_default.json", normalizeValidateGolden(out, path))
}

// With --cloud-compat the advisory findings appear, the rule pack identifies
// itself, and the command still succeeds.
func TestConfValidateCloudCompatGolden(t *testing.T) {
	path := writeCSF(t, cloudCompatCSF)
	out, code := runCLI(t, nil, "conf", "validate", path, "--cloud-compat")
	if code != exitOK {
		t.Fatalf("cloud-compat findings must not change the exit status: exit=%d out=%q", code, out)
	}
	if !strings.Contains(out, `"ok": true`) {
		t.Fatalf("cloud-compat findings must not clear ok: %s", out)
	}
	assertGolden(t, "conf_validate_cloud_compat.json", normalizeValidateGolden(out, path))
}

// A body with no Cloud-compat hits produces no findings even with the flag on.
func TestConfValidateCloudCompatNoFindings(t *testing.T) {
	path := writeCSF(t, `<p>plain</p><ac:structured-macro ac:name="toc"/>`)
	out, code := runCLI(t, nil, "conf", "validate", path, "--cloud-compat")
	if code != exitOK {
		t.Fatalf("exit=%d out=%q", code, out)
	}
	if strings.Contains(out, "cloud-compat/") {
		t.Fatalf("unexpected cloud-compat finding: %s", out)
	}
	if !strings.Contains(out, `"rule_pack": "v1"`) {
		t.Fatalf("rule pack identity missing: %s", out)
	}
}

// The flag must not rescue a malformed body: well-formedness still fails the
// command with the same exit code as the default path.
func TestConfValidateCloudCompatKeepsWellFormednessGate(t *testing.T) {
	path := writeCSF(t, `<ac:structured-macro ac:name="chart"><p>oops`)
	plainOut, plainCode := runCLI(t, nil, "conf", "validate", path)
	cloudOut, cloudCode := runCLI(t, nil, "conf", "validate", path, "--cloud-compat")
	if plainCode == exitOK {
		t.Fatalf("malformed body should fail: %s", plainOut)
	}
	if cloudCode != plainCode {
		t.Fatalf("exit code changed under --cloud-compat: %d vs %d", cloudCode, plainCode)
	}
	if strings.Contains(cloudOut, "cloud-compat/") {
		t.Fatalf("no rules should run without a DOM: %s", cloudOut)
	}
}
