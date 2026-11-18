package fetch

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/docker/sandbox-kit-spec/v3/spec"
	"github.com/stretchr/testify/require"
)

func TestSelectionTargetsAndExpandedDescriptorsThroughPublicAPIs(t *testing.T) {
	for _, method := range []string{"Resolve", "ResolvePartial", "Assemble"} {
		t.Run(method, func(t *testing.T) {
			reg := newRegistry(t)
			base, _ := assemblyFixture(t, spec.KindWorkload, "base", "base")
			mixin, _ := assemblyFixture(t, spec.KindMixin, "feature", "feature")
			port := "9000"
			cache := "/cache"
			d := &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindMixin,
				Args: map[string]spec.Arg{"port": {Default: &port}, "cache": {Default: &cache, Env: "CACHE_PATH"}},
				Capabilities: []spec.Capability{
					{Type: spec.CapabilityPort, Source: &spec.CapabilitySource{Kit: "original", Path: "capabilities[7]"},
						Config: map[string]any{"container": "${{ kit.args.port }}"}},
					{Source: &spec.CapabilitySource{Path: "capabilities[8]"}, Group: &spec.CapabilityGroup{Optional: true, Capabilities: []spec.Capability{
						{Type: spec.CapabilityLifecycle, Source: &spec.CapabilitySource{Kit: "original", Path: "capabilities[9]"},
							Config: map[string]any{"files": []any{map[string]any{"path": "${{ kit.env.CACHE_PATH }}/settings", "content": "yes"}}}},
						{Type: spec.CapabilityAgentContext, Config: map[string]any{"content": "skipped guidance"}},
					}}},
				}}
			mixin.Manifest.Annotations[spec.AnnotationDescriptor] = string(kitJSON(t, d))
			for repo, input := range map[string]*LoadedKit{"base": base, "feature": mixin} {
				manifest, err := json.Marshal(input.Manifest)
				require.NoError(t, err)
				config, err := json.Marshal(input.Config)
				require.NoError(t, err)
				reg.blobs[repo+"/blobs/"+input.Manifest.Config.Digest.String()] = config
				reg.tag(repo, "first", manifest)
				reg.tag(repo, "second", manifest)
			}
			client, err := New()
			require.NoError(t, err)
			first, second := reg.ref("feature", "first"), reg.ref("feature", "second")
			requests := reqs(reg.ref("base", "first"), first, second)
			// The two tags resolve to the same artifact and publisher labels;
			// selection can still address their declarations independently.
			targets := map[spec.SelectionTarget]bool{}
			selector := func(_ context.Context, target spec.SelectionTarget, _ spec.Descriptor, c spec.Capability) spec.CapabilityDecision {
				require.Contains(t, []string{first, second}, target.Reference)
				require.False(t, targets[target], "a member is selected once")
				targets[target] = true
				if c.Type == spec.CapabilityPort {
					var config spec.Port
					require.NoError(t, spec.DecodeCapabilityConfig(c, &config))
					require.Equal(t, 9000, config.Container)
				}
				if c.Type == spec.CapabilityLifecycle {
					var config spec.Lifecycle
					require.NoError(t, spec.DecodeCapabilityConfig(c, &config))
					require.Equal(t, "/cache/settings", config.Files[0].Path)
				}
				return spec.CapabilityDecision{Accepted: target.Path != "capabilities[1].group.capabilities[0]"}
			}
			var result *Resolved
			switch method {
			case "Resolve":
				result, err = client.Resolve(t.Context(), requests, WithCapabilitySelector(selector), WithExpandedDescriptors())
			case "ResolvePartial":
				result, err = client.ResolvePartial(t.Context(), requests[1:], WithCapabilitySelector(selector), WithExpandedDescriptors())
			case "Assemble":
				var assembled *Result
				assembled, err = Assemble(t.Context(), requests, Options{Loader: client.LoadKit, CapabilitySelector: selector, RetainExpandedDescriptors: true})
				if err == nil {
					result = assembled.Resolved
				}
			}
			require.NoError(t, err)
			require.Len(t, targets, 6)
			for i, selection := range result.Selections {
				expanded := selection.ExpandedDescriptor
				require.NotNil(t, expanded)
				if selection.Reference == requests[0].Reference {
					require.Empty(t, expanded.Capabilities, "kits without declarations retain an expanded descriptor")
					continue
				}
				for _, record := range append(selection.Selection.Selected, selection.Selection.Skipped...) {
					for _, path := range record.Members {
						require.True(t, targets[spec.SelectionTarget{Reference: selection.Reference, Path: path}])
					}
				}
				require.Len(t, expanded.Capabilities, 2)
				require.Len(t, expanded.Capabilities[1].Group.Capabilities, 2)
				require.Len(t, result.Kits[i].Descriptor.Capabilities, 1)
				var expandedPort spec.Port
				require.NoError(t, spec.DecodeCapabilityConfig(expanded.Capabilities[0], &expandedPort))
				require.Equal(t, 9000, expandedPort.Container)
				var config spec.Lifecycle
				require.NoError(t, spec.DecodeCapabilityConfig(expanded.Capabilities[1].Group.Capabilities[0], &config))
				require.Equal(t, "/cache/settings", config.Files[0].Path)
				require.Equal(t, d.Capabilities[0].Source, expanded.Capabilities[0].Source)
				require.Equal(t, d.Capabilities[1].Source, expanded.Capabilities[1].Source, "path-only sources stay path-only")
				require.Equal(t, d.Capabilities[1].Group.Capabilities[0].Source, expanded.Capabilities[1].Group.Capabilities[0].Source)
				require.Nil(t, expanded.Capabilities[1].Group.Capabilities[1].Source, "missing sources are not filled in")
				require.JSONEq(t, string(kitJSON(t, d)), string(kitJSON(t, selection.PublishedDescriptor)))
				require.Len(t, selection.Selection.Skipped, 1)
				require.Equal(t, []spec.CapabilityDecision{{}, {Accepted: true}}, selection.Selection.Skipped[0].Decisions)
			}
		})
	}
}

