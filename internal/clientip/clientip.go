// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package clientip finds the address of the client behind trusted reverse
// proxies.
//
// X-Forwarded-For is honored only when the connection's peer is a trusted
// proxy. Each proxy appends the address it received the request from, so the
// list is read from the right, skipping trusted proxies; the first address
// that is not one is the client. Addresses to its left were supplied by the
// client and are never trusted.
package clientip

import (
	"fmt"
	"net/http"
	"net/netip"
	"slices"
	"strings"

	"github.com/mdhender/fuingo/internal/cerrs"
)

const (
	ErrInvalidProxy = cerrs.Error("invalid trusted proxy")
)

// Header is the forwarding header read from trusted proxies.
const Header = "X-Forwarded-For"

// Resolver finds client addresses. The zero Resolver trusts no proxy and
// always returns the connection's peer.
type Resolver struct {
	trusted []netip.Prefix
}

// New returns a resolver that trusts proxies in the given prefixes.
func New(trusted []netip.Prefix) Resolver {
	return Resolver{trusted: slices.Clone(trusted)}
}

// ParsePrefixes parses a comma-separated list of addresses and CIDR prefixes.
// A bare address is a prefix of its full length. An empty list trusts no
// proxy.
func ParsePrefixes(s string) ([]netip.Prefix, error) {
	var prefixes []netip.Prefix
	for field := range strings.SplitSeq(s, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		var p netip.Prefix
		var err error
		if strings.Contains(field, "/") {
			p, err = netip.ParsePrefix(field)
		} else {
			var a netip.Addr
			if a, err = netip.ParseAddr(field); err == nil {
				p = netip.PrefixFrom(a, a.BitLen())
			}
		}
		if err != nil {
			return nil, fmt.Errorf("%w %q: %v", ErrInvalidProxy, field, err)
		}
		prefixes = append(prefixes, p.Masked())
	}
	return prefixes, nil
}

// Addr returns the client's address, or the zero Addr if the peer address
// cannot be parsed.
func (r Resolver) Addr(req *http.Request) netip.Addr {
	peer, err := netip.ParseAddrPort(req.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	client := normalize(peer.Addr())
	if !r.isTrusted(client) {
		return client
	}
	var hops []string
	for _, v := range req.Header.Values(Header) {
		hops = append(hops, strings.Split(v, ",")...)
	}
	for _, hop := range slices.Backward(hops) {
		a, err := netip.ParseAddr(strings.TrimSpace(hop))
		if err != nil {
			// The trusted proxy to the right forwarded this hop; stop at it.
			return client
		}
		client = normalize(a)
		if !r.isTrusted(client) {
			return client
		}
	}
	return client
}

func (r Resolver) isTrusted(a netip.Addr) bool {
	return slices.ContainsFunc(r.trusted, func(p netip.Prefix) bool { return p.Contains(a) })
}

// normalize unmaps IPv4-mapped IPv6 addresses and drops zones.
func normalize(a netip.Addr) netip.Addr {
	return a.Unmap().WithZone("")
}
