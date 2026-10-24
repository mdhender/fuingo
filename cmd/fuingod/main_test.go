// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"mime/multipart"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mdhender/fuingo/internal/cli"
	"github.com/mdhender/fuingo/internal/clientip"
	"github.com/mdhender/fuingo/internal/devdata"
	"github.com/mdhender/fuingo/internal/store"
)

const pepper = "test-pepper"

func clearEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"FUINGO_ENV", "FUINGO_ADDR", "FUINGO_DB", "FUINGO_PEPPER", "FUINGO_TRUSTED_PROXIES"} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
}

func TestConfigDefaultsAndPrecedence(t *testing.T) {
	clearEnv(t)
	t.Setenv("FUINGO_PEPPER", pepper)
	cfg, err := loadConfig([]string{"--env", "development", "--db", ":memory:"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.addr != "127.0.0.1:18443" {
		t.Errorf("default addr = %q, want 127.0.0.1:18443", cfg.addr)
	}
	if want := []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")}; !slices.Equal(cfg.proxies, want) {
		t.Errorf("default trusted proxies = %v, want %v", cfg.proxies, want)
	}
	t.Setenv("FUINGO_TRUSTED_PROXIES", "10.0.0.0/8")
	if cfg, err = loadConfig([]string{"--env", "development", "--db", ":memory:"}, io.Discard); err != nil {
		t.Fatal(err)
	} else if want := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}; !slices.Equal(cfg.proxies, want) {
		t.Errorf("FUINGO_TRUSTED_PROXIES: %v, want %v", cfg.proxies, want)
	}
	if cfg, err = loadConfig([]string{"--env", "development", "--db", ":memory:", "--trusted-proxies", ""}, io.Discard); err != nil {
		t.Fatal(err)
	} else if len(cfg.proxies) != 0 {
		t.Errorf("--trusted-proxies \"\" = %v, want none", cfg.proxies)
	}
	os.Unsetenv("FUINGO_TRUSTED_PROXIES")

	t.Setenv("FUINGO_ENV", "development")
	t.Setenv("FUINGO_ADDR", "127.0.0.1:28443")
	t.Setenv("FUINGO_DB", ":memory:")
	if cfg, err = loadConfig(nil, io.Discard); err != nil {
		t.Fatal(err)
	} else if cfg.addr != "127.0.0.1:28443" || cfg.env != "development" {
		t.Errorf("from environment: %+v", cfg)
	}
	if cfg, err = loadConfig([]string{"--addr", "0.0.0.0:38443"}, io.Discard); err != nil {
		t.Fatal(err)
	} else if cfg.addr != "0.0.0.0:38443" {
		t.Errorf("--addr did not override FUINGO_ADDR: %q", cfg.addr)
	}
}

func TestConfigRefusals(t *testing.T) {
	clearEnv(t)
	cases := []struct {
		name   string
		pepper string
		args   []string
		want   error
	}{
		{"missing pepper", "", []string{"--env", "development", "--db", ":memory:"}, cli.ErrMissingPepper},
		{"missing db", pepper, []string{"--env", "development"}, ErrMissingDatabase},
		{"memory in test", pepper, []string{"--env", "test", "--db", ":memory:"}, ErrMemoryDatabase},
		{"memory in production", pepper, []string{"--env", "production", "--db", ":memory:"}, ErrMemoryDatabase},
		{"bad addr", pepper, []string{"--env", "development", "--addr", "18443", "--db", ":memory:"}, ErrInvalidAddress},
		{"bad port", pepper, []string{"--env", "development", "--addr", "127.0.0.1:99999", "--db", ":memory:"}, ErrInvalidAddress},
		{"bad trusted proxy", pepper, []string{"--env", "development", "--trusted-proxies", "caddy", "--db", ":memory:"}, clientip.ErrInvalidProxy},
	}
	for _, c := range cases {
		os.Unsetenv("FUINGO_PEPPER")
		if c.pepper != "" {
			t.Setenv("FUINGO_PEPPER", c.pepper)
		}
		if _, err := loadConfig(c.args, io.Discard); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}
	t.Setenv("FUINGO_PEPPER", pepper)
	if _, err := loadConfig([]string{"--db", ":memory:"}, io.Discard); err == nil {
		t.Errorf("no environment: succeeded")
	}
}

func TestRunNeverCreatesOrMigrates(t *testing.T) {
	clearEnv(t)
	t.Setenv("FUINGO_PEPPER", pepper)
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.db")
	err := run(t.Context(), []string{"--env", "development", "--addr", "127.0.0.1:0", "--db", missing}, io.Discard, nil)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing database: %v, want ErrNotExist", err)
	}
	if _, err := os.Stat(missing); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("fuingod created %s", missing)
	}
}

