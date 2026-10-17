// Package assistant builds the context-aware rule assistant: a server-side
// prompt with a sanitized, budgeted snapshot of the user's configuration, and a
// strict validator for the model's reply. The model only proposes; every action
// is validated against real state and applied by the user from the browser.
package assistant

import (
	"strings"
	"unicode"
)

const (
	fenceOpen  = "<<<"
	fenceClose = ">>>"
)

// Per-field limits for values copied from rules, traffic and config.
const (
	limName    = 80
	limDesc    = 120
	limSummary = 200
	limHost    = 100
	limPath    = 150
	limMisc    = 100
)

func stripFences(s string) string {
	for strings.Contains(s, fenceOpen) || strings.Contains(s, fenceClose) {
		s = strings.ReplaceAll(strings.ReplaceAll(s, fenceOpen, ""), fenceClose, "")
	}
	return s
}

func truncRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// clean makes an untrusted value safe to embed in a one-line context entry:
// control characters and newlines are dropped, whitespace collapsed, fence
// markers removed, and the result truncated to n runes.
func clean(s string, n int) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsSpace(r) || r == ' ' || r == ' ':
			b.WriteRune(' ')
		case unicode.IsControl(r) || r == '‎' || r == '‏' || (r >= '‪' && r <= '‮'):
			// dropped
		default:
			b.WriteRune(r)
		}
	}
	s = strings.Join(strings.Fields(b.String()), " ")
	return truncRunes(stripFences(s), n)
}

// cleanText is clean for free text that may span lines (model replies): it
// keeps newlines but still removes other control characters and fences.
func cleanText(s string, n int) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r == '\r':
		case unicode.IsControl(r) || r == ' ' || r == ' ':
		default:
			b.WriteRune(r)
		}
	}
	return truncRunes(stripFences(strings.TrimSpace(b.String())), n)
}
