package spec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func groupHook(text string) Capability {
	return Capability{Type: CapabilityLifecycle, Config: map[string]any{"startup": []any{map[string]any{"command": text}}}}
}

func TestGroupGrammar(t *testing.T) {
	for _, item := range []string{
		"group: {}",
		"declarationBlocks: null\ngroup: {capabilities: [{type: com.example/member@1}]}",
		"declarationBlocks: [12]\ngroup: {capabilities: [{type: com.example/member@1}]}",
		"declarationBlocks: [null]\ngroup: {capabilities: [{type: com.example/member@1}]}",
		"declarationBlocks: ['']\ngroup: {capabilities: [{type: com.example/member@1}]}",
		"declarationBlocks: [same, same]\ngroup: {capabilities: [{type: com.example/member@1}]}",
		"declarationBlocks: [same]\ngroup: {capabilities: [{type: com.example/member@1}, {type: com.example/other@1}]}",
		"group: {capabilities: [{type: com.example/member@1, declarationBlocks: []}]}",
		"group: {capabilities: []}",
		"group: null",
		"type: com.example/feature@1\ngroup: {capabilities: [{type: com.example/member@1}]}",
		"optional: false\ngroup: {capabilities: [{type: com.example/member@1}]}",
		"group: {name: 12, capabilities: [{type: com.example/member@1}]}",
		"group: {description: 12, capabilities: [{type: com.example/member@1}]}",
		"group: {description: true, capabilities: [{type: com.example/member@1}]}",
		"group: {description: null, capabilities: [{type: com.example/member@1}]}",
		"group: {optional: null, capabilities: [{type: com.example/member@1}]}",
		"group: {unknown: true, capabilities: [{type: com.example/member@1}]}",
		"group: {capabilities: [{type: com.example/member@1, unknown: true}]}",
		"group: {capabilities: [{type: com.example/member@1, optional: false}]}",
		"group: {capabilities: [{type: com.example/member@1, optional: true}]}",
		"group: {capabilities: [{group: {capabilities: [{type: com.example/member@1}]}}]}",
		"group: {capabilities: [{type: com.docker.sandbox/volume@1, config: {path: relative}}]}",
	} {
		t.Run(item, func(t *testing.T) {
			raw := []byte("schemaVersion: '3'\nkind: mixin\ncapabilities:\n  - " + strings.ReplaceAll(item, "\n", "\n    ") + "\n")
			d, err := Decode(raw)
			if err == nil {
				_, err = ValidateRaw(raw, d)
			}
			require.Error(t, err)
			var v any
			require.NoError(t, yaml.Unmarshal(raw, &v))
			encoded, err := json.Marshal(v)
			require.NoError(t, err)
			d, err = Decode(encoded)
			if err == nil {
				_, err = ValidateRaw(encoded, d)
			}
			require.Error(t, err)
			var jsonDescriptor Descriptor
			jsonErr := json.Unmarshal(encoded, &jsonDescriptor)
			if jsonErr == nil {
				_, jsonErr = ValidateRaw(encoded, &jsonDescriptor)
			}
			require.Error(t, jsonErr)
		})
	}
}

func TestGroupValidationPathsAndBlocks(t *testing.T) {
	d := &Descriptor{Kind: KindMixin, Capabilities: []Capability{groupHook("base"), {Group: &CapabilityGroup{Optional: true, Capabilities: []Capability{groupHook("selected"), {Type: CapabilityVolume, Config: map[string]any{"path": "relative"}}}}}}}
	_, err := Validate(d)
	require.ErrorContains(t, err, "capabilities[1].group.capabilities[1].config.path")
	d.Capabilities[1].Group.Capabilities = d.Capabilities[1].Group.Capabilities[:1]
	_, err = Validate(d)
	require.NoError(t, err, "singleton arity is per declaration block")
	d.Capabilities = append(d.Capabilities, groupHook("duplicate"))
	_, err = Validate(d)
	require.ErrorContains(t, err, "capabilities[2].type")
	d.Capabilities = d.Capabilities[:2]
	d.Capabilities[1].Group.Capabilities = append(d.Capabilities[1].Group.Capabilities, groupHook("duplicate"))
	_, err = Validate(d)
	require.ErrorContains(t, err, "capabilities[1].group.capabilities[1].type")
}

func TestDuplicateDiagnosticsRetainOriginalPaths(t *testing.T) {
	for _, duplicate := range []Capability{
		groupHook("same"),
		{Type: CapabilityVolume, Config: map[string]any{"path": "/cache"}},
	} {
		group := Capability{Group: &CapabilityGroup{Capabilities: []Capability{{Type: "com.example/feature@1"}}}}
		d := &Descriptor{Kind: KindMixin, Capabilities: []Capability{group, duplicate, group, duplicate}}
		_, err := Validate(d)
		require.Error(t, err)
		require.Contains(t, err.Error(), "capabilities[3]")
		require.Contains(t, err.Error(), "capabilities[1]")
		require.NotContains(t, err.Error(), "capabilities[0]")
		var field *FieldError
		require.ErrorAs(t, err, &field)
		require.Contains(t, field.Path, "capabilities[3]")

		d.Capabilities = []Capability{group, {Group: &CapabilityGroup{Capabilities: []Capability{duplicate, duplicate}}}}
		_, err = Validate(d)
		require.ErrorContains(t, err, "capabilities[1].group.capabilities[0]")
		require.ErrorContains(t, err, "capabilities[1].group.capabilities[1]")
	}
	sentinel := errors.New("typed cause")
	original := &FieldError{Path: "capabilities[1].config", err: fmt.Errorf("capabilities[1] duplicates capabilities[0]: %w", sentinel)}
	mapped := remapCapabilityErrors(original, []string{"capabilities[1]", "capabilities[3]"})
	require.ErrorIs(t, mapped, sentinel)
	require.ErrorContains(t, mapped, "capabilities[3] duplicates capabilities[1]")
	require.Equal(t, "capabilities[1].config", original.Path)
}

