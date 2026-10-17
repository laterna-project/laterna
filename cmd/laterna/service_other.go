//go:build !windows

package main

import (
	"context"
	"errors"
	"io"
)

// serviceCommand manages the Windows service, which only exists on Windows: elsewhere the
// packages install a systemd unit.
func serviceCommand(context.Context, []string, io.Writer, io.Writer) error {
	return errors.New("laterna service manages the Windows service: on Linux, the deb and rpm packages install a systemd unit (docs/install.md)")
}
