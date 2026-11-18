package spec

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/pelletier/go-toml/v2"
)

// ErrNoCredentialStructure is returned by Render when the credentialFile
// declares a path but no structure, or an empty one. That is a valid Kit:
// there is nothing to render, so a runtime writes no file for it, rather
// than treating it as a failure. An empty structure counts as an omitted
// one because serialization cannot tell them apart: composition rewrites
// each credential through JSON, which drops an empty structure.
var ErrNoCredentialStructure = errors.New("credentialFile: structure is not declared or is empty; nothing to render")

// credentialFileEncoding is the single source of which credentialFile
// format values exist and which encoding each selects: "" and "json" are
// JSON, "toml" is TOML. ok is false for any other value.
func credentialFileEncoding(format string) (encoding string, ok bool) {
	switch format {
	case "", "json":
		return "json", true
	case "toml":
		return "toml", true
	}
	return "", false
}

// CredentialFileValues are the values a runtime substitutes into a
// credentialFile structure.
type CredentialFileValues struct {
	AccessToken   string
	RefreshToken  string
	ExpiresAt     int64
	Scopes        []string
	PrimaryApiKey string
}

// Render encodes f.Structure, after placeholder substitution, in f.Format.
//
// Format "" and "json" produce compact JSON with encoding/json semantics,
// except that "<", ">" and "&" are written literally rather than as \u
// escapes; "toml" produces a TOML 1.0 document in which nested maps become
// tables. Any other format is an error. The output is deterministic: JSON
// sorts keys, and TOML sorts keys within each table, writing a table's
// plain values before its sub-tables. A Structure that is nil or has no
// entries returns ErrNoCredentialStructure (test with errors.Is): the Kit
// is valid, there is nothing to render, and a runtime writes no file in
// that case. A structure whose entries are all omitted during
// substitution, such as one holding only {{.PrimaryApiKey}} with no key
// captured, was declared with content and still renders as an empty
// document ({} in JSON, nothing in TOML).
//
// Placeholders are recognized only in leaf string values, written
// {{.Name}} with optional spaces inside the braces. The names are
// AccessToken, RefreshToken and PrimaryApiKey (strings), ExpiresAt (an
// integer) and Scopes (an array of strings). A leaf that is exactly one
// placeholder renders in the target encoding's own type: ExpiresAt as a
// JSON number or TOML integer, Scopes as an array, the rest as strings.
// A placeholder embedded in a larger string splices its string form:
// strings as they are, ExpiresAt in decimal, and Scopes joined by single
// spaces, the scope-string form of RFC 6749 section 3.3.
//
// A leaf that references {{.PrimaryApiKey}}, whether it is the whole leaf
// or part of a longer string, and whether or not other placeholders appear
// beside it, omits its enclosing map key or array element when no key is
// captured: an agent would read "Bearer " as a key. Empty AccessToken and
// RefreshToken values still render their key with an empty string.
//
// An unknown placeholder name, any other template action, an unterminated
// "{{", or "{{" in a map key is an error. So is an unexpanded Kit
// reference (a ${{ kit.args.* }} or ${{ kit.env.* }} value) in a leaf or a
// key: Render renders the structure of an expanded descriptor. Substitution is literal, never
// template execution: a substituted value that itself reads like a
// placeholder is not expanded again.
//
// TOML has no null, so a null anywhere in the structure is an error under
// that format; JSON keeps it. A nil map or nil slice counts as a null, not
// as an empty container; an empty non-nil one renders as {} or []. Arrays
// need not be homogeneous, matching the schema's tomlValue. A whole-number
// literal renders as a TOML integer: typed decoding goes through JSON,
// which turns every number into a float64, and the author who wrote 8080
// meant an integer; negative zero stays a float, so both encodings keep
// its sign. A float64 that is NaN or infinite is an error in both
// encodings, as neither can write it portably. So is one of magnitude 2^53
// or more, because integers there are not exact in a float64; Go int and
// int64 values are exact and are not subject to that limit. Digits a
// float64 cannot hold that were lost when the descriptor was decoded,
// below that magnitude, are not detectable here.
//
// The structure may hold only what descriptor decoding or a plain Go map
// produces: nil, string, bool, float64, int, int64, map[string]any and
// []any. Any other Go type (float32, the unsigned kinds, the narrower
// signed kinds, typed slices and maps) is an error. Both encodings require
// valid UTF-8, which they would otherwise repair silently, so a literal
// string, a map key, or a substituted value that is not valid UTF-8 is an
// error; the error names the location and the placeholder, never the bytes.
//
// JSON output has no trailing newline; TOML output ends with one, which is
// how the TOML encoder lays out a document.
//
// Every error names the offending location as a path from "structure"
// (for example structure.auth.token or structure.scopes[1]), so an author
// can find it in the descriptor. Render never mutates f.Structure.
func (f *CredentialFile) Render(v CredentialFileValues) ([]byte, error) {
	if f == nil {
		return nil, errors.New("credentialFile: not declared; nothing to render")
	}
	encoding, ok := credentialFileEncoding(f.Format)
	if !ok {
		return nil, fmt.Errorf("credentialFile: format %q is not json or toml", f.Format)
	}
	if len(f.Structure) == 0 {
		return nil, ErrNoCredentialStructure
	}
	encode := marshalJSON
	if encoding == "toml" {
		encode = toml.Marshal
	}
	r := credentialFileRenderer{values: v, toml: encoding == "toml"}
	rendered, err := r.renderMap("structure", f.Structure)
	if err != nil {
		return nil, fmt.Errorf("credentialFile: %w", err)
	}
	out, err := encode(rendered)
	if err != nil {
		return nil, fmt.Errorf("credentialFile: encode %s: %w", encoding, err)
	}
	return out, nil
}

