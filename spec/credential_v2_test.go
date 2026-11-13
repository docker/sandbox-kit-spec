package spec

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func nativeCredentialV2() Capability {
	return Capability{Type: CapabilityCredentialV2, Config: map[string]any{
		"service": "openai", "phase": "runtime", "onMissing": CredentialOnMissingUnauthenticated,
		"oauth": map[string]any{
			"profile":       OAuthProfileOpenAIChatGPT,
			"tokenEndpoint": map[string]any{"host": "auth.openai.com", "path": "/oauth/token"},
			"sentinels":     map[string]any{"accessToken": "openai-access-proxy-managed", "refreshToken": "openai-refresh-proxy-managed"},
			"credentialFile": map[string]any{"path": "~/.codex/auth.json", "structure": map[string]any{
				"auth_mode": "chatgpt", "tokens": map[string]any{
					"id_token": "{{.IDToken}}", "access_token": "{{.AccessToken}}", "refresh_token": "{{.RefreshToken}}", "account_id": "{{.AccountID}}",
				}, "last_refresh": "{{.LastRefresh}}",
			}},
		},
	}}
}

func credentialV2Descriptor(needs ...Capability) *Descriptor {
	return &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: needs}
}

func validateCredentialV2Test(t *testing.T, d *Descriptor, want string) {
	t.Helper()
	raw, err := json.Marshal(d)
	require.NoError(t, err)
	decoded, err := Decode(raw)
	require.NoError(t, err)
	for _, validate := range []func([]byte, *Descriptor) ([]string, error){ValidateRaw, ValidatePublished, ValidateEffective} {
		_, err := validate(raw, decoded)
		if want == "" {
			require.NoError(t, err)
		} else {
			require.ErrorContains(t, err, want)
		}
	}
}

func TestCredentialV2NativeValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(map[string]any, map[string]any)
		want   string
	}{
		{"native", func(_, _ map[string]any) {}, ""},
		{"default missing policy", func(c, _ map[string]any) { delete(c, "onMissing") }, ""},
		{"error missing policy", func(c, _ map[string]any) { c["onMissing"] = CredentialOnMissingError }, ""},
		{"empty missing policy", func(c, _ map[string]any) { c["onMissing"] = "" }, "onMissing is empty"},
		{"unknown missing policy", func(c, _ map[string]any) { c["onMissing"] = "skip" }, "unknown onMissing"},
		{"null missing policy", func(c, _ map[string]any) { c["onMissing"] = nil }, "onMissing is null"},
		{"unknown profile", func(_, o map[string]any) { o["profile"] = "other" }, "unknown OAuth profile"},
		{"empty profile", func(_, o map[string]any) { o["profile"] = "" }, "profile is empty"},
		{"null profile", func(_, o map[string]any) { o["profile"] = nil }, "oauth.profile is null"},
		{"case variant", func(_, o map[string]any) { o["Profile"] = o["profile"]; delete(o, "profile") }, "unknown field"},
		{"unknown field", func(_, o map[string]any) { o["identityClaims"] = []any{"sub"} }, "unknown field"},
		{"other service", func(c, _ map[string]any) { c["service"] = "anthropic" }, "requires service openai"},
		{"missing endpoint", func(_, o map[string]any) { delete(o, "tokenEndpoint") }, "requires tokenEndpoint"},
		{"other host", func(_, o map[string]any) {
			o["tokenEndpoint"] = map[string]any{"host": "auth.example.com", "path": "/oauth/token"}
		}, "requires tokenEndpoint"},
		{"other path", func(_, o map[string]any) {
			o["tokenEndpoint"] = map[string]any{"host": "auth.openai.com", "path": "/oauth/authorize"}
		}, "requires tokenEndpoint"},
		{"missing sentinels", func(_, o map[string]any) { delete(o, "sentinels") }, "requires nonempty"},
		{"one sentinel", func(_, o map[string]any) { o["sentinels"] = map[string]any{"accessToken": "access-marker"} }, "requires nonempty"},
		{"empty sentinel", func(_, o map[string]any) {
			o["sentinels"] = map[string]any{"accessToken": "", "refreshToken": "refresh"}
		}, "requires nonempty"},
		{"jwt sentinel", func(_, o map[string]any) {
			o["sentinels"] = map[string]any{"accessToken": "head.payload.sig", "refreshToken": "refresh"}
		}, "requires nonempty"},
		{"whitespace sentinel", func(_, o map[string]any) {
			o["sentinels"] = map[string]any{"accessToken": "access\n", "refreshToken": "refresh"}
		}, "requires nonempty"},
		{"same sentinel", func(_, o map[string]any) {
			o["sentinels"] = map[string]any{"accessToken": "same", "refreshToken": "same"}
		}, "requires distinct"},
		{"passthrough", func(_, o map[string]any) { o["passthrough"] = true }, "does not permit passthrough"},
		{"false passthrough", func(_, o map[string]any) { o["passthrough"] = false }, ""},
		{"nonstandard mapping", func(_, o map[string]any) { o["responseFields"] = map[string]any{"refreshToken": "token"} }, "standard OAuth response field names"},
		{"empty mapping", func(_, o map[string]any) { o["responseFields"] = map[string]any{"refreshToken": ""} }, "standard OAuth response field names"},
		{"standard mapping", func(_, o map[string]any) {
			o["responseFields"] = map[string]any{"accessToken": "access_token", "refreshToken": "refresh_token", "expiresIn": "expires_in"}
		}, ""},
		{"cleartext API key", func(c, _ map[string]any) { c["apiKey"] = map[string]any{"name": "OPENAI_API_KEY"} }, "requires proxyManaged"},
		{"proxy API key", func(c, _ map[string]any) {
			c["apiKey"] = map[string]any{"name": "OPENAI_API_KEY", "proxyManaged": true}
		}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entry := nativeCredentialV2()
			tc.change(entry.Config, entry.Config["oauth"].(map[string]any))
			validateCredentialV2Test(t, credentialV2Descriptor(entry), tc.want)
		})
	}
}

