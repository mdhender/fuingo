// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package clientip_test

import (
	"errors"
	"net/http/httptest"
	"net/netip"
	"slices"
	"testing"

	"github.com/mdhender/fuingo/internal/clientip"
)

func TestParsePrefixes(t *testing.T) {
	got, err := clientip.ParsePrefixes(" 127.0.0.0/8, ::1 ,10.1.2.3,, 2001:db8::1/48 ")
	if err != nil {
		t.Fatal(err)
	}
	want := []netip.Prefix{
		netip.MustParsePrefix("127.0.0.0/8"),
		netip.MustParsePrefix("::1/128"),
		netip.MustParsePrefix("10.1.2.3/32"),
		netip.MustParsePrefix("2001:db8::/48"),
	}
	if !slices.Equal(got, want) {
		t.Errorf("ParsePrefixes = %v, want %v", got, want)
	}
	if got, err := clientip.ParsePrefixes(""); err != nil || len(got) != 0 {
		t.Errorf("empty list = %v, %v; want none", got, err)
	}
	for _, bad := range []string{"localhost", "10.0.0.0/33", "10.0.0.1:80", "127.0.0.1,nope"} {
		if _, err := clientip.ParsePrefixes(bad); !errors.Is(err, clientip.ErrInvalidProxy) {
			t.Errorf("ParsePrefixes(%q) = %v, want ErrInvalidProxy", bad, err)
		}
	}
}

func TestAddr(t *testing.T) {
	trusted, err := clientip.ParsePrefixes("127.0.0.0/8,::1,10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	r := clientip.New(trusted)
	cases := []struct {
		name    string
		remote  string
		headers []string
		want    string
	}{
		{"untrusted peer, no header", "198.51.100.7:1234", nil, "198.51.100.7"},
		{"untrusted peer ignores header", "198.51.100.7:1234", []string{"203.0.113.1"}, "198.51.100.7"},
		{"trusted peer, no header", "127.0.0.1:1234", nil, "127.0.0.1"},
		{"trusted peer", "127.0.0.1:1234", []string{"203.0.113.1"}, "203.0.113.1"},
		{"rightmost untrusted", "127.0.0.1:1234", []string{"192.0.2.9, 203.0.113.1"}, "203.0.113.1"},
		{"skips trusted hops", "127.0.0.1:1234", []string{"192.0.2.9, 203.0.113.1, 10.0.0.2"}, "203.0.113.1"},
		{"several headers", "127.0.0.1:1234", []string{"192.0.2.9", "203.0.113.1", "10.0.0.2"}, "203.0.113.1"},
		{"all trusted", "127.0.0.1:1234", []string{"10.0.0.3, 10.0.0.2"}, "10.0.0.3"},
		{"malformed hop", "127.0.0.1:1234", []string{"203.0.113.1, garbage, 10.0.0.2"}, "10.0.0.2"},
		{"malformed last hop", "127.0.0.1:1234", []string{"203.0.113.1, garbage"}, "127.0.0.1"},
		{"IPv6 peer", "[::1]:1234", []string{"2001:db8::7"}, "2001:db8::7"},
		{"IPv4-mapped peer", "[::ffff:127.0.0.1]:1234", []string{"::ffff:203.0.113.1"}, "203.0.113.1"},
		{"zone dropped", "[fe80::1%en0]:1234", nil, "fe80::1"},
	}
	for _, c := range cases {
		req := httptest.NewRequest("POST", "/upload", nil)
		req.RemoteAddr = c.remote
		for _, h := range c.headers {
			req.Header.Add(clientip.Header, h)
		}
		if got := r.Addr(req); got != netip.MustParseAddr(c.want) {
			t.Errorf("%s: Addr = %v, want %s", c.name, got, c.want)
		}
	}

	req := httptest.NewRequest("POST", "/upload", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set(clientip.Header, "203.0.113.1")
	if got := (clientip.Resolver{}).Addr(req); got != netip.MustParseAddr("127.0.0.1") {
		t.Errorf("zero Resolver: Addr = %v, want the peer", got)
	}
	req.RemoteAddr = "not an address"
	if got := r.Addr(req); got.IsValid() {
		t.Errorf("unparsable peer: Addr = %v, want the zero Addr", got)
	}
}
