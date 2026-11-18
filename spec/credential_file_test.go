package spec

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"github.com/stretchr/testify/require"
)

// credentialFileValues are distinct, non-empty values for every
// placeholder, so a test can tell which one landed where.
var credentialFileValues = CredentialFileValues{
	AccessToken:   "sk-access",
	RefreshToken:  "sk-refresh",
	ExpiresAt:     1700000000,
	Scopes:        []string{"user:inference", "user:profile"},
	PrimaryApiKey: "pk-primary",
}

// renderCredentialFile renders structure in format and fails the test on
// an error, so a case states only what the output must be.
func renderCredentialFile(t *testing.T, format string, structure map[string]any, v CredentialFileValues) []byte {
	t.Helper()
	f := &CredentialFile{Path: "~/.credentials", Format: format, Structure: structure}
	out, err := f.Render(v)
	require.NoError(t, err)
	return out
}

// decodeTOML parses rendered TOML back into a map: the structure is what
// a reader sees, and asserting it keeps most cases independent of the
// encoder's choice of quoting and table layout.
func decodeTOML(t *testing.T, out []byte) map[string]any {
	t.Helper()
	got := map[string]any{}
	require.NoError(t, toml.Unmarshal(out, &got), "rendered TOML must parse:\n%s", out)
	return got
}

// cloneStructure deep-copies a structure so a test can prove Render left
// the caller's map alone.
func cloneStructure(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, value := range t {
			out[k] = cloneStructure(value)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, value := range t {
			out[i] = cloneStructure(value)
		}
		return out
	default:
		return v
	}
}