func TestCredentialV2PlaceholderProfiles(t *testing.T) {
	for _, native := range []bool{false, true} {
		for _, placeholder := range []string{"IDToken", "AccessToken", "RefreshToken", "AccountID", "LastRefresh", "ExpiresAt", "Scopes", "PrimaryApiKey", "Other"} {
			t.Run(fmt.Sprintf("native=%t/%s", native, placeholder), func(t *testing.T) {
				entry := nativeCredentialV2()
				oauth := entry.Config["oauth"].(map[string]any)
				if !native {
					delete(oauth, "profile")
				}
				oauth["credentialFile"].(map[string]any)["structure"] = map[string]any{"nested": []any{map[string]any{"value": "prefix {{." + placeholder + "}} suffix"}}}
				allowed := placeholder == "AccessToken" || placeholder == "RefreshToken" || (native && (placeholder == "IDToken" || placeholder == "AccountID" || placeholder == "LastRefresh")) || (!native && (placeholder == "ExpiresAt" || placeholder == "Scopes" || placeholder == "PrimaryApiKey"))
				want := "unsupported credential-file placeholder"
				if allowed {
					want = ""
				}
				validateCredentialV2Test(t, credentialV2Descriptor(entry), want)
			})
		}
	}
}

func TestCredentialV2VersionIsolation(t *testing.T) {
	generic := Capability{Type: CapabilityCredentialV2, Config: map[string]any{
		"service": "openai", "phase": "runtime", "oauth": map[string]any{
			"tokenEndpoint": map[string]any{"host": "legacy.example.com", "path": "/token"},
			"passthrough":   true, "responseFields": map[string]any{"refreshToken": "token"},
			"sentinels": map[string]any{"refreshToken": "legacy.marker"},
		},
	}}
	validateCredentialV2Test(t, credentialV2Descriptor(generic), "")
	legacy := generic
	legacy.Type = CapabilityCredential
	validateCredentialV2Test(t, credentialV2Descriptor(legacy), "")
	old, err := CredentialsOf([]Capability{nativeCredentialV2(), generic, legacy})
	require.NoError(t, err)
	require.Len(t, old, 1)
	require.True(t, old[0].Required)
	require.True(t, old[0].OAuth.Passthrough)
	current, err := CredentialsV2Of([]Capability{nativeCredentialV2(), legacy, generic})
	require.NoError(t, err)
	require.Len(t, current, 2)
	require.Equal(t, OAuthProfileOpenAIChatGPT, current[0].OAuth.Profile)
	require.True(t, current[0].Required)
	require.Equal(t, CredentialOnMissingUnauthenticated, current[0].EffectiveOnMissing())
	require.Empty(t, current[1].OAuth.Profile)
	require.Equal(t, CredentialOnMissingError, current[1].EffectiveOnMissing())
	filtered, err := CredentialsV2OfPhase([]Capability{nativeCredentialV2(), legacy}, "install")
	require.NoError(t, err)
	require.Empty(t, filtered)
	for _, field := range []string{"onMissing", "profile"} {
		t.Run("v1 refuses "+field, func(t *testing.T) {
			entry := nativeCredentialV2()
			entry.Type = CapabilityCredential
			if field == "profile" {
				delete(entry.Config, "onMissing")
			}
			validateCredentialV2Test(t, credentialV2Descriptor(entry), "unknown field")
		})
	}
}

