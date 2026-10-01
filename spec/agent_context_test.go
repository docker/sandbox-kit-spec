package spec

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAgentContextDirectoryValidation(t *testing.T) {
	for _, tt := range []struct {
		name    string
		kind    string
		config  map[string]any
		wantErr string
	}{
		{"legacy workload", KindWorkload, map[string]any{"filename": "AGENTS.md"}, ""},
		{"mixin filename without directory", KindMixin, map[string]any{"filename": "${{ kit.args.filename }}"}, "filename without directory is workload-kit-only"},
		{"legacy mixin", KindMixin, map[string]any{"content": "Instructions"}, ""},
		{"global directory", KindWorkload, map[string]any{"filename": "AGENTS.md", "directory": "/home/agent/.codex"}, ""},
		{"set profile", KindSet, map[string]any{"filename": "AGENTS.md", "directory": "/home/agent/.codex"}, ""},
		{"parameterized directory", KindWorkload, map[string]any{"filename": "AGENTS.md", "directory": "${{ kit.args.directory }}"}, ""},
		{"mixin directory", KindMixin, map[string]any{"filename": "AGENTS.md", "directory": "/home/agent/.codex"}, ""},
		{"parameterized mixin", KindMixin, map[string]any{"filename": "AGENTS.md", "directory": "${{ kit.args.directory }}"}, ""},
		{"missing filename", KindWorkload, map[string]any{"directory": "/home/agent/.codex"}, "directory requires filename"},
		{"parameterized missing filename", KindWorkload, map[string]any{"directory": "${{ kit.args.directory }}"}, "directory requires filename"},
		{"empty filename", KindWorkload, map[string]any{"filename": "", "directory": "/home/agent/.codex"}, "directory requires filename"},
		{"relative directory", KindWorkload, map[string]any{"filename": "AGENTS.md", "directory": ".codex"}, "absolute, canonical path"},
		{"empty directory", KindWorkload, map[string]any{"filename": "AGENTS.md", "directory": ""}, "absolute, canonical path"},
		{"aliased directory", KindWorkload, map[string]any{"filename": "AGENTS.md", "directory": "/home/agent/../agent/.codex"}, "absolute, canonical path"},
		{"trailing slash", KindWorkload, map[string]any{"filename": "AGENTS.md", "directory": "/home/agent/.codex/"}, "absolute, canonical path"},
		{"null directory", KindWorkload, map[string]any{"filename": "AGENTS.md", "directory": nil}, "directory must be a string"},
		{"wrong directory type", KindWorkload, map[string]any{"filename": "AGENTS.md", "directory": 42}, "directory must be a string"},
		{"filename traversal", KindWorkload, map[string]any{"filename": "../AGENTS.md", "directory": "/home/agent/.codex"}, "single filename"},
		{"absolute filename", KindWorkload, map[string]any{"filename": "/AGENTS.md", "directory": "/home/agent/.codex"}, "single filename"},
		{"dot filename", KindWorkload, map[string]any{"filename": "..", "directory": "/home/agent/.codex"}, "single filename"},
		{"literal invalid with parameterized body", KindWorkload, map[string]any{"filename": "AGENTS.md", "directory": ".codex", "content": "${{ kit.args.body }}"}, "absolute, canonical path"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d := &Descriptor{Kind: tt.kind, Capabilities: []Capability{{Type: CapabilityAgentContext, Config: tt.config}}}
			err := validateCapabilityBlock(d)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			context, err := AgentContextOf(d.Capabilities)
			require.NoError(t, err)
			if directory, ok := tt.config["directory"]; ok {
				require.Equal(t, directory, context.Directory)
			} else {
				require.Empty(t, context.Directory)
			}
		})
	}
}

