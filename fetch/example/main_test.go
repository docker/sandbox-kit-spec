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
