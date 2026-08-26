package intent

import (
	"strings"
	"unicode"
)

// Normalize prepares a transcript for matching: lower case, no punctuation,
// single spaces. Non-Latin scripts are left alone — Hebrew text must survive
// this untouched, so there is deliberately no ASCII folding here.
func Normalize(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	lastSpace := true
	for _, r := range strings.ToLower(strings.TrimSpace(text)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r):
			b.WriteRune(r)
			lastSpace = false
		case unicode.IsSpace(r):
			if !lastSpace {
				b.WriteRune(' ')
				lastSpace = true
			}
		default:
			// Punctuation and symbols are dropped entirely.
		}
	}
	return strings.TrimSpace(b.String())
}