func TestCredentialV2CompositionAndPersistence(t *testing.T) {
	entry := nativeCredentialV2()
	entry.Config["phase"] = []any{"install", "runtime"}
	entry.Name, entry.Description = "OpenAI account", "Native identity"
	before, err := json.Marshal(entry)
	require.NoError(t, err)
	composed, err := Compose([]Contribution{{Reference: "native", Descriptor: credentialV2Descriptor(entry)}})
	require.NoError(t, err)
	raw, err := json.Marshal(composed)
	require.NoError(t, err)
	persisted, err := Decode(raw)
	require.NoError(t, err)
	validateCredentialV2Test(t, persisted, "")
	requests, err := CredentialsV2Of(persisted.Capabilities)
	require.NoError(t, err)
	require.Len(t, requests, 2)
	for index, phase := range []string{"install", "runtime"} {
		request := requests[index]
		require.Equal(t, Phases{phase}, request.Phase)
		require.Equal(t, OAuthProfileOpenAIChatGPT, request.OAuth.Profile)
		require.Equal(t, CredentialOnMissingUnauthenticated, request.OnMissing)
		require.Equal(t, entry.Name, request.Name)
		require.Equal(t, entry.Description, request.Description)
		key, arity, err := InstanceKeyOf(persisted.Capabilities[index])
		require.NoError(t, err)
		require.Equal(t, ArityInstance, arity)
		require.Equal(t, InstanceKey{Space: CapabilityCredential, Parts: []string{"openai", phase}}, key)
	}
	after, err := json.Marshal(entry)
	require.NoError(t, err)
	require.JSONEq(t, string(before), string(after))
}

func TestCredentialV2CrossVersionOwnership(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, parameterized := range []bool{false, true} {
			t.Run(fmt.Sprintf("reverse=%t/parameterized=%t", reverse, parameterized), func(t *testing.T) {
				native := nativeCredentialV2()
				if parameterized {
					native.Config["onMissing"] = "${{ kit.args.policy }}"
				}
				legacy := Capability{Type: CapabilityCredential, Config: map[string]any{"service": "openai", "phase": "runtime", "oauth": map[string]any{}}}
				entries := []Capability{native, legacy}
				if reverse {
					entries[0], entries[1] = entries[1], entries[0]
				}
				d := credentialV2Descriptor(entries...)
				policy := CredentialOnMissingError
				d.Args = map[string]Arg{"policy": {Default: &policy}}
				raw, err := json.Marshal(d)
				require.NoError(t, err)
				_, err = ValidateRaw(raw, d)
				require.ErrorContains(t, err, "already declared")
				_, err = Compose([]Contribution{
					{Reference: "first", Descriptor: credentialV2Descriptor(entries[0])},
					{Reference: "second", Descriptor: credentialV2Descriptor(entries[1])},
				})
				require.ErrorContains(t, err, "one credential has one owner")
			})
		}
	}
	native := nativeCredentialV2()
	legacy := Capability{Type: CapabilityCredential, Config: map[string]any{"service": "openai", "phase": "install", "oauth": map[string]any{}}}
	validateCredentialV2Test(t, credentialV2Descriptor(native, legacy), "")
	_, err := Compose([]Contribution{{Reference: "different-phases", Descriptor: credentialV2Descriptor(native, legacy)}})
	require.NoError(t, err)
}