func TestSelectCapabilitiesAtomicAndOrdered(t *testing.T) {
	items := []Capability{groupHook("before"), {Group: &CapabilityGroup{Name: "feature", Optional: true, Capabilities: []Capability{{Type: "com.example/feature@1"}, groupHook("inside")}}},
		{Group: &CapabilityGroup{Name: "feature", Capabilities: []Capability{{Type: CapabilityVolume, Config: map[string]any{"path": "/data"}}}}}}
	var calls []string
	selected, err := SelectCapabilities(t.Context(), "test-kit", &Descriptor{Kind: KindWorkload, Capabilities: items}, func(_ context.Context, _ SelectionTarget, _ Descriptor, c Capability) CapabilityDecision {
		calls = append(calls, c.Type)
		return CapabilityDecision{Accepted: c.Type != "com.example/feature@1"}
	})
	require.NoError(t, err)
	require.Len(t, calls, 4, "all members evaluated for rejection diagnostics")
	require.Len(t, selected.Capabilities, 2)
	require.Len(t, selected.Selected, 2)
	require.Len(t, selected.Skipped, 1)
	require.Equal(t, []string{"capabilities[1].group.capabilities[0]"}, selected.Skipped[0].Rejected)
	require.Equal(t, "capabilities[2].group.capabilities[0]", selected.Capabilities[1].Source.Path)
	surface := SurfaceOf(&Descriptor{Capabilities: selected.Capabilities})
	require.Equal(t, []string{"/data"}, surface.StoragePaths)
	require.Empty(t, surface.Services)
	accepted, err := SelectCapabilities(t.Context(), "test-kit", &Descriptor{Kind: KindWorkload, Capabilities: items}, Supported(CapabilityLifecycle, CapabilityVolume, "com.example/feature@1"))
	require.NoError(t, err)
	require.Len(t, accepted.Capabilities, 4)
	require.Equal(t, CapabilityLifecycle, accepted.Capabilities[2].Type)
	items[1].Group.Optional = false
	refused, err := SelectCapabilities(t.Context(), "test-kit", &Descriptor{Kind: KindWorkload, Capabilities: items}, Supported(CapabilityLifecycle, CapabilityVolume))
	require.ErrorContains(t, err, "capabilities[1].group.capabilities[0]")
	require.Len(t, refused.Skipped, 1)
	require.Len(t, items[1].Group.Capabilities, 2, "selection does not mutate declarations")
}

func TestSelectionValidatesBeforeCallingRuntime(t *testing.T) {
	for _, c := range []Capability{
		{Type: CapabilityVolume, Config: map[string]any{"path": "relative"}},
		{Type: CapabilityPort, Config: map[string]any{"container": "${{ kit.args.port }}"}},
	} {
		_, err := SelectCapabilities(t.Context(), "test-kit", &Descriptor{Kind: KindWorkload, Capabilities: []Capability{{Group: &CapabilityGroup{Optional: true, Capabilities: []Capability{c}}}}}, func(context.Context, SelectionTarget, Descriptor, Capability) CapabilityDecision {
			t.Fatal("invalid declarations must not reach runtime selection")
			return CapabilityDecision{Accepted: false}
		})
		require.Error(t, err)
	}
	_, err := SelectCapabilities(t.Context(), "test-kit", nil, nil)
	require.Error(t, err)
}

func TestSelectionValidatesDescriptorKindBeforePolicy(t *testing.T) {
	for _, member := range []Capability{
		{Type: CapabilityAgentContext, Config: map[string]any{"filename": "AGENTS.md"}},
		{Type: CapabilitySbx},
	} {
		for _, grouped := range []bool{false, true} {
			item := member
			if grouped {
				item = Capability{Group: &CapabilityGroup{Optional: true, Capabilities: []Capability{member}}}
			}
			d := &Descriptor{Kind: KindMixin, Capabilities: []Capability{item}}
			_, err := SelectCapabilities(t.Context(), "test-kit", d, func(context.Context, SelectionTarget, Descriptor, Capability) CapabilityDecision {
				t.Fatal("invalid mixin declaration reached runtime policy")
				return CapabilityDecision{Accepted: false}
			})
			require.ErrorContains(t, err, "workload")
			d.Kind = KindWorkload
			selected, err := SelectCapabilities(t.Context(), "test-kit", d, Supported(member.Type))
			require.NoError(t, err)
			require.Len(t, selected.Capabilities, 1)
		}
	}
	_, err := SelectCapabilities(t.Context(), "test-kit", nil, Supported())
	require.ErrorContains(t, err, "no descriptor")
}

