package spec

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func contextV2(config map[string]any) Capability {
	return Capability{Type: CapabilityAgentContextV2, Config: config}
}

func TestAgentContextV2StrictValidation(t *testing.T) {
	for _, tt := range []struct {
		name   string
		config map[string]any
		want   string
	}{
		{"body", map[string]any{"content": "Use the tools."}, ""},
		{"profile", map[string]any{"filename": "AGENTS.md"}, ""},
		{"explicit profile", map[string]any{"directory": "/home/agent/.agent", "filename": "AGENTS.md"}, ""},
		{"file", map[string]any{"contentFile": "./body.md"}, "unknown field"},
		{"file and template", map[string]any{"contentFile": "./body.md", "content": "${{ kit.args.team }}"}, "unknown field"},
		{"typo", map[string]any{"contents": "body"}, "unknown field"},
		{"null", map[string]any{"content": nil}, "must be a string"},
		{"malformed arg", map[string]any{"content": "${{ kit.args.bad-name }}"}, "malformed Kit reference"},
		{"malformed env", map[string]any{"content": "${{ kit.env.HOME"}, "malformed Kit reference"},
		{"empty", map[string]any{}, "requires a profile"},
		{"empty body", map[string]any{"content": ""}, "requires a profile"},
		{"relative directory", map[string]any{"directory": ".agent", "filename": "AGENTS.md"}, "absolute, canonical"},
		{"missing filename", map[string]any{"directory": "/home/agent/.agent", "content": "body"}, "requires filename"},
		{"nested filename", map[string]any{"directory": "/home/agent/.agent", "filename": "sub/AGENTS.md"}, "single filename"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindWorkload, Args: map[string]Arg{"team": {}}, Capabilities: []Capability{contextV2(tt.config)}}
			raw, err := json.Marshal(d)
			require.NoError(t, err)
			for _, validate := range []func([]byte, *Descriptor) ([]string, error){ValidateRaw, ValidatePublished} {
				_, err := validate(raw, d)
				if tt.want == "" {
					require.NoError(t, err)
				} else {
					require.ErrorContains(t, err, tt.want)
				}
			}
		})
	}
	// The versioned accessor never silently interprets @1 as @2.
	profiles, err := AgentContextsV2Of([]Capability{{Type: CapabilityAgentContext, Config: map[string]any{"contentFile": "/kit/body.md"}}})
	require.NoError(t, err)
	require.Empty(t, profiles)
	require.Contains(t, KnownCapabilities(), CapabilityAgentContextV2)
	require.Equal(t, Surface{}, SurfaceOf(&Descriptor{Capabilities: []Capability{contextV2(map[string]any{"content": "body"})}}))
}

func TestAgentContextV2Composition(t *testing.T) {
	for _, multi := range []bool{false, true} {
		t.Run(fmt.Sprintf("multiple profiles=%t", multi), func(t *testing.T) {
			contributions := []Contribution{
				{Reference: "tools", Descriptor: &Descriptor{Kind: KindMixin, Capabilities: []Capability{contextV2(map[string]any{"content": "Tools guidance.\n"})}}},
				{Reference: "agent", Descriptor: &Descriptor{Kind: KindWorkload, Capabilities: []Capability{contextV2(map[string]any{"filename": "AGENTS.md", "content": "Agent guidance."})}}},
			}
			if multi {
				contributions = append(contributions, Contribution{Reference: "second", Descriptor: &Descriptor{Kind: KindMixin, Capabilities: []Capability{
					contextV2(map[string]any{"directory": "/home/agent/.agent", "filename": "AGENTS.md", "content": "Second guidance."}),
					contextV2(map[string]any{"directory": "/home/agent/.other", "filename": "OTHER.md"}),
				}}})
			}
			before, err := json.Marshal(contributions)
			require.NoError(t, err)
			d, err := Compose(contributions)
			require.NoError(t, err)
			raw, err := json.Marshal(d)
			require.NoError(t, err)
			decoded, err := Decode(raw)
			require.NoError(t, err)
			_, err = ValidateEffective(raw, decoded)
			require.NoError(t, err)
			profiles, err := AgentContextsV2Of(decoded.Capabilities)
			require.NoError(t, err)
			want := "Tools guidance.\n\n\nAgent guidance."
			count := 1
			if multi {
				count = 2
				want += "\n\nSecond guidance."
			}
			require.Len(t, profiles, count)
			for _, profile := range profiles {
				require.Equal(t, want, profile.Content)
			}
			if multi {
				require.Equal(t, "/home/agent/.agent", profiles[0].Directory)
			}
			after, err := json.Marshal(contributions)
			require.NoError(t, err)
			require.JSONEq(t, string(before), string(after))
		})
	}
	d, err := Compose([]Contribution{{Reference: "body-only", Descriptor: &Descriptor{Kind: KindMixin, Capabilities: []Capability{contextV2(map[string]any{"content": "body"})}}}})
	require.NoError(t, err)
	profiles, err := AgentContextsV2Of(d.Capabilities)
	require.NoError(t, err)
	require.Equal(t, []AgentContextV2{{Content: "body"}}, profiles)
}