// TestCredentialFileRenderSubstitutes pins substitution in both
// encodings. JSON is pinned to exact bytes because compact, key-sorted
// encoding/json output is fully determined; TOML is pinned to the decoded
// structure, with TOML's own types: integers decode as int64 and arrays
// as []any.
func TestCredentialFileRenderSubstitutes(t *testing.T) {
	tests := []struct {
		name      string
		structure map[string]any
		values    CredentialFileValues
		json      string
		toml      map[string]any
	}{
		{
			name:      "access token leaf is a string",
			structure: map[string]any{"token": "{{.AccessToken}}"},
			values:    credentialFileValues,
			json:      `{"token":"sk-access"}`,
			toml:      map[string]any{"token": "sk-access"},
		},
		{
			name:      "refresh token leaf is a string",
			structure: map[string]any{"refresh": "{{.RefreshToken}}"},
			values:    credentialFileValues,
			json:      `{"refresh":"sk-refresh"}`,
			toml:      map[string]any{"refresh": "sk-refresh"},
		},
		{
			name:      "primary api key leaf is a string",
			structure: map[string]any{"key": "{{.PrimaryApiKey}}"},
			values:    credentialFileValues,
			json:      `{"key":"pk-primary"}`,
			toml:      map[string]any{"key": "pk-primary"},
		},
		{
			name:      "expires at leaf is a number",
			structure: map[string]any{"expiresAt": "{{.ExpiresAt}}"},
			values:    credentialFileValues,
			json:      `{"expiresAt":1700000000}`,
			toml:      map[string]any{"expiresAt": int64(1700000000)},
		},
		{
			name:      "zero expires at leaf is still a number",
			structure: map[string]any{"expiresAt": "{{.ExpiresAt}}"},
			values:    CredentialFileValues{},
			json:      `{"expiresAt":0}`,
			toml:      map[string]any{"expiresAt": int64(0)},
		},
		{
			name:      "scopes leaf is an array",
			structure: map[string]any{"scopes": "{{.Scopes}}"},
			values:    credentialFileValues,
			json:      `{"scopes":["user:inference","user:profile"]}`,
			toml:      map[string]any{"scopes": []any{"user:inference", "user:profile"}},
		},
		{
			// A nil slice would encode as JSON null, which TOML cannot
			// spell; an empty array is the one value both encodings share.
			name:      "no scopes is an empty array",
			structure: map[string]any{"scopes": "{{.Scopes}}"},
			values:    CredentialFileValues{},
			json:      `{"scopes":[]}`,
			toml:      map[string]any{"scopes": []any{}},
		},
		{
			name: "whitespace inside the braces is allowed",
			structure: map[string]any{
				"a": "{{ .AccessToken }}",
				"b": "{{.RefreshToken }}",
				"c": "{{  .ExpiresAt}}",
				"d": "{{ .Scopes }}",
			},
			values: credentialFileValues,
			json:   `{"a":"sk-access","b":"sk-refresh","c":1700000000,"d":["user:inference","user:profile"]}`,
			toml: map[string]any{
				"a": "sk-access",
				"b": "sk-refresh",
				"c": int64(1700000000),
				"d": []any{"user:inference", "user:profile"},
			},
		},
		{
			name:      "embedded string placeholder splices",
			structure: map[string]any{"header": "Bearer {{.AccessToken}}"},
			values:    credentialFileValues,
			json:      `{"header":"Bearer sk-access"}`,
			toml:      map[string]any{"header": "Bearer sk-access"},
		},
		{
			name:      "embedded expires at splices in decimal",
			structure: map[string]any{"note": "exp={{.ExpiresAt}}"},
			values:    credentialFileValues,
			json:      `{"note":"exp=1700000000"}`,
			toml:      map[string]any{"note": "exp=1700000000"},
		},
		{
			name:      "embedded scopes splice space-joined",
			structure: map[string]any{"scope": "scope={{.Scopes}}"},
			values:    credentialFileValues,
			json:      `{"scope":"scope=user:inference user:profile"}`,
			toml:      map[string]any{"scope": "scope=user:inference user:profile"},
		},
		{
			name:      "several placeholders in one string",
			structure: map[string]any{"pair": "{{.AccessToken}}:{{ .RefreshToken }}@{{.ExpiresAt}}"},
			values:    credentialFileValues,
			json:      `{"pair":"sk-access:sk-refresh@1700000000"}`,
			toml:      map[string]any{"pair": "sk-access:sk-refresh@1700000000"},
		},
		{
			name: "nested maps substitute at every depth",
			structure: map[string]any{
				"claudeAiOauth": map[string]any{
					"accessToken": "{{.AccessToken}}",
					"expiresAt":   "{{.ExpiresAt}}",
					"scopes":      "{{.Scopes}}",
					"meta":        map[string]any{"refresh": "{{.RefreshToken}}"},
				},
			},
			values: credentialFileValues,
			json:   `{"claudeAiOauth":{"accessToken":"sk-access","expiresAt":1700000000,"meta":{"refresh":"sk-refresh"},"scopes":["user:inference","user:profile"]}}`,
			toml: map[string]any{
				"claudeAiOauth": map[string]any{
					"accessToken": "sk-access",
					"expiresAt":   int64(1700000000),
					"scopes":      []any{"user:inference", "user:profile"},
					"meta":        map[string]any{"refresh": "sk-refresh"},
				},
			},
		},
		{
			name:      "array elements are leaves",
			structure: map[string]any{"list": []any{"{{.AccessToken}}", "x {{.RefreshToken}}", "{{.ExpiresAt}}"}},
			values:    credentialFileValues,
			json:      `{"list":["sk-access","x sk-refresh",1700000000]}`,
			toml:      map[string]any{"list": []any{"sk-access", "x sk-refresh", int64(1700000000)}},
		},
		{
			name:      "maps inside arrays substitute",
			structure: map[string]any{"servers": []any{map[string]any{"token": "{{.AccessToken}}"}}},
			values:    credentialFileValues,
			json:      `{"servers":[{"token":"sk-access"}]}`,
			toml:      map[string]any{"servers": []any{map[string]any{"token": "sk-access"}}},
		},
		{
			// Mixed-type arrays are legal TOML 1.0, and the schema's
			// tomlValue deliberately does not judge them.
			name: "literal values pass through",
			structure: map[string]any{
				"s":     "plain",
				"b":     true,
				"i":     7,
				"n":     map[string]any{"deep": "v"},
				"mixed": []any{"a", 1, false},
			},
			values: credentialFileValues,
			json:   `{"b":true,"i":7,"mixed":["a",1,false],"n":{"deep":"v"},"s":"plain"}`,
			toml: map[string]any{
				"s":     "plain",
				"b":     true,
				"i":     int64(7),
				"n":     map[string]any{"deep": "v"},
				"mixed": []any{"a", int64(1), false},
			},
		},
		{
			name:      "empty access and refresh tokens keep their keys",
			structure: map[string]any{"a": "{{.AccessToken}}", "r": "{{.RefreshToken}}", "h": "Bearer {{.AccessToken}}"},
			values:    CredentialFileValues{},
			json:      `{"a":"","h":"Bearer ","r":""}`,
			toml:      map[string]any{"a": "", "r": "", "h": "Bearer "},
		},
		{
			// A value that reads like a placeholder is data, never a
			// second round of expansion.
			name: "substitution is literal",
			structure: map[string]any{
				"a": "{{.AccessToken}}",
				"e": "x{{.AccessToken}}",
				"k": "{{.PrimaryApiKey}}",
			},
			values: CredentialFileValues{AccessToken: "{{.RefreshToken}}", RefreshToken: "never", PrimaryApiKey: "{{.Foo}}"},
			json:   `{"a":"{{.RefreshToken}}","e":"x{{.RefreshToken}}","k":"{{.Foo}}"}`,
			toml:   map[string]any{"a": "{{.RefreshToken}}", "e": "x{{.RefreshToken}}", "k": "{{.Foo}}"},
		},
		{
			name:      "values that need escaping stay well-formed",
			structure: map[string]any{"a": "{{.AccessToken}}"},
			values:    CredentialFileValues{AccessToken: "a\"b\\c\n'd"},
			json:      `{"a":"a\"b\\c\n'd"}`,
			toml:      map[string]any{"a": "a\"b\\c\n'd"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Run("json", func(t *testing.T) {
				require.Equal(t, tt.json, string(renderCredentialFile(t, "json", tt.structure, tt.values)))
			})
			t.Run("toml", func(t *testing.T) {
				require.Equal(t, tt.toml, decodeTOML(t, renderCredentialFile(t, "toml", tt.structure, tt.values)))
			})
		})
	}
}

