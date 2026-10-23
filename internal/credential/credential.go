// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package credential computes upload credential hashes.
package credential

import (
	"crypto/hmac"
	"crypto/sha256"
	"strings"
)

// NormalizeEmail lowercases an email address. No other normalization is
// applied.
func NormalizeEmail(email string) string {
	return strings.ToLower(email)
}

// Hash returns HMAC-SHA256(pepper, lowercased-email + "\x00" + token).
// It lowercases the email itself, so callers may pass it as entered.
func Hash(pepper, email, token string) []byte {
	mac := hmac.New(sha256.New, []byte(pepper))
	mac.Write([]byte(NormalizeEmail(email)))
	mac.Write([]byte{0})
	mac.Write([]byte(token))
	return mac.Sum(nil)
}

// Equal compares two hashes in constant time.
func Equal(a, b []byte) bool {
	return hmac.Equal(a, b)
}