func TestAgentContextDirectorySurvivesCompositionAndPublication(t *testing.T) {
	workload := Contribution{Reference: "codex", Descriptor: &Descriptor{Kind: KindWorkload, Capabilities: []Capability{
		{Type: CapabilityAgentContext, Config: map[string]any{"filename": "AGENTS.md", "directory": "/home/agent/.codex", "contentFile": "/kit/codex.md"}},
	}}}
	mixin := Contribution{Reference: "artifact-store", Descriptor: &Descriptor{Kind: KindMixin, Capabilities: []Capability{
		{Type: CapabilityAgentContext, Config: map[string]any{"content": "Save artifacts in /home/agent/artifacts."}},
	}}}
	for _, grouped := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "grouped"}[grouped], func(t *testing.T) {
			kit := *workload.Descriptor
			kit.Capabilities = append([]Capability(nil), kit.Capabilities...)
			if grouped {
				kit.Capabilities = []Capability{{Group: &CapabilityGroup{Capabilities: kit.Capabilities}}}
			}
			published, err := Merge([]Contribution{{Reference: workload.Reference, Descriptor: &kit}, mixin}, MergeOptions{ContextPath: "/kit/merged.md"})
			require.NoError(t, err)
			selected, err := SelectCapabilities(published.Descriptor, Supported(CapabilityAgentContext))
			require.NoError(t, err)
			effective := *published.Descriptor
			effective.Capabilities = selected.Capabilities
			composed, err := Compose([]Contribution{{Reference: "published", Descriptor: &effective}})
			require.NoError(t, err)
			context, err := AgentContextOf(composed.Capabilities)
			require.NoError(t, err)
			require.Equal(t, "AGENTS.md", context.Filename)
			require.Equal(t, "/home/agent/.codex", context.Directory)
			require.Len(t, published.ContextSources, 2)
		})
	}
}

func TestExplicitAgentProfileOverridesLegacyFallback(t *testing.T) {
	shell := Contribution{Reference: "shell", Descriptor: &Descriptor{Kind: KindWorkload, Capabilities: []Capability{
		{Type: CapabilityAgentContext, Config: map[string]any{"filename": "AGENTS.md", "content": "Shell instructions"}},
	}}}
	agent := Contribution{Reference: "claude", Descriptor: &Descriptor{Kind: KindMixin, Capabilities: []Capability{
		{Type: CapabilityAgentContext, Config: map[string]any{"filename": "CLAUDE.md", "directory": "/home/agent/.claude", "content": "Claude instructions"}},
	}}}
	for _, grouped := range []bool{false, true} {
		for _, reversed := range []bool{false, true} {
			kit := *agent.Descriptor
			if grouped {
				kit.Capabilities = []Capability{{Group: &CapabilityGroup{Capabilities: kit.Capabilities}}}
			}
			contributions := []Contribution{shell, {Reference: agent.Reference, Descriptor: &kit}}
			if reversed {
				contributions[0], contributions[1] = contributions[1], contributions[0]
			}
			result, err := Merge(contributions, MergeOptions{ContextPath: "/kit/context.md"})
			require.NoError(t, err)
			selected, err := SelectCapabilities(result.Descriptor, Supported(CapabilityAgentContext))
			require.NoError(t, err)
			effective := *result.Descriptor
			effective.Capabilities = selected.Capabilities
			composed, err := Compose([]Contribution{{Reference: "published", Descriptor: &effective}})
			require.NoError(t, err)
			profile, err := AgentContextOf(composed.Capabilities)
			require.NoError(t, err)
			require.Equal(t, "CLAUDE.md", profile.Filename)
			require.Equal(t, "/home/agent/.claude", profile.Directory)
			require.Len(t, result.ContextSources, 2)
		}
	}
	_, err := Compose([]Contribution{shell, agent, agent})
	require.NoError(t, err, "identical explicit profiles describe one destination")
	other := Contribution{Reference: "codex", Descriptor: &Descriptor{Kind: KindMixin, Capabilities: []Capability{
		{Type: CapabilityAgentContext, Config: map[string]any{"filename": "AGENTS.md", "directory": "/home/agent/.codex"}},
	}}}
	for _, contributions := range [][]Contribution{{shell, agent, other}, {other, shell, agent}} {
		_, err := Compose(contributions)
		require.ErrorContains(t, err, "conflicting explicit agent-context profiles")
		require.ErrorContains(t, err, "claude")
		require.ErrorContains(t, err, "codex")
	}
}