// TestCredentialFileRenderOmitsEmptyPrimaryApiKey pins the one value
// whose absence removes structure: any leaf that references
// {{.PrimaryApiKey}}, whole or embedded, disappears with its key or array
// element when no key was captured, and nothing else does.
func TestCredentialFileRenderOmitsEmptyPrimaryApiKey(t *testing.T) {
	noKey := credentialFileValues
	noKey.PrimaryApiKey = ""
	tests := []struct {
		name      string
		structure map[string]any
		json      string
		toml      map[string]any
	}{
		{
			name:      "top-level key is omitted",
			structure: map[string]any{"keep": "v", "key": "{{.PrimaryApiKey}}"},
			json:      `{"keep":"v"}`,
			toml:      map[string]any{"keep": "v"},
		},
		{
			name:      "whitespace form is omitted too",
			structure: map[string]any{"keep": "v", "key": "{{ .PrimaryApiKey }}"},
			json:      `{"keep":"v"}`,
			toml:      map[string]any{"keep": "v"},
		},
		{
			name:      "nested key is omitted",
			structure: map[string]any{"auth": map[string]any{"key": "{{.PrimaryApiKey}}", "url": "u"}},
			json:      `{"auth":{"url":"u"}}`,
			toml:      map[string]any{"auth": map[string]any{"url": "u"}},
		},
		{
			// Only the enclosing key goes; the map that held it is
			// declared structure and stays, even when left empty.
			name:      "a map emptied by omission stays",
			structure: map[string]any{"auth": map[string]any{"key": "{{.PrimaryApiKey}}"}},
			json:      `{"auth":{}}`,
			toml:      map[string]any{"auth": map[string]any{}},
		},
		{
			name:      "array element is omitted",
			structure: map[string]any{"keys": []any{"{{.PrimaryApiKey}}", "static"}},
			json:      `{"keys":["static"]}`,
			toml:      map[string]any{"keys": []any{"static"}},
		},
		{
			name:      "embedded reference omits the leaf",
			structure: map[string]any{"keep": "v", "header": "key={{.PrimaryApiKey}}"},
			json:      `{"keep":"v"}`,
			toml:      map[string]any{"keep": "v"},
		},
		{
			name:      "bearer form omits the leaf",
			structure: map[string]any{"keep": "v", "authorization": "Bearer {{.PrimaryApiKey}}"},
			json:      `{"keep":"v"}`,
			toml:      map[string]any{"keep": "v"},
		},
		{
			name:      "two placeholders in one leaf omit it",
			structure: map[string]any{"keep": "v", "pair": "{{.AccessToken}}:{{.PrimaryApiKey}}"},
			json:      `{"keep":"v"}`,
			toml:      map[string]any{"keep": "v"},
		},
		{
			name:      "embedded reference in an array element is omitted",
			structure: map[string]any{"headers": []any{"Bearer {{.PrimaryApiKey}}", "static"}},
			json:      `{"headers":["static"]}`,
			toml:      map[string]any{"headers": []any{"static"}},
		},
		{
			name:      "other placeholders still render",
			structure: map[string]any{"key": "{{.PrimaryApiKey}}", "token": "{{.AccessToken}}"},
			json:      `{"token":"sk-access"}`,
			toml:      map[string]any{"token": "sk-access"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Run("json", func(t *testing.T) {
				require.Equal(t, tt.json, string(renderCredentialFile(t, "json", tt.structure, noKey)))
			})
			t.Run("toml", func(t *testing.T) {
				require.Equal(t, tt.toml, decodeTOML(t, renderCredentialFile(t, "toml", tt.structure, noKey)))
			})
		})
	}
}

// TestCredentialFileRenderFormats pins format selection: json is the
// default, the two JSON spellings agree byte for byte, and anything the
// grammar does not name is refused rather than guessed at.
func TestCredentialFileRenderFormats(t *testing.T) {
	structure := map[string]any{"b": "{{.AccessToken}}", "a": map[string]any{"z": 1, "y": "{{.ExpiresAt}}"}}
	const want = `{"a":{"y":1700000000,"z":1},"b":"sk-access"}`

	t.Run("omitted format is compact json", func(t *testing.T) {
		require.Equal(t, want, string(renderCredentialFile(t, "", structure, credentialFileValues)))
	})
	t.Run("json is compact json", func(t *testing.T) {
		require.Equal(t, want, string(renderCredentialFile(t, "json", structure, credentialFileValues)))
	})
	t.Run("toml is not json", func(t *testing.T) {
		out := renderCredentialFile(t, "toml", structure, credentialFileValues)
		require.False(t, strings.HasPrefix(strings.TrimSpace(string(out)), "{"), "toml output looks like JSON:\n%s", out)
		require.Equal(t, map[string]any{
			"a": map[string]any{"y": int64(1700000000), "z": int64(1)},
			"b": "sk-access",
		}, decodeTOML(t, out))
	})
	for _, format := range []string{"yaml", "JSON", "Toml", "ini"} {
		t.Run("unknown format "+format+" is refused", func(t *testing.T) {
			f := &CredentialFile{Path: "~/.credentials", Format: format, Structure: structure}
			out, err := f.Render(credentialFileValues)
			require.ErrorContains(t, err, format)
			require.Nil(t, out)
		})
	}
}

// TestCredentialFileRenderEmptyStructure separates "nothing declared",
// which is the ErrNoCredentialStructure sentinel (a valid Kit that writes no
// file), from "an empty document declared", which renders.
func TestCredentialFileRenderEmptyStructure(t *testing.T) {
	for _, format := range []string{"", "json", "toml"} {
		t.Run("nil structure is refused/"+format, func(t *testing.T) {
			f := &CredentialFile{Path: "~/.credentials", Format: format}
			out, err := f.Render(credentialFileValues)
			require.ErrorIs(t, err, ErrNoCredentialStructure)
			require.Nil(t, out)
		})
	}
	t.Run("nil receiver is an ordinary error", func(t *testing.T) {
		var f *CredentialFile
		out, err := f.Render(credentialFileValues)
		require.Error(t, err)
		require.NotErrorIs(t, err, ErrNoCredentialStructure)
		require.Nil(t, out)
	})
	t.Run("empty structure is {} in json", func(t *testing.T) {
		require.Equal(t, "{}", string(renderCredentialFile(t, "json", map[string]any{}, credentialFileValues)))
	})
	t.Run("empty structure is an empty toml document", func(t *testing.T) {
		require.Empty(t, renderCredentialFile(t, "toml", map[string]any{}, credentialFileValues))
	})
}

