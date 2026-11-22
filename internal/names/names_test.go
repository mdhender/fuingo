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

func TestFile(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		"a", "orders.txt", "G3-turn_012.Report.html", "README", "0", "a..b", "-", "_x-",
		strings.Repeat("a", 48), strings.Repeat("a", 44) + ".txt",
	} {
		if err := names.File(name); err != nil {
			t.Errorf("File(%q) = %v, want accepted", name, err)
		}
	}
	for _, name := range []string{
		"", strings.Repeat("a", 49), ".hidden", "trailing.", ".", "..",
		"my orders.txt", " orders.txt", "orders.txt ", "a\tb", "a\nb", "a\x00b", "a\x7fb",
		"a/b", `a\b`, "a<b", "a>b", "a&b", "a;b", `a"b`, "a'b", "a`b", "a’b",
		"a:b", "a*b", "a?b", "a|b", "a$b", "a%b", "a#b", "a~b", "a+b", "a=b", "a,b", "a(b", "a!b", "a@b",
		"órdenes.txt", "命令.txt", "a b", "\xff", strings.Repeat("é", 24) + "x",
	} {
		if err := names.File(name); !errors.Is(err, names.ErrFile) {
			t.Errorf("File(%q) = %v, want names.ErrFile", name, err)
		}
	}
}

func TestText(t *testing.T) {
	t.Parallel()
	const bom = "\xef\xbb\xbf"
	for name, tc := range map[string]struct {
		contents string
		want     error
	}{
		"one byte":           {"x", nil},
		"lines":              {"move 1\nmove 2\n", nil},
		"CRLF and tabs":      {"a\tb\r\nc\r\n", nil},
		"only a newline":     {"\n", nil},
		"BOM then text":      {bom + "orders\n", nil},
		"non-ASCII":          {"naïve café 命令 🎲\n", nil},
		"BOM inside":         {"a" + bom + "b", nil},
		"two BOMs":           {bom + bom, nil},
		"empty":              {"", names.ErrFileEmpty},
		"only a BOM":         {bom, names.ErrFileEmpty},
		"NUL":                {"a\x00b", names.ErrFileControl},
		"NUL at the start":   {"\x00ab", names.ErrFileControl},
		"NUL at the end":     {"ab\x00", names.ErrFileControl},
		"escape":             {"a\x1bb", names.ErrFileControl},
		"form feed":          {"a\x0cb", names.ErrFileControl},
		"vertical tab":       {"a\x0bb", names.ErrFileControl},
		"backspace":          {"a\x08b", names.ErrFileControl},
		"DEL":                {"a\x7fb", names.ErrFileControl},
		"C1 control":         {"a\u0085b", names.ErrFileControl},
		"control after BOM":  {bom + "\x00", names.ErrFileControl},
		"bad start":          {"\xffabc", names.ErrFileUTF8},
		"bad middle":         {"ab\xffcd", names.ErrFileUTF8},
		"bad end":            {"abcd\xc3", names.ErrFileUTF8},
		"Latin-1":            {"caf\xe9\n", names.ErrFileUTF8},
		"overlong":           {"\xc0\x80", names.ErrFileUTF8},
		"surrogate":          {"\xed\xa0\x80", names.ErrFileUTF8},
		"UTF-16":             {"\xff\xfeo\x00r\x00", names.ErrFileUTF8},
		"bad after long run": {strings.Repeat("a", 100_000) + "\xff", names.ErrFileUTF8},
		"NUL after long run": {strings.Repeat("a", 100_000) + "\x00", names.ErrFileControl},
	} {
		if err := names.Text([]byte(tc.contents)); !errors.Is(err, tc.want) {
			t.Errorf("%s: Text = %v, want %v", name, err, tc.want)
		}
	}
	// The replacement character itself, correctly encoded, is valid.
	if err := names.Text([]byte("a�b")); err != nil {
		t.Errorf("U+FFFD: %v", err)
	}
}
