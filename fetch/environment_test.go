package fetch

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/docker/sandbox-kit-spec/v3/spec"
	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

func TestResolveEnvironmentDiagnosticsUseDecodedReferences(t *testing.T) {
	for _, form := range []struct{ name, args, path string }{
		{"escaped space", "", `"${{\u0020kit.env.HOME}}/config"`},
		{"escaped dollar", "", `"\u0024{{ kit.env.HOME }}/config"`},
		{"escaped newline", "", `"${{\nkit.env.HOME}}/config"`},
	} {
		for _, failure := range []string{"composition", "final validation"} {
			t.Run(form.name+"/"+failure, func(t *testing.T) {
				var kits []*Kit
				for i := range 2 {
					filePath, content := form.path, "data"
					if failure == "final validation" {
						filePath = strings.ReplaceAll(filePath, "/config", fmt.Sprintf("/config-%d", i))
						content = strings.Repeat("x", spec.SizeErrorBytes/2+1024)
					}
					raw := []byte("schemaVersion: \"3\"\nkind: mixin\n" + form.args + `capabilities:
  - type: com.docker.sandbox/lifecycle@1
    config:
      files:
        - path: ` + filePath + `
          content: ` + content + "\n")
					require.False(t, spec.ContainsEnvRef(string(raw)), "regression requires the serialized scan to miss the reference")
					d, err := spec.Decode(raw)
					require.NoError(t, err)
					kits = append(kits, &Kit{Reference: fmt.Sprintf("example.com/kit%d:1.0.0", i), Digest: digest.FromBytes(raw).String(), Raw: raw, Descriptor: d})
				}
				result, err := mergeKits(t.Context(), kits, []map[string]string{nil, nil}, true,
					WithEnvironment(map[string]string{"HOME": "/private-environment-value"}, nil))
				require.Nil(t, result)
				require.Error(t, err)
				require.NotContains(t, err.Error(), "private-environment-value")
				if failure == "composition" {
					require.ErrorContains(t, err, "compose capabilities after environment expansion")
				} else {
					require.ErrorContains(t, err, "merged descriptor is invalid after environment expansion")
				}
			})
		}
	}
}

func TestResolveWithEnvironment(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprintf("partial=%t", partial), func(t *testing.T) {
			reg := newRegistry(t)
			kind := spec.KindWorkload
			if partial {
				kind = spec.KindMixin
			}
			home := "/home/export"
			d := &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: kind,
				Args: map[string]spec.Arg{"home": {Default: &home, Env: "HOME"}},
				Capabilities: []spec.Capability{{Type: spec.CapabilityLifecycle, Config: map[string]any{
					"files": []any{map[string]any{"path": "${{kit.env.HOME}}/config", "content": "${{kit.env.MESSAGE}}"}},
				}}},
			}
			reg.tag("kits/environment", "1.0.0", reg.image(t, kitJSON(t, d)))
			client, err := New()
			require.NoError(t, err)
			resolve := client.Resolve
			if partial {
				resolve = client.ResolvePartial
			}
			requests := reqs(reg.ref("kits/environment", "1.0.0"))
			t.Setenv("MESSAGE", "host-value-must-not-be-used")
			_, err = resolve(t.Context(), requests)
			require.ErrorContains(t, err, `variable "MESSAGE"`)

			for _, tc := range []struct {
				name, message, wantHome string
				overrides               map[string]string
			}{
				{"exports replace defaults", "image-message", home, nil},
				{"overrides replace exports", "caller-message", "/home/caller", map[string]string{"HOME": "/home/caller", "MESSAGE": "caller-message"}},
				{"empty override is present", "", home, map[string]string{"MESSAGE": ""}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					defaults := map[string]string{"HOME": "/home/image", "MESSAGE": "image-message"}
					option := WithEnvironment(defaults, tc.overrides)
					defaults["HOME"], defaults["MESSAGE"] = "/mutated", "mutated"
					if tc.overrides != nil {
						tc.overrides["MESSAGE"] = "mutated"
					}
					calls := 0
					for range 2 { // Reusing an option cannot retain mutations from a previous result.
						result, err := resolve(t.Context(), requests, option, WithCapabilitySelector(func(_ context.Context, _ spec.Descriptor, c spec.Capability) spec.CapabilityDecision {
							lc, err := spec.LifecycleOf([]spec.Capability{c})
							require.NoError(t, err)
							require.Equal(t, tc.wantHome+"/config", lc.Files[0].Path)
							require.Equal(t, tc.message, lc.Files[0].Content)
							calls++
							return spec.CapabilityDecision{Accepted: true}
						}))
						require.NoError(t, err)
						require.Equal(t, map[string]string{"HOME": home}, result.ContainerEnv)
						lc, err := spec.LifecycleOf(result.Descriptor.Capabilities)
						require.NoError(t, err)
						require.Equal(t, tc.wantHome+"/config", lc.Files[0].Path)
						require.Equal(t, tc.message, lc.Files[0].Content)
						result.ContainerEnv["HOME"] = "/mutated-result"
					}
					require.Equal(t, 2, calls)
				})
			}
		})
	}
}

