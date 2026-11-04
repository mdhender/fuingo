// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package olympia

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/mdhender/fuingo/internal/api"
	"github.com/mdhender/fuingo/internal/cerrs"
	"github.com/mdhender/fuingo/internal/upload"
)

const (
	ErrInsecureServer = cerrs.Error("the server URL must use https (http is allowed only for localhost and loopback addresses)")
	ErrNotLoggedIn    = cerrs.Error("not logged in")
)

// maxListSize bounds an upload list response.
const maxListSize = 16 << 20

// Client calls fuingo's /api/ routes. It holds the bearer token from Login
// in memory only.
type Client struct {
	base  *url.URL
	http  *http.Client
	token string
}

// NewClient returns a client for the fuingo server at serverURL, such as
// https://fuingo.example.com. If hc is nil, it uses a client with a one
// minute timeout.
func NewClient(serverURL string, hc *http.Client) (*Client, error) {
	u, err := url.Parse(serverURL)
	if err != nil {
		return nil, fmt.Errorf("server URL: %w", err)
	}
	if u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("server URL %q: want scheme://host[:port][/path]", serverURL)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !isLoopback(u.Hostname()) {
			return nil, ErrInsecureServer
		}
	default:
		return nil, ErrInsecureServer
	}
	if hc == nil {
		hc = &http.Client{Timeout: time.Minute}
	}
	return &Client{base: u, http: hc}, nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Server returns the server URL, without credentials.
func (c *Client) Server() string {
	return c.base.String()
}

func (c *Client) endpoint(segments ...string) string {
	return c.base.JoinPath(segments...).String()
}

// Login logs in with an account's email and API password.
func (c *Client) Login(ctx context.Context, email, password string) error {
	body, err := json.Marshal(api.LoginRequest{Email: email, Password: password})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint("api", "login"), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("login: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("login: %w", responseError(resp))
	}
	var lr api.LoginResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&lr); err != nil {
		return fmt.Errorf("login: %w", err)
	} else if lr.Token == "" {
		return fmt.Errorf("login: the server returned no token")
	}
	c.token = lr.Token
	return nil
}

// Uploads lists the uploads to the game for the turn, oldest first.
func (c *Client) Uploads(ctx context.Context, game, turn string) ([]api.Upload, error) {
	u := c.base.JoinPath("api", "games", game, "uploads")
	u.RawQuery = url.Values{"turn": {turn}}.Encode()
	resp, err := c.get(ctx, u.String())
	if err != nil {
		return nil, fmt.Errorf("list uploads: %w", err)
	}
	defer resp.Body.Close()
	var list api.UploadList
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxListSize)).Decode(&list); err != nil {
		return nil, fmt.Errorf("list uploads: %w", err)
	}
	return list.Uploads, nil
}

// Body downloads an upload's contents. It returns the contents and the
// content hash the server reported; the caller verifies them.
func (c *Client) Body(ctx context.Context, game string, id int64) ([]byte, string, error) {
	resp, err := c.get(ctx, c.endpoint("api", "games", game, "uploads", strconv.FormatInt(id, 10), "body"))
	if err != nil {
		return nil, "", fmt.Errorf("download upload %d: %w", id, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, upload.MaxFileSize+1))
	if err != nil {
		return nil, "", fmt.Errorf("download upload %d: %w", id, err)
	}
	return body, resp.Header.Get(api.HeaderContentSHA256), nil
}

// get sends an authenticated GET and returns a 200 response.
func (c *Client) get(ctx context.Context, u string) (*http.Response, error) {
	if c.token == "" {
		return nil, ErrNotLoggedIn
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, responseError(resp)
	}
	return resp, nil
}

// responseError describes a failed response with the server's message.
func responseError(resp *http.Response) error {
	var e api.Error
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if json.Unmarshal(data, &e) == nil && e.Error != "" {
		return fmt.Errorf("%s: %s", resp.Status, e.Error)
	}
	return fmt.Errorf("%s", resp.Status)
}
