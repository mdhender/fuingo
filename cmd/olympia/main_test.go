// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mdhender/fuingo"
	"github.com/mdhender/fuingo/internal/devdata"
	"github.com/mdhender/fuingo/internal/olympia"
	"github.com/mdhender/fuingo/internal/server"
	"github.com/mdhender/fuingo/internal/store"
)

const pepper = "test-pepper"

var (
	acct01  = devdata.Accounts[0]
	catbird = devdata.Uploaders[0]
)

// newServer runs fuingo with the default accounts and one upload by
// catbird for game01 turn 7.
func newServer(t *testing.T) string {
	t.Helper()
	s, err := store.OpenPrivateMemory(t.Context(), t.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := devdata.Seed(t.Context(), s, "development", pepper); err != nil {
		t.Fatal(err)
	}
	h, err := server.New(server.Config{Env: "production", Store: s, Pepper: pepper, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "turn.txt")
	io.WriteString(fw, "game01 7 "+catbird.Email+" "+catbird.Token+"\nbegin 1 \"pw\"\nend\n")
	mw.Close()
	resp, err := http.Post(srv.URL+"/upload", mw.FormDataContentType(), &body)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("upload: %v %v", resp, err)
	}
	resp.Body.Close()
	return srv.URL
}

func dirs(t *testing.T) (spool, batch string) {
	t.Helper()
	dir := t.TempDir()
	spool, batch = filepath.Join(dir, "spool"), filepath.Join(dir, "batch")
	os.Mkdir(spool, 0o755)
	os.Mkdir(batch, 0o755)
	return spool, batch
}

func runOlympia(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := run(t.Context(), args, &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

func TestVersion(t *testing.T) {
	out, _, err := runOlympia(t, "version")
	if err != nil || out != fuingo.Version().String()+"\n" {
		t.Errorf("version: %q %v", out, err)
	}
}

func TestStageCommand(t *testing.T) {
	url := newServer(t)
	spool, batch := dirs(t)
	t.Setenv("OLYMPIA_PASSWORD", acct01.Password)
	t.Setenv("OLYMPIA_SERVER", url)
	t.Setenv("OLYMPIA_EMAIL", strings.ToUpper(acct01.Email))
	t.Setenv("OLYMPIA_SPOOL", spool)
	t.Setenv("OLYMPIA_BATCH", batch)
	out, _, err := runOlympia(t, "--env", "test", "stage", "--game", "GAME01", "--turn", "7")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "staged 1 files for game01 turn 7 from the downloaded batch") || !strings.Contains(out, "m7-0001-catbird-example-com-1") {
		t.Errorf("output = %q", out)
	}
	if strings.Contains(out, acct01.Password) || strings.Contains(out, catbird.Token) {
		t.Errorf("output contains a credential: %q", out)
	}
	data, err := os.ReadFile(filepath.Join(spool, "m7-0001-catbird-example-com-1"))
	if err != nil || !strings.HasPrefix(string(data), "From catbird@example.com ") || !strings.HasSuffix(string(data), "\n\nbegin 1 \"pw\"\nend\n") {
		t.Errorf("spool file = %q, %v", data, err)
	}

	// The engine has not read the file yet, so staging again is refused.
	if _, _, err := runOlympia(t, "--env", "test", "stage", "--game", "game01", "--turn", "7"); !errors.Is(err, olympia.ErrSpoolNotEmpty) {
		t.Errorf("second stage: err = %v", err)
	}
	out, _, err = runOlympia(t, "--env", "test", "stage", "--game", "game01", "--turn", "7", "--force")
	if err != nil || !strings.Contains(out, "reused batch") {
		t.Errorf("forced stage: %q %v", out, err)
	}
}

func TestStageCommandLineOnlyFlags(t *testing.T) {
	url := newServer(t)
	spool, batch := dirs(t)
	t.Setenv("OLYMPIA_PASSWORD", acct01.Password)
	t.Setenv("OLYMPIA_GAME", "game01")
	t.Setenv("OLYMPIA_TURN", "7")
	t.Setenv("OLYMPIA_FORCE", "true")
	args := []string{"--env", "test", "stage", "--server", url, "--email", acct01.Email, "--spool", spool, "--batch", batch}
	if _, _, err := runOlympia(t, args...); err == nil || !strings.Contains(err.Error(), "is required") {
		t.Errorf("game and turn from the environment: err = %v", err)
	}
	os.WriteFile(filepath.Join(spool, "m-old"), nil, 0o644)
	if _, _, err := runOlympia(t, append(args, "--game", "game01", "--turn", "7")...); !errors.Is(err, olympia.ErrSpoolNotEmpty) {
		t.Errorf("OLYMPIA_FORCE was honored: err = %v", err)
	}
}

func TestStageRequiresPassword(t *testing.T) {
	spool, batch := dirs(t)
	t.Setenv("OLYMPIA_PASSWORD", "")
	_, _, err := runOlympia(t, "--env", "test", "stage", "--server", "https://fuingo.example", "--email", acct01.Email, "--spool", spool, "--batch", batch, "--game", "game01", "--turn", "1")
	if !errors.Is(err, ErrMissingPassword) {
		t.Errorf("err = %v", err)
	}
}

func TestStageRefusesPlainHTTP(t *testing.T) {
	spool, batch := dirs(t)
	t.Setenv("OLYMPIA_PASSWORD", "secret")
	_, _, err := runOlympia(t, "--env", "test", "stage", "--server", "http://fuingo.example", "--email", acct01.Email, "--spool", spool, "--batch", batch, "--game", "game01", "--turn", "1")
	if !errors.Is(err, olympia.ErrInsecureServer) {
		t.Errorf("err = %v", err)
	}
}
