package proc

import (
	"fmt"
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

// job is the server's job object, kept open until it dies. Closing it kills all its members.
var job windows.Handle

// Bind puts the server in a job object that kills all its members when closed. Processes the server
// starts join it automatically, and the OS closes the object when the server dies, however that
// happens. Call it once at startup.
func Bind() error {
	if job != 0 {
		return nil
	}
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("proc: job object: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE},
	}
	if _, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil { //nolint:gosec // Win32 struct passed by address, fixed size
		_ = windows.CloseHandle(h)
		return fmt.Errorf("proc: job object: %w", err)
	}
	if err := windows.AssignProcessToJobObject(h, windows.CurrentProcess()); err != nil {
		_ = windows.CloseHandle(h)
		return fmt.Errorf("proc: job object: %w", err)
	}
	job = h
	return nil
}

func bind(*exec.Cmd) {}
