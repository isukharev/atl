package domain

import (
	"errors"
	"testing"
)

func TestReadDispatchExpiredIsDistinctCheckFailure(t *testing.T) {
	if ErrReadDispatchExpired == ErrCheckFailed || !errors.Is(ErrReadDispatchExpired, ErrCheckFailed) {
		t.Fatalf("ErrReadDispatchExpired=%v must be a distinct ErrCheckFailed class", ErrReadDispatchExpired)
	}
	if errors.Is(ErrReadDispatchExpired, ErrAuth) {
		t.Fatal("dispatch expiry must not classify as authentication failure")
	}
}
