// Command laterna is the media server.
//
//	laterna [serve]      start the server (default command)
//	laterna migrate      apply the database migrations, then exit
//	laterna backup       back the database up
//	laterna restore      prepare the restore of a backup
//	laterna version      print the version
//	laterna healthcheck  query /health on the local server (Docker healthcheck)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/laterna-project/laterna/internal/buildinfo"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

const usage = `Usage: laterna <command> [options]

Commands:
  serve     start the server (default)
  migrate   apply the database migrations, then exit
  backup    back up the database right now, whether the server runs or not
  restore <backup>  stage a restore, applied at the next start
  version   print the version
  healthcheck  query /health on the local server (Docker probe)

Options of serve, migrate, backup and restore:
  -config <file>      TOML file (default: $LATERNA_CONFIG, else ./laterna.toml if it exists)
  -address <address>  listen address (serve only, e.g. :8096)
`

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	cmd := "serve"
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "serve":
		err = serveCommand(ctx, args, stdout, stderr)
	case "migrate":
		err = migrateCommand(ctx, args, stdout, stderr)
	case "backup":
		err = backupCommand(ctx, args, stdout, stderr)
	case "restore":
		err = restoreCommand(ctx, args, stdout, stderr)
	case "healthcheck":
		err = healthcheckCommand(ctx, args, stdout, stderr)
	case "version":
		_, _ = fmt.Fprintln(stdout, versionString())
	case "help", "-h", "--help":
		_, _ = io.WriteString(stdout, usage)
	default:
		_, _ = fmt.Fprintf(stderr, "unknown command: %s\n\n%s", cmd, usage)
		return 2
	}
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "laterna: %v\n", err)
		return 1
	}
	return 0
}

func versionString() string {
	v := "laterna " + buildinfo.Version
	if c := buildinfo.Commit(); c != "" {
		v += " (" + c + ")"
	}
	return v
}

// configPath picks the configuration file: flag, environment variable, then ./laterna.toml.
func configPath(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if env := os.Getenv("LATERNA_CONFIG"); env != "" {
		return env
	}
	if _, err := os.Stat("laterna.toml"); err == nil {
		return "laterna.toml"
	}
	return ""
}
