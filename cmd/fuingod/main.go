// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Command fuingod is the Fuingo server.
//
//	fuingod [--env ENV] [--addr HOST:PORT] [--trusted-proxies LIST] [--max-api-upload BYTES] [--max-player-upload BYTES] [--max-gm-upload BYTES] [--public-url URL] --db PATH
//
// Every flag can also be set with a FUINGO_ environment variable. Explicit
// flags override the environment, which overrides the defaults:
//
//	--env   FUINGO_ENV   development, test, production, or agent (required)
//	--addr  FUINGO_ADDR  backend listen address (default 127.0.0.1:18443)
//	--db    FUINGO_DB    existing database file; :memory: in development and agent only
//	--trusted-proxies  FUINGO_TRUSTED_PROXIES  comma-separated proxy addresses and
//	        CIDR prefixes whose X-Forwarded-For is trusted (default loopback)
//	--max-api-upload   FUINGO_MAX_API_UPLOAD   largest file accepted through /api/,
//	        in bytes (default 1048576)
//	--max-player-upload  FUINGO_MAX_PLAYER_UPLOAD  largest file a player may
//	        upload on the web, in bytes (default 65536)
//	--max-gm-upload    FUINGO_MAX_GM_UPLOAD    largest file a GM may upload on
//	        the web, in bytes (default 1048576)
//	--public-url       FUINGO_PUBLIC_URL       the server's public origin, such as
//	        https://fuingo.example.com, for the links in mail (required except in
//	        development and agent, where it is https://htmx-app.localhost:8443)
//
// fuingod mails invitations and password resets through Mailgun. The Mailgun
// settings are secrets, read only from the environment: FUINGO_MAILGUN_DOMAIN,
// FUINGO_MAILGUN_API_KEY, FUINGO_MAILGUN_FROM, and optionally
// FUINGO_MAILGUN_API_BASE and FUINGO_MAILGUN_SANDBOX_TO_ADDR. In production
// fuingod does not start without them; elsewhere it starts and warns that it
// cannot send mail.
//
// fuingod serves plain HTTP behind a TLS-terminating reverse proxy. It opens an
// existing database and never creates or migrates one; use fuingodb for that.
// Failed logins are limited per client address, which comes from
// X-Forwarded-For when the connection is from a trusted proxy.
// With --db :memory:, it runs against a fresh private in-memory database seeded
// with the default accounts, discarded when the server stops.
//
// On SIGTERM or an interrupt, fuingod stops accepting connections, gives
// in-flight requests 10 seconds to finish, cancels and cuts off any still
// running, closes the database, and exits, non-zero if it cut requests off.
// A second signal stops it at once. contrib/systemd/fuingod.service runs it
// as a systemd service.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/mdhender/fuingo"
	"github.com/mdhender/fuingo/internal/api"
	"github.com/mdhender/fuingo/internal/cerrs"
	"github.com/mdhender/fuingo/internal/cli"
	"github.com/mdhender/fuingo/internal/clientip"
	"github.com/mdhender/fuingo/internal/devdata"
	"github.com/mdhender/fuingo/internal/links"
	"github.com/mdhender/fuingo/internal/mail"
	"github.com/mdhender/fuingo/internal/server"
	"github.com/mdhender/fuingo/internal/store"
	"github.com/peterbourgon/ff/v4"
	"github.com/peterbourgon/ff/v4/ffhelp"
)

const (
	ErrInvalidAddress     = cerrs.Error("invalid listen address")
	ErrShutdownTimeout    = cerrs.Error("requests still running at the shutdown deadline were cut off")
	ErrMissingPublicURL   = cerrs.Error("--public-url (FUINGO_PUBLIC_URL) is required")
	ErrInvalidUploadLimit = cerrs.Error("an upload limit must be 1 or more")
	ErrMemoryDatabase     = cerrs.Error(":memory: is allowed only in development and agent")
	ErrMissingDatabase    = cerrs.Error("--db (FUINGO_DB) is required")
)

const (
	defaultAddr = "127.0.0.1:18443"
	// defaultTrustedProxies is loopback, where the development proxy runs.
	defaultTrustedProxies = "127.0.0.0/8,::1"
)

