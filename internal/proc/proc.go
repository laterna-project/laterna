// Package proc starts the server's external programs (FFmpeg, ffprobe) in a way that does not let
// them outlive it. A server that stops abruptly (crash, kill, service stopped) takes its children
// with it. Otherwise they would keep reading files and hogging the CPU or the GPU:
//   - Windows: Bind puts the server in a job object that is closed with it. Every process it
//     starts belongs to the job and dies with it;
//   - Linux: each command gets SIGKILL when the server dies (Pdeathsig);
//   - elsewhere (macOS): there is no such thing without outside help. A normal shutdown always
//     stops the children (canceled context).
package proc

import (
	"context"
	"os/exec"
)

// Command prepares a command that will not outlive the server. Canceling ctx stops it.
func Command(ctx context.Context, name string, args ...string) *exec.Cmd {
	//nolint:gosec // executable from the configuration (FFmpeg), arguments as a slice, never a shell
	cmd := exec.CommandContext(ctx, name, args...)
	bind(cmd)
	return cmd
}
