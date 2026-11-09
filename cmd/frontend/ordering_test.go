package main

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/docker/sandbox-kit-spec/v3/spec"
)

func TestPublishedSetPreservesExecutionPlan(t *testing.T) {
	order := func(n int32) *int32 { return &n }
	lifecycle := func(n int32, config map[string]any) spec.Capability {
		return spec.Capability{Type: spec.CapabilityLifecycle, Order: order(n), Config: config}
	}
	inputs := []spec.Contribution{
		{Reference: "configuration", Descriptor: &spec.Descriptor{
			SchemaVersion: spec.SchemaVersion, Kind: spec.KindMixin,
			Capabilities: []spec.Capability{
				lifecycle(400, map[string]any{"files": []any{map[string]any{"path": "/home/agent/daemon.json", "content": "{}"}}}),
				{Type: spec.CapabilityDockerd},
			},
		}},
		{Reference: "consumer", Descriptor: &spec.Descriptor{
			SchemaVersion: spec.SchemaVersion, Kind: spec.KindMixin,
			Capabilities: []spec.Capability{
				lifecycle(1000, map[string]any{"startup": []any{map[string]any{"command": "echo first"}}}),
				{Group: &spec.CapabilityGroup{Optional: true, Capabilities: []spec.Capability{
					{Type: spec.CapabilityVolume, Config: map[string]any{"path": "/cache"}},
					lifecycle(1000, map[string]any{"startup": []any{map[string]any{"command": "echo optional"}}}),
				}}},
				lifecycle(1000, map[string]any{"startup": []any{map[string]any{"command": "echo last"}}}),
			},
		}},
	}
	for _, reject := range []bool{false, true} {
		t.Run(map[bool]string{false: "selected", true: "skipped"}[reject], func(t *testing.T) {
			opts := spec.RuntimeOptions{Select: spec.Supported(spec.CapabilityLifecycle, spec.CapabilityDockerd, spec.CapabilityVolume)}
			if reject {
				opts.Select = spec.Supported(spec.CapabilityLifecycle, spec.CapabilityDockerd)
			}
			direct, err := spec.ComposeRuntime(t.Context(), inputs, opts)
			require.NoError(t, err)
			want := observablePlan(t, direct.Plan)
			current := inputs
			for depth := range 3 {
				published, err := spec.Merge(current, spec.MergeOptions{})
				require.NoError(t, err)
				raw, err := publishedSetDescriptor(published.Descriptor)
				require.NoError(t, err)
				d, err := spec.Decode(raw)
				require.NoError(t, err)
				declarations, _, err := kitDeclarations(d, spec.Kit{}, nil)
				require.NoError(t, err)
				current = []spec.Contribution{{Reference: "published", Descriptor: declarations}}
				composed, err := spec.ComposeRuntime(t.Context(), current, opts)
				require.NoError(t, err, "publication depth %d", depth)
				require.JSONEq(t, want, observablePlan(t, composed.Plan), "publication depth %d", depth)
				require.Len(t, spec.DeclaredCapabilities(d.Capabilities), 6, "publication preserves every independently selectable declaration")
			}
		})
	}
}

func observablePlan(t *testing.T, plan spec.ExecutionPlan) string {
	t.Helper()
	type effect struct {
		Stage  spec.ExecutionStage
		Action string
		Order  int32
		Type   string
		Config map[string]any
	}
	var effects []effect
	for _, stage := range plan.Stages {
		for _, step := range stage.Steps {
			effects = append(effects, effect{stage.Stage, step.Action, step.Order, step.Capability.Type, step.Capability.Config})
		}
	}
	raw, err := json.Marshal(effects)
	require.NoError(t, err)
	return string(raw)
}