// shutdownTimeout is how long in-flight requests may run after a stop
// signal. Service managers must wait longer than this before killing the
// process. Tests shorten it.
var shutdownTimeout = 10 * time.Second

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// After the first signal starts the shutdown, restore the default
	// handling so a second one stops the process at once.
	go func() {
		<-ctx.Done()
		stop()
	}()
	if err := run(ctx, os.Args[1:], os.Stderr, nil); err != nil {
		if !errors.Is(err, ff.ErrHelp) {
			fmt.Fprintf(os.Stderr, "fuingod: %v\n", err)
		}
		os.Exit(1)
	}
}

type config struct {
	env  string
	addr string
	db   string

	trustedProxies string
	proxies        []netip.Prefix

	maxAPIUpload    int64
	maxPlayerUpload int64
	maxGMUpload     int64

	publicURL string
	sender    mail.Sender
}

// loadConfig resolves and validates the configuration.
func loadConfig(args []string, stderr io.Writer) (config, error) {
	var cfg config
	fs := ff.NewFlagSet("fuingod")
	fs.StringVar(&cfg.env, 0, "env", "", "environment: development, test, production, or agent (FUINGO_ENV)")
	fs.StringVar(&cfg.addr, 0, "addr", defaultAddr, "backend listen address, host:port (FUINGO_ADDR)")
	fs.StringVar(&cfg.db, 0, "db", "", "existing database file, or :memory: in development and agent (FUINGO_DB)")
	fs.StringVar(&cfg.trustedProxies, 0, "trusted-proxies", defaultTrustedProxies, "comma-separated proxy addresses and CIDR prefixes whose X-Forwarded-For is trusted; empty trusts none (FUINGO_TRUSTED_PROXIES)")
	fs.Int64Var(&cfg.maxAPIUpload, 0, "max-api-upload", api.DefaultMaxUpload, "largest file accepted through /api/, in bytes (FUINGO_MAX_API_UPLOAD)")
	fs.Int64Var(&cfg.maxPlayerUpload, 0, "max-player-upload", server.DefaultMaxPlayerUpload, "largest file a player may upload on the web, in bytes (FUINGO_MAX_PLAYER_UPLOAD)")
	fs.Int64Var(&cfg.maxGMUpload, 0, "max-gm-upload", server.DefaultMaxGMUpload, "largest file a GM may upload on the web, in bytes (FUINGO_MAX_GM_UPLOAD)")
	fs.StringVar(&cfg.publicURL, 0, "public-url", "", "the server's public origin, for the links in mail; required except in development and agent (FUINGO_PUBLIC_URL)")
	cmd := &ff.Command{
		Name:     "fuingod",
		Usage:    "fuingod [--env ENV] [--addr HOST:PORT] [--trusted-proxies LIST] [--max-api-upload BYTES] [--max-player-upload BYTES] [--max-gm-upload BYTES] [--public-url URL] --db PATH",
		LongHelp: "fuingod " + fuingo.Version().String(),
		Flags:    fs,
	}
	if err := cli.Parse(cmd, args, &cfg.env); err != nil {
		if errors.Is(err, ff.ErrHelp) {
			fmt.Fprint(stderr, ffhelp.Command(cmd))
		}
		return cfg, err
	}
	if len(fs.GetArgs()) != 0 {
		return cfg, fmt.Errorf("unexpected arguments %q", fs.GetArgs())
	}

	if err := checkAddress(cfg.addr); err != nil {
		return cfg, err
	}
	var err error
	if cfg.proxies, err = clientip.ParsePrefixes(cfg.trustedProxies); err != nil {
		return cfg, err
	}
	for _, limit := range []struct {
		flag  string
		bytes int64
	}{
		{"--max-api-upload (FUINGO_MAX_API_UPLOAD)", cfg.maxAPIUpload},
		{"--max-player-upload (FUINGO_MAX_PLAYER_UPLOAD)", cfg.maxPlayerUpload},
		{"--max-gm-upload (FUINGO_MAX_GM_UPLOAD)", cfg.maxGMUpload},
	} {
		if limit.bytes < 1 {
			return cfg, fmt.Errorf("%s: %w: %d", limit.flag, ErrInvalidUploadLimit, limit.bytes)
		}
	}
	if cfg.publicURL == "" {
		if cfg.publicURL = links.DefaultPublicURL(cfg.env); cfg.publicURL == "" {
			return cfg, ErrMissingPublicURL
		}
	}
	if cfg.publicURL, err = links.PublicURL(cfg.publicURL); err != nil {
		return cfg, fmt.Errorf("--public-url: %w", err)
	}
	// The Mailgun settings are secrets: they have no flags.
	if cfg.sender, err = mail.SenderFromEnv(cfg.env); err != nil {
		return cfg, err
	}
	if _, unconfigured := cfg.sender.(mail.Unconfigured); unconfigured && cfg.env == "production" {
		return cfg, mail.ErrNotConfigured
	}
	switch cfg.db {
	case "":
		return cfg, ErrMissingDatabase
	case ":memory:":
		if !devdata.Allowed(cfg.env) {
			return cfg, ErrMemoryDatabase
		}
	}
	return cfg, nil
}

