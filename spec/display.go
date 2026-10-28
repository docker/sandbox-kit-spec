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
// reordering characters become the escapes strconv.QuoteRuneToASCII
// writes: C0 controls, DEL, C1 controls, non-ASCII spaces and separators,
// format characters such as bidirectional overrides and zero-width
// characters, default-ignorable characters, variation selectors, and a
// mark that does not follow a character printed as itself.
// Bytes that are not UTF-8 become \xNN. A backslash is escaped too, so
// distinct strings render distinctly. Lookalike letters and right-to-left
// letters are out of scope: they print as themselves.
func displayText(s string) string {
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
		case r != '\\' && printable(r, !based):
			b.WriteRune(r)
			raw = true
		default:
			q := strconv.QuoteRuneToASCII(r)
			b.WriteString(q[1 : len(q)-1])
		}
		based = raw
		i += size
	}
	return b.String()
}

// printable reports whether r draws as itself: a letter, mark, number,
// punctuation, symbol, or the ASCII space, and not a format,
// default-ignorable or variation-selector character. A mark of any
// kind, combining or spacing, with no base printed before it does not count.
func printable(r rune, unbased bool) bool {
	switch {
	case !strconv.IsPrint(r),
		r == brailleBlank,
		unicode.In(r, unicode.Cf, unicode.Other_Default_Ignorable_Code_Point, unicode.Variation_Selector),
		unbased && unicode.Is(unicode.M, r):
		return false
	}
	return true
}
