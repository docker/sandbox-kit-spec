package spec

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func nativeChatGPTDescriptor(path string) *Descriptor {
	return &Descriptor{SchemaVersion: SchemaVersion, Kind: KindWorkload, Capabilities: []Capability{{
		Type: CapabilityOpenAIChatGPT, Name: "ChatGPT", Description: "Native account access", Config: map[string]any{"path": path},
	}}}
}

func genericOpenAI(phase any) Capability {
	return Capability{Type: CapabilityCredential, Config: map[string]any{
		"service": "openai", "phase": phase, "apiKey": map[string]any{"name": "OPENAI_API_KEY", "proxyManaged": true},
	}}
}

func TestOpenAIChatGPTGrammar(t *testing.T) {
	for _, path := range []string{"~/.codex/auth.json", "/home/agent/.codex/auth.json", "/auth.json"} {
		d := nativeChatGPTDescriptor(path)
		_, err := Validate(d)
		require.NoError(t, err)
		entries, err := OpenAIChatGPTOf(d.Capabilities)
		require.NoError(t, err)
		require.Equal(t, []OpenAIChatGPTCapability{{OpenAIChatGPT: OpenAIChatGPT{Path: path}, Required: true, Name: "ChatGPT", Description: "Native account access"}}, entries)
		generic, err := CredentialsOf(d.Capabilities)
		require.NoError(t, err)
		require.Empty(t, generic, "generic credential consumers must explicitly adopt native authentication")
		key, arity, err := InstanceKeyOf(d.Capabilities[0])
		require.NoError(t, err)
		require.Equal(t, ArityInstance, arity)
		require.Equal(t, InstanceKey{Space: CapabilityCredential, Parts: []string{"openai", "runtime"}}, key)
	}
	for _, config := range []map[string]any{
		nil, {}, {"path": nil}, {"path": 12}, {"path": ""}, {"path": "/"}, {"path": "~/"}, {"path": "relative/auth.json"},
		{"path": "~other/auth.json"}, {"path": "/auth/"}, {"path": "/au\x00th"}, {"path": "/auth\n.json"},
		{"path": "/auth", "service": "openai"}, {"path": "/auth", "phase": "runtime"},
		{"path": "/auth", "onMissing": "unauthenticated"}, {"path": "/auth", "passthrough": true},
		{"path": "/auth", "identityFields": []string{"email"}},
	} {
		d := nativeChatGPTDescriptor("/auth")
		d.Capabilities[0].Config = config
		_, err := Validate(d)
		require.Error(t, err, "config %#v", config)
	}
	_, err := OpenAIChatGPTOf([]Capability{{Type: CapabilityOpenAIChatGPT, Config: map[string]any{"unknown": true}}})
	require.ErrorContains(t, err, "unknown field")
}

func TestOpenAIChatGPTRequiresExactRuntimeSupport(t *testing.T) {
	for _, optional := range []bool{false, true} {
		d := nativeChatGPTDescriptor("~/.codex/auth.json")
		d.Capabilities[0].Optional = optional
		selected, err := SelectCapabilities(t.Context(), "native", d, Supported(CapabilityCredential))
		if optional {
			require.NoError(t, err)
			require.Empty(t, selected.Capabilities)
			require.Len(t, selected.Skipped, 1)
		} else {
			require.ErrorContains(t, err, CapabilityOpenAIChatGPT)
		}
		selected, err = SelectCapabilities(t.Context(), "native", d, Supported(CapabilityOpenAIChatGPT))
		require.NoError(t, err)
		require.Len(t, selected.Capabilities, 1)
		entries, err := OpenAIChatGPTOf(selected.Capabilities)
		require.NoError(t, err)
		require.Equal(t, !optional, entries[0].Required)
	}
	old := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindWorkload, Capabilities: []Capability{genericOpenAI("runtime")}}
	selected, err := SelectCapabilities(t.Context(), "old", old, Supported(CapabilityCredential))
	require.NoError(t, err)
	creds, err := CredentialsOf(selected.Capabilities)
	require.NoError(t, err)
	require.Len(t, creds, 1)
	require.Equal(t, "OPENAI_API_KEY", creds[0].APIKey.Name)
}

