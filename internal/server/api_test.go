// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/mdhender/fuingo/internal/api"
	"github.com/mdhender/fuingo/internal/devdata"
	"github.com/mdhender/fuingo/internal/server"
)

var acct01 = devdata.Accounts[0]

func apiLogin(email, password string) *http.Request {
	body, _ := json.Marshal(api.LoginRequest{Email: email, Password: password})
	r := httptest.NewRequest(http.MethodPost, publicOrigin+"/api/login", strings.NewReader(string(body)))
	r.Header.Set("Content-Type", "application/json")
	return r
}

func apiList(token string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, publicOrigin+"/api/games/game01/uploads", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	return r
}

func tokenFrom(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var resp api.LoginResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || resp.Token == "" {
		t.Fatalf("login response %q: %v", w.Body, err)
	}
	return resp.Token
}

func TestAPILoginFromCommandLineClient(t *testing.T) {
	t.Parallel()
	h, _ := newServer(t, "production")
	// A non-browser client sends neither Origin nor Sec-Fetch-Site.
	w := serve(h, apiLogin(acct01.Email, acct01.Password))
	if w.Code != http.StatusOK {
		t.Fatalf("login: %d %s", w.Code, w.Body)
	}
	if w := serve(h, apiList(tokenFrom(t, w))); w.Code != http.StatusOK {
		t.Errorf("list: %d %s", w.Code, w.Body)
	}
}

func TestCrossOriginAPILoginIsRejected(t *testing.T) {
	t.Parallel()
	h, _ := newServer(t, "production")
	r := apiLogin(acct01.Email, acct01.Password)
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	if w := serve(h, r); w.Code != http.StatusForbidden {
		t.Errorf("cross-site login: %d", w.Code)
	}
	r = apiLogin(acct01.Email, acct01.Password)
	r.Header.Set("Origin", "https://evil.localhost:8443")
	if w := serve(h, r); w.Code != http.StatusForbidden {
		t.Errorf("cross-origin login: %d", w.Code)
	}
}

func TestCredentialKindsAreSeparate(t *testing.T) {
	t.Parallel()
	h, _ := newServer(t, "production")
	// An upload account cannot log in to the API with its token.
	if w := serve(h, apiLogin(catbird.Email, catbird.Token)); w.Code != http.StatusUnauthorized {
		t.Errorf("API login with upload token: %d", w.Code)
	}
	// An API password does not authorize an upload.
	if w := serve(h, uploadRequest(t, acct01.Email, "game01", acct01.Password, "turn\n")); w.Code != http.StatusForbidden {
		t.Errorf("upload with API password: %d", w.Code)
	}
}

func apiHelper(prefix, name, email string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, publicOrigin+prefix+"/"+name+"/"+url.PathEscape(email), nil)
	r.Header.Set("Origin", publicOrigin)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	return r
}

func TestAPIHelperPrefixIsolation(t *testing.T) {
	t.Parallel()
	for env, available := range map[string]string{"development": "/__development", "agent": "/__agent", "test": "", "production": ""} {
		h, _ := newServer(t, env)
		for i, prefix := range []string{"/__development", "/__agent"} {
			email := "api" + string(rune('a'+i)) + "@example.com"
			for _, name := range []string{"create-api-account", "log-me-in"} {
				w := serve(h, apiHelper(prefix, name, email))
				want := http.StatusNotFound
				if prefix == available && name == "create-api-account" {
					want = http.StatusCreated
				} else if prefix == available {
					want = http.StatusOK
				}
				if w.Code != want {
					t.Errorf("%s: POST %s/%s: status %d, want %d", env, prefix, name, w.Code, want)
				}
			}
		}
	}
}

func TestCreateAPIAccountHelper(t *testing.T) {
	t.Parallel()
	for _, env := range []string{"development", "agent"} {
		h, s := newServer(t, env)
		prefix := server.HelperPrefix(env)
		w := serve(h, apiHelper(prefix, "create-api-account", "New+API@Example.com"))
		if w.Code != http.StatusCreated {
			t.Fatalf("%s: create: %d %s", env, w.Code, w.Body)
		}
		password := strings.TrimSpace(w.Body.String())
		before, _, _ := s.FindAccount(t.Context(), "new+api@example.com")
		// The password logs in.
		if w := serve(h, apiLogin("new+api@example.com", password)); w.Code != http.StatusOK {
			t.Errorf("%s: login with helper password: %d", env, w.Code)
		}
		// A duplicate is refused without changing the account.
		if w := serve(h, apiHelper(prefix, "create-api-account", "new+api@example.com")); w.Code != http.StatusConflict {
			t.Errorf("%s: duplicate: %d", env, w.Code)
		}
		after, _, _ := s.FindAccount(t.Context(), "new+api@example.com")
		if string(before.HashedSecret) != string(after.HashedSecret) {
			t.Errorf("%s: duplicate changed the password", env)
		}
		// The account manages no games.
		token := tokenFrom(t, serve(h, apiLogin("new+api@example.com", password)))
		if w := serve(h, apiList(token)); w.Code != http.StatusNotFound {
			t.Errorf("%s: list game01: %d", env, w.Code)
		}
	}
}

func TestLogMeInHelper(t *testing.T) {
	t.Parallel()
	h, s := newServer(t, "agent")
	w := serve(h, apiHelper("/__agent", "log-me-in", strings.ToUpper(acct01.Email)))
	if w.Code != http.StatusOK || w.Header().Get("Set-Cookie") != "" {
		t.Fatalf("log-me-in: %d %v %s", w.Code, w.Header(), w.Body)
	}
	if w := serve(h, apiList(tokenFrom(t, w))); w.Code != http.StatusOK {
		t.Errorf("list with log-me-in token: %d", w.Code)
	}
	if w := serve(h, apiHelper("/__agent", "log-me-in", "nobody@example.com")); w.Code != http.StatusNotFound {
		t.Errorf("missing account: %d", w.Code)
	}
	if w := serve(h, apiHelper("/__agent", "log-me-in", catbird.Email)); w.Code != http.StatusForbidden {
		t.Errorf("account without password: %d", w.Code)
	}
	if err := s.SetAccountActive(t.Context(), acct01.Email, false); err != nil {
		t.Fatal(err)
	}
	if w := serve(h, apiHelper("/__agent", "log-me-in", acct01.Email)); w.Code != http.StatusForbidden {
		t.Errorf("disabled account: %d", w.Code)
	}
}

func TestAPIHelpersRequirePostAndSameOrigin(t *testing.T) {
	t.Parallel()
	h, s := newServer(t, "development")
	for _, name := range []string{"create-api-account", "log-me-in"} {
		for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
			r := apiHelper("/__development", name, "get@example.com")
			r.Method = method
			if w := serve(h, r); w.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s %s: status %d, want 405", method, name, w.Code)
			}
		}
		r := apiHelper("/__development", name, "csrf@example.com")
		r.Header.Set("Sec-Fetch-Site", "cross-site")
		if w := serve(h, r); w.Code != http.StatusForbidden {
			t.Errorf("cross-site %s: %d", name, w.Code)
		}
	}
	for _, email := range []string{"get@example.com", "csrf@example.com"} {
		if _, found, _ := s.FindAccount(t.Context(), email); found {
			t.Errorf("%s was created", email)
		}
	}
}