func TestCredentialV2RequiresExplicitRuntimeSupport(t *testing.T) {
	entry := nativeCredentialV2()
	_, err := SelectCapabilities(t.Context(), "native", credentialV2Descriptor(entry), Supported(CapabilityCredential))
	require.ErrorContains(t, err, "unsupported capability type "+CapabilityCredentialV2)
	selected, err := SelectCapabilities(t.Context(), "native", credentialV2Descriptor(entry), Supported(CapabilityCredentialV2))
	require.NoError(t, err)
	require.Len(t, selected.Capabilities, 1)
	requests, err := CredentialsV2Of(selected.Capabilities)
	require.NoError(t, err)
	require.True(t, requests[0].Required)
	require.Equal(t, CredentialOnMissingUnauthenticated, requests[0].OnMissing)
	entry.Optional = true
	selected, err = SelectCapabilities(t.Context(), "native", credentialV2Descriptor(entry), Supported(CapabilityCredential))
	require.NoError(t, err)
	require.Empty(t, selected.Capabilities)
	require.Empty(t, SurfaceOf(credentialV2Descriptor(selected.Capabilities...)).Services)
}

func TestCredentialV2IdentitySurface(t *testing.T) {
	native := nativeCredentialV2()
	generic := Capability{Type: CapabilityCredentialV2, Config: map[string]any{"service": "openai", "phase": "runtime", "oauth": map[string]any{}}}
	legacy := generic
	legacy.Type = CapabilityCredential
	legacySurface := SurfaceOf(credentialV2Descriptor(legacy))
	genericSurface := SurfaceOf(credentialV2Descriptor(generic))
	require.Equal(t, legacySurface, genericSurface)
	nativeSurface := SurfaceOf(credentialV2Descriptor(native))
	require.Equal(t, []string{"openai"}, nativeSurface.CredentialsRuntime)
	require.Equal(t, []string{CapabilityCredentialV2 + " openai runtime " + OAuthProfileOpenAIChatGPT}, nativeSurface.Services)
	require.NotEmpty(t, DiffWidenings(legacySurface, nativeSurface))
	require.Empty(t, DiffWidenings(nativeSurface, legacySurface))
	delete(native.Config, "onMissing")
	require.Equal(t, nativeSurface, SurfaceOf(credentialV2Descriptor(native)))
}