func TestOpenAIChatGPTHasOneCredentialOwner(t *testing.T) {
	for _, phase := range []any{"runtime", []string{"install", "runtime"}} {
		for _, reverse := range []bool{false, true} {
			for _, parameterized := range []bool{false, true} {
				for _, grouped := range []bool{false, true} {
					native := nativeChatGPTDescriptor("/auth.json")
					if parameterized {
						native.Args = map[string]Arg{"path": {}}
						native.Capabilities[0].Config["path"] = "${{ kit.args.path }}"
					}
					generic := genericOpenAI(phase)
					items := []Capability{native.Capabilities[0], generic}
					if reverse {
						items[0], items[1] = items[1], items[0]
					}
					both := *native
					both.Capabilities = items
					if grouped {
						both.Capabilities = []Capability{{Group: &CapabilityGroup{Optional: true, Capabilities: items}}}
					}
					_, err := Validate(&both)
					require.ErrorContains(t, err, "already declared")
					if !grouped {
						var fieldErr *FieldError
						require.ErrorAs(t, err, &fieldErr)
						if reverse {
							require.Equal(t, "capabilities[1].type", fieldErr.Path)
						} else {
							require.Equal(t, "capabilities[1].config.service", fieldErr.Path)
						}
					}
					other := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{generic}}
					parts := []Contribution{{Reference: "native", Descriptor: native}, {Reference: "generic", Descriptor: other}}
					if reverse {
						parts[0], parts[1] = parts[1], parts[0]
					}
					_, err = Merge(parts, MergeOptions{})
					require.ErrorContains(t, err, "one credential has one owner")
					if !parameterized {
						_, err = Compose(parts)
						require.ErrorContains(t, err, "one credential has one owner")
					}
				}
			}
		}
	}
	native := nativeChatGPTDescriptor("/auth.json")
	other := nativeChatGPTDescriptor("/different.json")
	other.Kind = KindMixin
	_, err := Compose([]Contribution{{Reference: "native", Descriptor: native}, {Reference: "other", Descriptor: other}})
	require.ErrorContains(t, err, "one credential has one owner")
	other.Capabilities = []Capability{genericOpenAI("install")}
	composed, err := Compose([]Contribution{{Reference: "native", Descriptor: native}, {Reference: "install", Descriptor: other}})
	require.NoError(t, err)
	require.Len(t, composed.Capabilities, 2)
}

func TestOpenAIChatGPTPermissionAndPersistence(t *testing.T) {
	d := nativeChatGPTDescriptor("~/.codex/auth.json")
	old := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindWorkload, Capabilities: []Capability{genericOpenAI("runtime")}}
	surface := SurfaceOf(d)
	require.Equal(t, []string{"openai"}, surface.CredentialsRuntime)
	require.Empty(t, surface.CredentialsInstall)
	widenings := DiffWidenings(SurfaceOf(old), surface)
	require.Len(t, widenings, 1)
	require.Equal(t, "services", widenings[0].Category)
	require.Contains(t, widenings[0].Detail, CapabilityOpenAIChatGPT)
	for _, optional := range []bool{false, true} {
		d.Capabilities[0].Optional = optional
		raw, err := json.Marshal(d)
		require.NoError(t, err)
		decoded, err := Decode(raw)
		require.NoError(t, err)
		_, err = ValidatePublished(raw, decoded)
		require.NoError(t, err)
		selected, err := SelectCapabilities(t.Context(), "native", decoded, Supported(CapabilityOpenAIChatGPT))
		require.NoError(t, err)
		stored, err := json.Marshal(selected)
		require.NoError(t, err)
		var restored Selection
		require.NoError(t, json.Unmarshal(stored, &restored))
		decoded.Capabilities = restored.Capabilities
		composed, err := Compose([]Contribution{{Reference: "native", Descriptor: decoded}})
		require.NoError(t, err)
		entries, err := OpenAIChatGPTOf(composed.Capabilities)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		require.Equal(t, "~/.codex/auth.json", entries[0].Path)
		require.Equal(t, !optional, entries[0].Required)
		require.Equal(t, "ChatGPT", entries[0].Name)
		require.Equal(t, "Native account access", entries[0].Description)
		require.Equal(t, surface, SurfaceOf(composed))
	}
}

func TestOpenAIChatGPTPathExpansion(t *testing.T) {
	d := nativeChatGPTDescriptor("${{ kit.args.path }}")
	d.Args = map[string]Arg{"path": {}}
	_, err := Validate(d)
	require.NoError(t, err)
	raw, err := json.Marshal(d)
	require.NoError(t, err)
	for _, path := range []string{"/home/agent/.codex/auth.json", "relative", ""} {
		expanded, err := ExpandCreateArgs(raw, d.Args, map[string]string{"path": path})
		require.NoError(t, err)
		decoded, err := Decode(expanded)
		require.NoError(t, err)
		_, err = ValidateEffective(expanded, decoded)
		if path == "/home/agent/.codex/auth.json" {
			require.NoError(t, err)
			entries, readErr := OpenAIChatGPTOf(decoded.Capabilities)
			require.NoError(t, readErr)
			require.Equal(t, path, entries[0].Path)
		} else {
			require.Error(t, err)
		}
	}
}

func TestOpenAIChatGPTSchema(t *testing.T) {
	schema := loadJSON(t, perTypeSchemaPath(CapabilityOpenAIChatGPT))
	require.Equal(t, []any{"path"}, schema["required"])
	require.Equal(t, false, schema["additionalProperties"])
	path := at(t, schema, "properties", "path")
	require.Equal(t, openAIChatGPTPath.String(), literalPattern(t, path))
	assertAcceptsKitArg(t, path, "bearing")
}
