//go:build !linux

package brokerjournal

import (
	"errors"
	"testing"
)

func TestUnqualifiedPlatformRemainsUnavailable(t *testing.T) {
	identity := Identity{BrokerSHA256: digest([]byte("broker")), BackendSHA256: digest([]byte("backend"))}
	if _, err := Create("unqualified", identity, Limits{}); !errors.Is(err, errUnavailable) {
		t.Fatalf("create=%v", err)
	}
	if _, err := Open("unqualified", identity, Limits{}); !errors.Is(err, errUnavailable) {
		t.Fatalf("open=%v", err)
	}
}