// marshalJSON is json.Marshal without HTML escaping: the file is read by
// an agent, not embedded in a page, so "<", ">" and "&" stay as written.
func marshalJSON(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

// placeholderAction is one complete action that names a value. Anything
// else between "{{" and "}}" is template syntax this grammar does not
// have, and is refused rather than passed through as literal text that
// would reach the agent looking like an unrendered template.
var placeholderAction = regexp.MustCompile(`^\{\{ *\.([A-Za-z_][A-Za-z0-9_]*) *\}\}$`)

// credentialFileRenderer walks a structure once, building a substituted
// copy. The walk is the only place the input is read, so leaving the
// input untouched is a property of construction, not of care.
type credentialFileRenderer struct {
	values CredentialFileValues
	toml   bool
}

func (r credentialFileRenderer) renderMap(at string, m map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(m))
	// Sorted, so that with several invalid entries the error reported is
	// the same on every run, like the output.
	for _, key := range slices.Sorted(maps.Keys(m)) {
		value := m[key]
		// Keys are the file's schema, which the agent defines; a key that
		// varies with a secret would make the file unreadable to it.
		if !utf8.ValidString(key) {
			return nil, fmt.Errorf("%s: a key is not valid UTF-8", at)
		}
		if strings.Contains(key, "{{") {
			if ContainsKitPlaceholder(key) {
				return nil, fmt.Errorf("%s: key %q holds an unexpanded Kit reference; Render needs the expanded descriptor", at, key)
			}
			return nil, fmt.Errorf("%s: key %q holds a placeholder; placeholders are allowed only in leaf values", at, key)
		}
		rendered, keep, err := r.renderValue(at+"."+key, value)
		if err != nil {
			return nil, err
		}
		if keep {
			out[key] = rendered
		}
	}
	return out, nil
}

func (r credentialFileRenderer) renderSlice(at string, s []any) ([]any, error) {
	out := make([]any, 0, len(s))
	for i, value := range s {
		rendered, keep, err := r.renderValue(fmt.Sprintf("%s[%d]", at, i), value)
		if err != nil {
			return nil, err
		}
		if keep {
			out = append(out, rendered)
		}
	}
	return out, nil
}

// renderValue returns the substituted value and whether its slot is kept;
// keep is false only for an omitted {{.PrimaryApiKey}} leaf.
func (r credentialFileRenderer) renderValue(at string, value any) (any, bool, error) {
	switch t := value.(type) {
	case nil:
		return r.renderNull(at)
	case string:
		return r.renderLeaf(at, t)
	case map[string]any:
		// A nil container is a null, not an empty one: JSON would write
		// null for it, so the two encodings must not disagree on it.
		if t == nil {
			return r.renderNull(at)
		}
		m, err := r.renderMap(at, t)
		return m, err == nil, err
	case []any:
		if t == nil {
			return r.renderNull(at)
		}
		s, err := r.renderSlice(at, t)
		return s, err == nil, err
	case float64:
		n, err := r.renderFloat(at, t)
		return n, err == nil, err
	case bool, int, int64:
		return t, true, nil
	default:
		// The structure is declarative data decoded from a descriptor;
		// any other Go type came from a caller and is not one of the
		// shapes the grammar allows, so it is refused rather than left to
		// an encoder whose two formats may treat it differently.
		return nil, false, fmt.Errorf("%s: unsupported value of type %T", at, value)
	}
}

// maxExactFloat is 2^53, the point from which a float64 no longer holds
// every integer, so a decoded number at or above it may not be what the
// author wrote.
const maxExactFloat = 1 << 53

// renderNull is a null slot: JSON keeps it, TOML has no spelling for it.
func (r credentialFileRenderer) renderNull(at string) (any, bool, error) {
	if r.toml {
		return nil, false, fmt.Errorf("%s is null, which TOML cannot represent", at)
	}
	return nil, true, nil
}

