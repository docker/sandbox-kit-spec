package spec

import (
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"testing"
	"testing/quick"
	"unicode"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func TestDisplayText(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"host", "api.example.com", "api.example.com"},
		{"method and path", "GET api.example.com/v1/**", "GET api.example.com/v1/**"},
		{"mount mode", "/x (readwrite)", "/x (readwrite)"},
		{"non-ASCII letters", "/données/日本", "/données/日本"},
		{"empty", "", ""},
		{"backslash", `/a\b`, `/a\\b`},
		{"escape lookalike", `/a\x1b`, `/a\\x1b`},
		{"NUL", "/a\x00b", `/a\x00b`},
		{"tab", "/a\tb", `/a\tb`},
		{"LF", "/a\nb", `/a\nb`},
		{"CR", "/a\rb", `/a\rb`},
		{"ESC sequence", "a\x1b[2K", `a\x1b[2K`},
		{"DEL", "/a\x7f", `/a\x7f`},
		{"C1 NEL", "/a\u0085", `/a\u0085`},
		{"C1 CSI", "/a\u009b", `/a\u009b`},
		{"tag character", "/a\U000e0041", `/a\U000e0041`},
		{"invalid UTF-8", "/a\x9bb", `/a\x9bb`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, displayText(tc.in))
		})
	}
	// Characters that print as nothing or reorder the text around them,
	// spelled by code point so the source shows them.
	for _, tc := range []struct {
		name string
		r    rune
	}{
		{"line separator", 0x2028},
		{"no-break space", 0x00a0},
		{"bidi embedding", 0x202a},
		{"bidi override", 0x202e},
		{"bidi isolate", 0x2066},
		{"pop directional isolate", 0x2069},
		{"arabic letter mark", 0x061c},
		{"zero-width space", 0x200b},
		{"zero-width joiner", 0x200d},
		{"byte order mark", 0xfeff},
		{"combining grapheme joiner", 0x034f},
		{"hangul choseong filler", 0x115f},
		{"hangul jungseong filler", 0x1160},
		{"hangul filler", 0x3164},
		{"halfwidth hangul filler", 0xffa0},
		{"variation selector 16", 0xfe0f},
		{"mongolian variation selector", 0x180b},
		{"braille blank", 0x2800},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, fmt.Sprintf(`/a\u%04xb`, tc.r), displayText("/a"+string(tc.r)+"b"))
		})
	}
	acute, diaeresis := string(rune(0x0301)), string(rune(0x0308))
	for _, tc := range []struct{ name, in, want string }{
		{"variation selector supplement", "/a" + string(rune(0xe0100)), `/a\U000e0100`},
		{"leading combining mark", acute + "a", `\u0301a`},
		{"combining mark on a base", "e" + acute, "e" + acute},
		{"marks on a base", "e" + acute + diaeresis, "e" + acute + diaeresis},
		{"consecutive leading marks", acute + diaeresis + "api.example.com", `\u0301\u0308api.example.com`},
		{"mark after a control", "/a\x1b" + acute, `/a\x1b\u0301`},
		{"mark after a backslash", `/a\` + acute, `/a\\\u0301`},
		{"mark after an escaped format character", "/a" + string(rune(0x200b)) + acute, `/a\u200b\u0301`},
		{"replacement character", "/a" + string(utf8.RuneError), "/a" + string(utf8.RuneError)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, displayText(tc.in))
		})
	}
}

// unescape reverses displayText, independently of it, so a round trip
// shows the rendering loses nothing.
func unescape(t *testing.T, s string) string {
	t.Helper()
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			b.WriteByte(s[i])
			continue
		}
		require.Less(t, i+1, len(s), "%q ends in a lone backslash", s)
		i++
		digits := 0
		switch s[i] {
		case '\\':
			b.WriteByte('\\')
		case 't':
			b.WriteByte('\t')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 'x':
			digits = 2
		case 'u':
			digits = 4
		case 'U':
			digits = 8
		default:
			require.Failf(t, "unknown escape", "%q at %d", s, i)
		}
		if digits == 0 {
			continue
		}
		require.LessOrEqual(t, i+1+digits, len(s), "%q has a short escape", s)
		v, err := strconv.ParseUint(s[i+1:i+1+digits], 16, 32)
		require.NoError(t, err, "%q", s)
		if digits == 2 {
			b.WriteByte(byte(v))
		} else {
			b.WriteRune(rune(v))
		}
		i += digits
	}
	return b.String()
}

// hidden reports whether r would act on a terminal or draw as nothing,
// spelled from Unicode classes rather than from the rendering's rule.
func hidden(r rune, first bool) bool {
	return r < 0x20 || (r >= 0x7f && r <= 0x9f) ||
		unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp, unicode.Other_Default_Ignorable_Code_Point, unicode.Variation_Selector) ||
		(unicode.Is(unicode.Zs, r) && r != ' ') ||
		r == 0x2800 ||
		(first && unicode.In(r, unicode.Mn, unicode.Me))
}

// TestDisplayTextRoundTrips checks, on generated strings, that a rendering
// reads back to exactly the string it came from and holds no character
// that would act on a terminal or draw as nothing.
func TestDisplayTextRoundTrips(t *testing.T) {
	alphabet := []string{"a", "/", " ", `\`, `\x1b`, "x", "1", "é", "\x00", "\t", "\n", "\r", "\x1b", "\x7f", "\u0085", "\u009b", "\x9b", string(utf8.RuneError)}
	for _, r := range []rune{0x00a0, 0x0301, 0x034f, 0x115f, 0x180b, 0x200b, 0x2028, 0x2029, 0x202e, 0x2066, 0x2800, 0x3000, 0x3164, 0xfe0f, 0xfeff, 0xffa0, 0xe0041, 0xe0100} {
		alphabet = append(alphabet, string(r))
	}
	fromAlphabet := func(picks []uint8) string {
		var b strings.Builder
		for _, p := range picks {
			b.WriteString(alphabet[int(p)%len(alphabet)])
		}
		return b.String()
	}
	check := func(s string) bool {
		out := displayText(s)
		if !utf8.ValidString(out) || unescape(t, out) != s {
			return false
		}
		for i, r := range out {
			if hidden(r, i == 0) {
				return false
			}
		}
		return true
	}
	for name, property := range map[string]any{
		"alphabet":  func(x []uint8) bool { return check(fromAlphabet(x)) },
		"raw bytes": func(x []byte) bool { return check(string(x)) },
		"runes":     func(x string) bool { return check(x) },
	} {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, quick.Check(property, &quick.Config{MaxCount: 2000, Rand: rand.New(rand.NewSource(1))}))
		})
	}
}

// TestFormatWideningsShowsEveryGrant renders widenings whose fields carry
// characters a terminal acts on. Each widening stays on one line, and the
// lines after it still print.
func TestFormatWideningsShowsEveryGrant(t *testing.T) {
	for _, tc := range []struct{ name, host, want string }{
		{"CR", "registry.example.com\r", `registry.example.com\r`},
		{"LF with a fake line", "registry.example.com\n  + nothing", `registry.example.com\n  + nothing`},
		{"erase line", "registry.example.com\x1b[2K\x1b[1A", `registry.example.com\x1b[2K\x1b[1A`},
		{"recolor", "\x1b[8mregistry.example.com", `\x1b[8mregistry.example.com`},
		{"C1 CSI", "registry.example.com\u009b2K", `registry.example.com\u009b2K`},
		{"bidi override", "registry.example.com" + string(rune(0x202e)), `registry.example.com\u202e`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{
				{Type: CapabilityNetworkPolicyV2, Config: map[string]any{"install": map[string]any{"allow": []any{tc.host}}}},
				{Type: CapabilityPrivileged},
			}}
			ws := DiffWidenings(Surface{}, SurfaceOf(d))
			require.Len(t, ws, 2)
			require.Equal(t, tc.host, ws[0].Detail, "the widening keeps the exact value")
			require.Equal(t, []string{
				"  + network.install.allow: " + tc.want,
				"  + privileged: requests privileged execution",
			}, strings.Split(FormatWidenings(ws), "\n"))
		})
	}
	require.Equal(t, `  + storage\r: /data`, FormatWidenings([]Widening{{Category: "storage\r", Detail: "/data"}}),
		"a caller-built category renders the same way")
}