func TestAgentContextV2PublishedSelection(t *testing.T) {
	for _, allOptional := range []bool{false, true} {
		for _, grouped := range []bool{false, true} {
			t.Run(fmt.Sprintf("optional=%t/grouped=%t", allOptional, grouped), func(t *testing.T) {
				first := contextV2(map[string]any{"directory": "/profiles/first", "filename": "AGENTS.md", "content": "First body."})
				first.Optional = true
				first.Name = "first"
				second := contextV2(map[string]any{"directory": "/profiles/second", "filename": "OTHER.md", "content": "Second body."})
				second.Optional = allOptional
				second.Name = "second"
				feature := contextV2(map[string]any{"content": "Optional feature body."})
				feature.Optional = true
				feature.Name = "feature"
				if grouped {
					feature.Optional = false
					feature = Capability{Group: &CapabilityGroup{Optional: true, Name: "feature", Capabilities: []Capability{feature, {Type: CapabilityVolume, Config: map[string]any{"path": "/cache"}}}}}
				}
				contributions := []Contribution{
					{Reference: "first", Descriptor: &Descriptor{Kind: KindMixin, Capabilities: []Capability{first}}},
					{Reference: "second", Descriptor: &Descriptor{Kind: KindWorkload, Capabilities: []Capability{second}}},
					{Reference: "feature", Descriptor: &Descriptor{Kind: KindMixin, Capabilities: []Capability{feature}}},
				}
				merged, err := Merge(contributions, MergeOptions{})
				require.NoError(t, err)
				require.Empty(t, merged.ContextSources)
				// A nested publication keeps the same boundaries, without combining bodies twice.
				nested, err := Merge([]Contribution{{Reference: "published", Descriptor: merged.Descriptor}}, MergeOptions{})
				require.NoError(t, err)
				require.Empty(t, nested.ContextSources)
				raw, err := json.Marshal(nested.Descriptor)
				require.NoError(t, err)
				published, err := Decode(raw)
				require.NoError(t, err)
				_, err = ValidatePublished(raw, published)
				require.NoError(t, err)
				for _, skipFirst := range []bool{false, true} {
					for _, skipFeature := range []bool{false, true} {
						selected, err := SelectCapabilities(t.Context(), "set", published, func(_ context.Context, _ SelectionTarget, _ Descriptor, c Capability) CapabilityDecision {
							if skipFirst && c.Name == "first" {
								return CapabilityDecision{}
							}
							if skipFeature && (c.Name == "feature" || c.Type == CapabilityVolume) {
								return CapabilityDecision{}
							}
							return CapabilityDecision{Accepted: true}
						})
						require.NoError(t, err)
						copy := *published
						copy.Capabilities = selected.Capabilities
						effective, err := Compose([]Contribution{{Reference: "set", Descriptor: &copy}})
						require.NoError(t, err)
						effectiveRaw, err := json.Marshal(effective)
						require.NoError(t, err)
						_, err = ValidateEffective(effectiveRaw, effective)
						require.NoError(t, err)
						profiles, err := AgentContextsV2Of(effective.Capabilities)
						require.NoError(t, err)
						count := 2
						if skipFirst {
							count = 1
						}
						require.Len(t, profiles, count)
						bodies := []string{}
						if !skipFirst {
							bodies = append(bodies, "First body.")
						}
						bodies = append(bodies, "Second body.")
						if !skipFeature {
							bodies = append(bodies, "Optional feature body.")
						}
						for _, p := range profiles {
							require.Equal(t, strings.Join(bodies, "\n\n"), p.Content)
						}
					}
				}
			})
		}
	}
}

