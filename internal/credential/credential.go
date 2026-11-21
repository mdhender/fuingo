// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package credential computes upload credential hashes.
package credential

import (
	"crypto/hmac"
	"crypto/sha256"
	"strconv"
	"strings"
)

// NormalizeEmail lowercases an email address. No other normalization is
// applied.
func NormalizeEmail(email string) string {
	return strings.ToLower(email)
}

// NormalizeGameCode lowercases a game code. No other normalization is
// applied.
func NormalizeGameCode(code string) string {
	return strings.ToLower(code)
}

// HeaderWord reports whether s is one or more bytes in 0x21-0x7E, as every
// word of a turn file's header line must be. Game codes, emails, and tokens
// must be header words, so that an upload can name them.
func HeaderWord(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		if s[i] < 0x21 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// Hash returns HMAC-SHA256(pepper, lowercased-email + "\x00" + game-id +
// "\x00" + token), where game-id is the game's integer ID in decimal. The ID,
// not the game code, keeps credentials valid when a game's code changes.
// Hash lowercases the email itself, so callers may pass it as entered.
func Hash(pepper, email string, gameID int64, token string) []byte {
	mac := hmac.New(sha256.New, []byte(pepper))
	mac.Write([]byte(NormalizeEmail(email)))
	mac.Write([]byte{0})
	mac.Write([]byte(strconv.FormatInt(gameID, 10)))
	mac.Write([]byte{0})
	mac.Write([]byte(token))
	return mac.Sum(nil)
}

// Equal compares two hashes in constant time.
func Equal(a, b []byte) bool {
	return hmac.Equal(a, b)
}