// TestCredentialFileRenderRefusesTemplates pins that the placeholder
// language is five names and nothing else: no actions, no pipelines, no
// functions, and no placeholders where a key belongs. Each error must
// name where the offending text sits, so an author can find it.
func TestCredentialFileRenderRefusesTemplates(t *testing.T) {
	tests := []struct {
		name      string
		structure map[string]any
		want      string
	}{
		{name: "unknown placeholder", structure: map[string]any{"unknownLeaf": "{{.Foo}}"}, want: "unknownLeaf"},
		{name: "unknown embedded placeholder", structure: map[string]any{"embeddedLeaf": "Bearer {{.Foo}}"}, want: "embeddedLeaf"},
		{name: "names are case-sensitive", structure: map[string]any{"caseLeaf": "{{.accessToken}}"}, want: "caseLeaf"},
		{name: "if action", structure: map[string]any{"ifLeaf": "{{if .AccessToken}}yes{{end}}"}, want: "ifLeaf"},
		{name: "range action", structure: map[string]any{"rangeLeaf": "{{range .Scopes}}{{.}}{{end}}"}, want: "rangeLeaf"},
		{name: "function call", structure: map[string]any{"callLeaf": `{{printf "%s" .AccessToken}}`}, want: "callLeaf"},
		{name: "pipeline", structure: map[string]any{"pipeLeaf": "{{.AccessToken | html}}"}, want: "pipeLeaf"},
		{name: "field chain", structure: map[string]any{"chainLeaf": "{{.AccessToken.Foo}}"}, want: "chainLeaf"},
		{name: "dot", structure: map[string]any{"dotLeaf": "{{.}}"}, want: "dotLeaf"},
		{name: "comment", structure: map[string]any{"commentLeaf": "{{/* note */}}"}, want: "commentLeaf"},
		{name: "unknown placeholder in a nested map", structure: map[string]any{"outer": map[string]any{"nestedLeaf": "{{.Foo}}"}}, want: "nestedLeaf"},
		{name: "unknown placeholder in an array", structure: map[string]any{"listLeaf": []any{"ok", "{{.Foo}}"}}, want: "listLeaf"},
		{name: "unterminated action", structure: map[string]any{"openLeaf": "Bearer {{.AccessToken"}, want: "openLeaf"},
		{name: "unterminated action after a placeholder", structure: map[string]any{"tailLeaf": "{{.AccessToken}} {{"}, want: "tailLeaf"},
		{name: "unterminated action in a nested map", structure: map[string]any{"outer": map[string]any{"openNested": "{{ .RefreshToken"}}, want: "openNested"},
		{name: "unterminated action in an array", structure: map[string]any{"openList": []any{"ok", "x {{"}}, want: "openList"},
		{name: "placeholder in a key", structure: map[string]any{"{{.AccessToken}}": "v"}, want: "{{.AccessToken}}"},
		{name: "placeholder in a nested key", structure: map[string]any{"outer": map[string]any{"{{ .RefreshToken }}": "v"}}, want: "RefreshToken"},
		{name: "embedded placeholder in a key", structure: map[string]any{"key-{{.PrimaryApiKey}}": "v"}, want: "PrimaryApiKey"},
	}
	for _, tt := range tests {
		for _, format := range []string{"json", "toml"} {
			t.Run(tt.name+"/"+format, func(t *testing.T) {
				f := &CredentialFile{Path: "~/.credentials", Format: format, Structure: tt.structure}
				out, err := f.Render(credentialFileValues)
				require.ErrorContains(t, err, tt.want)
				require.Nil(t, out)
			})
		}
	}
}

// TestCredentialFileRenderNulls pins the one place the encodings differ
// in what they can represent: TOML has no null, so a null is refused
// wherever it nests, while JSON writes it.
func TestCredentialFileRenderNulls(t *testing.T) {
	tests := []struct {
		name      string
		structure map[string]any
		want      string
	}{
		{name: "top-level null", structure: map[string]any{"ok": "v", "goneLeaf": nil}, want: "goneLeaf"},
		{name: "nested null", structure: map[string]any{"outer": map[string]any{"innerLeaf": nil}}, want: "innerLeaf"},
		{name: "null array element", structure: map[string]any{"listLeaf": []any{"a", nil}}, want: "listLeaf"},
		{name: "null inside a map in an array", structure: map[string]any{"servers": []any{map[string]any{"urlLeaf": nil}}}, want: "urlLeaf"},
	}
	for _, tt := range tests {
		t.Run("toml refuses "+tt.name, func(t *testing.T) {
			f := &CredentialFile{Path: "~/.credentials", Format: "toml", Structure: tt.structure}
			out, err := f.Render(credentialFileValues)
			require.ErrorContains(t, err, tt.want)
			require.Nil(t, out)
		})
	}
	t.Run("json keeps nulls", func(t *testing.T) {
		structure := map[string]any{
			"gone":  nil,
			"list":  []any{"a", nil},
			"outer": map[string]any{"inner": nil},
		}
		require.Equal(t,
			`{"gone":null,"list":["a",null],"outer":{"inner":null}}`,
			string(renderCredentialFile(t, "json", structure, credentialFileValues)))
	})
}

// TestCredentialFileRenderTOMLBytes pins the exact TOML text for a
// structure with nested tables, not only what it decodes to: a credential
// file is read by an agent's own parser, so the layout is part of the
// contract, and determinism has to hold at the byte level.
func TestCredentialFileRenderTOMLBytes(t *testing.T) {
	structure := map[string]any{
		"z": "top",
		"auth": map[string]any{
			"token":      "{{.AccessToken}}",
			"expires_at": "{{.ExpiresAt}}",
			"scopes":     "{{.Scopes}}",
			"inner":      map[string]any{"k": true},
		},
	}
	const want = "z = 'top'\n" +
		"\n" +
		"[auth]\n" +
		"expires_at = 1700000000\n" +
		"scopes = ['user:inference', 'user:profile']\n" +
		"token = 'sk-access'\n" +
		"\n" +
		"[auth.inner]\n" +
		"k = true\n"
	require.Equal(t, want, string(renderCredentialFile(t, "toml", structure, credentialFileValues)))
}