func TestAgentContextV2MixedVersions(t *testing.T) {
	for _, body := range []map[string]any{{"content": "legacy"}, {"contentFile": "/kit/legacy.md"}, {"filename": "AGENTS.md"}} {
		legacy := &Descriptor{Kind: KindWorkload, Capabilities: []Capability{{Type: CapabilityAgentContext, Config: body}}}
		modern := &Descriptor{Kind: KindMixin, Capabilities: []Capability{contextV2(map[string]any{"content": "modern"})}}
		_, err := Compose([]Contribution{{Reference: "legacy", Descriptor: legacy}, {Reference: "modern", Descriptor: modern}})
		require.ErrorContains(t, err, "@1 and @2")
	}
}

func TestAgentContextV2CompositionSize(t *testing.T) {
	for _, size := range []int{SizeWarnBytes/2 + 100, SizeErrorBytes/2 + 100} {
		var contributions []Contribution
		for i := range 2 {
			contributions = append(contributions, Contribution{Reference: fmt.Sprint(i), Descriptor: &Descriptor{Kind: KindMixin, Capabilities: []Capability{contextV2(map[string]any{"content": strings.Repeat("x", size)})}}})
		}
		composed, err := Compose(contributions)
		if size > SizeErrorBytes/2 {
			require.ErrorContains(t, err, "byte budget")
			_, err = Merge(contributions, MergeOptions{})
			require.ErrorContains(t, err, "byte budget")
			continue
		}
		require.NoError(t, err)
		raw, err := json.Marshal(composed)
		require.NoError(t, err)
		warnings, err := ValidateEffective(raw, composed)
		require.NoError(t, err)
		require.NotEmpty(t, warnings)
		merged, err := Merge(contributions, MergeOptions{})
		require.NoError(t, err)
		require.NotEmpty(t, merged.Warnings)
	}
	// The hard budget counts the full content of every effective profile.
	_, err := Compose([]Contribution{{Reference: "profiles", Descriptor: &Descriptor{Kind: KindMixin, Capabilities: []Capability{
		contextV2(map[string]any{"directory": "/a", "filename": "AGENTS.md", "content": strings.Repeat("x", SizeErrorBytes/2)}),
		contextV2(map[string]any{"directory": "/b", "filename": "AGENTS.md"}),
	}}}})
	require.ErrorContains(t, err, "byte budget")
}