func TestSelectedCompositionAndPublishedOrder(t *testing.T) {
	a := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{groupHook("same"), {Group: &CapabilityGroup{Optional: true, Capabilities: []Capability{{Type: "com.example/feature@1"}, groupHook("inside")}}}}}
	b := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{groupHook("same")}}
	inputs := []Contribution{{Reference: "a", Descriptor: a}, {Reference: "b", Descriptor: b}}
	published, err := Merge(inputs, MergeOptions{})
	require.NoError(t, err)
	raw, err := json.Marshal(published.Descriptor)
	require.NoError(t, err)
	decoded, err := Decode(raw)
	require.NoError(t, err)
	_, err = ValidatePublished(raw, decoded)
	require.NoError(t, err)
	for _, accept := range []bool{false, true} {
		t.Run(fmt.Sprint(accept), func(t *testing.T) {
			selector := func(_ context.Context, _ SelectionTarget, _ Descriptor, c Capability) CapabilityDecision {
				return CapabilityDecision{Accepted: accept || c.Type != "com.example/feature@1"}
			}
			var direct []Contribution
			for _, input := range inputs {
				selection, err := SelectCapabilities(t.Context(), "test-kit", input.Descriptor, selector)
				require.NoError(t, err)
				d := *input.Descriptor
				d.Capabilities = selection.Capabilities
				direct = append(direct, Contribution{Reference: input.Reference, Descriptor: &d})
			}
			merged, err := Compose(direct)
			require.NoError(t, err)
			selection, err := SelectCapabilities(t.Context(), "test-kit", decoded, selector)
			require.NoError(t, err)
			effective := *decoded
			effective.Capabilities = selection.Capabilities
			fromSet, err := Compose([]Contribution{{Reference: "published", Descriptor: &effective}})
			require.NoError(t, err)
			want, err := LifecycleOf(merged.Capabilities)
			require.NoError(t, err)
			got, err := LifecycleOf(fromSet.Capabilities)
			require.NoError(t, err)
			require.Equal(t, want, got)
			require.Len(t, got.Startup, 2+map[bool]int{true: 1}[accept], "identical hooks are not deduplicated")
		})
	}
}

func TestConcreteStorageMergePreservesGroupSelection(t *testing.T) {
	volume := func(size string) Capability {
		return Capability{Type: CapabilityVolume, Config: map[string]any{"path": "/cache", "size": size}}
	}
	inputs := []Contribution{
		{Reference: "a", Descriptor: &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{volume("1g")}}},
		{Reference: "b", Descriptor: &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{
			{Group: &CapabilityGroup{Optional: true, Capabilities: []Capability{{Type: "com.example/feature@1"}, volume("2g")}}},
			volume("1024m"),
		}}},
	}
	merged, err := Merge(inputs, MergeOptions{})
	require.NoError(t, err, "a conditional conflict waits for group selection")
	require.Len(t, merged.Descriptor.Capabilities, 2, "only ungrouped matching storage collapses")
	require.Len(t, merged.Descriptor.Capabilities[1].Group.Capabilities, 2, "the explicit group remains intact")
	raw, err := json.Marshal(merged.Descriptor)
	require.NoError(t, err)
	_, err = ValidatePublished(raw, merged.Descriptor)
	require.NoError(t, err)
	for _, accept := range []bool{false, true} {
		selected, err := SelectCapabilities(t.Context(), "test-kit", merged.Descriptor, func(_ context.Context, _ SelectionTarget, _ Descriptor, c Capability) CapabilityDecision {
			return CapabilityDecision{Accepted: accept || c.Type != "com.example/feature@1"}
		})
		require.NoError(t, err)
		d := *merged.Descriptor
		d.Capabilities = selected.Capabilities
		composed, err := Compose([]Contribution{{Reference: "set", Descriptor: &d}})
		if accept {
			require.ErrorContains(t, err, "different com.docker.sandbox/volume@1 configurations at /cache")
			continue
		}
		require.NoError(t, err)
		volumes, err := VolumesOf(composed.Capabilities)
		require.NoError(t, err)
		require.Equal(t, []Volume{{Path: "/cache", Size: "1g"}}, volumes)
	}
}

func TestPublishPreservesConditionalContext(t *testing.T) {
	d := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{
		{Type: CapabilityAgentContext, Config: map[string]any{"content": "always"}},
		{Group: &CapabilityGroup{Optional: true, Capabilities: []Capability{{Type: "com.example/feature@1"}, {Type: CapabilityAgentContext, Config: map[string]any{"contentFile": "/kit/context.md"}}}}},
	}}
	result, err := Merge([]Contribution{{Reference: "source", Descriptor: d}}, MergeOptions{ContextPath: "/set/context.md"})
	require.NoError(t, err)
	require.Len(t, result.ContextSources, 2)
	require.NotEqual(t, result.ContextSources[0].Target, result.ContextSources[1].Target)
	selection, err := SelectCapabilities(t.Context(), "test-kit", result.Descriptor, Supported(CapabilityAgentContext))
	require.NoError(t, err)
	require.Len(t, selection.Capabilities, 1)
	require.Equal(t, "/set/context.md.parts/0.md", selection.Capabilities[0].Config["contentFile"])
	require.Equal(t, "capabilities[0]", selection.Capabilities[0].Source.Path)
	require.Equal(t, "/kit/context.md", d.Capabilities[1].Group.Capabilities[1].Config["contentFile"])
}

