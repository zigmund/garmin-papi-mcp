// Command garmin-papi-mcp is a read-only MCP server for the Garmin Connect
// Personal API, and the OAuth 2.1 authorization server in front of it: the
// user's Personal API key, entered on the authorize page, is sealed into the
// tokens handed to the MCP client.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/zigmund/garmin-papi-mcp/internal/certreload"
	"github.com/zigmund/garmin-papi-mcp/internal/config"
	"github.com/zigmund/garmin-papi-mcp/internal/logging"
	"github.com/zigmund/garmin-papi-mcp/internal/mcp"
	"github.com/zigmund/garmin-papi-mcp/internal/seal"
	"github.com/zigmund/garmin-papi-mcp/internal/server"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

// The MCP initialize response reports the binary version.
func init() { mcp.Version = version }

const defaultConfigPath = "/etc/garmin-papi-mcp/config.yaml"

// errUsage marks an error that was already reported (bad command line).
var errUsage = errors.New("invalid command line")

func main() {
	err := run(os.Args[1:], os.Stdout, os.Stderr)
	if err != nil && !errors.Is(err, errUsage) {
		fmt.Fprintf(os.Stderr, "garmin-papi-mcp: %v\n", err)
	}
	os.Exit(exitCode(err))
}

// exitCode maps the result of run to the process exit status: 0 on success,
// 2 for a bad command line, 1 for any other failure.
func exitCode(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, errUsage):
		return 2
	default:
		return 1
	}
}

// hooks are optional injection points for execute.
type hooks struct {
	reload <-chan os.Signal // reload requests (SIGHUP in production)
	ready  func(net.Addr)   // called with the bound address once listening
	stop   func()           // called once shutdown begins (restores default signal handling)
}

// run wires OS signals to execute: SIGINT/SIGTERM cancel the context (graceful
// shutdown), SIGHUP is delivered as a reload request and never kills the
// process.
func run(args []string, stdout, stderr io.Writer) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)

	return execute(ctx, args, stdout, stderr, hooks{reload: hup, stop: stop})
}

// execute is the testable entry point. It serves until ctx is cancelled.
// See hooks for the optional injection points.
func execute(ctx context.Context, args []string, stdout, stderr io.Writer, h hooks) error {
	fs := flag.NewFlagSet("garmin-papi-mcp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", defaultConfigPath, "path to the YAML config file")
	showVersion := fs.Bool("version", false, "print the version and exit")
	check := fs.Bool("check", false, "validate the config (token keys and TLS files included) and exit")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(fs.Output(), "usage: garmin-papi-mcp [-config path] [-check] [-version]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil // usage already printed
		}
		return errUsage // flag package already printed the error
	}
	if fs.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "unexpected argument %q\n", fs.Arg(0))
		fs.Usage()
		return errUsage
	}

	if *showVersion {
		_, err := fmt.Fprintln(stdout, mcp.Version)
		return err
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	log := logging.New(cfg.Log, stderr)
	sealer, err := seal.New(cfg.Secrets.Keys)
	if err != nil {
		return fmt.Errorf("initializing token sealer: %w", err)
	}

	var certs *certreload.Reloader
	if cfg.TLSEnabled() {
		// Fail fast: never fall back to plain HTTP when TLS was requested.
		certs, err = certreload.New(cfg.TLS.CertFile, cfg.TLS.KeyFile, log)
		if err != nil {
			return fmt.Errorf("loading tls certificate: %w", err)
		}
	}
	if *check {
		_, err := fmt.Fprintln(stdout, "config ok")
		return err
	}

	srv, err := server.New(cfg, sealer, server.NewGarminClient(cfg, version), log)
	if err != nil {
		return fmt.Errorf("building server: %w", err)
	}
	if certs != nil {
		srv.EnableTLS(&tls.Config{
			MinVersion:     tls.VersionTLS12,
			GetCertificate: certs.GetCertificate,
		})
		srv.OnReload("tls certificate", certs.Reload)
	}

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", cfg.Listen, err)
	}

	log.Info("starting",
		"version", version,
		"listen", ln.Addr().String(),
		"public_url", cfg.PublicURL,
		"garmin_base_url", cfg.Garmin.BaseURL,
		"tls", certs != nil,
	)
	if cfg.AllowInsecureHTTP {
		log.Warn("allow_insecure_http is set: plain http is accepted for public_url and oauth.allowed_redirect_uris; use it only on trusted networks")
	}
	if h.ready != nil {
		h.ready(ln.Addr())
	}

	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go srv.WatchReload(wctx, h.reload)
	var watchers sync.WaitGroup
	if certs != nil {
		watchers.Go(func() { certs.Watch(wctx, cfg.TLS.ReloadInterval) })
	}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		<-wctx.Done() // ctx cancelled, or Serve returned and cancel ran
		if ctx.Err() == nil {
			return
		}
		log.Info("shutdown signal received; send it again to force exit")
		if h.stop != nil {
			h.stop() // a second SIGINT/SIGTERM now kills the process
		}
	}()

	err = srv.Serve(ctx, ln)
	cancel()
	<-stopped
	watchers.Wait()
	if err != nil {
		return err
	}
	log.Info("stopped")
	return nil
}