// TestCredentialFileRenderIsDeterministic renders a structure wide enough
// that Go's randomized map iteration would reorder keys between runs if
// the encoder followed it.
func TestCredentialFileRenderIsDeterministic(t *testing.T) {
	structure := map[string]any{}
	for _, k := range strings.Split("q w e r t y u i o p a s d f g h j k l z x c v b n m", " ") {
		structure[k] = "{{.AccessToken}}-" + k
	}
	structure["nested"] = map[string]any{"b": "{{.RefreshToken}}", "a": "{{.ExpiresAt}}", "c": "x"}
	for _, format := range []string{"json", "toml"} {
		t.Run(format, func(t *testing.T) {
			first := renderCredentialFile(t, format, structure, credentialFileValues)
			for range 20 {
				require.Equal(t, string(first), string(renderCredentialFile(t, format, structure, credentialFileValues)))
			}
		})
	}
}

// TestCredentialFileRenderDoesNotMutate pins that Render works on a copy:
// the structure is the decoded descriptor, and a runtime renders it again
// on every refresh, so substituting or omitting in place would corrupt
// the next render.
func TestCredentialFileRenderDoesNotMutate(t *testing.T) {
	structure := map[string]any{
		"token": "{{.AccessToken}}",
		"key":   "{{.PrimaryApiKey}}",
		"auth": map[string]any{
			"expiresAt": "{{.ExpiresAt}}",
			"scopes":    "{{.Scopes}}",
			"key":       "{{.PrimaryApiKey}}",
		},
		"list": []any{"{{.PrimaryApiKey}}", "Bearer {{.AccessToken}}", map[string]any{"r": "{{.RefreshToken}}"}},
	}
	before := cloneStructure(structure)
	noKey := credentialFileValues
	noKey.PrimaryApiKey = ""
	for _, format := range []string{"json", "toml"} {
		for _, v := range []CredentialFileValues{credentialFileValues, noKey} {
			f := &CredentialFile{Path: "~/.credentials", Format: format, Structure: structure}
			_, err := f.Render(v)
			require.NoError(t, err)
			require.Equal(t, before, structure, "Render mutated the structure (format %q)", format)
			require.Equal(t, format, f.Format)
			require.Equal(t, "~/.credentials", f.Path)
		}
	}
}

// devinCredentialFile reads the devin example's credential file the way
// a consumer does: decode, validate, then the typed credential accessor.
func devinCredentialFile(t *testing.T) *CredentialFile {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "examples", "devin", "devin.yaml"))
	require.NoError(t, err)
	d, err := Decode(raw)
	require.NoError(t, err)
	_, err = ValidateDeclarations(raw, d)
	require.NoError(t, err)
	creds, err := CredentialsOf(DeclaredCapabilities(d.Capabilities))
	require.NoError(t, err)
	for _, c := range creds {
		if c.Service != "devin" {
			continue
		}
		require.NotNil(t, c.OAuth, "devin declares an oauth presentation")
		require.NotNil(t, c.OAuth.CredentialFile, "devin declares a credential file")
		require.Equal(t, "toml", c.OAuth.CredentialFile.Format)
		return c.OAuth.CredentialFile
	}
	require.FailNow(t, "examples/devin declares no devin credential")
	return nil
}

// TestDevinCredentialFileRendersTOML is the regression the renderer
// exists for: Devin CLI parses credentials.toml as TOML, and a JSON
// object written to that path fails its parser.
func TestDevinCredentialFileRendersTOML(t *testing.T) {
	f := devinCredentialFile(t)
	out, err := f.Render(CredentialFileValues{PrimaryApiKey: "devin-proxy-managed"})
	require.NoError(t, err)
	require.False(t, strings.HasPrefix(strings.TrimSpace(string(out)), "{"), "devin's credentials.toml was rendered as JSON:\n%s", out)
	require.Equal(t, map[string]any{
		"api_server_url":    "https://server.codeium.com",
		"devin_api_url":     "https://api.devin.ai",
		"devin_webapp_host": "app.devin.ai",
		"windsurf_api_key":  "devin-proxy-managed",
	}, decodeTOML(t, out))
	require.Equal(t, "api_server_url = 'https://server.codeium.com'\n"+
		"devin_api_url = 'https://api.devin.ai'\n"+
		"devin_webapp_host = 'app.devin.ai'\n"+
		"windsurf_api_key = 'devin-proxy-managed'\n", string(out))
}

// TestDevinCredentialFileOmitsMissingKey pins the other half of devin's
// declaration: before login captures a key, the file carries the static
// endpoints and no windsurf_api_key at all, not an empty one.
func TestDevinCredentialFileOmitsMissingKey(t *testing.T) {
	f := devinCredentialFile(t)
	out, err := f.Render(CredentialFileValues{})
	require.NoError(t, err)
	require.Equal(t, map[string]any{
		"api_server_url":    "https://server.codeium.com",
		"devin_api_url":     "https://api.devin.ai",
		"devin_webapp_host": "app.devin.ai",
	}, decodeTOML(t, out))
}