// A request whose key still holds a reference sends the whole set down the
// deferred path, which keeps the other kits' requests apart too, but an
// unrelated conflict between concrete requests is still refused at
// publication, as it would be without the reference. An optional group
// waits for selection, which may decline it for an alternative its kit
// offers. Identical concrete requests publish apart and reconcile at
// create, and an agent-context body is staged for its own entry to carry
// through selection.
func TestDeferredPublicationRefusesConcreteConflicts(t *testing.T) {
	deferring := Capability{Type: CapabilityPort, Config: map[string]any{"container": 9090, "transport": "${{ kit.args.transport }}"}}
	credential := Capability{Type: CapabilityCredential, Config: map[string]any{"service": "github", "phase": "runtime", "apiKey": map[string]any{"name": "TOKEN"}}}
	resources := func(cpu int) Capability {
		return Capability{Type: CapabilityResources, Config: map[string]any{"cpu": cpu}}
	}
	acp := func(command string) Capability {
		return Capability{Type: CapabilityACP, Config: map[string]any{"agent": "claude", "command": []any{command}}}
	}
	port := Capability{Type: CapabilityPort, Config: map[string]any{"container": 8080}}
	context := Capability{Type: CapabilityAgentContext, Config: map[string]any{"directory": "/home/agent/.claude", "filename": "CLAUDE.md", "content": "Be brief."}}
	referencedToken := Capability{Type: CapabilityCredential, Config: map[string]any{"service": "github", "phase": "runtime", "apiKey": map[string]any{"name": "${{ kit.args.token }}"}}}
	hostMount := Capability{Type: CapabilityHostMount, Config: map[string]any{"path": "/cache", "mode": "${{ kit.args.mode }}"}}
	volume := Capability{Type: CapabilityVolume, Config: map[string]any{"path": "/cache/"}}
	sizedVolume := Capability{Type: CapabilityVolume, Config: map[string]any{"path": "/cache/./", "size": "${{ kit.args.size }}"}}
	phase := func(phase any) Capability {
		return Capability{Type: CapabilityCredential, Config: map[string]any{"service": "github", "phase": phase, "apiKey": map[string]any{"name": "TOKEN"}}}
	}
	phasedToken := phase([]any{"install", "${{ kit.args.phase }}"})
	group := func(optional bool, members ...Capability) Capability {
		return Capability{Group: &CapabilityGroup{Optional: optional, Capabilities: members}}
	}
	for _, tc := range []struct {
		name          string
		first, second Capability
		err           string
	}{
		{"credential twins", credential, credential, "one credential has one owner"},
		{"credential twin with a reference beside its key", referencedToken, credential, "one credential has one owner"},
		{"host mount with a reference beside its path", hostMount, volume, "a host mount has one owner"},
		{"credential's literal phase beside a referenced one", phasedToken, phase("install"), "one credential has one owner"},
		{"credential's referenced phase", phasedToken, credential, ""},
		{"volume with a reference beside a host mount", sizedVolume, Capability{Type: CapabilityHostMount, Config: map[string]any{"path": "/cache"}}, "a host mount has one owner"},
		{"volume with a reference beside a volume", sizedVolume, Capability{Type: CapabilityVolume, Config: map[string]any{"path": "/cache", "size": "1g"}}, ""},
		{"credential in a required group", group(false, credential, port), credential, "one credential has one owner"},
		{"credential alone in an optional group", group(true, credential), credential, ""},
		{"credential in an optional group", group(true, credential, port), credential, ""},
		{"different resources", resources(1), resources(2), "it describes the whole sandbox"},
		{"different acp", acp("claude-acp"), acp("other-acp"), "ask for different things"},
		{"identical ports", port, port, ""},
		{"agent-context profiles", context, context, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kit := func(c Capability) *Descriptor {
				return &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{c}}
			}
			result, err := Merge([]Contribution{
				{Reference: "deferring-kit", Descriptor: &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin,
					Args: map[string]Arg{"transport": {Required: true}}, Capabilities: []Capability{deferring}}},
				{Reference: "first-kit", Descriptor: kit(tc.first)},
				{Reference: "second-kit", Descriptor: kit(tc.second)},
			}, MergeOptions{ContextPath: "/set/context.md"})
			if tc.err != "" {
				require.Nil(t, result)
				require.ErrorContains(t, err, tc.err)
				require.ErrorContains(t, err, "first-kit")
				require.ErrorContains(t, err, "second-kit")
				return
			}
			require.NoError(t, err)
			require.Len(t, result.Descriptor.Capabilities, 3)
			for _, c := range result.Descriptor.Capabilities {
				if c.Group == nil {
					require.Contains(t, []string{CapabilityVolume, CapabilityHostMount}, c.Type, "only concrete storage reconciles at publication")
				}
			}
			if tc.first.Type == CapabilityAgentContext {
				require.Len(t, result.ContextSources, 2)
				require.Equal(t, "/set/context.md.parts/0.md", result.ContextSources[0].Target)
				require.Equal(t, "/set/context.md.parts/0.md", result.Descriptor.Capabilities[1].Group.Capabilities[0].Config["contentFile"])
			}
		})
	}

	// One kit may offer two optional groups as alternatives for one key;
	// only selection can tell which it gets.
	alternatives := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{
		group(true, credential),
		group(true, Capability{Type: CapabilityCredential, Config: map[string]any{"service": "github", "phase": "runtime", "apiKey": map[string]any{"name": "OTHER_TOKEN"}}}),
	}}
	_, err := Validate(alternatives)
	require.NoError(t, err)
	_, err = Merge([]Contribution{{Reference: "alternatives-kit", Descriptor: alternatives}}, MergeOptions{})
	require.NoError(t, err)

	// A nested set's required requests arrive as required one-member
	// groups, and a conflict with one of them is still a conflict.
	inner, err := Merge([]Contribution{
		{Reference: "deferring-kit", Descriptor: &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin,
			Args: map[string]Arg{"transport": {Required: true}}, Capabilities: []Capability{deferring}}},
		{Reference: "credential-kit", Descriptor: &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{credential}}},
	}, MergeOptions{})
	require.NoError(t, err)
	_, err = Merge([]Contribution{
		{Reference: "inner-set", Descriptor: inner.Descriptor},
		{Reference: "other-kit", Descriptor: &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{credential}}},
	}, MergeOptions{})
	require.ErrorContains(t, err, "one credential has one owner")
	require.ErrorContains(t, err, "credential-kit")
	require.ErrorContains(t, err, "other-kit")
}