func TestExpandedDescriptorCaptureDoesNotSerializeSkippedSecrets(t *testing.T) {
	for _, method := range []string{"Resolve", "ResolvePartial", "Assemble"} {
		for _, capture := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/capture=%t", method, capture), func(t *testing.T) {
				d := &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindWorkload,
					Capabilities: []spec.Capability{{Type: "com.example/secret@1", Optional: true,
						Config: map[string]any{"value": "${{ kit.env.SECRET }}"}}}}
				secret := "sensitive-skipped-value"
				result := resolveSelectionFixture(t, method, d, capture, map[string]string{"SECRET": secret},
					func(context.Context, spec.SelectionTarget, spec.Descriptor, spec.Capability) spec.CapabilityDecision {
						return spec.CapabilityDecision{}
					})
				selection := result.Selections[0]
				require.Empty(t, result.Descriptor.Capabilities)
				require.Len(t, selection.Selection.Skipped, 1)
				if capture {
					require.NotNil(t, selection.ExpandedDescriptor)
					require.Equal(t, secret, selection.ExpandedDescriptor.Capabilities[0].Config["value"])
				} else {
					require.Nil(t, selection.ExpandedDescriptor)
				}
				raw, err := json.Marshal(result.Selections)
				require.NoError(t, err)
				require.NotContains(t, string(raw), secret)
				require.NotContains(t, string(raw), "ExpandedDescriptor")
			})
		}
	}
}

