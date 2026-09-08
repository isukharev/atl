package brokercontract

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func TestContentFreeErrorDropsJoinedPrivateCauses(t *testing.T) {
	private := errors.New("PRIVATE-CANARY.example.invalid/policy")
	ok, safe := ContentFreeError(errors.Join(reject(domain.BrokerReasonDenied), private))
	reason, reasonOK := Reason(safe)
	if !ok || !reasonOK || reason != domain.BrokerReasonDenied || !errors.Is(safe, domain.ErrForbidden) {
		t.Fatalf("safe=%v reason=%q", safe, reason)
	}
	for _, formatted := range []string{safe.Error(), fmt.Sprintf("%+v", safe), fmt.Sprintf("%#v", safe), fmt.Sprint(errors.Unwrap(safe))} {
		if strings.Contains(formatted, "PRIVATE-CANARY") {
			t.Fatalf("private cause survived: %q", formatted)
		}
	}
}