func TestCredentialV2SchemaPins(t *testing.T) {
	v1 := loadJSON(t, perTypeSchemaPath(CapabilityCredential))
	v2 := loadJSON(t, perTypeSchemaPath(CapabilityCredentialV2))
	require.NotContains(t, at(t, v1, "properties"), "onMissing")
	require.NotContains(t, at(t, v1, "definitions", "oauth", "properties"), "profile")
	require.ElementsMatch(t, []any{CredentialOnMissingError, CredentialOnMissingUnauthenticated}, literalEnum(t, at(t, v2, "properties", "onMissing")))
	require.Equal(t, CredentialOnMissingError, at(t, v2, "properties", "onMissing")["default"])
	require.ElementsMatch(t, []any{OAuthProfileOpenAIChatGPT}, literalEnum(t, at(t, v2, "definitions", "oauth", "properties", "profile")))
	for _, path := range [][]string{{"properties", "onMissing"}, {"definitions", "oauth", "properties", "profile"}} {
		assertAcceptsKitArg(t, at(t, v2, path...), "bearing")
	}
	shape := at(t, v2, "definitions", "oauth", "then")
	require.ElementsMatch(t, []any{"tokenEndpoint", "sentinels"}, shape["required"])
	endpoint := at(t, shape, "properties", "tokenEndpoint")
	require.ElementsMatch(t, []any{"host", "path"}, endpoint["required"])
	for key, value := range map[string]string{"host": "auth.openai.com", "path": "/oauth/token"} {
		branches := at(t, endpoint, "properties", key)["anyOf"].([]any)
		require.Equal(t, value, branches[0].(map[string]any)["const"])
	}
	for _, field := range []string{"accessToken", "refreshToken"} {
		pattern := literalPattern(t, at(t, shape, "properties", "sentinels", "properties", field))
		require.Equal(t, nativeCredentialSentinel.String(), pattern)
		rule := regexp.MustCompile(pattern)
		for _, marker := range []string{"proxy-managed", "A_9", "", "token.with.dots", "space marker", "newline\n", "é"} {
			require.Equal(t, nativeCredentialSentinel.MatchString(marker), rule.MatchString(marker))
		}
	}
	require.Equal(t, at(t, v1, "properties", "phase"), at(t, v2, "properties", "phase"))
	require.Equal(t, at(t, v1, "definitions", "apiKey"), at(t, v2, "definitions", "apiKey"))
	require.True(t, strings.HasSuffix(v2["title"].(string), "credential@2"))
}

func TestCredentialV2ParameterizedNativeValidation(t *testing.T) {
	for _, tc := range []struct {
		field   string
		valid   string
		invalid string
		want    string
	}{
		{"profile", OAuthProfileOpenAIChatGPT, "unknown", "unknown OAuth profile"},
		{"onMissing", CredentialOnMissingUnauthenticated, "skip", "unknown onMissing"},
		{"host", "auth.openai.com", "other.example.com", "requires tokenEndpoint"},
		{"path", "/oauth/token", "/oauth/authorize", "requires tokenEndpoint"},
		{"proxyManaged", "true", "false", "requires proxyManaged"},
	} {
		t.Run(tc.field, func(t *testing.T) {
			entry := nativeCredentialV2()
			oauth := entry.Config["oauth"].(map[string]any)
			const ref = "${{ kit.args.value }}"
			switch tc.field {
			case "profile":
				oauth["profile"] = ref
			case "onMissing":
				entry.Config["onMissing"] = ref
			case "host", "path":
				oauth["tokenEndpoint"].(map[string]any)[tc.field] = ref
			case "proxyManaged":
				entry.Config["apiKey"] = map[string]any{"name": "OPENAI_API_KEY", "proxyManaged": ref}
			}
			d := credentialV2Descriptor(entry)
			d.Args = map[string]Arg{"value": {}}
			raw, err := json.Marshal(d)
			require.NoError(t, err)
			_, err = ValidateRaw(raw, d)
			require.NoError(t, err)
			for _, value := range []string{tc.valid, tc.invalid} {
				expanded, err := ExpandCreateArgs(raw, d.Args, map[string]string{"value": value})
				require.NoError(t, err)
				effective, err := Decode(expanded)
				require.NoError(t, err)
				_, err = ValidateEffective(expanded, effective)
				if value == tc.valid {
					require.NoError(t, err)
				} else {
					require.ErrorContains(t, err, tc.want)
				}
			}
		})
	}
}

