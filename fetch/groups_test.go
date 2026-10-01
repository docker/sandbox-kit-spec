package fetch

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/docker/sandbox-kit-spec/v3/spec"
)

func TestResolveGroupsThroughPublicAPIs(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprint(partial), func(t *testing.T) {
			reg := newRegistry(t)
			kind := spec.KindWorkload
			if partial {
				kind = spec.KindMixin
			}
			defaultValue := "9000"
			d := &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: kind, Args: map[string]spec.Arg{"port": {Default: &defaultValue, Env: "PORT"}}, Capabilities: []spec.Capability{
				{Type: spec.CapabilityPort, Config: map[string]any{"container": 8080}},
				{Group: &spec.CapabilityGroup{Name: "same name", Optional: true, Capabilities: []spec.Capability{
					{Type: spec.CapabilityPort, Config: map[string]any{"container": "${{ kit.args.port }}"}},
					{Type: spec.CapabilityAgentContext, Config: map[string]any{"content": "must not reach a handler"}},
				}}},
			}}
			reg.tag("kits/groups", "1.0.0", reg.image(t, kitJSON(t, d)))
			client, err := New()
			require.NoError(t, err)
			method := client.Resolve
			if partial {
				method = client.ResolvePartial
			}
			calls := 0
			result, err := method(t.Context(), reqs(reg.ref("kits/groups", "1.0.0")), WithCapabilitySelector(func(_ context.Context, _ spec.Descriptor, c spec.Capability) spec.CapabilityDecision {
				calls++
				if c.Type != spec.CapabilityPort {
					return spec.CapabilityDecision{Accepted: true}
				}
				var port spec.Port
				require.NoError(t, spec.DecodeCapabilityConfig(c, &port))
				c.Config["container"] = 1
				return spec.CapabilityDecision{Accepted: port.Container == 8080, Message: "host permits only port 8080"}
			}))
			require.NoError(t, err)
			require.Equal(t, 3, calls)
			require.Len(t, result.Kits, 1)
			require.Len(t, result.Kits[0].Descriptor.Capabilities, 1)
			require.Len(t, result.Selections[0].Selection.Skipped, 1)
			require.Equal(t, []spec.CapabilityDecision{
				{Message: "host permits only port 8080"}, {Accepted: true},
			}, result.Selections[0].Selection.Skipped[0].Decisions)
			require.True(t, spec.HasGroups(result.Selections[0].PublishedDescriptor.Capabilities))
			require.JSONEq(t, string(kitJSON(t, d)), string(kitJSON(t, result.Selections[0].PublishedDescriptor)), "published declarations survive expansion and selection")
			require.Equal(t, "9000", result.ContainerEnv["PORT"])
			require.Equal(t, map[string]string{"PORT": "9000"}, result.Kits[0].Env)
			require.Empty(t, spec.SurfaceOf(result.Descriptor).Services)
			require.Equal(t, []string{"8080/tcp"}, spec.SurfaceOf(result.Descriptor).Ports)
			result.Kits[0].Descriptor.Capabilities[0].Config["container"] = 1234
			var originalPort spec.Port
			require.NoError(t, spec.DecodeCapabilityConfig(result.Selections[0].PublishedDescriptor.Capabilities[0], &originalPort))
			require.Equal(t, 8080, originalPort.Container)
		})
	}
}

func TestSelectionPrecedesCredentialOwnershipAndCrossChecks(t *testing.T) {
	reg := newRegistry(t)
	cred := spec.Capability{Type: spec.CapabilityCredential, Config: map[string]any{"service": "github", "phase": "runtime", "apiKey": map[string]any{"name": "GH_TOKEN", "inject": []any{map[string]any{"domain": "api.github.com", "header": "Authorization", "format": "Bearer %s"}}}}}
	group := spec.Capability{Group: &spec.CapabilityGroup{Optional: true, Capabilities: []spec.Capability{
		{Type: "com.example/unavailable@1"}, cred,
	}}}
	base := &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindWorkload, Capabilities: []spec.Capability{cred, {Type: spec.CapabilityNetworkPolicy, Config: map[string]any{"runtime": map[string]any{"allow": []any{"api.github.com"}}}}}}
	mixin := &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindMixin, Capabilities: []spec.Capability{group}}
	reg.tag("kits/base", "1.0.0", reg.image(t, kitJSON(t, base)))
	reg.tag("kits/group", "1.0.0", reg.image(t, kitJSON(t, mixin)))
	client, err := New()
	require.NoError(t, err)
	requests := reqs(reg.ref("kits/base", "1.0.0"), reg.ref("kits/group", "1.0.0"))
	result, err := client.Resolve(t.Context(), requests)
	require.NoError(t, err)
	require.Len(t, result.Kits[1].Descriptor.Capabilities, 0)
	_, err = client.Resolve(t.Context(), requests, WithCapabilitySelector(func(context.Context, spec.Descriptor, spec.Capability) spec.CapabilityDecision {
		return spec.CapabilityDecision{Accepted: true}
	}))
	require.ErrorContains(t, err, "one credential has one owner")
	require.ErrorContains(t, err, "group.capabilities[1]")
	_, err = client.Resolve(t.Context(), requests, WithCapabilitySelector(func(_ context.Context, _ spec.Descriptor, c spec.Capability) spec.CapabilityDecision {
		return spec.CapabilityDecision{Accepted: c.Type != spec.CapabilityNetworkPolicy}
	}))
	require.ErrorContains(t, err, "required capability selection rejected")
	// Make the only policy optional: rejection must now fail final coherence,
	// rather than silently leave a credential injectable outside its allowlist.
	base.Capabilities[1].Optional = true
	reg.tag("kits/base", "2.0.0", reg.image(t, kitJSON(t, base)))
	_, err = client.Resolve(t.Context(), reqs(reg.ref("kits/base", "2.0.0")), WithCapabilitySelector(func(_ context.Context, _ spec.Descriptor, c spec.Capability) spec.CapabilityDecision {
		return spec.CapabilityDecision{Accepted: c.Type != spec.CapabilityNetworkPolicy}
	}))
	require.ErrorContains(t, err, "not in the network policy")
}

