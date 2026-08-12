//go:build darwin

package agenteval

import "golang.org/x/sys/unix"

// darwinProcessStateZombie is SZOMB from Darwin's sys/proc.h. x/sys exposes
// ExternProc.P_stat but not the corresponding process-state constants.
const darwinProcessStateZombie int8 = 5

func normalizeProcessGroupSignalError(pgid int, signalErr error) error {
	return normalizeExhaustedProcessGroupError(pgid, signalErr, inspectDarwinProcessGroup)
}

func inspectDarwinProcessGroup(pgid int32) ([]processGroupMember, error) {
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.pgrp", int(pgid))
	if err != nil {
		return nil, err
	}
	members := make([]processGroupMember, len(processes))
	for index, process := range processes {
		members[index] = processGroupMember{
			pid:    process.Proc.P_pid,
			pgrp:   process.Eproc.Pgid,
			zombie: process.Proc.P_stat == darwinProcessStateZombie,
		}
	}
	return members, nil
}
