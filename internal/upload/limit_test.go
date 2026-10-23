// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package upload_test

import (
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mdhender/fuingo/internal/clientip"
	"github.com/mdhender/fuingo/internal/ratelimit"
	"github.com/mdhender/fuingo/internal/upload"
)

const msgTooManyFailed = "Too many failed uploads from your address."

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// newLimitedFixture returns a fixture with the default failure limit on a
// fake clock, trusting the given proxies.
func newLimitedFixture(t *testing.T, trusted ...string) (*fixture, *clock) {
	t.Helper()
	f := newFixture(t)
	c := &clock{now: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	prefixes, err := clientip.ParsePrefixes(strings.Join(trusted, ","))
	if err != nil {
		t.Fatal(err)
	}
	h, err := upload.New(upload.Config{
		Store:    f.store,
		Pepper:   pepper,
		Logger:   slog.New(slog.DiscardHandler),
		Clients:  clientip.New(prefixes),
		Failures: ratelimit.New(upload.FailureBurst, upload.FailureInterval, c.Now),
	})
	if err != nil {
		t.Fatal(err)
	}
	f.handler = h
	return f, c
}

// from uploads file from the peer remote, with optional X-Forwarded-For
// headers.
func (f *fixture) from(remote string, file []byte, forwardedFor ...string) response {
	f.t.Helper()
	r := request(f.t, file)
	r.RemoteAddr = remote
	for _, v := range forwardedFor {
		r.Header.Add("X-Forwarded-For", v)
	}
	return f.do(r)
}

func wrongToken() []byte {
	return []byte(catbird.Game + " 1 " + catbird.Email + " 8eb87beb-0869-4815-8ce8-afeef14da57f\norders\n")
}

func expectStatus(t *testing.T, name string, r response, status int) {
	t.Helper()
	if r.status != status {
		t.Errorf("%s: status %d, want %d: %s", name, r.status, status, r.body)
	}
}

func expectLimited(t *testing.T, name string, r response, retryAfter string) {
	t.Helper()
	expectStatus(t, name, r, http.StatusTooManyRequests)
	if !strings.Contains(r.body, msgTooManyFailed) || !strings.Contains(r.body, "in "+retryAfter+" seconds") {
		t.Errorf("%s: body %q, want the limit message with %s seconds", name, r.body, retryAfter)
	}
	if got := r.header.Get("Retry-After"); got != retryAfter {
		t.Errorf("%s: Retry-After %q, want %q", name, got, retryAfter)
	}
	if strings.Contains(r.body, catbird.Token) {
		t.Errorf("%s: response contains the token", name)
	}
}

func TestFailureLimit(t *testing.T) {
	t.Parallel()
	f, c := newLimitedFixture(t)
	const guesser, other = "198.51.100.7:4321", "198.51.100.8:4321"
	for range upload.FailureBurst {
		expectStatus(t, "failure", f.from(guesser, wrongToken()), http.StatusForbidden)
	}
	expectLimited(t, "over the limit", f.from(guesser, wrongToken()), "60")
	expectLimited(t, "valid credentials while limited", f.from(guesser, turnFile(catbird, "orders\n")), "60")
	if n := len(f.uploads()); n != 0 {
		t.Errorf("a limited client recorded %d uploads", n)
	}
	// Format errors come before authorization and are still reported.
	expectStatus(t, "header error while limited", f.from(guesser, []byte("not a header\norders\n")), http.StatusBadRequest)

	// The same uploader is not locked out from another address.
	expectStatus(t, "same email from another address", f.from(other, turnFile(catbird, "orders\n")), http.StatusOK)

	c.Advance(30 * time.Second)
	expectLimited(t, "after 30 seconds", f.from(guesser, wrongToken()), "30")
	c.Advance(30 * time.Second)
	expectStatus(t, "valid after a minute", f.from(guesser, turnFile(catbird, "more orders\n")), http.StatusOK)
	expectStatus(t, "one regained failure", f.from(guesser, wrongToken()), http.StatusForbidden)
	expectLimited(t, "limited again", f.from(guesser, wrongToken()), "60")
}

func TestSuccessesAndFormatErrorsDoNotCount(t *testing.T) {
	t.Parallel()
	f, _ := newLimitedFixture(t)
	const client = "198.51.100.7:4321"
	for range 3 * upload.FailureBurst {
		expectStatus(t, "success", f.from(client, turnFile(catbird, "orders\n")), http.StatusOK)
		expectStatus(t, "header error", f.from(client, []byte(catbird.Game+" 1 "+catbird.Email+"\norders\n")), http.StatusBadRequest)
		expectStatus(t, "empty body", f.from(client, []byte(headerLine(catbird))), http.StatusBadRequest)
	}
	for range upload.FailureBurst {
		expectStatus(t, "failure", f.from(client, wrongToken()), http.StatusForbidden)
	}
	expectLimited(t, "over the limit", f.from(client, wrongToken()), "60")
}

func TestConcurrentFailures(t *testing.T) {
	t.Parallel()
	f, _ := newLimitedFixture(t)
	const n = 40
	statuses := make(chan int, n)
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() { statuses <- f.from("198.51.100.7:4321", wrongToken()).status })
	}
	wg.Wait()
	close(statuses)
	counts := map[int]int{}
	for s := range statuses {
		counts[s]++
	}
	if counts[http.StatusForbidden] != upload.FailureBurst || counts[http.StatusTooManyRequests] != n-upload.FailureBurst {
		t.Errorf("statuses = %v, want %d 403s and %d 429s", counts, upload.FailureBurst, n-upload.FailureBurst)
	}
}

