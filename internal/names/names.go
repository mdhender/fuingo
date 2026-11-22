// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package names checks and normalizes the codes of systems and games, and
// the email addresses of accounts.
package names

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mdhender/fuingo/internal/cerrs"
)

const (
	ErrCode  = cerrs.Error("code must be 1 to 32 letters, digits, hyphens, or underscores")
	ErrEmail = cerrs.Error("email must be one or more printable ASCII bytes with no spaces")
)

// MaxCodeLength is the most characters, not bytes, a code may hold.
const MaxCodeLength = 32

// Code lowercases the code of a system or game with strings.ToLower and
// checks it: 1 to MaxCodeLength characters, each a letter or digit in any
// script, a hyphen, or an underscore. Codes appear in URLs and on pages, so
// nothing else is allowed.
func Code(code string) (string, error) {
	if !utf8.ValidString(code) {
		return "", ErrCode
	}
	code = strings.ToLower(code)
	if n := utf8.RuneCountInString(code); n < 1 || n > MaxCodeLength {
		return "", ErrCode
	}
	for _, r := range code {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '_' {
			return "", ErrCode
		}
	}
	return code, nil
}

// Email lowercases an email address with strings.ToLower and checks that it
// is one or more bytes in 0x21-0x7E. No other normalization is applied.
func Email(email string) (string, error) {
	if email == "" {
		return "", ErrEmail
	}
	for i := range len(email) {
		if email[i] < 0x21 || email[i] > 0x7e {
			return "", ErrEmail
		}
	}
	return strings.ToLower(email), nil
}