func TestGroupConflictNamesOriginalSources(t *testing.T) {
	file := Capability{Type: CapabilityLifecycle, Config: map[string]any{"files": []any{map[string]any{"path": "/same", "content": "x"}}}}
	d := &Descriptor{Kind: KindMixin, Capabilities: []Capability{file, {Group: &CapabilityGroup{Capabilities: []Capability{file}}}}}
	selected, err := SelectCapabilities(t.Context(), "test-kit", d, Supported(CapabilityLifecycle))
	require.NoError(t, err)
	d.Capabilities = selected.Capabilities
	_, err = Compose([]Contribution{{Reference: "source", Descriptor: d}})
	require.ErrorContains(t, err, "capabilities[0]")
	require.ErrorContains(t, err, "capabilities[1].group.capabilities[0]")
	require.ErrorContains(t, err, "source")
}

func TestSelectedContextBodiesRemainSeparate(t *testing.T) {
	items := []Capability{
		{Type: CapabilityAgentContext, Config: map[string]any{"content": "first"}},
		{Group: &CapabilityGroup{Capabilities: []Capability{{Type: CapabilityAgentContext, Config: map[string]any{"content": "second"}}}}},
	}
	_, err := AgentContextsOf(items)
	require.Error(t, err)
	selected, err := SelectCapabilities(t.Context(), "test-kit", &Descriptor{Kind: KindWorkload, Capabilities: items}, Supported(CapabilityAgentContext))
	require.NoError(t, err)
	bodies, err := AgentContextsOf(selected.Capabilities)
	require.NoError(t, err)
	require.Equal(t, []AgentContext{{Content: "first"}, {Content: "second"}}, bodies)
}

func TestGroupContextConflictRetainsOriginalMemberLocations(t *testing.T) {
	filename := Capability{Type: CapabilityAgentContext, Config: map[string]any{"filename": "AGENTS.md"}}
	d := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindWorkload, Capabilities: []Capability{
		{Group: &CapabilityGroup{Capabilities: []Capability{filename}}},
		{Group: &CapabilityGroup{Capabilities: []Capability{filename}}},
	}}
	published, err := Merge([]Contribution{{Reference: "original-kit", Descriptor: d}}, MergeOptions{})
	require.NoError(t, err)
	selected, err := SelectCapabilities(t.Context(), "test-kit", published.Descriptor, Supported(CapabilityAgentContext))
	require.NoError(t, err)
	effective := *published.Descriptor
	effective.Capabilities = selected.Capabilities
	_, err = Compose([]Contribution{{Reference: "consuming-set", Descriptor: &effective}})
	require.ErrorContains(t, err, "agent-context filename")
	require.ErrorContains(t, err, "original-kit capabilities[0].group.capabilities[0]")
	require.ErrorContains(t, err, "original-kit capabilities[1].group.capabilities[0]")
	require.ErrorContains(t, err, "consuming-set")
}

func TestPublicationCompletesPathOnlySources(t *testing.T) {
	for _, kit := range []string{"", "original-kit"} {
		t.Run("source-kit="+kit, func(t *testing.T) {
			ordinary := groupHook("ordinary")
			ordinary.Source = &CapabilitySource{Kit: kit, Path: "capabilities[7]"}
			member := groupHook("member")
			member.Source = &CapabilitySource{Kit: kit, Path: "capabilities[8].group.capabilities[2]"}
			d := &Descriptor{Kind: KindMixin, Capabilities: []Capability{ordinary, {
				Source: &CapabilitySource{Kit: kit, Path: "capabilities[8]"},
				Group:  &CapabilityGroup{Capabilities: []Capability{member, {Type: CapabilityVolume, Config: map[string]any{"path": "/cache"}}}},
			}}}
			before, err := json.Marshal(d)
			require.NoError(t, err)
			published, err := Merge([]Contribution{{Reference: "contributing-kit", Descriptor: d}}, MergeOptions{})
			require.NoError(t, err)
			wantKit := kit
			if wantKit == "" {
				wantKit = "contributing-kit"
			}
			for _, item := range published.Descriptor.Capabilities {
				require.Equal(t, wantKit, item.Source.Kit)
				for _, member := range item.Group.Capabilities {
					require.Equal(t, wantKit, member.Source.Kit)
				}
			}
			republished, err := Merge([]Contribution{{Reference: "published-set", Descriptor: published.Descriptor}}, MergeOptions{})
			require.NoError(t, err)
			selected, err := SelectCapabilities(t.Context(), "test-kit", republished.Descriptor, Supported(CapabilityLifecycle, CapabilityVolume))
			require.NoError(t, err)
			require.Equal(t, []CapabilitySource{{Kit: wantKit, Path: "capabilities[7]"}}, selected.Selected[0].MemberSources)
			require.Equal(t, []CapabilitySource{
				{Kit: wantKit, Path: "capabilities[8].group.capabilities[2]"},
				{Kit: wantKit, Path: "capabilities[8].group.capabilities[1]"},
			}, selected.Selected[1].MemberSources)
			// Publication must neither fill nor alias the caller's source objects.
			published.Descriptor.Capabilities[0].Source.Path = "changed"
			published.Descriptor.Capabilities[1].Source.Path = "changed"
			published.Descriptor.Capabilities[1].Group.Capabilities[0].Source.Path = "changed"
			after, err := json.Marshal(d)
			require.NoError(t, err)
			require.JSONEq(t, string(before), string(after))
			require.Equal(t, "capabilities[8]", republished.Descriptor.Capabilities[1].Source.Path)
		})
	}
}

