package main

import (
	"testing"

	"github.com/docker/sandbox-kit-spec/v3/spec"
	"github.com/stretchr/testify/require"
)

func TestPreviewContextSelection(t *testing.T) {
	for _, allowVolumes := range []bool{false, true} {
		for _, supported := range []string{"", spec.CapabilityAgentContext, spec.CapabilityAgentContextV2} {
			d := &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindWorkload, Capabilities: []spec.Capability{
				{Type: spec.CapabilityAgentContext, Optional: true, Config: map[string]any{"filename": "AGENTS.md", "content": "Guidance."}},
				{Type: spec.CapabilityAgentContextV2, Optional: true, Config: map[string]any{"filename": "AGENTS.md", "content": "Guidance."}},
				{Type: spec.CapabilityVolume, Optional: true, Config: map[string]any{"path": "/cache"}},
			}}
			selection, err := spec.SelectCapabilities(t.Context(), "shell", d, capabilitySelector(supported, allowVolumes))
			require.NoError(t, err)
			d.Capabilities = selection.Capabilities
			_, err = spec.Validate(d)
			require.NoError(t, err)
			want := spec.CapabilityAgentContextV2
			if supported != "" {
				want = supported
			}
			require.Equal(t, want, d.Capabilities[0].Type)
			if supported == "" && allowVolumes {
				require.Len(t, d.Capabilities, 2)
			} else {
				require.Len(t, d.Capabilities, 1)
			}
		}
	}
}

func TestPreviewContextFallbackForVolumeGroup(t *testing.T) {
	for _, allowVolumes := range []bool{false, true} {
		for _, optional := range []bool{false, true} {
			d := &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindWorkload, Capabilities: []spec.Capability{
				{Type: spec.CapabilityAgentContext, Optional: true, Config: map[string]any{"filename": "AGENTS.md", "content": "Legacy guidance."}},
				{Group: &spec.CapabilityGroup{Optional: optional, Capabilities: []spec.Capability{
					{Type: spec.CapabilityAgentContextV2, Config: map[string]any{"filename": "AGENTS.md", "content": "Modern guidance."}},
					{Type: spec.CapabilityVolume, Config: map[string]any{"path": "/cache"}},
				}}},
			}}
			selection, err := spec.SelectCapabilities(t.Context(), "shell", d, capabilitySelector("", allowVolumes))
			if !allowVolumes && !optional {
				require.ErrorContains(t, err, "required capability selection rejected")
				continue
			}
			require.NoError(t, err)
			selected := *d
			selected.Capabilities = selection.Capabilities
			_, err = spec.Validate(&selected)
			require.NoError(t, err)
			if allowVolumes {
				require.Len(t, selected.Capabilities, 2)
				require.Equal(t, spec.CapabilityAgentContextV2, selected.Capabilities[0].Type)
				require.Equal(t, spec.CapabilityVolume, selected.Capabilities[1].Type)
			} else {
				require.Len(t, selected.Capabilities, 1)
				require.Equal(t, spec.CapabilityAgentContext, selected.Capabilities[0].Type)
				require.Equal(t, "Legacy guidance.", selected.Capabilities[0].Config["content"])
				require.Len(t, selection.Skipped, 1)
				require.Len(t, selection.Skipped[0].Decisions, 2)
				require.Equal(t, "persistent volumes disabled by host policy", selection.Skipped[0].Decisions[1].Message)
			}
			require.Len(t, d.Capabilities, 2, "policy leaves original declarations intact")
			require.Len(t, d.Capabilities[1].Group.Capabilities, 2)
		}
	}
}
