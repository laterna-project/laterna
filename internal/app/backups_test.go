package app

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// Rotation: nightly backups beyond the setting go, as do pre-migration or pre-restore ones beyond
// three. Manual backups and foreign files stay.
func TestPruneBackups(t *testing.T) {
	a, _ := newTestApp(t)
	a.backupDir = t.TempDir()
	base := time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC)
	touch := func(name string, age int) {
		t.Helper()
		path := filepath.Join(a.backupDir, name)
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		at := base.Add(-time.Duration(age) * 24 * time.Hour)
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 5 {
		touch("laterna-auto-2026090"+string(rune('1'+i))+"T030000Z.db", 5-i)
		touch("laterna-migration-v2"+string(rune('0'+i))+"-2026090"+string(rune('1'+i))+"T030000Z.db", 5-i)
	}
	touch("laterna-manual-20260801T120000Z.db", 60)
	touch("notes.txt", 90)
	touch("laterna-auto-n-importe-quoi.db", 90)
	if err := a.pruneBackups(2); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(a.backupDir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	want := []string{
		"laterna-auto-20260904T030000Z.db", "laterna-auto-20260905T030000Z.db", "laterna-auto-n-importe-quoi.db",
		"laterna-manual-20260801T120000Z.db",
		"laterna-migration-v22-20260903T030000Z.db", "laterna-migration-v23-20260904T030000Z.db", "laterna-migration-v24-20260905T030000Z.db",
		"notes.txt",
	}
	if !slices.Equal(names, want) {
		t.Errorf("after rotation: %v", names)
	}
}