func TestResolveValidatesSkippedMembersAfterExpansion(t *testing.T) {
	reg := newRegistry(t)
	d := &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindMixin, Args: map[string]spec.Arg{"port": {Required: true}}, Capabilities: []spec.Capability{{Group: &spec.CapabilityGroup{Optional: true, Capabilities: []spec.Capability{{Type: spec.CapabilityPort, Config: map[string]any{"container": "${{ kit.args.port }}"}}}}}}}
	reg.tag("kits/group", "1.0.0", reg.image(t, kitJSON(t, d)))
	client, err := New()
	require.NoError(t, err)
	_, err = client.ResolvePartial(t.Context(), []Request{{Reference: reg.ref("kits/group", "1.0.0"), Args: map[string]string{"port": "70000"}}}, WithCapabilitySelector(func(context.Context, spec.Descriptor, spec.Capability) spec.CapabilityDecision {
		t.Fatal("invalid member reached selection")
		return spec.CapabilityDecision{Accepted: false}
	}))
	require.ErrorContains(t, err, "capabilities[0].group.capabilities[0]")
}

func TestRepublishedRejectionNamesOriginalMember(t *testing.T) {
	reg := newRegistry(t)
	base := &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindMixin, Capabilities: []spec.Capability{{Type: spec.CapabilityLifecycle, Config: map[string]any{"startup": []any{map[string]any{"command": "true"}}}}}}
	original := &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindMixin, Capabilities: []spec.Capability{
		{Group: &spec.CapabilityGroup{Capabilities: []spec.Capability{
			{Type: spec.CapabilityVolume, Config: map[string]any{"path": "/cache"}},
		}}},
	}}
	published, err := spec.Merge([]spec.Contribution{
		{Reference: "registry.example/base:1.0.0", Descriptor: base},
		{Reference: "registry.example/feature:1.0.0", Descriptor: original},
	}, spec.MergeOptions{})
	require.NoError(t, err)
	// Republish once more: the original member's attribution must survive.
	published, err = spec.Merge([]spec.Contribution{{Reference: "registry.example/first-set:1.0.0", Descriptor: published.Descriptor}}, spec.MergeOptions{})
	require.NoError(t, err)
	reg.tag("kits/republished", "1.0.0", reg.image(t, kitJSON(t, published.Descriptor)))
	client, err := New()
	require.NoError(t, err)
	ref := reg.ref("kits/republished", "1.0.0")
	result, err := client.ResolvePartial(t.Context(), reqs(ref), WithCapabilitySelector(spec.Supported(spec.CapabilityLifecycle)))
	require.Nil(t, result)
	require.ErrorContains(t, err, ref)
	require.ErrorContains(t, err, "registry.example/feature:1.0.0 capabilities[0].group.capabilities[0]")
	var field *spec.FieldError
	require.ErrorAs(t, err, &field)
	require.Equal(t, "capabilities[1].group.capabilities[0]", field.Path)
}

