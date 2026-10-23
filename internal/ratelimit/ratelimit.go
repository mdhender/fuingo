// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package ratelimit limits events per client address with token buckets.
//
// Each client starts with burst tokens and regains one per interval, up to
// burst. IPv4 clients are keyed by address and IPv6 clients by their /64
// prefix, since one IPv6 subscriber usually holds a whole /64. Buckets are
// kept in memory; idle full buckets are dropped. At most MaxClients buckets
// are kept: a new client beyond that replaces the bucket that has refilled
// the most, which is the client least recently limited.
package ratelimit

import (
	"maps"
	"net/netip"
	"sync"
	"time"
)

// MaxClients is the most clients a Limiter tracks at once.
const MaxClients = 16 << 10

// Limiter is a set of per-client token buckets. It is safe for concurrent use.
type Limiter struct {
	burst    float64
	interval time.Duration
	now      func() time.Time

	mu        sync.Mutex
	buckets   map[netip.Prefix]*bucket
	lastSweep time.Time
}

type bucket struct {
	tokens float64
	at     time.Time
}

// New returns a limiter allowing burst events per client, then one more per
// interval. If now is nil, it uses time.Now.
func New(burst int, interval time.Duration, now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	return &Limiter{
		burst:    float64(max(burst, 1)),
		interval: max(interval, time.Nanosecond),
		now:      now,
		buckets:  map[netip.Prefix]*bucket{},
	}
}

// Take takes one token for a. If none is left, it returns false and how long
// until one is.
func (l *Limiter) Take(a netip.Addr) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.sweep(now, false)
	k := key(a)
	b := l.buckets[k]
	if b == nil {
		if len(l.buckets) >= MaxClients {
			l.evict(now)
		}
		b = &bucket{tokens: l.burst, at: now}
		l.buckets[k] = b
	}
	l.refill(b, now)
	if b.tokens < 1 {
		return false, time.Duration((1 - b.tokens) * float64(l.interval))
	}
	b.tokens--
	return true, 0
}

// Return gives back a token taken for a, for an event that turned out not to
// count.
func (l *Limiter) Return(a netip.Addr) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if b := l.buckets[key(a)]; b != nil {
		l.refill(b, l.now())
		b.tokens = min(b.tokens+1, l.burst)
	}
}

func (l *Limiter) refill(b *bucket, now time.Time) {
	if elapsed := now.Sub(b.at); elapsed > 0 {
		b.tokens = min(b.tokens+float64(elapsed)/float64(l.interval), l.burst)
		b.at = now
	}
}

// sweep drops buckets that have refilled, at most once per refill period
// unless forced.
func (l *Limiter) sweep(now time.Time, force bool) {
	period := time.Duration(l.burst * float64(l.interval))
	if !force && now.Sub(l.lastSweep) < period {
		return
	}
	l.lastSweep = now
	maps.DeleteFunc(l.buckets, func(_ netip.Prefix, b *bucket) bool {
		l.refill(b, now)
		return b.tokens >= l.burst
	})
}

// evict makes room for one bucket: it sweeps, and if every bucket is still
// short of tokens, drops the one with the most.
func (l *Limiter) evict(now time.Time) {
	l.sweep(now, true)
	if len(l.buckets) < MaxClients {
		return
	}
	var victim netip.Prefix
	most := -1.0
	for k, b := range l.buckets {
		l.refill(b, now)
		if b.tokens > most {
			victim, most = k, b.tokens
		}
	}
	delete(l.buckets, victim)
}

// key groups a with the other addresses of its client.
func key(a netip.Addr) netip.Prefix {
	a = a.Unmap().WithZone("")
	bits := 64
	if a.Is4() {
		bits = 32
	}
	p, err := a.Prefix(bits)
	if err != nil {
		return netip.Prefix{} // invalid addresses share one bucket
	}
	return p
}