func TestIPv6ClientsAreGroupedBy64(t *testing.T) {
	t.Parallel()
	f, _ := newLimitedFixture(t)
	for range upload.FailureBurst {
		expectStatus(t, "failure", f.from("[2001:db8:1:2::1]:4321", wrongToken()), http.StatusForbidden)
	}
	expectLimited(t, "same /64", f.from("[2001:db8:1:2:ffff:ffff:ffff:ffff]:4321", wrongToken()), "60")
	expectStatus(t, "another /64", f.from("[2001:db8:1:3::1]:4321", wrongToken()), http.StatusForbidden)
}

func TestForwardedForFromUntrustedPeerIsIgnored(t *testing.T) {
	t.Parallel()
	f, _ := newLimitedFixture(t)
	for i := range upload.FailureBurst {
		forged := netip.AddrFrom4([4]byte{203, 0, 113, byte(i)}).String()
		expectStatus(t, "failure", f.from("198.51.100.7:4321", wrongToken(), forged), http.StatusForbidden)
	}
	expectLimited(t, "new forged address", f.from("198.51.100.7:4321", wrongToken(), "203.0.113.200"), "60")
}

func TestForwardedForBehindTrustedProxies(t *testing.T) {
	t.Parallel()
	f, _ := newLimitedFixture(t, "127.0.0.0/8", "10.0.0.0/8")
	const proxy = "127.0.0.1:50000"
	for range upload.FailureBurst {
		expectStatus(t, "failure", f.from(proxy, wrongToken(), "203.0.113.1"), http.StatusForbidden)
	}
	expectLimited(t, "same client", f.from(proxy, wrongToken(), "203.0.113.1"), "60")
	expectLimited(t, "client-supplied address to the left", f.from(proxy, wrongToken(), "192.0.2.99, 203.0.113.1"), "60")
	expectLimited(t, "through a second trusted proxy", f.from(proxy, wrongToken(), "203.0.113.1, 10.1.2.3"), "60")
	expectLimited(t, "in separate headers", f.from(proxy, wrongToken(), "203.0.113.1", "10.1.2.3"), "60")
	expectStatus(t, "another client", f.from(proxy, wrongToken(), "203.0.113.2"), http.StatusForbidden)
	expectStatus(t, "valid upload from another client", f.from(proxy, turnFile(catbird, "orders\n"), "203.0.113.3"), http.StatusOK)
}
