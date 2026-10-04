//go:build !linux && !windows

package metrics

func residentMemory() (uint64, bool) { return 0, false }