func TestExpandedDescriptorCaptureIsIsolated(t *testing.T) {
	for _, method := range []string{"Resolve", "ResolvePartial", "Assemble"} {
		for _, tc := range []struct{ name, field string }{
			{"omitted", ""}, {"empty", `,"config":{}`}, {"null", `,"config":null`},
		} {
			t.Run(method+"/config-presence/"+tc.name, func(t *testing.T) {
				config := tc.field
				kind := spec.KindWorkload
				if method == "ResolvePartial" {
					kind = spec.KindMixin
				}
				raw := []byte(fmt.Sprintf(`{"schemaVersion":"3","kind":%q,"capabilities":[
					{"type":"com.example/ordinary@1","optional":true%s},
					{"group":{"optional":true,"capabilities":[{"type":"com.example/grouped@1"%s}]}}
				]}`, kind, config, config))
				var observed []spec.Capability
				result := resolveSelectionFixtureRaw(t, method, kind, raw, true, nil,
					func(_ context.Context, _ spec.SelectionTarget, _ spec.Descriptor, c spec.Capability) spec.CapabilityDecision {
						require.Equal(t, config != "", c.ConfigStated())
						if c.Config != nil {
							c.Config["selector"] = "changed"
						}
						observed = append(observed, c)
						return spec.CapabilityDecision{}
					})
				require.Empty(t, result.Descriptor.Capabilities)
				require.Len(t, result.Selections[0].Selection.Skipped, 2)
				captured := spec.DeclaredCapabilities(result.Selections[0].ExpandedDescriptor.Capabilities)
				require.Len(t, captured, 2)
				require.Len(t, observed, 2)
				for i, c := range captured {
					require.Equal(t, config != "", c.ConfigStated())
					if config == `,"config":{}` {
						require.NotNil(t, c.Config)
						require.Empty(t, c.Config)
						c.Config["snapshot"] = "changed"
						require.Equal(t, map[string]any{"selector": "changed"}, observed[i].Config)
					} else {
						require.Nil(t, c.Config)
					}
				}
			})
		}
		t.Run(method, func(t *testing.T) {
			entry := func(typ string) spec.Capability {
				return spec.Capability{Type: typ, Source: &spec.CapabilitySource{Kit: "original", Path: "capabilities[7]"},
					Config: map[string]any{"values": []any{map[string]any{"value": "original"}}}}
			}
			d := &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindWorkload, Licenses: []string{"MIT"},
				Capabilities: []spec.Capability{entry("com.example/ordinary@1"),
					{Group: &spec.CapabilityGroup{Optional: true, Capabilities: []spec.Capability{entry("com.example/grouped@1")}}}}}
			result := resolveSelectionFixture(t, method, d, true, nil,
				func(_ context.Context, _ spec.SelectionTarget, owner spec.Descriptor, c spec.Capability) spec.CapabilityDecision {
					c.Config["values"].([]any)[0].(map[string]any)["value"] = "selector"
					c.Source.Kit = "selector"
					owner.Licenses[0] = "Apache-2.0"
					return spec.CapabilityDecision{Accepted: true}
				})
			snapshot := result.Selections[0].ExpandedDescriptor
			require.NotNil(t, snapshot)
			require.Equal(t, []string{"MIT"}, snapshot.Licenses)
			captured := spec.DeclaredCapabilities(snapshot.Capabilities)
			effective := result.Kits[0].Descriptor.Capabilities
			require.Len(t, captured, 2)
			require.Len(t, effective, 2)
			for i, c := range captured {
				values := c.Config["values"].([]any)
				require.Equal(t, "original", values[0].(map[string]any)["value"])
				require.Equal(t, "original", c.Source.Kit)
				effective[i].Config["values"].([]any)[0].(map[string]any)["value"] = "effective"
				require.Equal(t, "original", values[0].(map[string]any)["value"])
				values[0].(map[string]any)["value"] = "snapshot"
				c.Source.Kit = "snapshot"
				require.Equal(t, "effective", effective[i].Config["values"].([]any)[0].(map[string]any)["value"])
				require.Equal(t, "original", effective[i].Source.Kit)
			}
		})
	}
}

