package proc

import (
	"os/exec"
	"syscall"
)

// Bind does nothing on Linux: each command is tied to the server when it is created.
func Bind() error { return nil }

// bind asks the kernel to kill the command when the server dies.
func bind(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
