//go:build !windows

package agenteval

import (
	"errors"
	"syscall"
)

type processGroupMember struct {
	pid    int32
	pgrp   int32
	zombie bool
}

type processGroupInspector func(int32) ([]processGroupMember, error)

func normalizeExhaustedProcessGroupError(pgid int, signalErr error, inspect processGroupInspector) error {
	if !errors.Is(signalErr, syscall.EPERM) {
		return signalErr
	}
	target := int32(pgid)
	if target <= 0 || int(target) != pgid || inspect == nil {
		return signalErr
	}
	members, err := inspect(target)
	if err != nil || !processGroupExhausted(target, members) {
		return signalErr
	}
	return nil
}

func processGroupExhausted(target int32, members []processGroupMember) bool {
	if target <= 0 {
		return false
	}
	for _, member := range members {
		if member.pid <= 0 || member.pgrp != target || !member.zombie {
			return false
		}
	}
	return true
}