func TestResolveRejectsMalformedInsertedPlaceholders(t *testing.T) {
	for _, value := range []string{"${{ kit.args.123 }}", "${{ kit.args.NAME", "${{\nkit.args.NAME", "${{ kit.env.123 }}", "${{ kit.env.NAME"} {
		for _, source := range []string{"argument", "environment"} {
			t.Run(source+"/"+value, func(t *testing.T) {
				reg := newRegistry(t)
				d := &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindMixin,
					Capabilities: []spec.Capability{{Type: spec.CapabilityLifecycle, Config: map[string]any{
						"files": []any{map[string]any{"path": "/config", "content": "${{ kit.env.VALUE }}"}},
					}}},
				}
				if source == "argument" {
					d.Args = map[string]spec.Arg{"value": {Required: true, Env: "VALUE"}}
					d.Capabilities[0].Config["files"].([]any)[0].(map[string]any)["content"] = "${{kit.args.value}}"
				}
				reg.tag("kits/placeholders", "1.0.0", reg.image(t, kitJSON(t, d)))
				client, err := New()
				require.NoError(t, err)
				requests := reqs(reg.ref("kits/placeholders", "1.0.0"))
				secret := "private-value " + value
				if source == "argument" {
					requests[0].Args = map[string]string{"value": secret}
				}
				result, err := client.ResolvePartial(t.Context(), requests,
					WithEnvironment(map[string]string{"VALUE": secret}, nil),
					WithCapabilitySelector(func(context.Context, spec.Descriptor, spec.Capability) spec.CapabilityDecision {
						t.Fatal("invalid inserted value reached selector")
						return spec.CapabilityDecision{Accepted: true}
					}))
				require.Nil(t, result)
				require.ErrorContains(t, err, "placeholders")
				require.NotContains(t, err.Error(), "private-value")
			})
		}
	}
}

