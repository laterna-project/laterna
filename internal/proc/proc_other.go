//go:build !windows && !linux

package proc

import "os/exec"

// Bind does nothing on this OS: children are only stopped by a normal shutdown.
func Bind() error { return nil }

func bind(*exec.Cmd) {}