func TestExpandedDescriptorCaptureIsolatesCapabilityScopes(t *testing.T) {
	for _, method := range []string{"Resolve", "ResolvePartial", "Assemble"} {
		for _, mutation := range []struct {
			name   string
			change func([]spec.CapabilityScope, string)
			read   func([]spec.CapabilityScope) string
		}{
			{"scope table", func(scopes []spec.CapabilityScope, value string) {
				scopes[0] = spec.CapabilityScope{Provides: []string{value}}
			}, func(scopes []spec.CapabilityScope) string { return scopes[0].Provides[0] }},
			{"scope bindings", func(scopes []spec.CapabilityScope, value string) {
				scopes[0].Provides[0] = value
			}, func(scopes []spec.CapabilityScope) string { return scopes[0].Provides[0] }},
			{"scope requirements", func(scopes []spec.CapabilityScope, value string) {
				scopes[0].Requires[0] = value
			}, func(scopes []spec.CapabilityScope) string { return scopes[0].Requires[0] }},
			{"scope integrations", func(scopes []spec.CapabilityScope, value string) {
				scopes[0].Integrates[0] = value
			}, func(scopes []spec.CapabilityScope) string { return scopes[0].Integrates[0] }},
		} {
			t.Run(method+"/"+mutation.name, func(t *testing.T) {
				zero := uint32(0)
				d := &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindWorkload,
					Provides: []string{"com.example/ordinary@1"},
					CapabilityScopes: []spec.CapabilityScope{
						{Provides: []string{"com.example/ordinary"}, Requires: []string{"com.example/ordinary"}, Integrates: []string{"com.example/ordinary"}}, {},
					},
					Capabilities: []spec.Capability{{Type: "com.example/ordinary@1", ID: "setup", Scope: &zero}},
				}
				result := resolveSelectionFixture(t, method, d, true, nil,
					func(context.Context, spec.SelectionTarget, spec.Descriptor, spec.Capability) spec.CapabilityDecision {
						return spec.CapabilityDecision{Accepted: true}
					})
				snapshot := result.Selections[0].ExpandedDescriptor
				require.NotNil(t, snapshot)
				effective := result.Kits[0].Descriptor
				require.Equal(t, d.CapabilityScopes, snapshot.CapabilityScopes)
				require.Equal(t, d.CapabilityScopes, effective.CapabilityScopes)

				// Callers may edit either returned view. Neither replacing a
				// scope nor editing one binding may rewrite the other view.
				mutation.change(effective.CapabilityScopes, "com.example/effective")
				require.Equal(t, "com.example/ordinary", mutation.read(snapshot.CapabilityScopes))
				mutation.change(snapshot.CapabilityScopes, "com.example/snapshot")
				require.Equal(t, "com.example/effective", mutation.read(effective.CapabilityScopes))
				require.Len(t, snapshot.CapabilityScopes, 2, "empty original scopes retain their indexes")
				require.Nil(t, snapshot.CapabilityScopes[1].Provides)
				require.Nil(t, effective.CapabilityScopes[1].Provides)
			})
		}
	}
}

func resolveSelectionFixture(t *testing.T, method string, d *spec.Descriptor, capture bool, environment map[string]string, selector spec.SelectCapability) *Resolved {
	t.Helper()
	if method == "ResolvePartial" {
		d.Kind = spec.KindMixin
	}
	return resolveSelectionFixtureRaw(t, method, d.Kind, kitJSON(t, d), capture, environment, selector)
}

func resolveSelectionFixtureRaw(t *testing.T, method, kind string, raw []byte, capture bool, environment map[string]string, selector spec.SelectCapability) *Resolved {
	t.Helper()
	reg := newRegistry(t)
	input, _ := assemblyFixture(t, kind, "selection", "selection")
	input.Manifest.Annotations[spec.AnnotationDescriptor] = string(raw)
	manifest, err := json.Marshal(input.Manifest)
	require.NoError(t, err)
	config, err := json.Marshal(input.Config)
	require.NoError(t, err)
	reg.blobs["kit/blobs/"+input.Manifest.Config.Digest.String()] = config
	reg.tag("kit", "test", manifest)
	client, err := New()
	require.NoError(t, err)
	requests := reqs(reg.ref("kit", "test"))
	contracts := map[string]spec.ExecutionContract{
		"com.example/ordinary@1": {},
		"com.example/grouped@1":  {},
	}
	options := []ResolveOption{WithCapabilitySelector(selector), WithEnvironment(nil, environment), WithExecutionContracts(contracts)}
	if capture {
		options = append(options, WithExpandedDescriptors())
	}
	var result *Resolved
	switch method {
	case "Resolve":
		result, err = client.Resolve(t.Context(), requests, options...)
	case "ResolvePartial":
		result, err = client.ResolvePartial(t.Context(), requests, options...)
	case "Assemble":
		var assembled *Result
		assembled, err = Assemble(t.Context(), requests, Options{Loader: client.LoadKit, CapabilitySelector: selector,
			ExecutionContracts: contracts, RetainExpandedDescriptors: capture, Overrides: Overrides{Env: environment}})
		if err == nil {
			result = assembled.Resolved
		}
	default:
		t.Fatalf("unknown resolution API %q", method)
	}
	require.NoError(t, err)
	return result
}
