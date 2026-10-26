package fetch

import (
	"os"
	"testing"

	"github.com/docker/sandbox-kit-spec/v3/spec"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func TestDefaultContextSelectionThroughPublicAPIs(t *testing.T) {
	raw, err := os.ReadFile("../examples/shell/shell.yaml")
	require.NoError(t, err)
	shell, err := spec.Decode(raw)
	require.NoError(t, err)
	for _, method := range []string{"Resolve", "ResolvePartial", "Assemble"} {
		for _, withMixin := range []bool{false, true} {
			name := "shell"
			if withMixin {
				name += "+mixin"
			}
			t.Run(method+"/"+name, func(t *testing.T) {
				reg := newRegistry(t)
				reg.tag("kits/shell", "1.0.0", reg.imageWithConfig(t, "kits/shell", kitJSON(t, shell), ocispec.Image{Platform: defaultPlatform()}))
				requests := reqs(reg.ref("kits/shell", "1.0.0"))
				if withMixin {
					tool := inlineContextDescriptor(spec.KindMixin, "Tool guidance.")
					tool.Capabilities[0].Optional = true
					tool.Capabilities = append(tool.Capabilities, spec.Capability{
						Type: spec.CapabilityAgentContext, Optional: true, Config: map[string]any{"content": "Tool guidance."},
					})
					reg.tag("kits/tool", "1.0.0", reg.imageWithConfig(t, "kits/tool", kitJSON(t, tool), ocispec.Image{Platform: defaultPlatform()}))
					requests = append(requests, Request{Reference: reg.ref("kits/tool", "1.0.0")})
				}
				client, err := New()
				require.NoError(t, err)
				var result *Resolved
				switch method {
				case "Resolve":
					result, err = client.Resolve(t.Context(), requests)
				case "ResolvePartial":
					result, err = client.ResolvePartial(t.Context(), requests)
				case "Assemble":
					var assembled *Result
					assembled, err = Assemble(t.Context(), requests, Options{Loader: client.LoadKit})
					if err == nil {
						result = assembled.Resolved
					}
				}
				require.NoError(t, err)
				contexts, err := spec.AgentContextsV2Of(result.Descriptor.Capabilities)
				require.NoError(t, err)
				require.Len(t, contexts, 1)
				require.Equal(t, "AGENTS.md", contexts[0].Filename)
				require.Contains(t, contexts[0].Content, "This is a plain shell sandbox")
				if withMixin {
					require.Contains(t, contexts[0].Content, "Tool guidance.")
				}
				for _, selection := range result.Selections {
					require.Len(t, selection.Selection.Skipped, 1)
					require.Contains(t, selection.Selection.Skipped[0].Decisions[0].Message, spec.CapabilityAgentContextV2)
					for _, c := range selection.Selection.Capabilities {
						require.NotEqual(t, spec.CapabilityAgentContext, c.Type)
					}
				}
			})
		}
	}
}

func TestDefaultContextSelectionCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name           string
		legacy, modern bool
		legacyRequired bool
		grouped        bool
		selector       spec.SelectCapability
		want           string
		message        string
	}{
		{name: "optional legacy only", legacy: true, want: spec.CapabilityAgentContext},
		{name: "required legacy only", legacy: true, legacyRequired: true, want: spec.CapabilityAgentContext},
		{name: "modern only", modern: true, want: spec.CapabilityAgentContextV2},
		{name: "optional alternatives", legacy: true, modern: true, want: spec.CapabilityAgentContextV2},
		{name: "required legacy takes precedence", legacy: true, modern: true, legacyRequired: true, want: spec.CapabilityAgentContext},
		{name: "optional group alternatives", legacy: true, modern: true, grouped: true, want: spec.CapabilityAgentContextV2},
		{name: "required legacy group", legacy: true, modern: true, legacyRequired: true, grouped: true, want: spec.CapabilityAgentContext},
		{name: "explicit legacy runtime", legacy: true, modern: true, selector: spec.Supported(spec.CapabilityAgentContext), want: spec.CapabilityAgentContext},
		{name: "explicit both still conflicts", legacy: true, modern: true, selector: spec.Supported(spec.KnownCapabilities()...), message: "cannot compose"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindWorkload}
			for _, c := range []spec.Capability{
				{Type: spec.CapabilityAgentContext, Optional: !tc.legacyRequired, Config: map[string]any{"filename": "AGENTS.md", "contentFile": "/kit/context.md"}},
				{Type: spec.CapabilityAgentContextV2, Optional: true, Config: map[string]any{"filename": "AGENTS.md", "content": "Modern guidance."}},
			} {
				if (c.Type == spec.CapabilityAgentContext && !tc.legacy) || (c.Type == spec.CapabilityAgentContextV2 && !tc.modern) {
					continue
				}
				if tc.grouped {
					optional := c.Optional
					c.Optional = false
					c = spec.Capability{Group: &spec.CapabilityGroup{Optional: optional, Capabilities: []spec.Capability{c}}}
				}
				d.Capabilities = append(d.Capabilities, c)
			}
			input, _ := assemblyFixture(t, spec.KindWorkload, "base", "base")
			input.Descriptor = kitJSON(t, d)
			result, err := Assemble(t.Context(), fixtureRequests(1), Options{Loader: fixtureLoader(input), CapabilitySelector: tc.selector})
			if tc.message != "" {
				require.ErrorContains(t, err, tc.message)
				return
			}
			require.NoError(t, err)
			require.Len(t, result.Resolved.Descriptor.Capabilities, 1)
			require.Equal(t, tc.want, result.Resolved.Descriptor.Capabilities[0].Type)
			if tc.want == spec.CapabilityAgentContext {
				context, err := spec.AgentContextOf(result.Resolved.Kits[0].Descriptor.Capabilities)
				require.NoError(t, err)
				require.Equal(t, "/kit/context.md", context.ContentFile, "legacy file-backed guidance is retained without upgrading it")
			}
		})
	}
}
