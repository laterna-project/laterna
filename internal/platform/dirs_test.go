package platform

import (
	"errors"
	"path/filepath"
	"testing"
)

func env(values map[string]string) func(string) string {
	return func(k string) string { return values[k] }
}

func home(h string) func() (string, error) {
	return func() (string, error) { return h, nil }
}

func TestDefaultDirs(t *testing.T) {
	tests := []struct {
		name string
		goos string
		env  map[string]string
		want Dirs
	}{
		{
			name: "windows: LOCALAPPDATA, not the roaming profile",
			goos: "windows",
			env:  map[string]string{"LOCALAPPDATA": filepath.Join("C:", "Users", "alice", "AppData", "Local")},
			want: Dirs{
				Data:     filepath.Join("C:", "Users", "alice", "AppData", "Local", "Laterna", "data"),
				Cache:    filepath.Join("C:", "Users", "alice", "AppData", "Local", "Laterna", "cache"),
				Metadata: filepath.Join("C:", "Users", "alice", "AppData", "Local", "Laterna", "metadata"),
			},
		},
		{
			name: "linux: XDG set",
			goos: "linux",
			env:  map[string]string{"XDG_DATA_HOME": "/xdg/data", "XDG_CACHE_HOME": "/xdg/cache"},
			want: Dirs{
				Data:     filepath.Join("/xdg/data", "laterna"),
				Cache:    filepath.Join("/xdg/cache", "laterna"),
				Metadata: filepath.Join("/xdg/data", "laterna", "metadata"),
			},
		},
		{
			name: "linux: falls back to the home directory",
			goos: "linux",
			env:  map[string]string{},
			want: Dirs{
				Data:     filepath.Join("/home/k", ".local", "share", "laterna"),
				Cache:    filepath.Join("/home/k", ".cache", "laterna"),
				Metadata: filepath.Join("/home/k", ".local", "share", "laterna", "metadata"),
			},
		},
		{
			name: "macOS",
			goos: "darwin",
			env:  map[string]string{},
			want: Dirs{
				Data:     filepath.Join("/home/k", "Library", "Application Support", "Laterna"),
				Cache:    filepath.Join("/home/k", "Library", "Caches", "Laterna"),
				Metadata: filepath.Join("/home/k", "Library", "Application Support", "Laterna", "metadata"),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := defaultDirs(tt.goos, env(tt.env), home("/home/k"))
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestDefaultDirsWindowsWithoutLocalAppData(t *testing.T) {
	if _, err := defaultDirs("windows", env(nil), home("")); err == nil {
		t.Fatal("want an error without LOCALAPPDATA")
	}
}

func TestDefaultDirsLinuxWithoutHome(t *testing.T) {
	noHome := func() (string, error) { return "", errors.New("no HOME") }
	if _, err := defaultDirs("linux", env(nil), noHome); err == nil {
		t.Fatal("want an error without a home directory or XDG")
	}
}

func TestEnsureCreatesDirs(t *testing.T) {
	root := t.TempDir()
	d := Dirs{
		Data:     filepath.Join(root, "café"),
		Cache:    filepath.Join(root, "cache"),
		Metadata: filepath.Join(root, "metadata"),
	}
	if err := d.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := d.Ensure(); err != nil {
		t.Fatalf("Ensure must be idempotent: %v", err)
	}
}

func TestInContainer(t *testing.T) {
	for name, c := range map[string]struct {
		files []string
		want  bool
	}{
		"not in a container": {nil, false},
		"Docker":             {[]string{"/.dockerenv"}, true},
		"Podman":             {[]string{"/run/.containerenv"}, true},
		"another file":       {[]string{"/etc/hostname"}, false},
	} {
		got := inContainer(func(path string) bool {
			for _, f := range c.files {
				if f == path {
					return true
				}
			}
			return false
		})
		if got != c.want {
			t.Errorf("%s: %v", name, got)
		}
	}
}
