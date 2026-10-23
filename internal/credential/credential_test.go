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
	mac.Write([]byte("catbird@example.com\x0012\x00token"))
	want := mac.Sum(nil)

	if got := credential.Hash("pepper", "CatBird@Example.COM", 12, "token"); !credential.Equal(got, want) {
		t.Errorf("Hash with mixed-case email = %x, want %x", got, want)
	}
	others := []struct {
		pepper, email string
		gameID        int64
		token         string
	}{
		{"other", "catbird@example.com", 12, "token"},
		{"pepper", "catbird@example.org", 12, "token"},
		{"pepper", "catbird@example.com", 1, "token"},
		{"pepper", "catbird@example.com", 2, "token"},
		{"pepper", "catbird@example.com", 12, "Token"},
		{"pepper", "catbird@example.com", 1, "2\x00token"},
	}
	for _, o := range others {
		if got := credential.Hash(o.pepper, o.email, o.gameID, o.token); credential.Equal(got, want) {
			t.Errorf("Hash(%q, %q, %d, %q) matched", o.pepper, o.email, o.gameID, o.token)
		}
	}
}
