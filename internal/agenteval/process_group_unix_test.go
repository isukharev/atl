//go:build !windows

package agenteval

import (
	"errors"
	"fmt"
	"syscall"
	"testing"
)

func TestProcessGroupExhausted(t *testing.T) {
	const target int32 = 41
	tests := []struct {
		name    string
		target  int32
		members []processGroupMember
		want    bool
	}{
		{name: "empty", target: target, want: true},
		{
			name:   "zombie only",
			target: target,
			members: []processGroupMember{
				{pid: target, pgrp: target, zombie: true},
				{pid: target + 1, pgrp: target, zombie: true},
			},
			want: true,
		},
		{
			name:   "live member",
			target: target,
			members: []processGroupMember{
				{pid: target, pgrp: target, zombie: true},
				{pid: target + 1, pgrp: target},
			},
		},
		{
			name:    "mismatched group",
			target:  target,
			members: []processGroupMember{{pid: target, pgrp: target + 1, zombie: true}},
		},
		{
			name:    "invalid pid",
			target:  target,
			members: []processGroupMember{{pgrp: target, zombie: true}},
		},
		{name: "invalid target", target: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := processGroupExhausted(test.target, test.members); got != test.want {
				t.Fatalf("processGroupExhausted()=%t want=%t", got, test.want)
			}
		})
	}
}

func TestNormalizeExhaustedProcessGroupError(t *testing.T) {
	const target = 41
	permissionErr := fmt.Errorf("group signal: %w", syscall.EPERM)

	t.Run("empty group", func(t *testing.T) {
		got := normalizeExhaustedProcessGroupError(target, permissionErr, func(pgid int32) ([]processGroupMember, error) {
			if pgid != target {
				t.Fatalf("inspected group=%d want=%d", pgid, target)
			}
			return nil, nil
		})
		if got != nil {
			t.Fatalf("normalized error=%v", got)
		}
	})

	for _, test := range []struct {
		name        string
		pgid        int
		signalErr   error
		members     []processGroupMember
		inspectErr  error
		wantInspect bool
	}{
		{name: "non permission error", pgid: target, signalErr: syscall.EINVAL},
		{name: "invalid target", signalErr: permissionErr},
		{name: "inspection failure", pgid: target, signalErr: permissionErr, inspectErr: errors.New("inspection failed"), wantInspect: true},
		{name: "live member", pgid: target, signalErr: permissionErr, members: []processGroupMember{{pid: target, pgrp: target}}, wantInspect: true},
		{name: "mismatched member", pgid: target, signalErr: permissionErr, members: []processGroupMember{{pid: target, pgrp: target + 1, zombie: true}}, wantInspect: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			inspected := false
			got := normalizeExhaustedProcessGroupError(test.pgid, test.signalErr, func(pgid int32) ([]processGroupMember, error) {
				inspected = true
				return test.members, test.inspectErr
			})
			if got != test.signalErr {
				t.Fatalf("error=%v want original=%v", got, test.signalErr)
			}
			if inspected != test.wantInspect {
				t.Fatalf("inspected=%t want=%t", inspected, test.wantInspect)
			}
		})
	}
}
