package jira

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// StreamAttachment streams bytes straight from a content URL through the authed
// httpx client — with no ListAttachments round-trip — and injects the bearer PAT
// because the URL is same-host as the backend.
func TestStreamAttachmentStreamsWithBearer(t *testing.T) {
	const payload = "PNGDATA\x00\x01\x02"
	var gotAuth string
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path != "/secure/attachment/42/diagram.png" {
			t.Errorf("unexpected path %q (StreamAttachment must not re-list attachments)", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "image/png")
		_, _ = io.WriteString(w, payload)
	}))
	defer srv.Close()

	j := newTestJira(srv)
	rc, err := j.StreamAttachment(context.Background(), "/secure/attachment/42/diagram.png")
	if err != nil {
		t.Fatalf("StreamAttachment: %v", err)
	}
	data, rerr := io.ReadAll(rc)
	rc.Close()
	if rerr != nil {
		t.Fatalf("read stream: %v", rerr)
	}
	if string(data) != payload {
		t.Errorf("data = %q, want %q", data, payload)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("Authorization = %q, want %q (bearer injected for same-host content URL)", gotAuth, "Bearer tok")
	}
	if hits != 1 {
		t.Errorf("server hits = %d, want exactly 1 (no extra listing GET)", hits)
	}
}
