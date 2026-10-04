// Package buildinfo exposes Laterna's version and the commit the binary was built from.
package buildinfo

import "runtime/debug"

// Version is set at build time:
//
//	-ldflags "-X github.com/laterna-project/laterna/internal/buildinfo.Version=1.2.3"
var Version = "dev"

// commit is set when the git repository is not visible at build time (Docker image):
//
//	-ldflags "-X github.com/laterna-project/laterna/internal/buildinfo.commit=abc123"
var commit = ""

// Commit returns the git commit of the binary: the injected one, otherwise the one from Go's build
// info, otherwise "" (go run, build outside a repository).
func Commit() string {
	if commit != "" {
		return commit
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	revision, dirty := "", false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if len(revision) > 12 {
		revision = revision[:12]
	}
	if revision != "" && dirty {
		revision += "+dirty"
	}
	return revision
}
