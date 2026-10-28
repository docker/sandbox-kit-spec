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
// characters, variation selectors, a combining mark that does not follow
// a character printed as itself, and bytes that are not UTF-8. A backslash is escaped too, so
// distinct strings render distinctly. Lookalike letters and right-to-left
// letters are out of scope: they print as themselves.
func displayText(s string) string {
	if utf8.ValidString(s) && !needsEscape(s) {
		return s
	}
	var b strings.Builder
	// based is whether the last rune was printed as itself, so a combining
	// mark has its own base and cannot draw over an escape.
	based := false
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		raw := false
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
		case printable(r, !based):
			b.WriteRune(r)
			raw = true
		case r < 0x80:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r <= 0xffff:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			fmt.Fprintf(&b, `\U%08x`, r)
		}
		based = raw
		i += size
	}
	return b.String()
}

// needsEscape reports whether valid UTF-8 s holds a rune displayText escapes.
// Every rune before the first such rune prints as itself, so only a mark
// at the start lacks a base.
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
// default-ignorable or variation-selector character. A combining mark with
// no base printed before it does not count.
func printable(r rune, unbased bool) bool {
	switch {
	case !strconv.IsPrint(r),
		r == brailleBlank,
		unicode.In(r, unicode.Cf, unicode.Other_Default_Ignorable_Code_Point, unicode.Variation_Selector),
		unbased && unicode.In(r, unicode.Mn, unicode.Me):
		return false
	}
	return true
}
