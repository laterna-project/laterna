package testfixtures

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockFile waits for the exclusive lock on the file. Closing the file releases it.
func lockFile(f *os.File) error {
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, new(windows.Overlapped))
}
