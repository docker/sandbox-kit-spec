package spec

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// brailleBlank prints as an empty cell.
const brailleBlank = 0x2800

// displayText renders s for a person reading a terminal. Printable text,
// including non-ASCII letters, stays as it is. Control, invisible and
// reordering characters become visible escapes: C0 controls, DEL, C1
// controls, non-ASCII spaces and separators, format characters such as
// bidirectional overrides and zero-width characters, default-ignorable
// characters, variation selectors, a combining mark with nothing before
// it, and bytes that are not UTF-8. A backslash is escaped too, so
// distinct strings render distinctly. Lookalike letters and right-to-left
// letters are out of scope: they print as themselves.
func displayText(s string) string {
	if utf8.ValidString(s) && !needsEscape(s) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case printable(r, i == 0):
			b.WriteRune(r)
		case r < 0x80:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r <= 0xffff:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			fmt.Fprintf(&b, `\U%08x`, r)
		}
		i += size
	}
	return b.String()
}

// needsEscape reports whether valid UTF-8 s holds a rune displayText escapes.
func needsEscape(s string) bool {
	for i, r := range s {
		if r == '\\' || !printable(r, i == 0) {
			return true
		}
	}
	return false
}

// printable reports whether r draws as itself: a letter, mark, number,
// punctuation, symbol, or the ASCII space, and not a format,
// default-ignorable or variation-selector character. A combining mark
// first in the string has no base to attach to, so it does not count.
func printable(r rune, first bool) bool {
	switch {
	case !strconv.IsPrint(r),
		r == brailleBlank,
		unicode.In(r, unicode.Cf, unicode.Other_Default_Ignorable_Code_Point, unicode.Variation_Selector),
		first && unicode.In(r, unicode.Mn, unicode.Me):
		return false
	}
	return true
}
