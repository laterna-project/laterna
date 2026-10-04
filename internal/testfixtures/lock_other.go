//go:build !windows

package testfixtures

import (
	"os"
	"syscall"
)

// lockFile waits for the exclusive lock on the file. Closing the file releases it.
func lockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
}
