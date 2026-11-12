package spec

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestComposeReusedResultsFollowNewContributionOrder(t *testing.T) {
	entry := func(name string) Contribution {
		return Contribution{Reference: name, Descriptor: &Descriptor{
			SchemaVersion: SchemaVersion, Kind: KindMixin,
			Capabilities: []Capability{{Type: CapabilityLifecycle, Name: name,
				Config: map[string]any{"startup": []any{map[string]any{"command": "echo " + name}}}}},
		}}
	}
	for _, fromPlanner := range []bool{false, true} {
		inputs := []Contribution{entry("A"), entry("B")}
		var combined *Descriptor
		if fromPlanner {
			runtime, err := ComposeRuntime(t.Context(), inputs, RuntimeOptions{Select: Supported(CapabilityLifecycle)})
			require.NoError(t, err)
			combined = runtime.Descriptor
		} else {
			var err error
			combined, err = Compose(inputs)
			require.NoError(t, err)
		}
		result, err := Compose([]Contribution{entry("C"), {Reference: "AB", Descriptor: combined}})
		require.NoError(t, err)
		var names []string
		for _, capability := range result.Capabilities {
			names = append(names, capability.Name)
		}
		require.Equal(t, []string{"C", "A", "B"}, names, "planner=%t", fromPlanner)
	}
}

func TestComposeAndPublishKeepDifferentContextRepresentations(t *testing.T) {
	inputs := []Contribution{
		contribute(t, "tool", `schemaVersion: "3"
kind: mixin
capabilities:
  - type: com.docker.sandbox/agent-context@1
    name: Tool guidance
    optional: true
    config:
      content: Use the tool.
`),
		contribute(t, "agent", `schemaVersion: "3"
kind: workload
capabilities:
  - type: com.docker.sandbox/agent-context@1
    config:
      filename: AGENTS.md
      contentFile: /usr/share/sandbox/kit/agent/context.md
`),
	}
	before, err := json.Marshal(inputs)
	require.NoError(t, err)
	runtime, err := Compose(inputs)
	require.NoError(t, err)
	profile, err := AgentContextOf(runtime.Capabilities)
	require.NoError(t, err)
	require.Equal(t, &AgentContext{Filename: "AGENTS.md"}, profile)
	require.Equal(t, "Tool guidance", runtime.Capabilities[0].Name)
	require.False(t, runtime.Capabilities[0].Optional)

	_, err = Merge(inputs, MergeOptions{})
	require.ErrorContains(t, err, "requires ContextPath")
	published, err := Merge(inputs, MergeOptions{ContextPath: "/usr/share/sandbox/kit/set/context.md"})
	require.NoError(t, err)
	staged, err := AgentContextsOf(DeclaredCapabilities(published.Descriptor.Capabilities))
	require.NoError(t, err)
	require.Len(t, staged, 2)
	require.Equal(t, "AGENTS.md", staged[1].Filename)
	require.Equal(t, "/usr/share/sandbox/kit/set/context.md.parts/0.md", staged[0].ContentFile)
	require.Equal(t, "/usr/share/sandbox/kit/set/context.md.parts/1.md", staged[1].ContentFile)
	require.Equal(t, []ContextSource{
		{Reference: "tool", Content: "Use the tool.", Target: staged[0].ContentFile},
		{Reference: "agent", Path: "/usr/share/sandbox/kit/agent/context.md", Target: staged[1].ContentFile},
	}, published.ContextSources)
	require.True(t, published.Descriptor.Capabilities[0].Group.Optional)
	require.False(t, published.Descriptor.Capabilities[1].Group.Optional)

	after, err := json.Marshal(inputs)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after), "composition and publishing leave their inputs intact")
	require.NotContains(t, runtime.Capabilities[0].Config, "contentFile")
}

func TestComposeContextPresence(t *testing.T) {
	for _, tt := range []struct {
		name   string
		config map[string]any
		keep   bool
	}{
		{name: "no request", config: map[string]any{}},
		{name: "profile only", config: map[string]any{"filename": "AGENTS.md"}, keep: true},
		{name: "body only", config: map[string]any{"content": "Guidance"}, keep: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			result, err := Compose([]Contribution{{Reference: "agent", Descriptor: &Descriptor{
				SchemaVersion: SchemaVersion, Kind: KindWorkload,
				Capabilities: []Capability{{Type: CapabilityAgentContext, Name: "Guidance", Optional: true, Config: tt.config}},
			}}})
			require.NoError(t, err)
			if !tt.keep {
				require.Empty(t, result.Capabilities)
				return
			}
			require.Len(t, result.Capabilities, 1)
			require.True(t, result.Capabilities[0].Optional)
			require.Equal(t, "Guidance", result.Capabilities[0].Name)
			raw, err := json.Marshal(result)
			require.NoError(t, err)
			_, err = ValidateEffective(raw, result)
			require.NoError(t, err)
		})
	}
}