func TestComposeOmitsSourcesFromEffectiveCapabilities(t *testing.T) {
	for _, capability := range []Capability{
		{Type: CapabilityVolume, Config: map[string]any{"path": "/cache"}},
		{Type: CapabilityPort, Config: map[string]any{"container": 8080}},
		{Type: CapabilityResources, Config: map[string]any{"cpus": 2}},
	} {
		t.Run(capability.Type, func(t *testing.T) {
			capability.Source = &CapabilitySource{Kit: "original-kit", Path: "capabilities[7]"}
			d := &Descriptor{Kind: KindMixin, Capabilities: []Capability{capability}}
			inputs := []Contribution{{Reference: "first", Descriptor: d}}
			if capability.Type == CapabilityPort {
				duplicate := capability
				duplicate.Source = &CapabilitySource{Kit: "another-kit", Path: "capabilities[9]"}
				inputs = append(inputs, Contribution{Reference: "second", Descriptor: &Descriptor{Kind: KindMixin, Capabilities: []Capability{duplicate}}})
			}
			merged, err := Compose(inputs)
			require.NoError(t, err)
			require.Len(t, merged.Capabilities, 1)
			require.Nil(t, merged.Capabilities[0].Source)
			require.Equal(t, capability.Config, merged.Capabilities[0].Config)
			require.Equal(t, &CapabilitySource{Kit: "original-kit", Path: "capabilities[7]"}, d.Capabilities[0].Source)
		})
	}
}

func TestSelectionRejectsUnexpandedCapabilityMetadata(t *testing.T) {
	ref := "${{ kit.args.label }}"
	for _, grouped := range []bool{false, true} {
		for _, field := range []string{"name", "description", "member-name", "member-description"} {
			t.Run(fmt.Sprintf("group=%t/%s", grouped, field), func(t *testing.T) {
				member := groupHook("true")
				if field == "member-name" {
					member.Name = ref
				}
				if field == "member-description" {
					member.Description = ref
				}
				item := member
				if grouped {
					item = Capability{Group: &CapabilityGroup{Capabilities: []Capability{member}}}
					if field == "name" {
						item.Group.Name = ref
					}
					if field == "description" {
						item.Group.Description = ref
					}
				} else {
					if field == "name" {
						item.Name = ref
					}
					if field == "description" {
						item.Description = ref
					}
				}
				// A valid first item must not reach policy before the later error.
				d := &Descriptor{Kind: KindMixin, Capabilities: []Capability{{Type: CapabilityVolume, Config: map[string]any{"path": "/cache"}}, item}}
				selection, err := SelectCapabilities(t.Context(), "test-kit", d, func(context.Context, SelectionTarget, Descriptor, Capability) CapabilityDecision {
					t.Fatal("unexpanded declarations reached policy")
					return CapabilityDecision{Accepted: true}
				})
				require.ErrorContains(t, err, "unresolved arguments")
				require.Empty(t, selection)
			})
		}
	}
}

func TestSelectionReceivesContextAndDescriptor(t *testing.T) {
	ctx := t.Context()
	d := &Descriptor{Kind: KindWorkload, DisplayName: "My Kit",
		Capabilities: []Capability{{Group: &CapabilityGroup{Capabilities: []Capability{
			{Type: "com.example/feature@1", Config: map[string]any{"number": uint64(1 << 60)}},
			groupHook("true"),
		}}}},
	}
	calls := 0
	selection, err := SelectCapabilities(ctx, "test-kit", d, func(callCtx context.Context, _ SelectionTarget, kit Descriptor, c Capability) CapabilityDecision {
		require.Same(t, ctx, callCtx)
		require.Equal(t, *d, kit, "callbacks receive all declarations before selection")
		require.Equal(t, kit.Capabilities[0].Group.Capabilities[calls], c)
		calls++
		return CapabilityDecision{Accepted: true}
	})
	require.NoError(t, err)
	require.Equal(t, 2, calls)
	require.Equal(t, uint64(1<<60), selection.Capabilities[0].Config["number"])
}

func TestSelectionHonorsContextCancellation(t *testing.T) {
	for _, before := range []bool{false, true} {
		for _, accept := range []bool{false, true} {
			t.Run(fmt.Sprintf("before=%t/accept=%t", before, accept), func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if before {
					cancel()
				}
				calls := 0
				d := &Descriptor{Kind: KindWorkload, Capabilities: []Capability{{Group: &CapabilityGroup{Capabilities: []Capability{groupHook("first"), {Type: CapabilityVolume, Config: map[string]any{"path": "/data"}}}}}}}
				selection, err := SelectCapabilities(ctx, "test-kit", d, func(callCtx context.Context, _ SelectionTarget, _ Descriptor, _ Capability) CapabilityDecision {
					calls++
					cancel()
					require.ErrorIs(t, callCtx.Err(), context.Canceled)
					return CapabilityDecision{Accepted: accept}
				})
				require.ErrorIs(t, err, context.Canceled)
				require.Empty(t, selection)
				if before {
					require.Zero(t, calls)
				} else {
					require.Equal(t, 1, calls, "remaining members are not evaluated after cancellation")
				}
			})
		}
	}
}

