//go:build !windows

package proc

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

// alive reports a process that exists and is not a zombie (dead, waiting to be reaped).
func alive(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return true
	}
	fields := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:]))
	return len(fields) == 0 || fields[0] != "Z"
}