// renderFloat keeps a whole number an integer under TOML, where 8080.0
// and 8080 are different types an agent's parser may tell apart. JSON
// has one number type and encoding/json already writes 8080.
//
// Descriptor decoding hands every number over as a float64, where integers
// from 2^53 up are not all exact, so a number of that magnitude is refused
// rather than written as a possibly different one. Every float rule lives here
// so no caller can convert an out-of-range float to int64.
func (r credentialFileRenderer) renderFloat(at string, f float64) (any, error) {
	// Neither encoding has a portable spelling for these: JSON cannot
	// write NaN or an infinity, and TOML would write "nan" or "inf".
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return nil, fmt.Errorf("%s: %v is not a finite number", at, f)
	}
	if math.Abs(f) >= maxExactFloat {
		return nil, fmt.Errorf("%s: a number of magnitude 2^53 or more cannot be represented exactly", at)
	}
	// Negative zero stays a float so both encodings keep its sign; as an
	// integer it would lose it.
	if r.toml && f == math.Trunc(f) && !(f == 0 && math.Signbit(f)) {
		return int64(f), nil
	}
	return f, nil
}

// renderLeaf substitutes one leaf string. A leaf that is exactly one
// placeholder takes the value's own type; anything else is spliced text.
func (r credentialFileRenderer) renderLeaf(at, s string) (any, bool, error) {
	if !utf8.ValidString(s) {
		return nil, false, fmt.Errorf("%s: literal text is not valid UTF-8", at)
	}
	if !strings.Contains(s, "{{") {
		return s, true, nil
	}
	if ContainsKitPlaceholder(s) {
		return nil, false, fmt.Errorf("%s: the structure holds an unexpanded Kit reference; Render needs the expanded descriptor", at)
	}
	if m := placeholderAction.FindStringSubmatch(s); m != nil {
		return r.typedValue(at, m[1])
	}
	return r.splice(at, s)
}

// typedValue is the value of a leaf that is exactly one placeholder.
func (r credentialFileRenderer) typedValue(at, name string) (any, bool, error) {
	switch name {
	case "ExpiresAt":
		return r.values.ExpiresAt, true, nil
	case "Scopes":
		// Never nil: a nil slice encodes as JSON null, which TOML cannot
		// spell, and "no scopes" is an empty array in both.
		scopes := make([]any, 0, len(r.values.Scopes))
		for _, scope := range r.values.Scopes {
			if !utf8.ValidString(scope) {
				return nil, false, errInvalidUTF8Value(at, name)
			}
			scopes = append(scopes, scope)
		}
		return scopes, true, nil
	case "PrimaryApiKey":
		// An agent reads an empty key as a key, not as "not logged in
		// yet", so an uncaptured key is absent rather than blank.
		if r.values.PrimaryApiKey == "" {
			return nil, false, nil
		}
		// Otherwise it is the string like any other, UTF-8 check included.
	}
	text, err := r.text(at, name)
	return text, err == nil, err
}

// splice replaces every placeholder in the leaf with its string form. It
// scans once, left to right, and appends substituted values to the output
// without rescanning them, which is what keeps substitution literal. keep
// is false when the leaf references PrimaryApiKey and none is captured;
// the whole leaf is still checked, so a mistake beside it is not hidden.
func (r credentialFileRenderer) splice(at, leaf string) (spliced string, keep bool, err error) {
	var b strings.Builder
	keep = true
	// s is the unscanned rest of leaf; errors quote leaf, not s.
	s := leaf
	for {
		open := strings.Index(s, "{{")
		if open < 0 {
			b.WriteString(s)
			return b.String(), keep, nil
		}
		end := strings.Index(s[open:], "}}")
		if end < 0 {
			return "", false, fmt.Errorf("%s: unterminated %q in %q", at, "{{", leaf)
		}
		action := s[open : open+end+2]
		m := placeholderAction.FindStringSubmatch(action)
		if m == nil {
			return "", false, fmt.Errorf("%s: %q is not a placeholder; write {{.Name}}, with optional spaces inside the braces", at, action)
		}
		if m[1] == "PrimaryApiKey" && r.values.PrimaryApiKey == "" {
			keep = false
		}
		text, err := r.text(at, m[1])
		if err != nil {
			return "", false, err
		}
		b.WriteString(s[:open])
		b.WriteString(text)
		s = s[open+end+2:]
	}
}

// text is a placeholder's string form, for splicing into a larger string.
// It is also where a substituted value is checked for valid UTF-8, which
// both encodings require; the error names the placeholder, never the value.
func (r credentialFileRenderer) text(at, name string) (string, error) {
	var text string
	switch name {
	case "AccessToken":
		text = r.values.AccessToken
	case "RefreshToken":
		text = r.values.RefreshToken
	case "PrimaryApiKey":
		text = r.values.PrimaryApiKey
	case "ExpiresAt":
		return strconv.FormatInt(r.values.ExpiresAt, 10), nil
	case "Scopes":
		text = strings.Join(r.values.Scopes, " ")
	default:
		return "", fmt.Errorf("%s: unknown placeholder {{.%s}}", at, name)
	}
	if !utf8.ValidString(text) {
		return "", errInvalidUTF8Value(at, name)
	}
	return text, nil
}

// errInvalidUTF8Value names the placeholder and location, never the bytes.
func errInvalidUTF8Value(at, name string) error {
	return fmt.Errorf("%s: the value for {{.%s}} is not valid UTF-8", at, name)
}
