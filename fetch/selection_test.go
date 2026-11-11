package fetch

import (
	"context"
	"fmt"
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
		t.Run(method, func(t *testing.T) {
			_, err := resolveContextSelection(t, method, shell, nil)
			require.ErrorContains(t, err, "cannot compose")
		})
	}
}

func TestExactContextSelectionThroughPublicAPIs(t *testing.T) {
	declineModern := func(message string) spec.SelectCapability {
		return func(ctx context.Context, target spec.SelectionTarget, kit spec.Descriptor, c spec.Capability) spec.CapabilityDecision {
			decision := DefaultCapabilitySelector(ctx, target, kit, c)
			if c.Type == spec.CapabilityAgentContextV2 {
				return spec.CapabilityDecision{Message: message}
			}
			return decision
		}
	}
	for _, method := range []string{"Resolve", "ResolvePartial", "Assemble"} {
		for _, tc := range []struct {
			name           string
			legacyRequired bool
			modernRequired bool
			legacyOnly     bool
			grouped        bool
			unavailable    bool
			selector       spec.SelectCapability
			want           string
			rejectReason   string
			err            string
		}{
			{name: "default accepts both and conflicts", err: "cannot compose"},
			{name: "runtime supports only @1", selector: spec.Supported(spec.CapabilityAgentContext), want: spec.CapabilityAgentContext},
			{name: "runtime supports only @2", selector: spec.Supported(spec.CapabilityAgentContextV2), want: spec.CapabilityAgentContextV2},
			{name: "runtime supports both and conflicts", selector: spec.Supported(spec.CapabilityAgentContext, spec.CapabilityAgentContextV2), err: "cannot compose"},
			{name: "runtime supports neither", selector: spec.Supported()},
			{name: "@2 cannot discharge required @1", legacyRequired: true, selector: spec.Supported(spec.CapabilityAgentContextV2), err: "required capability selection rejected"},
			{name: "@1 cannot discharge required @2", modernRequired: true, selector: spec.Supported(spec.CapabilityAgentContext), err: "required capability selection rejected"},
			{name: "required @1 does not suppress optional @2", legacyRequired: true, err: "cannot compose"},
			{name: "required @2 does not suppress optional @1", modernRequired: true, err: "cannot compose"},
			{name: "@1-only unsupported on @2 runtime", legacyOnly: true, legacyRequired: true, selector: spec.Supported(spec.CapabilityAgentContextV2), err: "required capability selection rejected"},
			{name: "optional @1-only skipped on @2 runtime", legacyOnly: true, selector: spec.Supported(spec.CapabilityAgentContextV2)},
			{name: "independent groups both selected conflict", grouped: true, err: "cannot compose"},
			{name: "unavailable higher group preserves optional lower", grouped: true, unavailable: true, want: spec.CapabilityAgentContext},
			{name: "unavailable higher group preserves required lower", grouped: true, unavailable: true, legacyRequired: true, want: spec.CapabilityAgentContext},
			{name: "policy declines higher and preserves optional lower", selector: declineModern("policy denied @2"), rejectReason: "policy denied @2", want: spec.CapabilityAgentContext},
			{name: "policy declines higher and preserves required lower", legacyRequired: true, selector: declineModern("policy denied @2"), rejectReason: "policy denied @2", want: spec.CapabilityAgentContext},
			{name: "policy declines higher group and preserves optional lower", grouped: true, selector: declineModern("policy denied @2"), rejectReason: "policy denied @2", want: spec.CapabilityAgentContext},
			{name: "policy declines higher group and preserves required lower", grouped: true, legacyRequired: true, selector: declineModern("policy denied @2"), rejectReason: "policy denied @2", want: spec.CapabilityAgentContext},
			{name: "higher resource unavailable preserves optional lower", selector: declineModern("resource unavailable"), rejectReason: "resource unavailable", want: spec.CapabilityAgentContext},
			{name: "higher resource unavailable preserves required lower", legacyRequired: true, selector: declineModern("resource unavailable"), rejectReason: "resource unavailable", want: spec.CapabilityAgentContext},
			{name: "policy cannot discharge required higher", modernRequired: true, selector: declineModern("policy denied @2"), err: "policy denied @2"},
			{name: "policy cannot discharge required higher group", grouped: true, modernRequired: true, selector: declineModern("policy denied @2"), err: "policy denied @2"},
		} {
			t.Run(method+"/"+tc.name, func(t *testing.T) {
				d := &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindWorkload}
				for _, c := range []spec.Capability{
					{Type: spec.CapabilityAgentContext, Optional: !tc.legacyRequired, Config: map[string]any{"filename": "AGENTS.md", "contentFile": "/kit/context.md"}},
					{Type: spec.CapabilityAgentContextV2, Optional: !tc.modernRequired, Config: map[string]any{"filename": "AGENTS.md", "content": "Modern guidance."}},
				} {
					if tc.legacyOnly && c.Type == spec.CapabilityAgentContextV2 {
						continue
					}
					if tc.grouped {
						optional := c.Optional
						c.Optional = false
						members := []spec.Capability{c}
						if tc.unavailable && c.Type == spec.CapabilityAgentContextV2 {
							members = append(members, spec.Capability{Type: "com.example/unavailable@1"})
						}
						c = spec.Capability{Group: &spec.CapabilityGroup{Optional: optional, Capabilities: members}}
					}
					d.Capabilities = append(d.Capabilities, c)
				}
				result, err := resolveContextSelection(t, method, d, tc.selector)
				if tc.err != "" {
					require.ErrorContains(t, err, tc.err)
					if tc.err == "cannot compose" {
						require.ErrorContains(t, err, "kits/context")
						for i := range d.Capabilities {
							location := fmt.Sprintf("capabilities[%d]", i)
							if tc.grouped {
								location += ".group.capabilities[0]"
							}
							require.ErrorContains(t, err, location)
						}
					}
					return
				}
				require.NoError(t, err)
				if tc.want == "" {
					require.Empty(t, result.Descriptor.Capabilities)
					require.Len(t, result.Selections[0].Selection.Skipped, len(d.Capabilities))
					return
				}
				require.Len(t, result.Descriptor.Capabilities, 1)
				require.Equal(t, tc.want, result.Descriptor.Capabilities[0].Type)
				selected := result.Selections[0].Selection
				require.Len(t, selected.Selected, 1)
				require.Len(t, selected.Skipped, 1)
				if tc.rejectReason != "" {
					require.Equal(t, tc.rejectReason, selected.Skipped[0].Decisions[0].Message)
				}
				if tc.unavailable {
					require.Equal(t, []spec.CapabilityDecision{{Accepted: true}, {Message: "unsupported capability type com.example/unavailable@1"}}, selected.Skipped[0].Decisions)
				}
				if tc.want == spec.CapabilityAgentContext {
					guidance, err := spec.AgentContextOf(result.Kits[0].Descriptor.Capabilities)
					require.NoError(t, err)
					require.Equal(t, "/kit/context.md", guidance.ContentFile, "@1 retains its file-backed configuration")
				} else {
					guidance, err := spec.AgentContextsV2Of(result.Kits[0].Descriptor.Capabilities)
					require.NoError(t, err)
					require.Equal(t, "Modern guidance.", guidance[0].Content)
				}
			})
		}
	}
}

func resolveContextSelection(t *testing.T, method string, d *spec.Descriptor, selector spec.SelectCapability) (*Resolved, error) {
	t.Helper()
	reg := newRegistry(t)
	reg.tag("kits/context", "1.0.0", reg.imageWithConfig(t, "kits/context", kitJSON(t, d), ocispec.Image{Platform: defaultPlatform()}))
	client, err := New()
	require.NoError(t, err)
	requests := reqs(reg.ref("kits/context", "1.0.0"))
	var options []ResolveOption
	if selector != nil {
		options = append(options, WithCapabilitySelector(selector))
	}
	switch method {
	case "Resolve":
		return client.Resolve(t.Context(), requests, options...)
	case "ResolvePartial":
		return client.ResolvePartial(t.Context(), requests, options...)
	case "Assemble":
		result, err := Assemble(t.Context(), requests, Options{Loader: client.LoadKit, CapabilitySelector: selector})
		if err != nil {
			return nil, err
		}
		return result.Resolved, nil
	default:
		return nil, fmt.Errorf("unknown resolution API %q", method)
	}
}
