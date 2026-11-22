// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package names_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/mdhender/fuingo/internal/names"
)

func TestCode(t *testing.T) {
	accepted := map[string]string{
		"g3":                    "g3",
		"G3":                    "g3",
		"Olympia":               "olympia",
		"alpha-2_b":             "alpha-2_b",
		"Ελλάδα":                "ελλάδα",
		"été":                   "été",
		"٣":                     "٣", // a digit in another script
		strings.Repeat("a", 32): strings.Repeat("a", 32),
		strings.Repeat("é", 32): strings.Repeat("é", 32), // 64 bytes, 32 characters
	}
	for in, want := range accepted {
		if got, err := names.Code(in); err != nil || got != want {
			t.Errorf("Code(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	rejected := []string{
		"", strings.Repeat("a", 33), strings.Repeat("é", 33),
		"a b", " a", "a\t", "a\n", "a\x00", "a\x7f",
		"a/b", `a\b`, "a.b", "a<b", "a>b", "a&b", "a;b", `a"b`, "a'b", "a`b", "a“b",
		"a%b", "a?b", "a#b", "a+b", "a:b", "a@b",
		"a\xffb", // invalid UTF-8
	}
	for _, in := range rejected {
		if got, err := names.Code(in); !errors.Is(err, names.ErrCode) {
			t.Errorf("Code(%q) = %q, %v; want ErrCode", in, got, err)
		}
	}
}

func TestEmail(t *testing.T) {
	if got, err := names.Email("Catbird@Example.COM"); err != nil || got != "catbird@example.com" {
		t.Errorf("Email = %q, %v", got, err)
	}
	for _, in := range []string{"", "a b@example.com", "a@example.com\n", "é@example.com", "a\x7f@example.com"} {
		if got, err := names.Email(in); !errors.Is(err, names.ErrEmail) {
			t.Errorf("Email(%q) = %q, %v; want ErrEmail", in, got, err)
		}
	}
}
