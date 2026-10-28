package spec

import (
	"fmt"
	"math/rand"
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
		{"BEL", "/a\ab", `/a\ab`},
		{"backspace", "/a\bb", `/a\bb`},
		{"vertical tab", "/a\vb", `/a\vb`},
		{"form feed", "/a\fb", `/a\fb`},
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
		{"leading spacing mark", string(rune(0x302e)) + "a", `\u302ea`},
		{"spacing mark after an escape", "/a\x1b" + string(rune(0x0903)), `/a\x1b\u0903`},
		{"spacing mark on a base", string(rune(0x0915)) + string(rune(0x0903)), string(rune(0x0915)) + string(rune(0x0903))},
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

// hidden reports whether r would act on a terminal or draw as nothing,
// spelled from Unicode classes rather than from the rendering's rule.
func hidden(r rune, first bool) bool {
	return r < 0x20 || (r >= 0x7f && r <= 0x9f) ||
		unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp, unicode.Other_Default_Ignorable_Code_Point, unicode.Variation_Selector) ||
		(unicode.Is(unicode.Zs, r) && r != ' ') ||
		r == 0x2800 ||
		(first && unicode.Is(unicode.M, r))
}

// TestDisplayTextProperties checks, on generated strings, that a rendering
// holds no character that would act on a terminal or draw as nothing, and
// that no two distinct strings render the same.
func TestDisplayTextProperties(t *testing.T) {
	rendered := map[string]string{}
	check := func(s string) error {
		out := displayText(s)
		if prev, ok := rendered[out]; ok && prev != s {
			return fmt.Errorf("%q and %q both render as %q", prev, s, out)
		}
		rendered[out] = s
		for i, r := range out {
			if hidden(r, i == 0) {
				return fmt.Errorf("%q renders as %q, which holds %U", s, out, r)
			}
		}
		return nil
	}
	// Pieces that would collide if escaping lost information: a backslash,
	// text spelling an escape, and the characters those escapes stand for.
	alphabet := []string{"a", "/", " ", `\`, "x1b", "u0085", "\x00", "\a", "\t", "\n", "\r", "\x1b", "\x7f", "\x9b"}
	for _, r := range []rune{0x00e9, 0x0085, 0x009b, 0x00a0, 0x0301, 0x034f, 0x115f, 0x180b, 0x200b, 0x2028, 0x2029, 0x202e, 0x2066, 0x2800, 0x3000, 0x3164, 0xfe0f, 0xfeff, 0xffa0, 0xfffd, 0xe0041, 0xe0100} {
		alphabet = append(alphabet, string(r))
	}
	// Every string of up to three pieces, so each pair and triple of pieces
	// is compared against every other.
	level := []string{""}
	for range 3 {
		var next []string
		for _, s := range level {
			for _, p := range alphabet {
				next = append(next, s+p)
			}
		}
		for _, s := range next {
			require.NoError(t, check(s))
		}
		level = next
	}
	seeded := func() *quick.Config { return &quick.Config{MaxCount: 2000, Rand: rand.New(rand.NewSource(1))} }
	t.Run("raw bytes", func(t *testing.T) {
		require.NoError(t, quick.Check(func(x []byte) bool { return check(string(x)) == nil }, seeded()))
	})
	t.Run("runes", func(t *testing.T) {
		require.NoError(t, quick.Check(func(s string) bool { return check(s) == nil }, seeded()))
	})
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