// TestCredentialFileRenderTOMLIntegersFromTypedDecoding goes through the
// path a consumer takes, where the descriptor's config is decoded via
// JSON and every number in the structure arrives as a float64. Without
// the integer rule, the TOML file would say port = 8080.0, a float where
// the author wrote an integer.
func TestCredentialFileRenderTOMLIntegersFromTypedDecoding(t *testing.T) {
	const kit = `# syntax=docker/sandbox-kit:3
schemaVersion: "3"
displayName: TOML Numbers
description: A TOML credential file with literal numbers
kind: mixin
version: "0.1.0"
provides: [toml-numbers]

capabilities:
  - type: com.docker.sandbox/credential@1
    config:
      service: example
      phase: runtime
      oauth:
        tokenEndpoint: {host: api.example.com, path: /token}
        credentialFile:
          path: ~/.example/credentials.toml
          format: toml
          structure:
            port: 8080
            negative: -3
            ratio: 1.5
            server:
              retries: 2
              token: "{{.AccessToken}}"
            sizes: [1, 2.5]
`
	d, err := Decode([]byte(kit))
	require.NoError(t, err)
	creds, err := CredentialsOf(DeclaredCapabilities(d.Capabilities))
	require.NoError(t, err)
	require.Len(t, creds, 1)
	f := creds[0].OAuth.CredentialFile
	require.IsType(t, float64(0), f.Structure["port"], "the typed path must hand Render a float64, or this test proves nothing")

	out, err := f.Render(credentialFileValues)
	require.NoError(t, err)
	require.Equal(t, map[string]any{
		"port":     int64(8080),
		"negative": int64(-3),
		"ratio":    1.5,
		"server":   map[string]any{"retries": int64(2), "token": "sk-access"},
		"sizes":    []any{int64(1), 2.5},
	}, decodeTOML(t, out))
	require.Equal(t, "negative = -3\n"+
		"port = 8080\n"+
		"ratio = 1.5\n"+
		"sizes = [1, 2.5]\n"+
		"\n"+
		"[server]\n"+
		"retries = 2\n"+
		"token = 'sk-access'\n", string(out))

	f.Format = "json"
	out, err = f.Render(credentialFileValues)
	require.NoError(t, err)
	require.Equal(t, `{"negative":-3,"port":8080,"ratio":1.5,"server":{"retries":2,"token":"sk-access"},"sizes":[1,2.5]}`, string(out))
}

// TestCredentialFileRenderRefusesUndeclarativeValues pins that a Go value
// no descriptor can decode to is refused with its location, rather than
// handed to an encoder whose two formats would disagree about it.
func TestCredentialFileRenderRefusesUndeclarativeValues(t *testing.T) {
	for _, format := range []string{"json", "toml"} {
		t.Run(format, func(t *testing.T) {
			f := &CredentialFile{Path: "~/.credentials", Format: format, Structure: map[string]any{"typedLeaf": []string{"a"}}}
			out, err := f.Render(credentialFileValues)
			require.ErrorContains(t, err, "typedLeaf")
			require.Nil(t, out)
		})
	}
}

// TestCredentialFileRenderQuotesWholeLeafWhenUnterminated pins that the
// unterminated-action error quotes the leaf as written, not the tail left
// after earlier placeholders were consumed.
func TestCredentialFileRenderQuotesWholeLeafWhenUnterminated(t *testing.T) {
	const leaf = "{{.AccessToken}} {{"
	f := &CredentialFile{Path: "~/.credentials", Structure: map[string]any{"tailLeaf": leaf}}
	_, err := f.Render(credentialFileValues)
	require.ErrorContains(t, err, "tailLeaf")
	require.ErrorContains(t, err, strconv.Quote(leaf))
}

// TestCredentialFileRenderRefusesUnlistedTypes pins the allow-list of Go
// types a structure may hold: each refused type errors with its location
// under both encodings, instead of being encoded by whatever the encoder
// happens to do with it.
func TestCredentialFileRenderRefusesUnlistedTypes(t *testing.T) {
	values := map[string]any{
		"float32": float32(1.5),
		"int8":    int8(1),
		"int16":   int16(1),
		"int32":   int32(1),
		"uint":    uint(1),
		"uint8":   uint8(1),
		"uint16":  uint16(1),
		"uint32":  uint32(1),
		"uint64":  uint64(1),
		"uintptr": uintptr(1),
	}
	for name, value := range values {
		for _, format := range []string{"json", "toml"} {
			t.Run(name+"/"+format, func(t *testing.T) {
				f := &CredentialFile{Path: "~/.credentials", Format: format, Structure: map[string]any{"outer": map[string]any{"typedLeaf": value}}}
				out, err := f.Render(credentialFileValues)
				require.ErrorContains(t, err, "structure.outer.typedLeaf")
				require.Nil(t, out)
			})
		}
	}
}

// TestCredentialFileRenderGoIntegersAreTOMLIntegers pins that the two
// integer types a plain Go map holds render as TOML integers, not floats.
func TestCredentialFileRenderGoIntegersAreTOMLIntegers(t *testing.T) {
	out := renderCredentialFile(t, "toml", map[string]any{"small": 8080, "large": int64(math.MaxInt64)}, credentialFileValues)
	require.Equal(t, map[string]any{"small": int64(8080), "large": int64(math.MaxInt64)}, decodeTOML(t, out))
	require.NotContains(t, string(out), ".")
}

// TestCredentialFileRenderJSONIsNotHTMLEscaped pins the exact bytes for a
// value holding "<", ">" and "&": they are written as they are.
func TestCredentialFileRenderJSONIsNotHTMLEscaped(t *testing.T) {
	structure := map[string]any{"b": "{{.AccessToken}}", "a": "x<y>&z"}
	v := CredentialFileValues{AccessToken: "t<&>"}
	require.Equal(t, `{"a":"x<y>&z","b":"t<&>"}`, string(renderCredentialFile(t, "json", structure, v)))
}