func TestResolveRejectsEnvironmentReferencesIntroducedByArguments(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		args          map[string]string
	}{
		{"literal opener", "${{${{kit.args.x}}", map[string]string{"x": " kit.env.SECRET }}"}},
		{"split opener", "${{kit.args.x}}${{kit.args.y}}", map[string]string{"x": "${{", "y": " kit.env.SECRET }}"}},
		{"split namespace", "${{ kit.en${{kit.args.x}}", map[string]string{"x": "v.SECRET }}"}},
		{"variable name", "${{ kit.env.${{kit.args.x}} }}", map[string]string{"x": "SECRET"}},
		{"empty fragment", "${{ kit.env.SEC${{kit.args.x}}RET }}", map[string]string{"x": ""}},
		{"closing brace", "${{ kit.env.SECRET }${{kit.args.x}}", map[string]string{"x": "}"}},
		{"alongside original", "${{kit.env.PUBLIC}} ${{${{kit.args.x}}", map[string]string{"x": " kit.env.SECRET }}"}},
	} {
		for _, defaults := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/defaults=%t", tc.name, defaults), func(t *testing.T) {
				d := &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindMixin, Args: map[string]spec.Arg{},
					Capabilities: []spec.Capability{{Type: spec.CapabilityLifecycle, Config: map[string]any{
						"files": []any{map[string]any{"path": "/config", "content": tc.content}},
					}}},
				}
				for name, value := range tc.args {
					require.False(t, spec.ContainsKitPlaceholder(value), "individual values do not contain a full opener")
					if defaults {
						d.Args[name] = spec.Arg{Default: &value}
					} else {
						d.Args[name] = spec.Arg{Required: true}
					}
				}
				supplied := tc.args
				if defaults {
					supplied = nil
				}
				raw := kitJSON(t, d)
				kit := &Kit{Reference: "example.com/tool:1.0.0", Digest: digest.FromBytes(raw).String(), Descriptor: d, Raw: raw}
				result, err := mergeKits(t.Context(), []*Kit{kit}, []map[string]string{supplied}, true,
					WithEnvironment(map[string]string{"SECRET": "private-environment-value", "PUBLIC": "public"}, nil),
					WithCapabilitySelector(func(context.Context, spec.Descriptor, spec.Capability) spec.CapabilityDecision {
						t.Fatal("introduced reference reached selection")
						return spec.CapabilityDecision{Accepted: true}
					}))
				require.Nil(t, result)
				require.ErrorContains(t, err, "argument substitution introduces an environment reference")
				require.NotContains(t, err.Error(), "private-environment-value")
			})
		}
	}
}

func TestResolvePreservesOriginalEnvironmentReferencesBesideArguments(t *testing.T) {
	prefix, suffix := "é-prefix:", ":suffix"
	d := &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindMixin,
		Args: map[string]spec.Arg{"prefix": {Default: &prefix}, "suffix": {Default: &suffix}},
		Capabilities: []spec.Capability{{Type: spec.CapabilityLifecycle, Config: map[string]any{
			"files": []any{map[string]any{"path": "/config", "content": "${{kit.args.prefix}}${{\nkit.env.PUBLIC}}${{kit.args.suffix}}${{kit.env.PUBLIC}}"}},
		}}},
	}
	raw := kitJSON(t, d)
	kit := &Kit{Reference: "example.com/tool:1.0.0", Digest: digest.FromBytes(raw).String(), Descriptor: d, Raw: raw}
	result, err := mergeKits(t.Context(), []*Kit{kit}, []map[string]string{nil}, true, WithEnvironment(map[string]string{"PUBLIC": "public"}, nil))
	require.NoError(t, err)
	lc, err := spec.LifecycleOf(result.Descriptor.Capabilities)
	require.NoError(t, err)
	require.Equal(t, "é-prefix:public:suffixpublic", lc.Files[0].Content)
	require.Equal(t, d, result.Selections[0].PublishedDescriptor, "published argument and environment references are retained")
	require.JSONEq(t, string(result.Selections[0].PublishedBytes), string(kitJSON(t, result.Selections[0].PublishedDescriptor)))
}

func TestResolveInvalidEnvironmentGroupKeepsMemberLocation(t *testing.T) {
	d := &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindMixin,
		Capabilities: []spec.Capability{{Group: &spec.CapabilityGroup{Optional: true, Capabilities: []spec.Capability{
			{Type: spec.CapabilityLifecycle, Config: map[string]any{"files": []any{map[string]any{"path": "${{kit.env.PATH}}", "content": "data"}}}},
		}}}},
	}
	raw := kitJSON(t, d)
	kit := &Kit{Reference: "example.com/tool:1.0.0", Digest: digest.FromBytes(raw).String(), Descriptor: d, Raw: raw}
	for _, accept := range []bool{false, true} {
		_, err := mergeKits(t.Context(), []*Kit{kit}, []map[string]string{nil}, true,
			WithEnvironment(map[string]string{"PATH": "private-relative-path"}, nil),
			WithCapabilitySelector(func(context.Context, spec.Descriptor, spec.Capability) spec.CapabilityDecision {
				t.Fatal("invalid group reached selection")
				return spec.CapabilityDecision{Accepted: accept}
			}))
		require.ErrorContains(t, err, "capabilities[0].group.capabilities[0]")
		require.NotContains(t, err.Error(), "private-relative-path")
	}
}
