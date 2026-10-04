package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/laterna-project/laterna/internal/config"
)

// healthcheckCommand queries /health on the local server. It is the healthcheck of the Docker
// image, which then needs neither curl nor wget.
func healthcheckCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgFile := fs.String("config", "", "TOML configuration file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(configPath(*cfgFile), os.LookupEnv)
	if err != nil {
		return err
	}
	url, err := healthURL(cfg.Server.Address)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server unhealthy (%d): %s", resp.StatusCode, body)
	}
	_, _ = fmt.Fprintf(stdout, "%s\n", body)
	return nil
}

// healthURL works out the local address of the probe from the listen address: ":8096" or
// "0.0.0.0:8096" become 127.0.0.1:8096.
func healthURL(address string) (string, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", fmt.Errorf("listen address %q: %w", address, err)
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/health", nil
}