func TestRunReportsBindFailure(t *testing.T) {
	clearEnv(t)
	t.Setenv("FUINGO_PEPPER", pepper)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	err = run(t.Context(), []string{"--env", "development", "--addr", ln.Addr().String(), "--db", ":memory:"}, io.Discard, nil)
	if err == nil || !strings.Contains(err.Error(), "listen on "+ln.Addr().String()) {
		t.Errorf("bind failure: %v", err)
	}
}

// startServer runs fuingod on a free port and returns its base URL.
func startServer(t *testing.T, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	addrc := make(chan net.Addr, 1)
	errc := make(chan error, 1)
	go func() {
		errc <- run(ctx, append([]string{"--addr", "127.0.0.1:0"}, args...), io.Discard, func(a net.Addr) { addrc <- a })
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-errc; err != nil {
			t.Errorf("run: %v", err)
		}
	})
	select {
	case a := <-addrc:
		return "http://" + a.String()
	case err := <-errc:
		t.Fatalf("run: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("server did not start")
	}
	return ""
}

func post(t *testing.T, url, contentType string, body io.Reader, headers ...string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, body)
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Add(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data)
}

func postUpload(t *testing.T, base, email, game, token, file string, headers ...string) (int, string) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "turn.txt")
	io.WriteString(fw, game+" 1 "+email+" "+token+"\n"+file)
	mw.Close()
	return post(t, base+"/upload", mw.FormDataContentType(), &body, headers...)
}

func TestServeMemoryDatabase(t *testing.T) {
	clearEnv(t)
	t.Setenv("FUINGO_PEPPER", pepper)
	base := startServer(t, "--env", "development", "--db", ":memory:")

	u := devdata.Uploaders[0]
	if code, body := postUpload(t, base, u.Email, u.Game, u.Token, "turn\n"); code != http.StatusOK {
		t.Errorf("default account upload: %d %s", code, body)
	}
	if code, body := post(t, base+"/__development/create-account/game01/heron%40example.com/heron-token", "", nil); code != http.StatusCreated {
		t.Errorf("create-account: %d %s", code, body)
	}
	if code, body := postUpload(t, base, "heron@example.com", "game01", "heron-token", "turn 2\n"); code != http.StatusOK {
		t.Errorf("helper account upload: %d %s", code, body)
	}
	if code, _ := post(t, base+"/__agent/create-account/game01/a%40example.com/t", "", nil); code != http.StatusNotFound {
		t.Errorf("/__agent in development: %d, want 404", code)
	}
}