func TestSelectionCancellationDiscardsEarlierItems(t *testing.T) {
	for _, acceptEarlier := range []bool{false, true} {
		t.Run(fmt.Sprintf("accept earlier=%t", acceptEarlier), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			d := &Descriptor{Kind: KindMixin, Capabilities: []Capability{
				{Type: "com.example/feature@1", Optional: true},
				groupHook("later"),
			}}
			calls := 0
			selection, err := SelectCapabilities(ctx, "test-kit", d, func(context.Context, SelectionTarget, Descriptor, Capability) CapabilityDecision {
				calls++
				if calls == 1 {
					return CapabilityDecision{Accepted: acceptEarlier, Message: "earlier decision"}
				}
				cancel()
				return CapabilityDecision{Accepted: true}
			})
			require.ErrorIs(t, err, context.Canceled)
			require.Equal(t, 2, calls)
			require.Equal(t, Selection{}, selection, "cancellation discards earlier selected and skipped records")
		})
	}
}

func TestSelectionRetainsDecisionMessages(t *testing.T) {
	for _, grouped := range []bool{false, true} {
		for _, optional := range []bool{false, true} {
			for _, accept := range []bool{false, true} {
				t.Run(fmt.Sprintf("group=%t/optional=%t/accept=%t", grouped, optional, accept), func(t *testing.T) {
					item := Capability{Type: CapabilityVolume, Optional: optional, Config: map[string]any{"path": "/data"}}
					if grouped {
						item.Optional = false
						item = Capability{Group: &CapabilityGroup{Optional: optional, Capabilities: []Capability{item, groupHook("true")}}}
					}
					decision := CapabilityDecision{Accepted: accept, Message: "storage policy: 100% reviewed"}
					want := []CapabilityDecision{decision}
					if grouped {
						want = append(want, CapabilityDecision{Accepted: true, Message: "hooks available"})
					}
					selection, err := SelectCapabilities(t.Context(), "test-kit", &Descriptor{Kind: KindWorkload, Capabilities: []Capability{item}},
						func(_ context.Context, _ SelectionTarget, _ Descriptor, c Capability) CapabilityDecision {
							if c.Type == CapabilityVolume {
								return decision
							}
							return CapabilityDecision{Accepted: true, Message: "hooks available"}
						})
					if !accept && !optional {
						require.ErrorContains(t, err, decision.Message)
						require.NotContains(t, err.Error(), "hooks available", "only rejected members explain refusal")
						var field *FieldError
						require.ErrorAs(t, err, &field)
						require.Equal(t, selection.Skipped[0].Members[0], field.Path)
					} else {
						require.NoError(t, err)
					}
					records := selection.Skipped
					if accept {
						records = selection.Selected
					}
					require.Len(t, records, 1)
					require.Equal(t, want, records[0].Decisions)
					require.Len(t, records[0].Members, len(want))
					raw, err := json.Marshal(selection)
					require.NoError(t, err)
					var persisted Selection
					require.NoError(t, json.Unmarshal(raw, &persisted))
					require.Equal(t, selection.Selected, persisted.Selected, "selected decisions survive persistence for restart")
					require.Equal(t, selection.Skipped, persisted.Skipped, "skipped decisions survive persistence for restart")
				})
			}
		}
	}
}

func TestSelectionDecisionDefaultsAndSupported(t *testing.T) {
	d := &Descriptor{Kind: KindWorkload, Capabilities: []Capability{groupHook("true")}}
	selection, err := SelectCapabilities(t.Context(), "test-kit", d, func(context.Context, SelectionTarget, Descriptor, Capability) CapabilityDecision {
		return CapabilityDecision{}
	})
	require.ErrorContains(t, err, "required capability selection rejected")
	require.Empty(t, selection.Capabilities)
	require.Equal(t, []CapabilityDecision{{}}, selection.Skipped[0].Decisions)

	selector := Supported(CapabilityLifecycle)
	require.Equal(t, CapabilityDecision{Accepted: true}, selector(t.Context(), SelectionTarget{}, *d, d.Capabilities[0]))
	decision := selector(t.Context(), SelectionTarget{}, *d, Capability{Type: CapabilityVolume})
	require.False(t, decision.Accepted)
	require.Contains(t, decision.Message, CapabilityVolume)
}

func TestEffectiveCapabilitiesOmitDeclarationBlocks(t *testing.T) {
	d := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin,
		Capabilities: []Capability{{Type: CapabilityVolume, DeclarationBlocks: []string{"original"}, Config: map[string]any{"path": "/cache"}}}}
	raw, err := json.Marshal(d)
	require.NoError(t, err)
	_, err = ValidateExpandedDeclarations(raw, d)
	require.NoError(t, err)
	_, err = ValidateEffective(raw, d)
	require.ErrorContains(t, err, "unselected declaration blocks")
	selected, err := SelectCapabilities(t.Context(), "test-kit", d, Supported(CapabilityVolume))
	require.NoError(t, err)
	d.Capabilities = selected.Capabilities
	raw, err = json.Marshal(d)
	require.NoError(t, err)
	_, err = ValidateEffective(raw, d)
	require.NoError(t, err)
}

