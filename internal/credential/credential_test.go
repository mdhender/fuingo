// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package credential_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"testing"

	"github.com/mdhender/fuingo/internal/credential"
)

func TestHash(t *testing.T) {
	mac := hmac.New(sha256.New, []byte("pepper"))
	mac.Write([]byte("catbird@example.com\x00token"))
	want := mac.Sum(nil)

	if got := credential.Hash("pepper", "CatBird@Example.COM", "token"); !credential.Equal(got, want) {
		t.Errorf("Hash with mixed-case email = %x, want %x", got, want)
	}
	for _, other := range [][3]string{
		{"other", "catbird@example.com", "token"},
		{"pepper", "catbird@example.org", "token"},
		{"pepper", "catbird@example.com", "Token"},
		{"pepper", "catbird@example.com\x00", "token"},
	} {
		if got := credential.Hash(other[0], other[1], other[2]); credential.Equal(got, want) {
			t.Errorf("Hash(%q, %q, %q) matched", other[0], other[1], other[2])
		}
	}
}
