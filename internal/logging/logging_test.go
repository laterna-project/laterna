package logging

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestParseLevel(t *testing.T) {
	for in, want := range map[string]slog.Level{"debug": slog.LevelDebug, "info": slog.LevelInfo, "WARN": slog.LevelWarn} {
		got, err := ParseLevel(in)
		if err != nil || got != want {
			t.Errorf("ParseLevel(%q) = %v, %v", in, got, err)
		}
	}
	if _, err := ParseLevel("bavard"); err == nil {
		t.Error("unknown level accepted")
	}
}

func TestNewWritesConsoleAndFile(t *testing.T) {
	dir := t.TempDir()
	var console bytes.Buffer
	log, closer, err := New(&console, Options{Level: slog.LevelInfo, JSON: true, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	log.Debug("ignored")
	log.Info("startup", "port", 8096)
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(console.String(), "\"msg\":\"startup\"") || strings.Contains(console.String(), "ignored") {
		t.Errorf("unexpected standard output: %s", console.String())
	}
	files, _ := filepath.Glob(filepath.Join(dir, FilePrefix+"*"+FileExt))
	if len(files) != 1 {
		t.Fatalf("want one file for today, found %v", files)
	}
	raw, _ := os.ReadFile(files[0])
	if !strings.Contains(string(raw), "msg=startup port=8096") {
		t.Errorf("unexpected file: %s", raw)
	}
}

func TestDailyFileRotatesAndPrunes(t *testing.T) {
	dir := t.TempDir()
	d, err := NewDailyFile(dir, 2)
	if err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 9, 26, 23, 0, 0, 0, time.UTC)
	d.now = func() time.Time { return day }
	for range 4 {
		if _, err := d.Write([]byte("ligne\n")); err != nil {
			t.Fatal(err)
		}
		day = day.Add(24 * time.Hour)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	want := []string{"laterna_20260928.log", "laterna_20260929.log"}
	if !slices.Equal(names, want) {
		t.Errorf("files kept %v, want %v", names, want)
	}
}

func TestRing(t *testing.T) {
	ring := NewRing(3)
	log, closer, err := New(io.Discard, Options{Level: slog.LevelInfo, Ring: ring})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closer.Close() }()
	log.Debug("too low")
	log.Info("scan done", "library", "Films", slog.Group("stats", "added", 2))
	log.With("job", 7).Warn("job failed", "err", "boum")
	log.Info("one")
	log.Info("two") // the ring only keeps the last three
	all := ring.Entries(slog.LevelDebug, "", 10)
	if len(all) != 3 || all[0].Message != "two" || all[2].Message != "job failed" {
		t.Fatalf("messages: %+v", all)
	}
	if got := ring.Entries(slog.LevelWarn, "", 10); len(got) != 1 || len(got[0].Attrs) != 2 || got[0].Attrs[0] != (Attr{Key: "job", Value: "7"}) {
		t.Errorf("warnings: %+v", got)
	}
	if got := ring.Entries(slog.LevelDebug, "BOUM", 10); len(got) != 1 {
		t.Errorf("search in attributes: %+v", got)
	}
	if got := ring.Entries(slog.LevelDebug, "", 1); len(got) != 1 || got[0].Message != "two" {
		t.Errorf("limit: %+v", got)
	}
	ring2 := NewRing(5)
	l2, c2, _ := New(io.Discard, Options{Level: slog.LevelInfo, Ring: ring2})
	defer func() { _ = c2.Close() }()
	l2.WithGroup("http").Info("request", "status", 200, slog.Group("peer", "ip", "10.0.0.1"))
	if got := ring2.Entries(slog.LevelInfo, "", 5); len(got) != 1 || got[0].Attrs[1] != (Attr{Key: "http.peer.ip", Value: "10.0.0.1"}) {
		t.Errorf("groups: %+v", got)
	}
}