func TestResolveCompletesSelectionSourcesWithoutMutatingDeclarations(t *testing.T) {
	for _, grouped := range []bool{false, true} {
		for _, accept := range []bool{false, true} {
			for _, sourceKit := range []string{"", "original-publisher"} {
				t.Run(fmt.Sprintf("group=%t/accept=%t/source=%s", grouped, accept, sourceKit), func(t *testing.T) {
					reg := newRegistry(t)
					entry := spec.Capability{Type: spec.CapabilityVolume, Config: map[string]any{"path": "/cache"}}
					if grouped {
						entry = spec.Capability{Group: &spec.CapabilityGroup{Optional: true, Capabilities: []spec.Capability{entry}}}
					} else {
						entry.Optional = true
					}
					entry.Source = &spec.CapabilitySource{Kit: sourceKit, Path: "capabilities[7]"}
					d := &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindMixin, Capabilities: []spec.Capability{entry}}
					reg.tag("kits/source", "1.0.0", reg.image(t, kitJSON(t, d)))
					client, err := New()
					require.NoError(t, err)
					ref := reg.ref("kits/source", "1.0.0")
					result, err := client.ResolvePartial(t.Context(), reqs(ref), WithCapabilitySelector(func(context.Context, spec.Descriptor, spec.Capability) spec.CapabilityDecision {
						return spec.CapabilityDecision{Accepted: accept}
					}))
					require.NoError(t, err)
					selection := result.Selections[0]
					records := selection.Selection.Skipped
					if accept {
						records = selection.Selection.Selected
					}
					require.Len(t, records, 1)
					wantKit := sourceKit
					if wantKit == "" {
						wantKit = ref
					}
					require.Equal(t, &spec.CapabilitySource{Kit: wantKit, Path: "capabilities[7]"}, records[0].Source)
					original := selection.PublishedDescriptor.Capabilities[0].Source
					require.Equal(t, entry.Source, original)
					require.NotSame(t, original, records[0].Source)
				})
			}
		}
	}
}

func TestRequiredRejectionCompletesPathOnlySources(t *testing.T) {
	for _, grouped := range []bool{false, true} {
		t.Run(fmt.Sprint(grouped), func(t *testing.T) {
			reg := newRegistry(t)
			entry := spec.Capability{Type: spec.CapabilityVolume, Config: map[string]any{"path": "/cache"}, Source: &spec.CapabilitySource{Path: "capabilities[7]"}}
			if grouped {
				entry = spec.Capability{Group: &spec.CapabilityGroup{Capabilities: []spec.Capability{entry}}, Source: &spec.CapabilitySource{Path: "capabilities[8]"}}
			}
			d := &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindMixin, Capabilities: []spec.Capability{entry}}
			reg.tag("kits/source", "1.0.0", reg.image(t, kitJSON(t, d)))
			client, err := New()
			require.NoError(t, err)
			ref := reg.ref("kits/source", "1.0.0")
			_, err = client.ResolvePartial(t.Context(), reqs(ref), WithCapabilitySelector(func(context.Context, spec.Descriptor, spec.Capability) spec.CapabilityDecision {
				return spec.CapabilityDecision{Message: "storage unavailable"}
			}))
			require.ErrorContains(t, err, "original source "+ref+" capabilities[7]")
			require.ErrorContains(t, err, "storage unavailable")
			// Normalization must copy both group and member source pointers.
			copy := withSelectionSources(d, ref)
			require.Equal(t, "", d.Capabilities[0].Source.Kit)
			require.NotSame(t, d.Capabilities[0].Source, copy.Capabilities[0].Source)
			if grouped {
				require.Equal(t, "", d.Capabilities[0].Group.Capabilities[0].Source.Kit)
				require.NotSame(t, d.Capabilities[0].Group.Capabilities[0].Source, copy.Capabilities[0].Group.Capabilities[0].Source)
			}
		})
	}
}

func TestResolveSelectorReceivesOwningKit(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprint(partial), func(t *testing.T) {
			reg := newRegistry(t)
			ctx := t.Context()
			var requests []Request
			want := map[string]string{}
			for i, name := range []string{"Main Kit", "Tool Kit"} {
				kind := spec.KindMixin
				if i == 0 && !partial {
					kind = spec.KindWorkload
				}
				value := "expanded"
				d := &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: kind, DisplayName: name,
					Args:         map[string]spec.Arg{"value": {Default: &value}},
					Capabilities: []spec.Capability{{Type: "com.example/feature@1", Optional: true, Config: map[string]any{"value": "${{ kit.args.value }}", "home": "${{ kit.env.HOME }}"}}},
				}
				path := fmt.Sprintf("kits/owner%d", i)
				reg.tag(path, "1.0.0", reg.image(t, kitJSON(t, d)))
				ref := reg.ref(path, "1.0.0")
				requests = append(requests, Request{Reference: ref})
				want[ref] = name
			}
			client, err := New()
			require.NoError(t, err)
			method := client.Resolve
			if partial {
				method = client.ResolvePartial
			}
			seen := map[string]string{}
			result, err := method(ctx, requests, WithEnvironment(map[string]string{"HOME": "/home/agent"}, nil),
				WithCapabilitySelector(func(callCtx context.Context, kit spec.Descriptor, c spec.Capability) spec.CapabilityDecision {
					require.Same(t, ctx, callCtx)
					require.Len(t, kit.Capabilities, 1, "descriptor belongs to this Kit, before composition")
					require.Equal(t, c, kit.Capabilities[0])
					require.Equal(t, map[string]any{"value": "expanded", "home": "/home/agent"}, c.Config)
					seen[c.Source.Kit] = kit.DisplayName
					return spec.CapabilityDecision{Accepted: kit.DisplayName == "Tool Kit"}
				}))
			require.NoError(t, err)
			require.Equal(t, want, seen)
			require.Len(t, result.Descriptor.Capabilities, 1, "policy can decide using the owning Kit")
		})
	}
}