// TestCredentialFileRenderRefusesInvalidUTF8 pins that bytes both
// encodings would silently repair are refused instead, and that the error
// names the location and placeholder without echoing the bytes.
func TestCredentialFileRenderRefusesInvalidUTF8(t *testing.T) {
	const bad = "sk-\xffsecret"
	tests := []struct {
		name      string
		structure map[string]any
		values    CredentialFileValues
		want      string
	}{
		{name: "bad AccessToken", structure: map[string]any{"outer": map[string]any{"tokenLeaf": "{{.AccessToken}}"}}, values: CredentialFileValues{AccessToken: bad}, want: "structure.outer.tokenLeaf: the value for {{.AccessToken}}"},
		{name: "bad embedded RefreshToken", structure: map[string]any{"embedLeaf": "Bearer {{.RefreshToken}}"}, values: CredentialFileValues{RefreshToken: bad}, want: "structure.embedLeaf: the value for {{.RefreshToken}}"},
		{name: "bad PrimaryApiKey", structure: map[string]any{"keyLeaf": "{{.PrimaryApiKey}}"}, values: CredentialFileValues{PrimaryApiKey: bad}, want: "structure.keyLeaf: the value for {{.PrimaryApiKey}}"},
		{name: "bad scope", structure: map[string]any{"scopeLeaf": "{{.Scopes}}"}, values: CredentialFileValues{Scopes: []string{"ok", bad}}, want: "structure.scopeLeaf: the value for {{.Scopes}}"},
		{name: "bad literal leaf", structure: map[string]any{"literalLeaf": "plain\xc3("}, want: "structure.literalLeaf: literal text"},
		{name: "bad literal next to a placeholder", structure: map[string]any{"mixedLeaf": "\xff{{.AccessToken}}"}, want: "structure.mixedLeaf: literal text"},
		{name: "bad literal in an array", structure: map[string]any{"list": []any{"ok", "\xff"}}, want: "structure.list[1]: literal text"},
		{name: "bad key", structure: map[string]any{"outer": map[string]any{"key\xff": "v"}}, want: "structure.outer: a key is not valid UTF-8"},
	}
	for _, tt := range tests {
		for _, format := range []string{"json", "toml"} {
			t.Run(tt.name+"/"+format, func(t *testing.T) {
				f := &CredentialFile{Path: "~/.credentials", Format: format, Structure: tt.structure}
				out, err := f.Render(tt.values)
				require.ErrorContains(t, err, tt.want)
				require.NotContains(t, err.Error(), "secret")
				require.NotContains(t, err.Error(), "\xff")
				require.Nil(t, out)
			})
		}
	}
}

// TestCredentialFileRenderKeepsEmbeddedPrimaryApiKeyWhenCaptured pins the
// other side of omission: with a key captured, the embedded reference
// splices it as for any placeholder.
func TestCredentialFileRenderKeepsEmbeddedPrimaryApiKeyWhenCaptured(t *testing.T) {
	structure := map[string]any{"authorization": "Bearer {{.PrimaryApiKey}}"}
	require.Equal(t, `{"authorization":"Bearer pk-primary"}`, string(renderCredentialFile(t, "json", structure, credentialFileValues)))
}

// TestCredentialFileRenderOmissionStillValidatesTheLeaf pins that an
// omitted leaf is still judged: a mistake beside the reference is not
// hidden by the key being absent.
func TestCredentialFileRenderOmissionStillValidatesTheLeaf(t *testing.T) {
	noKey := credentialFileValues
	noKey.PrimaryApiKey = ""
	for _, leaf := range []string{"{{.PrimaryApiKey}} {{.Foo}}", "{{.PrimaryApiKey}} {{"} {
		f := &CredentialFile{Path: "~/.credentials", Structure: map[string]any{"mixedLeaf": leaf}}
		_, err := f.Render(noKey)
		require.ErrorContains(t, err, "mixedLeaf", leaf)
	}
}

// TestCredentialFileRenderRefusesUnexpandedKitReferences pins that a Kit
// reference left in the structure is named as such, since Render works on
// the expanded descriptor, rather than reported as a malformed placeholder.
func TestCredentialFileRenderRefusesUnexpandedKitReferences(t *testing.T) {
	tests := []struct {
		name      string
		structure map[string]any
		want      string
	}{
		{name: "args reference in a leaf", structure: map[string]any{"outer": map[string]any{"argLeaf": "${{ kit.args.region }}"}}, want: "structure.outer.argLeaf: the structure holds an unexpanded Kit reference"},
		{name: "env reference in a leaf", structure: map[string]any{"envLeaf": "${{kit.env.HOME}}/x"}, want: "structure.envLeaf: the structure holds an unexpanded Kit reference"},
		{name: "reference beside a placeholder", structure: map[string]any{"mixedLeaf": "{{.AccessToken}} ${{ kit.args.x }}"}, want: "structure.mixedLeaf: the structure holds an unexpanded Kit reference"},
		{name: "reference in an array element", structure: map[string]any{"list": []any{"ok", "${{ kit.args.x }}"}}, want: "structure.list[1]: the structure holds an unexpanded Kit reference"},
		{name: "args reference in a key", structure: map[string]any{"outer": map[string]any{"${{ kit.args.name }}": "v"}}, want: "structure.outer: key"},
		{name: "env reference in a key", structure: map[string]any{"${{ kit.env.NAME }}": "v"}, want: "holds an unexpanded Kit reference"},
	}
	for _, tt := range tests {
		for _, format := range []string{"json", "toml"} {
			t.Run(tt.name+"/"+format, func(t *testing.T) {
				f := &CredentialFile{Path: "~/.credentials", Format: format, Structure: tt.structure}
				out, err := f.Render(credentialFileValues)
				require.ErrorContains(t, err, tt.want)
				require.ErrorContains(t, err, "unexpanded Kit reference")
				require.NotContains(t, err.Error(), "not a placeholder")
				require.Nil(t, out)
			})
		}
	}
}

