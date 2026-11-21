package spec

import (
	"encoding/json"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func clipboardEntry(config map[string]any, optional bool) Capability {
	return Capability{Type: CapabilityClipboard, Config: config, Optional: optional}
}

func types(t ...string) []any {
	out := make([]any, 0, len(t))
	for _, s := range t {
		out = append(out, s)
	}
	return out
}

func TestClipboardValidation(t *testing.T) {
	for _, config := range []map[string]any{
		nil,
		{"read": types("image/*")},
		{"write": types()},
		{"read": types(), "write": types("text/plain")},
		{"read": types("*/*"), "write": types("text/*", "image/png")},
	} {
		for _, kind := range []string{KindWorkload, KindMixin, KindSet} {
			for _, optional := range []bool{false, true} {
				d := &Descriptor{SchemaVersion: SchemaVersion, Kind: kind, Capabilities: []Capability{clipboardEntry(config, optional)}}
				if kind == KindSet {
					d.Kits = []Kit{{Ref: "example.com/workload:1"}}
				}
				_, err := Validate(d)
				require.NoError(t, err, "%v", config)
			}
		}
	}
	for value, want := range map[string]string{
		"null":                                          "config must be an object",
		`{"read":null}`:                                 "read must be a list of media types",
		`{"read":[],"write":[]}`:                        "read and write both admit nothing",
		`{"read":"image/png"}`:                          "capability " + CapabilityClipboard,
		`{"read":["Image/PNG"]}`:                        "is not a lowercase media type",
		`{"read":["text/plain;charset=utf-8"]}`:         "is not a lowercase media type",
		`{"read":["UTF8_STRING"]}`:                      "is not a lowercase media type",
		`{"read":["image"]}`:                            "is not a lowercase media type",
		`{"write":["*/png"]}`:                           "is not a lowercase media type",
		`{"read":["image/png","image/png"]}`:            "is listed twice",
		`{"read":["image/x-png"]}`:                      `is an alias; write "image/png"`,
		`{"Read":["image/png"]}`:                        `did you mean "read"?`,
		`{"read":["image/png"],"types":["text/plain"]}`: `unknown field "types"`,
	} {
		var fromYAML, fromJSON Capability
		require.NoError(t, yaml.Unmarshal([]byte("type: "+CapabilityClipboard+"\nconfig: "+value+"\n"), &fromYAML))
		require.NoError(t, json.Unmarshal([]byte(`{"type":"`+CapabilityClipboard+`","config":`+value+`}`), &fromJSON))
		for _, entry := range []Capability{fromYAML, fromJSON} {
			_, err := Validate(&Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{entry}})
			require.ErrorContains(t, err, want, "config %s", value)
		}
	}
	// One entry carries both directions; a second is a duplicate, not a
	// way to state another filter.
	_, err := Validate(&Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{
		clipboardEntry(nil, false), clipboardEntry(map[string]any{"read": types("image/*")}, false),
	}})
	require.ErrorContains(t, err, "already declared")
}

// A config built in Go states its lists as []string, and a nil slice
// encodes as null; both are judged as the documents they encode as.
func TestClipboardGoBuiltConfig(t *testing.T) {
	const ref = "${{ kit.args.s }}"
	d := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindWorkload, Provides: []string{"claude@2.1.0"},
		Args: map[string]Arg{"s": {Required: true}}}
	for _, tc := range []struct {
		name   string
		config map[string]any
		want   string
	}{
		{name: "literal list", config: map[string]any{"read": []string{"image/*"}, "write": []string{}}},
		{name: "parameterized list", config: map[string]any{"read": []string{ref}}},
		{name: "nil literal list", config: map[string]any{"read": []string(nil)}, want: "read must be a list of media types"},
		{name: "nil list beside a reference", config: map[string]any{"read": []string{ref}, "write": []string(nil)}, want: "write must be a list of media types"},
		{name: "invalid literal in a Go list", config: map[string]any{"read": []string{ref, "BAD"}}, want: "is not a lowercase media type"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d.Capabilities = []Capability{clipboardEntry(tc.config, false)}
			_, err := Validate(d)
			if tc.want == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.want)
		})
	}
}

