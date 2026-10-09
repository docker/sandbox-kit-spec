package spec

import (
	"encoding/json"
	"os"
	"path/filepath"
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
			selected, err := SelectCapabilities(t.Context(), published.Descriptor, Supported(CapabilityAgentContext))
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
			selected, err := SelectCapabilities(t.Context(), result.Descriptor, Supported(CapabilityAgentContext))
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
}

func TestDifferingExplicitProfilesAreEachMaterialized(t *testing.T) {
	shell := Contribution{Reference: "shell", Descriptor: &Descriptor{Kind: KindWorkload, Capabilities: []Capability{
		{Type: CapabilityAgentContext, Config: map[string]any{"filename": "AGENTS.md", "content": "Shell instructions"}},
	}}}
	claude := Contribution{Reference: "claude", Descriptor: &Descriptor{Kind: KindMixin, Capabilities: []Capability{
		{Type: CapabilityAgentContext, Name: "Claude profile", Config: map[string]any{"filename": "CLAUDE.md", "directory": "/home/agent/.claude", "content": "Claude instructions"}},
	}}}
	codex := Contribution{Reference: "codex", Descriptor: &Descriptor{Kind: KindMixin, Capabilities: []Capability{
		{Type: CapabilityAgentContext, Optional: true, Config: map[string]any{"filename": "AGENTS.md", "directory": "/home/agent/.codex", "content": "Codex instructions"}},
	}}}
	claudeProfile := AgentContext{Filename: "CLAUDE.md", Directory: "/home/agent/.claude"}
	codexProfile := AgentContext{Filename: "AGENTS.md", Directory: "/home/agent/.codex"}
	// The shell's required body follows the first profile; codex's own ask
	// is optional, so its profile is required only when it comes first.
	for _, tt := range []struct {
		name          string
		contributions []Contribution
		want          []AgentContext
		wantOptional  []bool
	}{
		{"claude first", []Contribution{shell, claude, codex}, []AgentContext{claudeProfile, codexProfile}, []bool{false, true}},
		{"codex first", []Contribution{codex, shell, claude}, []AgentContext{codexProfile, claudeProfile}, []bool{false, false}},
		{"restated", []Contribution{shell, claude, codex, claude}, []AgentContext{claudeProfile, codexProfile}, []bool{false, true}},
		{"agents only", []Contribution{claude, codex}, []AgentContext{claudeProfile, codexProfile}, []bool{false, true}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			composed, err := Compose(tt.contributions)
			require.NoError(t, err)
			profiles, err := AgentContextsOf(composed.Capabilities)
			require.NoError(t, err)
			require.Equal(t, tt.want, profiles, "the shell's legacy profile yields, and each agent keeps its own")
			var optional []bool
			for _, c := range composed.Capabilities {
				optional = append(optional, c.Optional)
				if c.Config["directory"] == claudeProfile.Directory {
					require.Equal(t, "Claude profile", c.Name)
				}
			}
			require.Equal(t, tt.wantOptional, optional, "strictness is per destination")
			_, err = AgentContextOf(composed.Capabilities)
			require.ErrorContains(t, err, "AgentContextsOf", "a single-profile reader must not silently drop an agent")
			raw, err := json.Marshal(composed)
			require.NoError(t, err)
			_, err = ValidateEffective(raw, composed)
			require.NoError(t, err)

			published, err := Merge(tt.contributions, MergeOptions{ContextPath: "/kit/merged.md"})
			require.NoError(t, err)
			staged, err := AgentContextsOf(published.Descriptor.Capabilities)
			require.NoError(t, err)
			require.Len(t, staged, 2)
			require.Equal(t, "/kit/merged.md", staged[0].ContentFile, "the merged body rides the first profile")
			require.Empty(t, staged[1].ContentFile)
			require.Len(t, published.ContextSources, len(tt.contributions), "every body is staged once for every profile to index")
			raw, err = json.Marshal(published.Descriptor)
			require.NoError(t, err)
			_, err = ValidateEffective(raw, published.Descriptor)
			require.NoError(t, err, "a published set listing two agents must stay valid")
		})
	}
}

// The composition from the regression report: two agent mixins on the
// shell, each declaring its own discovery profile since 2ddbba6.
func TestExampleAgentMixinsComposeOnOneShell(t *testing.T) {
	load := func(name string) Contribution {
		raw, err := os.ReadFile(filepath.Join("..", "examples", name, name+".yaml"))
		require.NoError(t, err)
		d, err := Decode(raw)
		require.NoError(t, err)
		values, err := ResolveArgs(d.Args, nil)
		require.NoError(t, err)
		raw, err = ExpandCreateArgs(raw, d.Args, values)
		require.NoError(t, err)
		d, err = Decode(raw)
		require.NoError(t, err)
		d, err = ExpandEnvironment(d, map[string]string{"WORKSPACE_DIR": "/home/agent/workspace"})
		require.NoError(t, err)
		return Contribution{Reference: name, Descriptor: d}
	}
	composed, err := Compose([]Contribution{load("shell"), load("claude-mixin"), load("codex-mixin")})
	require.NoError(t, err)
	profiles, err := AgentContextsOf(composed.Capabilities)
	require.NoError(t, err)
	require.Equal(t, []AgentContext{
		{Filename: "CLAUDE.md", Directory: "/home/agent/.claude"},
		{Filename: "AGENTS.md", Directory: "/home/agent/.codex"},
	}, profiles)
}

func TestAgentContextArity(t *testing.T) {
	profile := func(directory, filename string) Capability {
		return Capability{Type: CapabilityAgentContext, Config: map[string]any{"directory": directory, "filename": filename}}
	}
	body := Capability{Type: CapabilityAgentContext, Config: map[string]any{"content": "Instructions"}}
	for _, tt := range []struct {
		name    string
		entries []Capability
		wantErr string
	}{
		{"two profiles", []Capability{profile("/home/agent/.claude", "CLAUDE.md"), profile("/home/agent/.codex", "AGENTS.md")}, ""},
		{"one directory, two filenames", []Capability{profile("/home/agent", "AGENTS.md"), profile("/home/agent", "GEMINI.md")}, ""},
		{"profiles beside a body", []Capability{profile("/home/agent/.claude", "CLAUDE.md"), body, profile("/home/agent/.codex", "AGENTS.md")}, ""},
		{"parameterized profiles", []Capability{profile("${{ kit.args.a }}", "AGENTS.md"), profile("${{ kit.args.b }}", "AGENTS.md")}, ""},
		{"one destination twice", []Capability{
			profile("/home/agent/.codex", "AGENTS.md"),
			{Type: CapabilityAgentContext, Config: map[string]any{"directory": "/home/agent/.codex", "filename": "AGENTS.md", "content": "Codex"}},
		}, "agent-context profile /home/agent/.codex/AGENTS.md already declared at capabilities[0]"},
		{"two undirected entries", []Capability{body, {Type: CapabilityAgentContext, Config: map[string]any{"filename": "AGENTS.md"}}}, "agent-context without a directory already declared at capabilities[0]"},
		{"two bodies", []Capability{
			{Type: CapabilityAgentContext, Config: map[string]any{"directory": "/home/agent/.claude", "filename": "CLAUDE.md", "content": "Claude"}},
			{Type: CapabilityAgentContext, Config: map[string]any{"directory": "/home/agent/.codex", "filename": "AGENTS.md", "contentFile": "/kit/codex.md"}},
		}, "agent-context body already declared at capabilities[0]"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCapabilityBlock(&Descriptor{Kind: KindWorkload, Capabilities: tt.entries})
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}
