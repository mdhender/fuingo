// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package names checks and normalizes the codes of systems and games and
// the email addresses of accounts, and checks the names of files.
package names

import (
	"bytes"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mdhender/fuingo/internal/cerrs"
)

const (
	ErrCode  = cerrs.Error("code must be 1 to 32 letters, digits, hyphens, or underscores")
	ErrEmail = cerrs.Error("email must be one or more printable ASCII bytes with no spaces")
	ErrFile  = cerrs.Error("file name must be 1 to 48 ASCII letters, digits, hyphens, underscores, or periods, and must not start or end with a period")
)

// MaxFileLength is the most bytes a file name may hold.
const MaxFileLength = 48

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

// File checks the name of a file: 1 to MaxFileLength bytes, each an ASCII
// letter or digit, a hyphen, an underscore, or a period, with no period at
// its start or end. File names appear in URLs, on pages, and in shell
// scripts, so nothing else is allowed. The name is not changed: its case
// is kept.
func File(name string) error {
	if len(name) < 1 || len(name) > MaxFileLength || name[0] == '.' || name[len(name)-1] == '.' {
		return ErrFile
	}
	for i := range len(name) {
		switch c := name[i]; {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9', c == '-', c == '_', c == '.':
		default:
			return ErrFile
		}
	}
	return nil
}

const (
	ErrFileEmpty   = cerrs.Error("the file is empty")
	ErrFileUTF8    = cerrs.Error("the file is not valid UTF-8")
	ErrFileControl = cerrs.Error("the file holds a control character other than tab, line feed, and carriage return")
)

// Text checks the whole contents of a file. After one optional UTF-8 byte
// order mark there must be at least one byte, all of it valid UTF-8, with no
// control character but tab, line feed, and carriage return.
func Text(contents []byte) error {
	rest := bytes.TrimPrefix(contents, []byte("\xef\xbb\xbf"))
	if len(rest) == 0 {
		return ErrFileEmpty
	}
	for len(rest) > 0 {
		r, size := utf8.DecodeRune(rest)
		if r == utf8.RuneError && size == 1 {
			return ErrFileUTF8
		}
		if unicode.IsControl(r) && r != '\t' && r != '\n' && r != '\r' {
			return ErrFileControl
		}
		rest = rest[size:]
	}
	return nil
}