// A reference defers the typed pass to create, so what a placeholder
// cannot hide is judged at publication: each direction is a list, literal
// entries are media types, and the rest wait for their values.
func TestClipboardParameterizedValidation(t *testing.T) {
	const ref = "${{ kit.args.s }}"
	for _, tc := range []struct {
		name      string
		config    map[string]any
		s         string
		published string // empty when publication accepts the entry
		created   string // empty when the expanded entry is valid
	}{
		{name: "reference entry", config: map[string]any{"read": types(ref)}, s: "image/png"},
		{name: "beside literals", config: map[string]any{"read": types("image/*", ref), "write": types()}, s: "text/plain"},
		{name: "expands to an invalid type", config: map[string]any{"read": types(ref)}, s: "Image", created: "is not a lowercase media type"},
		{name: "expands to a duplicate", config: map[string]any{"read": types("image/png", ref)}, s: "image/png", created: "is listed twice"},
		{name: "expands to an alias", config: map[string]any{"read": types(ref)}, s: "image/x-png", created: "is an alias"},
		{name: "alias literal beside a reference", config: map[string]any{"read": types(ref, "image/x-png")}, s: "image/png", published: "is an alias"},
		{name: "direction as a reference", config: map[string]any{"read": ref}, s: "image/png", published: "read must be a list of media types"},
		{name: "null direction", config: map[string]any{"read": types(ref), "write": nil}, s: "image/png", published: "write must be a list of media types"},
		{name: "non-string entry", config: map[string]any{"read": []any{ref, 1}}, s: "image/png", published: "a media type is a string"},
		{name: "invalid literal beside a reference", config: map[string]any{"read": types(ref, "BAD")}, s: "image/png", published: "is not a lowercase media type"},
		{name: "duplicate literal beside a reference", config: map[string]any{"read": types("text/plain", ref, "text/plain")}, s: "image/png", published: "is listed twice"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := parameterizedKit(t, CapabilityClipboard, tc.config, tc.s)
			err := validatePublished(t, raw)
			if tc.published != "" {
				require.ErrorContains(t, err, tc.published)
				return
			}
			require.NoError(t, err)
			err = validateCreated(t, raw)
			if tc.created != "" {
				require.ErrorContains(t, err, tc.created)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestClipboardFilter(t *testing.T) {
	filter := func(t ...string) Clipboard { return Clipboard{Read: &t} }
	require.Equal(t, ClipboardFilter{All: true}, Clipboard{}.Filter(ClipboardRead), "activation alone is a normal clipboard")
	require.Equal(t, ClipboardFilter{All: true}, Clipboard{}.Filter(ClipboardWrite))
	require.False(t, filter().Filter(ClipboardRead).Enabled(), "an empty list turns the direction off")
	require.Equal(t, ClipboardFilter{All: true}, filter("image/png", "*/*").Filter(ClipboardRead))
	require.Equal(t, ClipboardFilter{Types: []string{"image/*", "text/plain"}},
		filter("text/plain", "image/png", "image/*", "text/plain").Filter(ClipboardRead), "covered entries drop")
	// Unvalidated input keeps admission and the surface consistent: an
	// alias entry is the type it names.
	alias := filter("image/x-png", "image/png").Filter(ClipboardRead)
	require.Equal(t, ClipboardFilter{Types: []string{"image/png"}}, alias)
	require.True(t, alias.Admits("image/png"))
	require.True(t, alias.Admits("image/x-png"))
	require.Equal(t, []string{"read image/png", "write */*"}, clipboardSurface(filter("image/x-png")))

	for name, want := range map[string]string{
		"text/plain;charset=utf-8": "text/plain",
		"Text/HTML":                "text/html",
		"UTF8_STRING":              "text/plain",
		"STRING":                   "text/plain",
		"TEXT":                     "text/plain",
		"image/x-png":              "image/png",
		"image/png":                "image/png",
	} {
		require.Equal(t, want, CanonicalMediaType(name), name)
	}

	images := filter("image/*").Filter(ClipboardRead)
	require.True(t, images.Admits("image/png"))
	require.True(t, images.Admits("image/x-png"))
	require.False(t, images.Admits("text/plain"))
	require.False(t, images.Admits("UTF8_STRING"), "an alias is judged as the type it names")
	text := filter("text/plain").Filter(ClipboardRead)
	require.True(t, text.Admits("UTF8_STRING"))
	require.True(t, text.Admits("text/plain;charset=utf-8"))
	require.False(t, text.Admits("text/html"))
	require.False(t, filter().Filter(ClipboardRead).Admits("text/plain"))
	require.True(t, Clipboard{}.Filter(ClipboardRead).Admits("application/x-anything"))
}

func TestClipboardMergeUnionsFilters(t *testing.T) {
	contribution := func(ref, kind string, entry Capability) Contribution {
		return Contribution{Reference: ref, Descriptor: &Descriptor{SchemaVersion: SchemaVersion, Kind: kind, Capabilities: []Capability{entry}}}
	}
	for _, tc := range []struct {
		name            string
		workload, mixin Capability
		want            map[string]any
		wantOptional    bool
	}{
		{name: "full clipboards collapse", workload: clipboardEntry(nil, false), mixin: clipboardEntry(nil, false), want: nil},
		{name: "a full direction absorbs a filter", workload: clipboardEntry(map[string]any{"read": types("image/*")}, false), mixin: clipboardEntry(nil, false), want: nil},
		{name: "one direction each", workload: clipboardEntry(map[string]any{"write": types()}, false), mixin: clipboardEntry(map[string]any{"read": types()}, false), want: nil},
		{
			name:     "filters union",
			workload: clipboardEntry(map[string]any{"read": types("image/*"), "write": types()}, false),
			mixin:    clipboardEntry(map[string]any{"read": types("text/plain", "image/png"), "write": types("text/*")}, false),
			want:     map[string]any{"read": types("image/*", "text/plain"), "write": types("text/*")},
		},
		{
			name:     "an empty direction stays empty",
			workload: clipboardEntry(map[string]any{"read": types("image/*"), "write": types()}, true),
			mixin:    clipboardEntry(map[string]any{"read": types("image/png"), "write": types()}, true),
			want:     map[string]any{"read": types("image/*"), "write": types()}, wantOptional: true,
		},
		{name: "required wins", workload: clipboardEntry(nil, true), mixin: clipboardEntry(map[string]any{"read": types()}, false), want: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workload := contribution("workload", KindWorkload, tc.workload)
			mixin := contribution("mixin", KindMixin, tc.mixin)
			for _, inputs := range [][]Contribution{{workload, mixin}, {mixin, workload}} {
				merged := mergeOK(t, inputs...).Descriptor
				_, err := Validate(merged)
				require.NoError(t, err)
				require.Len(t, merged.Capabilities, 1)
				require.Equal(t, CapabilityClipboard, merged.Capabilities[0].Type)
				require.Equal(t, tc.want, merged.Capabilities[0].Config)
				require.Equal(t, tc.wantOptional, merged.Capabilities[0].Optional)
			}
		})
	}
}

// Contributions decoded from documents carry a config-presence flag that
// entries built in Go do not, so the union is held to validation through
// the decoder too.
func TestClipboardMergeOfDecodedEntries(t *testing.T) {
	for _, tc := range []struct {
		name, workload, mixin string
		want                  map[string]any
	}{
		{name: "one direction each", workload: "{write: []}", mixin: "{read: []}", want: nil},
		{name: "a filter absorbed", workload: "{read: [image/*]}", mixin: "{read: [text/plain]}", want: map[string]any{"read": types("image/*", "text/plain")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kit := func(ref, config string) Contribution {
				return contribute(t, ref, "schemaVersion: \"3\"\nkind: mixin\ncapabilities:\n  - type: "+CapabilityClipboard+"\n    config: "+config+"\n")
			}
			for _, inputs := range [][]Contribution{{kit("a", tc.workload), kit("b", tc.mixin)}, {kit("b", tc.mixin), kit("a", tc.workload)}} {
				merged := mergeOK(t, inputs...).Descriptor
				_, err := Validate(merged)
				require.NoError(t, err)
				require.Equal(t, tc.want, merged.Capabilities[0].Config)
				raw, err := json.Marshal(merged)
				require.NoError(t, err)
				reread, err := Decode(raw)
				require.NoError(t, err, "the merged descriptor reads back")
				_, err = Validate(reread)
				require.NoError(t, err)
			}
		})
	}
}

func TestClipboardSurface(t *testing.T) {
	surface := func(config map[string]any) Surface {
		return SurfaceOf(&Descriptor{Capabilities: []Capability{clipboardEntry(config, false)}})
	}
	full := surface(nil)
	images := surface(map[string]any{"read": types("image/*"), "write": types()})
	png := surface(map[string]any{"read": types("image/png"), "write": types()})
	writeOnly := surface(map[string]any{"read": types()})
	require.Equal(t, Surface{Clipboard: []string{"read */*", "write */*"}}, full, "no implied egress, port, or service grants")
	require.Equal(t, Surface{Clipboard: []string{"read image/*"}}, images)
	require.Equal(t, Surface{Clipboard: []string{"write */*"}}, writeOnly)

	require.Equal(t, []Widening{{Category: "clipboard", Detail: "read */*"}, {Category: "clipboard", Detail: "write */*"}}, DiffWidenings(Surface{}, full))
	require.Empty(t, DiffWidenings(full, images), "narrowing a filter is not a widening")
	require.Empty(t, DiffWidenings(images, png))
	require.Equal(t, []Widening{{Category: "clipboard", Detail: "read image/*"}}, DiffWidenings(png, images))
	require.Equal(t, []Widening{{Category: "clipboard", Detail: "read */*"}, {Category: "clipboard", Detail: "write */*"}}, DiffWidenings(images, full))
	require.Equal(t, []Widening{{Category: "clipboard", Detail: "write */*"}}, DiffWidenings(images, writeOnly), "one direction never covers the other")

	// Optionality and display metadata change nothing granted.
	entry := clipboardEntry(nil, true)
	entry.Name, entry.Description = "Host clipboard", "Paste screenshots"
	require.Equal(t, full, SurfaceOf(&Descriptor{Capabilities: []Capability{entry}}))
}

func TestClipboardSelection(t *testing.T) {
	for _, optional := range []bool{false, true} {
		d := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{clipboardEntry(nil, optional)}}
		selected, err := SelectCapabilities(t.Context(), "clipboard", d, Supported(CapabilityClipboard))
		require.NoError(t, err)
		require.Len(t, selected.Capabilities, 1)
		require.Equal(t, optional, selected.Capabilities[0].Optional)
		selected, err = SelectCapabilities(t.Context(), "clipboard", d, Supported())
		if optional {
			require.NoError(t, err)
			require.Empty(t, selected.Capabilities)
			require.NotEmpty(t, selected.Skipped)
		} else {
			require.ErrorContains(t, err, CapabilityClipboard)
		}
	}
}

// The schema states the same grammar: optional config, two lists of
// patterns the Go regex accepts or reference-bearing strings, and no
// entry turning both directions off.
func TestClipboardSchema(t *testing.T) {
	schema := loadJSON(t, perTypeSchemaPath(CapabilityClipboard))
	properties := at(t, schema, "properties")
	require.Len(t, properties, 2)
	for _, field := range []string{"read", "write"} {
		require.Equal(t, "#/definitions/filter", at(t, properties, field)["$ref"])
	}
	filter := at(t, schema, "definitions", "filter")
	require.Equal(t, "array", filter["type"])
	require.Equal(t, true, filter["uniqueItems"])
	items := at(t, filter, "items")["anyOf"].([]any)
	require.Len(t, items, 2)
	pattern := items[0].(map[string]any)["pattern"].(string)
	require.Equal(t, clipboardPattern.String(), pattern)
	compiled := regexp.MustCompile(pattern)
	for _, s := range []string{"image/png", "image/*", "*/*", "text/plain", "application/vnd.oasis.opendocument.text"} {
		require.True(t, compiled.MatchString(s), s)
	}
	for _, s := range []string{"Image/png", "*/png", "image", "text/plain;charset=utf-8", "UTF8_STRING"} {
		require.False(t, compiled.MatchString(s), s)
	}
	// The aliases the pattern would admit are refused beside it, as
	// validation refuses them; the rest fail the pattern already.
	var shaped []any
	for alias := range clipboardAliases {
		if clipboardPattern.MatchString(alias) {
			shaped = append(shaped, alias)
		}
	}
	require.NotEmpty(t, shaped)
	require.ElementsMatch(t, shaped, at(t, items[0].(map[string]any), "not")["enum"])
	require.Equal(t, "../../definitions/kit-arg.schema.json#/definitions/bearing", items[1].(map[string]any)["$ref"])
	require.Equal(t, []any{"read", "write"}, at(t, schema, "not")["required"])
	for _, field := range []string{"read", "write"} {
		require.EqualValues(t, 0, at(t, schema, "not", "properties", field)["maxItems"])
	}

	for _, c := range at(t, loadSchema(t), "definitions", "capability")["allOf"].([]any) {
		cond := c.(map[string]any)
		if at(t, cond, "if", "properties", "type")["const"] == CapabilityClipboard {
			require.Nil(t, at(t, cond, "then")["required"], "activation alone is a valid entry")
		}
	}
}

// A set re-exporting an arg into a clipboard filter publishes before the
// value exists, so the entry waits for create: canonicalizing it now would
// fold the reference's case into a different arg, and unioning it would
// judge a placeholder as a media type.
func TestClipboardReExportWaitsForCreate(t *testing.T) {
	const ref = "${{ kit.args.MediaType }}"
	reexported := Contribution{Reference: "paste", Descriptor: &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin,
		Args:         map[string]Arg{"MediaType": {Required: true}},
		Capabilities: []Capability{clipboardEntry(map[string]any{"read": types(ref), "write": types()}, false)}}}
	copying := Contribution{Reference: "copy", Descriptor: &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin,
		Capabilities: []Capability{clipboardEntry(map[string]any{"read": types(), "write": types("text/*")}, false)}}}
	for _, contributions := range [][]Contribution{{reexported, copying}, {copying, reexported}} {
		published := mergeOK(t, contributions...).Descriptor
		raw, err := json.Marshal(published)
		require.NoError(t, err)
		require.Contains(t, string(raw), ref, "the reference is published as written")
		_, err = Validate(published)
		require.NoError(t, err, "each request keeps its own declaration block until create")

		// At create, each Kit's value is known and the filters union.
		expanded := make([]Contribution, len(contributions))
		for i, c := range contributions {
			d := *c.Descriptor
			d.Capabilities = []Capability{c.Descriptor.Capabilities[0]}
			if c.Reference == "paste" {
				d.Capabilities[0].Config = map[string]any{"read": types("image/png"), "write": types()}
			}
			expanded[i] = Contribution{Reference: c.Reference, Descriptor: &d}
		}
		composed, err := Compose(expanded)
		require.NoError(t, err)
		_, err = Validate(composed)
		require.NoError(t, err)
		require.Equal(t, map[string]any{"read": types("image/png"), "write": types("text/*")}, composed.Capabilities[0].Config)
	}
}