// TestCredentialFileRenderTrailingNewline pins the one layout difference
// between the encodings that the doc comment states.
func TestCredentialFileRenderTrailingNewline(t *testing.T) {
	structure := map[string]any{"a": "v"}
	require.False(t, strings.HasSuffix(string(renderCredentialFile(t, "json", structure, credentialFileValues)), "\n"))
	require.True(t, strings.HasSuffix(string(renderCredentialFile(t, "toml", structure, credentialFileValues)), "\n"))
}

// TestCredentialFileRenderReportsTheSameErrorEveryRun pins that with
// several invalid entries the error does not depend on Go's randomized map
// iteration: the first in key order is the one reported.
func TestCredentialFileRenderReportsTheSameErrorEveryRun(t *testing.T) {
	structure := map[string]any{"a": nil, "b": "{{.Foo}}", "c": "{{.Bar}}", "d": []any{"{{.Baz}}"}}
	tomlFile := &CredentialFile{Path: "~/.credentials", Format: "toml", Structure: structure}
	_, first := tomlFile.Render(credentialFileValues)
	require.ErrorContains(t, first, "structure.a")
	for range 50 {
		_, err := tomlFile.Render(credentialFileValues)
		require.EqualError(t, err, first.Error())
	}
	// Under JSON the null is legal, so the first invalid entry in key
	// order is the unknown placeholder.
	jsonFile := &CredentialFile{Path: "~/.credentials", Structure: structure}
	_, firstJSON := jsonFile.Render(credentialFileValues)
	require.ErrorContains(t, firstJSON, "structure.b")
	for range 50 {
		_, err := jsonFile.Render(credentialFileValues)
		require.EqualError(t, err, firstJSON.Error())
	}
}

// TestCredentialFileRenderSpliceErrorDescribesTheGrammar pins that the
// error for template text inside a longer string states the real grammar,
// including the optional spaces the renderer accepts.
func TestCredentialFileRenderSpliceErrorDescribesTheGrammar(t *testing.T) {
	f := &CredentialFile{Path: "~/.credentials", Structure: map[string]any{"leaf": "Bearer {{.AccessToken | html}}"}}
	_, err := f.Render(credentialFileValues)
	require.ErrorContains(t, err, "structure.leaf")
	require.ErrorContains(t, err, "optional spaces inside the braces")
	got := renderCredentialFile(t, "json", map[string]any{"leaf": "Bearer {{ .AccessToken }}"}, credentialFileValues)
	require.Equal(t, `{"leaf":"Bearer sk-access"}`, string(got))
}

// TestCredentialFileRenderRefusesNonFiniteNumbers pins that NaN and the
// infinities, which neither encoding can write portably, are refused with
// their location rather than failing in the encoder or being written as
// "nan" or "inf".
func TestCredentialFileRenderRefusesNonFiniteNumbers(t *testing.T) {
	for name, n := range map[string]float64{"NaN": math.NaN(), "+Inf": math.Inf(1), "-Inf": math.Inf(-1)} {
		for _, format := range []string{"json", "toml"} {
			for where, structure := range map[string]map[string]any{
				"top level":  {"numLeaf": n},
				"nested map": {"outer": map[string]any{"numLeaf": n}},
				"array":      {"list": []any{1, n}},
			} {
				t.Run(name+"/"+format+"/"+where, func(t *testing.T) {
					f := &CredentialFile{Path: "~/.credentials", Format: format, Structure: structure}
					out, err := f.Render(credentialFileValues)
					require.ErrorContains(t, err, "structure.")
					require.ErrorContains(t, err, "not a finite number")
					require.Nil(t, out)
				})
			}
		}
	}
	f := &CredentialFile{Path: "~/.credentials", Format: "json", Structure: map[string]any{"outer": map[string]any{"numLeaf": math.Inf(1)}}}
	_, err := f.Render(credentialFileValues)
	require.ErrorContains(t, err, "structure.outer.numLeaf")
	f.Structure = map[string]any{"list": []any{1, math.NaN()}}
	_, err = f.Render(credentialFileValues)
	require.ErrorContains(t, err, "structure.list[1]")
}

// TestCredentialFileRenderPlaceholderSpacesAreSpacesOnly pins that the
// optional padding inside the braces is U+0020 and nothing else, as the
// specification says: other whitespace is template text, not a placeholder.
func TestCredentialFileRenderPlaceholderSpacesAreSpacesOnly(t *testing.T) {
	for _, format := range []string{"json", "toml"} {
		t.Run("spaces render/"+format, func(t *testing.T) {
			out := renderCredentialFile(t, format, map[string]any{"whole": "{{ .AccessToken }}", "wide": "{{   .AccessToken   }}", "part": "Bearer {{ .AccessToken }}"}, credentialFileValues)
			if format == "json" {
				require.Equal(t, `{"part":"Bearer sk-access","whole":"sk-access","wide":"sk-access"}`, string(out))
			} else {
				require.Equal(t, map[string]any{"part": "Bearer sk-access", "whole": "sk-access", "wide": "sk-access"}, decodeTOML(t, out))
			}
		})
		for name, leaf := range map[string]string{
			"tab before the name":        "{{\t.AccessToken}}",
			"newline after the name":     "{{.AccessToken\n}}",
			"carriage return":            "{{ .AccessToken\r}}",
			"form feed":                  "{{\f.AccessToken}}",
			"tab in a longer string":     "Bearer {{\t.AccessToken}}",
			"newline in a longer string": "Bearer {{.AccessToken\n}} x",
		} {
			t.Run(name+"/"+format, func(t *testing.T) {
				f := &CredentialFile{Path: "~/.credentials", Format: format, Structure: map[string]any{"padLeaf": leaf}}
				out, err := f.Render(credentialFileValues)
				require.ErrorContains(t, err, "structure.padLeaf")
				require.ErrorContains(t, err, "is not a placeholder")
				require.Nil(t, out)
			})
		}
	}
}