func TestCredentialV2DeferredPublication(t *testing.T) {
	native := nativeCredentialV2()
	native.Config["onMissing"] = "${{ kit.args.policy }}"
	legacy := Capability{Type: CapabilityCredential, Config: map[string]any{"service": "openai", "phase": "runtime", "oauth": map[string]any{}}}
	deferring := Capability{Type: CapabilityPort, Config: map[string]any{"container": "${{ kit.args.port }}"}}
	for _, grouped := range []bool{false, true} {
		t.Run(fmt.Sprintf("optional group=%t", grouped), func(t *testing.T) {
			request := native
			if grouped {
				request = Capability{Group: &CapabilityGroup{Optional: true, Capabilities: []Capability{request}}}
			}
			contributions := []Contribution{
				{Reference: "native", Descriptor: credentialV2Descriptor(request)},
				{Reference: "legacy", Descriptor: credentialV2Descriptor(legacy)},
				{Reference: "deferred", Descriptor: credentialV2Descriptor(deferring)},
			}
			published, err := Merge(contributions, MergeOptions{})
			if !grouped {
				require.ErrorContains(t, err, "one credential has one owner")
				return
			}
			require.NoError(t, err)
			raw, err := json.Marshal(published.Descriptor)
			require.NoError(t, err)
			expanded, err := ExpandCreateArgs(raw, map[string]Arg{"policy": {}, "port": {}}, map[string]string{"policy": CredentialOnMissingUnauthenticated, "port": "9090"})
			require.NoError(t, err)
			effective, err := Decode(expanded)
			require.NoError(t, err)
			skipped, err := SelectCapabilities(t.Context(), "set", effective, Supported(CapabilityCredential, CapabilityPort))
			require.NoError(t, err)
			require.Len(t, skipped.Skipped, 1)
			_, err = Compose([]Contribution{{Reference: "set", Descriptor: credentialV2Descriptor(skipped.Capabilities...)}})
			require.NoError(t, err)
			selected, err := SelectCapabilities(t.Context(), "set", effective, Supported(CapabilityCredential, CapabilityCredentialV2, CapabilityPort))
			require.NoError(t, err)
			_, err = Compose([]Contribution{{Reference: "set", Descriptor: credentialV2Descriptor(selected.Capabilities...)}})
			require.ErrorContains(t, err, "one credential has one owner")
		})
	}
}

func TestCredentialV2InjectionRequiresNetworkGrant(t *testing.T) {
	entry := Capability{Type: CapabilityCredentialV2, Config: map[string]any{
		"service": "github", "phase": []any{"install", "runtime"},
		"apiKey": map[string]any{"inject": []any{map[string]any{"domain": "api.github.com", "header": "Authorization"}}},
	}}
	policy := Capability{Type: CapabilityNetworkPolicyV2, Config: map[string]any{"runtime": map[string]any{"allow": []any{"api.github.com"}}}}
	d := credentialV2Descriptor(entry, policy)
	raw, err := json.Marshal(d)
	require.NoError(t, err)
	_, err = ValidateEffective(raw, d)
	require.ErrorContains(t, err, "not in the network policy's install allow list")
	policy.Config["install"] = policy.Config["runtime"]
	validateCredentialV2Test(t, credentialV2Descriptor(entry, policy), "")
}

func TestCredentialV2ParameterizedPresenceSurvivesComposition(t *testing.T) {
	for _, field := range []string{"onMissing", "profile", "format"} {
		for _, value := range []any{"", nil} {
			t.Run(fmt.Sprintf("%s=%v", field, value), func(t *testing.T) {
				entry := nativeCredentialV2()
				oauth := entry.Config["oauth"].(map[string]any)
				oauth["resourceHosts"] = []any{"${{ kit.args.host }}"}
				switch field {
				case "onMissing":
					entry.Config[field] = value
				case "profile":
					oauth[field] = value
				case "format":
					oauth["credentialFile"].(map[string]any)[field] = value
				}
				d := credentialV2Descriptor(entry)
				d.Args = map[string]Arg{"host": {}}
				raw, err := json.Marshal(d)
				require.NoError(t, err)
				_, err = ValidateRaw(raw, d)
				require.Error(t, err)
				require.Contains(t, err.Error(), field)
				composed, err := Compose([]Contribution{{Reference: "invalid", Descriptor: d}})
				require.NoError(t, err, "Compose callers validate, but invalid raw fields must not disappear")
				raw, err = json.Marshal(composed)
				require.NoError(t, err)
				expanded, err := ExpandCreateArgs(raw, d.Args, map[string]string{"host": "chatgpt.com"})
				require.NoError(t, err)
				effective, err := Decode(expanded)
				require.NoError(t, err)
				_, err = ValidateEffective(expanded, effective)
				require.Error(t, err)
				require.Contains(t, err.Error(), field)
			})
		}
	}
}
