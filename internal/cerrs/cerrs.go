// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package cerrs defines constant errors.
package cerrs

// Error is a string that implements the error interface, allowing sentinel
// errors to be declared as untyped constants.
type Error string

// Error implements the error interface.
func (e Error) Error() string { return string(e) }
