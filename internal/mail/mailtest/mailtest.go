// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package mailtest provides a mail.Sender for tests that records messages
// instead of sending them.
package mailtest

import (
	"context"
	"net/url"
	"strings"
	"sync"

	"github.com/mdhender/fuingo/internal/mail"
)

// Recorder is a mail.Sender that keeps every message it is given.
type Recorder struct {
	mu       sync.Mutex
	messages []mail.Message
	err      error
}

// Send records the message, or fails with the error set by Fail.
func (r *Recorder) Send(_ context.Context, m mail.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.messages = append(r.messages, m)
	return nil
}

// Fail makes every later Send fail with err; nil makes them succeed again.
func (r *Recorder) Fail(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.err = err
}

// Messages returns the messages recorded so far.
func (r *Recorder) Messages() []mail.Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]mail.Message(nil), r.messages...)
}

// Reset forgets the recorded messages and any failure.
func (r *Recorder) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.messages, r.err = nil, nil
}

// Link returns the first URL in the message's text that holds a token, and
// the token. Both are empty if there is none.
func Link(m mail.Message) (link, token string) {
	for _, word := range strings.Fields(m.Text) {
		if u, err := url.Parse(word); err == nil && u.Query().Get("token") != "" {
			return word, u.Query().Get("token")
		}
	}
	return "", ""
}
