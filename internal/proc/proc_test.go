package proc

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The test binary also plays the server and its child (LATERNA_PROC_ROLE).
func TestMain(m *testing.M) {
	switch os.Getenv("LATERNA_PROC_ROLE") {
	case "server":
		// Server: binds to its children, starts one, prints its PID, waits to be killed.
		if err := Bind(); err != nil {
			fmt.Println("erreur", err)
			os.Exit(2)
		}
		cmd := Command(context.Background(), os.Args[0], "-test.run=^$")
		cmd.Env = append(os.Environ(), "LATERNA_PROC_ROLE=child")
		if err := cmd.Start(); err != nil {
			fmt.Println("erreur", err)
			os.Exit(2)
		}
		fmt.Println(cmd.Process.Pid)
		time.Sleep(time.Minute)
		os.Exit(0)
	case "child":
		time.Sleep(time.Minute) // an FFmpeg that never ends
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// A server killed abruptly takes its children with it: no orphan FFmpeg.
func TestChildrenDieWithServer(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" {
		t.Skip("no process binding on " + runtime.GOOS)
	}
	server := exec.Command(os.Args[0], "-test.run=^$")
	server.Env = append(os.Environ(), "LATERNA_PROC_ROLE=server")
	out, err := server.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		_ = server.Process.Kill()
		t.Fatalf("server: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		_ = server.Process.Kill()
		t.Fatalf("server: %q", line)
	}
	if !alive(pid) {
		t.Fatal("child not started")
	}
	if err := server.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = server.Wait()
	for deadline := time.Now().Add(5 * time.Second); alive(pid); time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("child %d still alive after the server died", pid)
		}
	}
}
