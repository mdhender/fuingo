// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package ratelimit_test

import (
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mdhender/fuingo/internal/ratelimit"
)

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

var (
	a = netip.MustParseAddr("198.51.100.7")
	b = netip.MustParseAddr("198.51.100.8")
)

func TestBurstAndRefill(t *testing.T) {
	c := &clock{now: time.Unix(1_000_000, 0)}
	l := ratelimit.New(3, time.Minute, c.Now)
	for i := range 3 {
		if ok, _ := l.Take(a); !ok {
			t.Fatalf("take %d refused", i+1)
		}
	}
	if ok, wait := l.Take(a); ok || wait != time.Minute {
		t.Errorf("over the burst: %v, %v; want refused for a minute", ok, wait)
	}
	if ok, _ := l.Take(b); !ok {
		t.Errorf("another client was refused")
	}
	c.now = c.now.Add(45 * time.Second)
	if ok, wait := l.Take(a); ok || wait != 15*time.Second {
		t.Errorf("after 45s: %v, %v; want refused for 15s", ok, wait)
	}
	c.now = c.now.Add(15 * time.Second)
	if ok, _ := l.Take(a); !ok {
		t.Errorf("after a minute: refused")
	}
	if ok, _ := l.Take(a); ok {
		t.Errorf("only one token should have refilled")
	}
	c.now = c.now.Add(time.Hour)
	for i := range 3 {
		if ok, _ := l.Take(a); !ok {
			t.Errorf("after an hour, take %d refused", i+1)
		}
	}
	if ok, _ := l.Take(a); ok {
		t.Errorf("refill exceeded the burst")
	}
}

func TestReturn(t *testing.T) {
	c := &clock{now: time.Unix(1_000_000, 0)}
	l := ratelimit.New(2, time.Minute, c.Now)
	for range 10 {
		if ok, _ := l.Take(a); !ok {
			t.Fatal("returned tokens were not restored")
		}
		l.Return(a)
	}
	l.Return(a) // never above the burst
	l.Return(b) // unknown client: no effect
	for range 2 {
		l.Take(a)
	}
	if ok, _ := l.Take(a); ok {
		t.Errorf("Return raised the bucket above the burst")
	}
}

func TestKeys(t *testing.T) {
	c := &clock{now: time.Unix(1_000_000, 0)}
	l := ratelimit.New(1, time.Minute, c.Now)
	l.Take(netip.MustParseAddr("2001:db8:1:2::1"))
	if ok, _ := l.Take(netip.MustParseAddr("2001:db8:1:2:aaaa::9")); ok {
		t.Errorf("same IPv6 /64 is a separate client")
	}
	if ok, _ := l.Take(netip.MustParseAddr("2001:db8:1:3::1")); !ok {
		t.Errorf("another IPv6 /64 was refused")
	}
	l.Take(a)
	if ok, _ := l.Take(netip.MustParseAddr("::ffff:198.51.100.7")); ok {
		t.Errorf("IPv4-mapped address is a separate client")
	}
}

func TestConcurrentTakes(t *testing.T) {
	l := ratelimit.New(5, time.Hour, nil)
	var taken atomic.Int64
	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() {
			if ok, _ := l.Take(a); ok {
				taken.Add(1)
			}
		})
	}
	wg.Wait()
	if n := taken.Load(); n != 5 {
		t.Errorf("%d concurrent takes succeeded, want 5", n)
	}
}

func TestSweepKeepsLimitedClients(t *testing.T) {
	c := &clock{now: time.Unix(1_000_000, 0)}
	l := ratelimit.New(2, time.Minute, c.Now)
	l.Take(b) // sweeps; the next sweep is two minutes later
	c.now = c.now.Add(time.Minute)
	l.Take(a)
	l.Take(a)
	c.now = c.now.Add(time.Minute)
	l.Take(b) // sweeps while a has refilled only one token
	if ok, _ := l.Take(a); !ok {
		t.Fatal("refilled token refused")
	}
	if ok, _ := l.Take(a); ok {
		t.Errorf("sweep dropped a client that had not refilled")
	}
}