func checkAddress(addr string) error {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%w %q: %v", ErrInvalidAddress, addr, err)
	}
	if n, err := strconv.ParseUint(port, 10, 16); err != nil || (n == 0 && port != "0") {
		return fmt.Errorf("%w %q: bad port", ErrInvalidAddress, addr)
	}
	return nil
}

// run starts the server and blocks until ctx is canceled. If ready is not
// nil, it is called with the bound address once the server is listening.
func run(ctx context.Context, args []string, stderr io.Writer, ready func(net.Addr)) error {
	cfg, err := loadConfig(args, stderr)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewTextHandler(stderr, nil))

	s, err := openStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer s.Close()

	if _, unconfigured := cfg.sender.(mail.Unconfigured); unconfigured {
		logger.Warn("mail is not configured: invitations and password resets cannot be sent")
	}
	handler, err := server.New(server.Config{
		Env:    cfg.env,
		Store:  s,
		Logger: logger,

		TrustedProxies:  cfg.proxies,
		MaxAPIUpload:    cfg.maxAPIUpload,
		MaxPlayerUpload: cfg.maxPlayerUpload,
		MaxGMUpload:     cfg.maxGMUpload,
		Links:           &links.Service{Store: s, Sender: cfg.sender, PublicURL: cfg.publicURL},
	})
	if err != nil {
		return err
	}

	ln, err := net.Listen("tcp", cfg.addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.addr, err)
	}
	// Requests get a context that is canceled if they outlast the shutdown
	// deadline, which interrupts their SQLite statements and their waits for
	// a database connection.
	baseCtx, cancelRequests := context.WithCancel(context.Background())
	defer cancelRequests()
	srv := &http.Server{
		Handler:           handler,
		BaseContext:       func(net.Listener) context.Context { return baseCtx },
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       time.Minute,
		WriteTimeout:      time.Minute,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}
	logger.Info("fuingod listening", "version", fuingo.Version().String(), "env", cfg.env, "addr", ln.Addr().String(), "db", cfg.db, "trusted_proxies", cfg.proxies)
	if prefix := server.HelperPrefix(cfg.env); prefix != "" {
		logger.Info("account helpers enabled", "prefix", prefix)
	}
	if ready != nil {
		ready(ln.Addr())
	}

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	// Stop accepting connections and let in-flight requests finish. Requests
	// still running at the deadline are canceled and their connections
	// closed. The deferred s.Close then waits for their database connections
	// and closes the database.
	logger.Info("fuingod stopping", "timeout", shutdownTimeout.String())
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		cancelRequests()
		if errors.Is(err, context.DeadlineExceeded) {
			err = ErrShutdownTimeout
		}
		return errors.Join(err, srv.Close())
	}
	logger.Info("fuingod stopped")
	return nil
}

// openStore opens the configured database. It never creates or migrates a
// file; :memory: is a fresh private database with the default accounts.
func openStore(ctx context.Context, cfg config) (*store.Store, error) {
	if cfg.db != ":memory:" {
		return store.Open(ctx, cfg.db)
	}
	s, err := store.OpenPrivateMemory(ctx, "fuingod")
	if err != nil {
		return nil, err
	}
	if err := devdata.Seed(ctx, s, cfg.env); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}
