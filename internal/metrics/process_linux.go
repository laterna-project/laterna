package metrics

import (
	"os"
	"strconv"
	"strings"
)

// residentMemory reads the resident set size from /proc/self/statm (in pages).
func residentMemory() (uint64, bool) {
	data, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(data))
	if len(fields) < 2 {
		return 0, false
	}
	pages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0, false
	}
	return pages * uint64(os.Getpagesize()), true //nolint:gosec // the page size is positive
}