// inBlocks declares each entry in the same blocks declaration blocks.
func inBlocks(blocks int, entries ...Capability) *Descriptor {
	ids := make([]string, blocks)
	for i := range ids {
		ids[i] = fmt.Sprintf("b%d", i)
	}
	d := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin}
	for _, c := range entries {
		c.DeclarationBlocks = ids
		d.Capabilities = append(d.Capabilities, c)
	}
	return d
}

func acpWithEnv(vars int) Capability {
	env := make(map[string]any, vars)
	for i := range vars {
		env[fmt.Sprintf("K%d", i)] = strings.Repeat("v", 40)
	}
	return Capability{Type: CapabilityACP, Config: map[string]any{"agent": "codex", "command": []any{"codex-acp"}, "env": env}}
}

func TestEntryConfigReadOnceAcrossBlocks(t *testing.T) {
	for _, tt := range []struct {
		name     string
		validate func(raw []byte, d *Descriptor) ([]string, error)
	}{
		{"validate", func(_ []byte, d *Descriptor) ([]string, error) { return Validate(d) }},
		{"published", ValidatePublished},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// What a config's size costs is paid once per entry, not once
			// per block the entry joins.
			allocs := func(blocks, vars int) float64 {
				d := inBlocks(blocks, acpWithEnv(vars))
				raw, err := json.Marshal(d)
				require.NoError(t, err)
				_, err = tt.validate(raw, d)
				require.NoError(t, err)
				return testing.AllocsPerRun(3, func() { _, _ = tt.validate(raw, d) })
			}
			one := allocs(1, 500) - allocs(1, 1)
			many := allocs(256, 500) - allocs(256, 1)
			require.Positive(t, one)
			require.LessOrEqual(t, many, 2*one, "config cost in 256 blocks: %.0f allocations, in one: %.0f", many, one)
		})
	}
}

func TestBlockErrorsReportedOnce(t *testing.T) {
	volume := func(path, size string) Capability {
		config := map[string]any{"path": path}
		if size != "" {
			config["size"] = size
		}
		return Capability{Type: CapabilityVolume, Config: config}
	}
	bogus := acpWithEnv(1)
	bogus.Config["bogus"] = true
	oneMember := func(c Capability) Capability {
		return Capability{DeclarationBlocks: []string{"b0", "b1", "b2"}, Group: &CapabilityGroup{Capabilities: []Capability{c}}}
	}
	for _, tt := range []struct {
		name string
		d    *Descriptor
		// want counts each message's occurrences.
		want map[string]int
	}{
		{"own error in every block", inBlocks(8, bogus), map[string]int{`capabilities[0].config.bogus: capabilities[0]:`: 1}},
		{"own error of a one-member group", &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{oneMember(volume("relative", ""))}},
			map[string]int{"capabilities[0].group.capabilities[0]: volume path": 1}},
		{"identical entries in every block", inBlocks(8, volume("/cache", ""), volume("/cache", "")), map[string]int{"identical to capabilities[0]": 8}},
		{"repeated key in every block", inBlocks(8, volume("/cache", ""), volume("/cache", "1G")), map[string]int{"already declared at capabilities[0]": 8}},
		{"identical invalid entries", inBlocks(4, volume("relative", ""), volume("relative", "")),
			map[string]int{"capabilities[0]: volume path": 1, "capabilities[1]: volume path": 1, "identical to capabilities[0]": 4}},
		{"undeclared blocks", &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{bogus, bogus}},
			map[string]int{`capabilities[0].config.bogus`: 1, `capabilities[1].config.bogus`: 1, "identical to capabilities[0]": 1}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Validate(tt.d)
			require.Error(t, err)
			for message, count := range tt.want {
				require.Equal(t, count, strings.Count(err.Error(), message), "%q in:\n%v", message, err)
			}
		})
	}
}

func TestRemapCostGrowsWithErrors(t *testing.T) {
	// Every repeat of a singleton in one block is an error naming two
	// entries, so remapping touches each error and each path once.
	allocs := func(entries int) float64 {
		d := &Descriptor{Kind: KindMixin}
		for range entries {
			d.Capabilities = append(d.Capabilities, Capability{Type: CapabilityPrivileged, DeclarationBlocks: []string{"b"}})
		}
		_, err := Validate(d)
		require.ErrorContains(t, err, "already declared")
		return testing.AllocsPerRun(3, func() { _, _ = Validate(d) })
	}
	small, large := allocs(64), allocs(512)
	require.LessOrEqual(t, large, 12*small, "512 entries: %.0f allocations, 64: %.0f", large, small)
}

func TestCapabilityIndex(t *testing.T) {
	for _, tt := range []struct {
		path string
		i    int
		rest string
		ok   bool
	}{
		{"capabilities[0]", 0, "", true},
		{"capabilities[12].config.path", 12, ".config.path", true},
		{"capabilities[1]x", 0, "", false},
		{"capabilities[01]", 0, "", false},
		{"capabilities[]", 0, "", false},
		{"capabilities[-1]", 0, "", false},
		{"capabilities[1", 0, "", false},
		{"kits[0]", 0, "", false},
	} {
		t.Run(tt.path, func(t *testing.T) {
			i, rest, ok := capabilityIndex(tt.path)
			require.Equal(t, tt.ok, ok)
			if ok {
				require.Equal(t, tt.i, i)
				require.Equal(t, tt.rest, rest)
			}
		})
	}
}
