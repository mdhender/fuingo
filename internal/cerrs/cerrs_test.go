// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package cerrs_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/mdhender/fuingo/internal/cerrs"
)

func TestError(t *testing.T) {
	const sentinel = cerrs.Error("invalid order")
	if got := sentinel.Error(); got != "invalid order" {
		t.Fatalf("Error() = %q, want invalid order", got)
	}
	err := fmt.Errorf("turn failed: %w", sentinel)
	if !errors.Is(err, sentinel) {
		t.Fatal("wrapped error does not match sentinel")
	}
	if errors.Is(err, cerrs.Error("missing order")) {
		t.Fatal("wrapped error matches a different sentinel")
	}
}