func TestAgentContextV2Expansion(t *testing.T) {
	raw := []byte(`schemaVersion: "3"
kind: workload
args:
  build: {buildArg: BUILD}
  team: {}
capabilities:
- type: com.docker.sandbox/agent-context@2
  config:
    filename: AGENTS.md
    content: 'Build: ${{ kit.args.build }}; team: ${{ kit.args.team }}; home: ${{ kit.env.HOME }}; $HOME ${HOME}'
`)
	d, err := Decode(raw)
	require.NoError(t, err)
	built, err := ExpandBuildArgs(raw, d.Args, map[string]string{"build": "release"})
	require.NoError(t, err)
	published, err := Decode(built)
	require.NoError(t, err)
	_, err = ValidatePublished(built, published)
	require.NoError(t, err)
	require.Contains(t, string(built), "team: ${{ kit.args.team }}")
	for _, size := range []int{10, SizeWarnBytes + 100, SizeErrorBytes + 100} {
		expanded, err := ExpandCreateArgs(built, d.Args, map[string]string{"team": strings.Repeat("x", size)})
		require.NoError(t, err)
		effective, err := Decode(expanded)
		require.NoError(t, err)
		effective, err = ExpandEnvironment(effective, map[string]string{"HOME": "/home/final"})
		if size > SizeErrorBytes {
			require.Error(t, err)
			continue
		}
		require.NoError(t, err)
		encoded, err := json.Marshal(effective)
		require.NoError(t, err)
		warnings, err := ValidateEffective(encoded, effective)
		require.NoError(t, err)
		require.Equal(t, size > SizeWarnBytes, len(warnings) > 0)
		contexts, err := AgentContextsV2Of(effective.Capabilities)
		require.NoError(t, err)
		require.Equal(t, "Build: release; team: "+strings.Repeat("x", size)+"; home: /home/final; $HOME ${HOME}", contexts[0].Content)
	}
	_, err = ExpandCreateArgs(built, d.Args, nil)
	require.ErrorContains(t, err, "no resolved value")
	_, err = ExpandEnvironment(published, nil)
	require.ErrorContains(t, err, "no variable")
	_, err = ExpandEnvironment(published, map[string]string{"HOME": "${{ kit.args.team }}"})
	require.ErrorContains(t, err, "Kit placeholders")
	// Environment expansion can cross the budget even when the input fits.
	_, err = ExpandEnvironment(published, map[string]string{"HOME": strings.Repeat("x", SizeErrorBytes)})
	require.ErrorContains(t, err, "size budget")
	// Build expansion is judged on its expanded serialized output as well.
	oversized, err := ExpandBuildArgs(raw, d.Args, map[string]string{"build": strings.Repeat("x", SizeErrorBytes)})
	require.NoError(t, err)
	oversizedDescriptor, err := Decode(oversized)
	require.NoError(t, err)
	_, err = ValidatePublished(oversized, oversizedDescriptor)
	require.ErrorContains(t, err, "byte budget")
	_, err = ValidateRaw([]byte(strings.ReplaceAll(string(raw), "kit.args.team", "kit.args.missing")), d)
	require.ErrorContains(t, err, "declares no arg")
}

func TestAgentContextV2CompositionCountsSerializedEscapes(t *testing.T) {
	var contributions []Contribution
	for i := range 2 {
		d := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{contextV2(map[string]any{"content": strings.Repeat("<", 45000)})}}
		raw, err := json.Marshal(d)
		require.NoError(t, err)
		_, err = ValidatePublished(raw, d)
		require.NoError(t, err)
		contributions = append(contributions, Contribution{Reference: fmt.Sprint(i), Descriptor: d})
	}
	_, err := Compose(contributions)
	require.ErrorIs(t, err, ErrDescriptorTooLarge)
}

func TestAgentContextV2AccessorDoesNotDiscardRequests(t *testing.T) {
	absent, err := AgentContextV2Of(nil)
	require.NoError(t, err)
	require.Nil(t, absent)
	body := contextV2(map[string]any{"content": "body"})
	one, err := AgentContextV2Of([]Capability{body})
	require.NoError(t, err)
	require.Equal(t, &AgentContextV2{Content: "body"}, one)
	_, err = AgentContextV2Of([]Capability{body, contextV2(map[string]any{"filename": "AGENTS.md"})})
	require.ErrorContains(t, err, "AgentContextsV2Of")
	_, err = AgentContextsV2Of([]Capability{{Group: &CapabilityGroup{Capabilities: []Capability{body}}}})
	require.ErrorContains(t, err, "select groups first")
}