func TestServeDatabaseFile(t *testing.T) {
	clearEnv(t)
	t.Setenv("FUINGO_PEPPER", pepper)
	dir := t.TempDir()
	path := filepath.Join(dir, "fuingo.db")
	s, err := store.Create(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := devdata.Seed(t.Context(), s, "development", pepper); err != nil {
		t.Fatal(err)
	}
	s.Close()

	// In test, the defaults in the file still work but the helpers do not exist.
	base := startServer(t, "--env", "test", "--db", path)
	u := devdata.Uploaders[2]
	if code, body := postUpload(t, base, u.Email, u.Game, u.Token, "turn\n"); code != http.StatusOK {
		t.Errorf("upload: %d %s", code, body)
	}
	// The contents are in the database file, not on the filesystem.
	s, err = store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	blobs, err := s.Blobs(t.Context())
	s.Close()
	if err != nil || len(blobs) != 1 || string(blobs[0].Contents) != "turn\n" {
		t.Errorf("blobs = %+v, %v; want the uploaded file", blobs, err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) > 3 {
		t.Errorf("directory has %d entries; want only the database and its WAL files", len(entries))
	}
	for _, prefix := range []string{"/__development", "/__agent", "/__test"} {
		if code, _ := post(t, base+prefix+"/create-account/game01/a%40example.com/t", "", nil); code != http.StatusNotFound {
			t.Errorf("%s in test: %d, want 404", prefix, code)
		}
	}
}

// The test client connects over loopback, which fuingod trusts by default, so
// X-Forwarded-For names the client, as it does behind Caddy.
func TestServeLimitsFailuresPerForwardedClient(t *testing.T) {
	clearEnv(t)
	t.Setenv("FUINGO_PEPPER", pepper)
	base := startServer(t, "--env", "development", "--db", ":memory:")
	u := devdata.Uploaders[0]
	for i := range 5 {
		if code, body := postUpload(t, base, u.Email, u.Game, "wrong", "turn\n", "X-Forwarded-For", "203.0.113.1"); code != http.StatusForbidden {
			t.Fatalf("failure %d: %d %s", i+1, code, body)
		}
	}
	if code, body := postUpload(t, base, u.Email, u.Game, u.Token, "turn\n", "X-Forwarded-For", "203.0.113.1"); code != http.StatusTooManyRequests {
		t.Errorf("limited client: %d %s, want 429", code, body)
	}
	if code, body := postUpload(t, base, u.Email, u.Game, u.Token, "turn\n", "X-Forwarded-For", "203.0.113.2"); code != http.StatusOK {
		t.Errorf("another client: %d %s, want 200", code, body)
	}
}

// startStoppable runs fuingod on a free port with the given shutdown timeout
// and returns its address, a function that sends the stop signal, and the
// channel that receives run's result.
func startStoppable(t *testing.T, timeout time.Duration, args ...string) (string, context.CancelFunc, <-chan error) {
	t.Helper()
	old := shutdownTimeout
	shutdownTimeout = timeout
	t.Cleanup(func() { shutdownTimeout = old })
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	addrc := make(chan net.Addr, 1)
	errc := make(chan error, 1)
	go func() {
		errc <- run(ctx, append([]string{"--addr", "127.0.0.1:0"}, args...), io.Discard, func(a net.Addr) { addrc <- a })
	}()
	select {
	case a := <-addrc:
		return a.String(), cancel, errc
	case err := <-errc:
		t.Fatalf("run: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("server did not start")
	}
	return "", nil, nil
}

// startUpload opens a connection and sends an upload request with only part
// of its body, returning the connection, its reader, and the rest of the
// body.
func startUpload(t *testing.T, addr string) (net.Conn, *bufio.Reader, []byte) {
	t.Helper()
	u := devdata.Uploaders[0]
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "turn.txt")
	io.WriteString(fw, u.Game+" 1 "+u.Email+" "+u.Token+"\nturn\n")
	mw.Close()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	// A connection can be dialed before the server accepts it. Finish one
	// request on it first, so the server is serving it.
	br := bufio.NewReader(conn)
	if _, err := io.WriteString(conn, "GET / HTTP/1.1\r\nHost: "+addr+"\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	head := "POST /upload HTTP/1.1\r\nHost: " + addr + "\r\nSec-Fetch-Site: same-origin\r\n" +
		"Content-Type: " + mw.FormDataContentType() + "\r\nContent-Length: " + strconv.Itoa(body.Len()) + "\r\n\r\n"
	half := body.Len() / 2
	if _, err := conn.Write(append([]byte(head), body.Bytes()[:half]...)); err != nil {
		t.Fatal(err)
	}
	// Give the server time to read the headers and start the handler, which
	// then waits for the rest of the body.
	time.Sleep(100 * time.Millisecond)
	return conn, br, body.Bytes()[half:]
}

// waitStopped waits until the server refuses new connections, which it does
// as soon as the shutdown starts.
func waitStopped(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			return
		}
		conn.Close()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the server still accepts connections after the stop signal")
}

// TestShutdownFinishesInFlightRequests stops the server while an upload is
// being sent; the upload completes and run reports a clean stop.
func TestShutdownFinishesInFlightRequests(t *testing.T) {
	clearEnv(t)
	t.Setenv("FUINGO_PEPPER", pepper)
	addr, stop, errc := startStoppable(t, 5*time.Second, "--env", "development", "--db", ":memory:")
	conn, br, rest := startUpload(t, addr)
	stop()
	waitStopped(t, addr)
	if _, err := conn.Write(rest); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("reading the response: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("in-flight upload: %d, want 200", resp.StatusCode)
	}
	select {
	case err := <-errc:
		if err != nil {
			t.Errorf("run = %v, want a clean stop", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return")
	}
}

// TestShutdownCutsOffStalledRequests stops the server while an upload has
// stalled; at the deadline its connection is closed and run reports it.
func TestShutdownCutsOffStalledRequests(t *testing.T) {
	clearEnv(t)
	t.Setenv("FUINGO_PEPPER", pepper)
	addr, stop, errc := startStoppable(t, 100*time.Millisecond, "--env", "development", "--db", ":memory:")
	conn, br, _ := startUpload(t, addr)
	stop()
	select {
	case err := <-errc:
		if !errors.Is(err, ErrShutdownTimeout) {
			t.Errorf("run = %v, want ErrShutdownTimeout", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return after the shutdown deadline")
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if n, err := br.Read(make([]byte, 1)); err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
		t.Errorf("stalled connection: read %d bytes, %v; want it closed", n, err)
	}
}
