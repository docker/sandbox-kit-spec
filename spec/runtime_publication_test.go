package spec

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRuntimePublicationDefersThirdPartyOrderDefaults(t *testing.T) {
	const typ = "com.example/ordered@1"
	first := Capability{Type: typ, Optional: true, Config: map[string]any{"value": "same"}}
	second := first
	second.Order = new(int32(42))
	inputs := []Contribution{runtimeContribution("first", first), runtimeContribution("second", second)}
	opts := RuntimeOptions{Select: Supported(typ), Contracts: map[string]ExecutionContract{
		typ: fixedExecutionContract(42, PlannedAction{StageStart, "ready"}),
	}}
	direct, err := ComposeRuntime(t.Context(), inputs, opts)
	require.NoError(t, err)
	require.Len(t, runtimeStage(t, direct.Plan, StageStart), 1)
	for depth := range 3 {
		published, err := Merge(inputs, MergeOptions{})
		require.NoError(t, err, "publication cannot resolve a runtime-owned default")
		raw, err := json.Marshal(published.Descriptor)
		require.NoError(t, err)
		d, err := Decode(raw)
		require.NoError(t, err)
		inputs = []Contribution{{Reference: "published", Descriptor: d}}
		result, err := ComposeRuntime(t.Context(), inputs, opts)
		require.NoError(t, err, "depth %d", depth)
		steps := runtimeStage(t, result.Plan, StageStart)
		require.Len(t, steps, 1)
		require.Equal(t, int32(42), steps[0].Order)
		require.Len(t, steps[0].Origins, 2)
		require.Len(t, result.Selections[0].Selection.Selected, 2)
		conflicting := opts
		conflicting.Contracts = map[string]ExecutionContract{typ: fixedExecutionContract(43, PlannedAction{StageStart, "ready"})}
		_ = runtimeFailure(t, inputs, conflicting, "conflicting effective orders")
	}
}

func TestRuntimeOrderConflictReportsOriginalOccurrences(t *testing.T) {
	one := runtimeOrder(Capability{Type: CapabilityVolume, Config: map[string]any{"path": "/cache"},
		Source: &CapabilitySource{Kit: "invented", Path: "fake"}}, 100)
	two := runtimeOrder(one, 200)
	inputs := []Contribution{runtimeContribution("same", groupHook("first"), one), runtimeContribution("same", two)}
	detail := runtimeFailure(t, inputs, RuntimeOptions{Select: Supported(CapabilityVolume, CapabilityLifecycle)}, "conflicting effective orders")
	require.Contains(t, detail.Error(), "contribution[0] same capabilities[1]")
	require.Contains(t, detail.Error(), "contribution[1] same capabilities[0]")
	require.Contains(t, detail.Error(), "source invented fake")
}

func TestRuntimePublishedSetIsAnAggregateDependencyProvider(t *testing.T) {
	a := runtimeContribution("a", runtimeOrder(groupHook("a"), 2000))
	a.Descriptor.Provides = []string{"x@1"}
	b := runtimeContribution("b", runtimeOrder(groupHook("b"), 500))
	b.Descriptor.Provides = []string{"y@1"}
	c := runtimeContribution("c", groupHook("c"))
	c.Descriptor.Requires = []string{"y"}
	runtimeCompose(t, b, a, c)
	published, err := Merge([]Contribution{b, a}, MergeOptions{})
	require.NoError(t, err)
	_ = runtimeFailure(t, []Contribution{{Reference: "set", Descriptor: published.Descriptor}, c},
		RuntimeOptions{Select: Supported(CapabilityLifecycle)}, "must precede dependent hooks")
}
