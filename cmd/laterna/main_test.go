package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	laternav1 "github.com/laterna-project/laterna/internal/api/gen/laterna/v1"
	"github.com/laterna-project/laterna/internal/api/gen/laterna/v1/laternav1connect"
	"github.com/laterna-project/laterna/internal/config"
	"github.com/laterna-project/laterna/internal/domain"
	"github.com/laterna-project/laterna/internal/platform"
)

func TestRunVersionAndUnknownCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"version"}, &out, &errOut); code != 0 {
		t.Fatalf("code %d", code)
	}
	if !strings.HasPrefix(out.String(), "laterna ") {
		t.Errorf("version: %q", out.String())
	}
	if code := run(context.Background(), []string{"dance"}, &out, &errOut); code != 2 {
		t.Errorf("unknown command: code %d", code)
	}
}

// startServer starts a real server on a free port and returns its URL. It is stopped (and the
// shutdown checked) when the test ends.
func startServer(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dirs := platform.Dirs{
		Data:     filepath.Join(root, "café"),
		Cache:    filepath.Join(root, "cache"),
		Metadata: filepath.Join(root, "metadata"),
	}
	if err := dirs.Ensure(); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Server.Address = "127.0.0.1:0"

	ctx, cancel := context.WithCancel(context.Background())
	addrCh := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, cfg, dirs, slog.New(slog.DiscardHandler), nil, func(addr string) { addrCh <- addr })
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("shutdown: %v", err)
			}
		case <-time.After(20 * time.Second):
			t.Error("graceful shutdown did not complete")
		}
	})

	select {
	case addr := <-addrCh:
		return "http://" + addr
	case err := <-done:
		t.Fatalf("the server stopped while starting: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the server did not start")
	}
	return ""
}

func TestHealthURL(t *testing.T) {
	for address, want := range map[string]string{
		":8096":            "http://127.0.0.1:8096/health",
		"0.0.0.0:9000":     "http://127.0.0.1:9000/health",
		"[::]:8096":        "http://127.0.0.1:8096/health",
		"192.168.1.5:8096": "http://192.168.1.5:8096/health",
	} {
		got, err := healthURL(address)
		if err != nil || got != want {
			t.Errorf("healthURL(%q) = %q, %v; want %q", address, got, err, want)
		}
	}
	if _, err := healthURL("8096"); err == nil {
		t.Error("address without a port accepted")
	}
}

func TestHealthcheckCommand(t *testing.T) {
	baseURL := startServer(t)
	t.Setenv("LATERNA_ADDRESS", strings.TrimPrefix(baseURL, "http://"))
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"healthcheck"}, &out, &errOut); code != 0 {
		t.Fatalf("code %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), `"status":"ok"`) {
		t.Errorf("output: %s", out.String())
	}
}

func checkInfo(t *testing.T, info *laternav1.GetServerInfoResponse) {
	t.Helper()
	if info.GetName() == "" || info.GetVersion() == "" {
		t.Errorf("unexpected response: %v", info)
	}
	if _, err := domain.ParseID(info.GetId()); err != nil {
		t.Errorf("invalid ID %q: %v", info.GetId(), err)
	}
}

// The same service must answer the three ways of calling it: Connect protocol with JSON (browsers),
// gRPC over cleartext HTTP/2 (native clients), and a plain GET (a read without side effects, hence
// cacheable).
func TestServeAllProtocols(t *testing.T) {
	baseURL := startServer(t)
	ctx := context.Background()

	t.Run("connect JSON over HTTP/1.1", func(t *testing.T) {
		client := laternav1connect.NewServerServiceClient(http.DefaultClient, baseURL, connect.WithProtoJSON())
		resp, err := client.GetServerInfo(ctx, connect.NewRequest(&laternav1.GetServerInfoRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		checkInfo(t, resp.Msg)
	})

	t.Run("gRPC over cleartext HTTP/2", func(t *testing.T) {
		var protocols http.Protocols
		protocols.SetUnencryptedHTTP2(true)
		h2c := &http.Client{Transport: &http.Transport{Protocols: &protocols}}
		client := laternav1connect.NewServerServiceClient(h2c, baseURL, connect.WithGRPC())
		resp, err := client.GetServerInfo(ctx, connect.NewRequest(&laternav1.GetServerInfoRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		checkInfo(t, resp.Msg)
	})

	t.Run("cacheable GET", func(t *testing.T) {
		client := laternav1connect.NewServerServiceClient(http.DefaultClient, baseURL,
			connect.WithProtoJSON(), connect.WithHTTPGet())
		resp, err := client.GetServerInfo(ctx, connect.NewRequest(&laternav1.GetServerInfoRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		checkInfo(t, resp.Msg)
	})

	t.Run("curl: hand-written JSON POST", func(t *testing.T) {
		resp, err := http.Post(baseURL+laternav1connect.ServerServiceGetServerInfoProcedure, "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		var info map[string]any
		if err := json.Unmarshal(body, &info); err != nil || info["name"] == "" || info["setupRequired"] != true {
			t.Errorf("response %d: %s", resp.StatusCode, body)
		}
	})

	t.Run("health", func(t *testing.T) {
		resp, err := http.Get(baseURL + "/health")
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("status %d", resp.StatusCode)
		}
	})

	// Last: setup changes the answer of GetServerInfo. The stream is left open on purpose: the
	// graceful shutdown of the server (test cleanup) must not wait for it.
	t.Run("gRPC event stream, open during shutdown", func(t *testing.T) {
		var protocols http.Protocols
		protocols.SetUnencryptedHTTP2(true)
		h2c := &http.Client{Transport: &http.Transport{Protocols: &protocols}}
		setup, err := laternav1connect.NewAuthServiceClient(h2c, baseURL, connect.WithGRPC()).Setup(ctx, connect.NewRequest(&laternav1.SetupRequest{
			Username: "admin", Password: "a-strong-password",
			Device: &laternav1.Device{Name: "Test", Client: "Test", ClientVersion: "1", Platform: "Go"},
		}))
		if err != nil {
			t.Fatal(err)
		}
		req := connect.NewRequest(&laternav1.SubscribeRequest{})
		req.Header().Set("Authorization", "Bearer "+setup.Msg.GetToken())
		stream, err := laternav1connect.NewEventServiceClient(h2c, baseURL, connect.WithGRPC()).Subscribe(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		if !stream.Receive() || stream.Msg().GetEvent().GetHeartbeat() == nil {
			t.Fatalf("stream: %v", stream.Err())
		}
	})
}
