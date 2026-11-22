// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bytes"
	"net/url"
)

const (
	msgBadStripBOM     = "strip-bom must be true or false, given once"
	msgBadNormalizeEOL = "normalize-eol must be lf or crlf, given once"
)

var bom = []byte("\xef\xbb\xbf")

// normalization is what a download's query asks to change in the response.
// It never changes what is stored. The zero value changes nothing.
type normalization struct {
	// stripBOM removes a leading UTF-8 byte order mark.
	stripBOM bool
	// eol is the line ending every line gets, or nil to leave them alone.
	eol []byte
}

// parseNormalization reads strip-bom and normalize-eol from a download's
// query. It returns a message for the client if either is not understood.
func parseNormalization(q url.Values) (n normalization, problem string) {
	if q.Has("strip-bom") {
		switch v := q["strip-bom"]; {
		case len(v) == 1 && v[0] == "true":
			n.stripBOM = true
		case len(v) == 1 && v[0] == "false":
		default:
			return normalization{}, msgBadStripBOM
		}
	}
	if q.Has("normalize-eol") {
		switch v := q["normalize-eol"]; {
		case len(v) == 1 && v[0] == "lf":
			n.eol = []byte("\n")
		case len(v) == 1 && v[0] == "crlf":
			n.eol = []byte("\r\n")
		default:
			return normalization{}, msgBadNormalizeEOL
		}
	}
	return n, ""
}

// apply returns the contents as asked for. With an end of line chosen,
// every CRLF, lone CR, and LF becomes it, and the last line gets one if it
// has none. A byte order mark that is not stripped stays where it is.
func (n normalization) apply(contents []byte) []byte {
	var mark []byte
	if bytes.HasPrefix(contents, bom) {
		contents = contents[len(bom):]
		if !n.stripBOM {
			mark = bom
		}
	}
	if n.eol == nil {
		if mark == nil {
			return contents
		}
		return append(bytes.Clone(mark), contents...)
	}
	out := make([]byte, 0, len(mark)+len(contents)+len(contents)/16+len(n.eol))
	out = append(out, mark...)
	ended := true
	for i := 0; i < len(contents); i++ {
		switch c := contents[i]; c {
		case '\r':
			if i+1 < len(contents) && contents[i+1] == '\n' {
				i++
			}
			fallthrough
		case '\n':
			out = append(out, n.eol...)
			ended = true
		default:
			out = append(out, c)
			ended = false
		}
	}
	if !ended {
		out = append(out, n.eol...)
	}
	return out
}
