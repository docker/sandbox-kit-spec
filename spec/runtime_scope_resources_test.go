package spec

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRuntimeEmptyScopesDoNotBuildDenseDependencyGraph(t *testing.T) {
	// Empty scopes retain provider-presence semantics. They are valid input,
	// not execution nodes that justify allocating a square reachability table.
	descriptor := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, CapabilityScopes: make([]CapabilityScope, 20000)}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	result, err := ComposeRuntime(ctx, []Contribution{{Reference: "many-empty-scopes", Descriptor: descriptor}}, RuntimeOptions{Select: Supported(CapabilityLifecycle)})
	require.NoError(t, err)
	require.Empty(t, result.Plan.Entries)
}

func TestRuntimeAggregateAliasesShareEmptyScopeTraversal(t *testing.T) {
	provider := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, CapabilityScopes: make([]CapabilityScope, 8000)}
	requirer := runtimeTestDescriptor(runtimeTestHook(t, "consumer", true, false))
	requirer.Kind = KindMixin
	// Every name belongs to the same artifact, including its empty scopes.
	// Matching all aliases must traverse that shared range only once rather
	// than multiplying range expansion by the number of requirement names.
	for i := range 1000 {
		name := fmt.Sprintf("com.example/alias-%d", i)
		provider.Provides = append(provider.Provides, name+"@1")
		requirer.Requires = append(requirer.Requires, name)
	}
	zero := uint32(0)
	hook := runtimeTestHook(t, "provider", true, false)
	hook.Scope = &zero
	provider.Capabilities = []Capability{hook}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	result, err := ComposeRuntime(ctx, []Contribution{{Reference: "provider", Descriptor: provider}, {Reference: "consumer", Descriptor: requirer}}, RuntimeOptions{Select: Supported(CapabilityLifecycle)})
	require.NoError(t, err)
	require.Equal(t, []string{"provider/install", "consumer/install"}, runtimeTestActionLabels(runtimeTestActions(result.Plan, PhaseInstall, StepExecute)))
}

func TestRuntimeDependenciesCrossEmptyIntermediateScopes(t *testing.T) {
	descriptor := runtimeTestDescriptor(runtimeTestHook(t, "provider", true, false), runtimeTestHook(t, "consumer", true, false))
	descriptor.Provides = []string{"com.example/provider@1", "com.example/bridge@1", "com.example/consumer@1"}
	descriptor.CapabilityScopes = []CapabilityScope{
		{Provides: []string{"com.example/provider"}},
		{Provides: []string{"com.example/bridge"}, Requires: []string{"com.example/provider"}},
		{Provides: []string{"com.example/consumer"}, Requires: []string{"com.example/bridge"}},
	}
	zero, two := uint32(0), uint32(2)
	descriptor.Capabilities[0].Scope = &zero
	descriptor.Capabilities[1].Scope = &two
	// The unrelated contribution moves the provider. Its consumer must still
	// follow it through a dependency scope that contributes no effects.
	descriptor.Capabilities[0].After = []string{"com.example/extra"}
	extra := runtimeTestDescriptor(runtimeTestHook(t, "extra", true, false))
	extra.Kind = KindMixin
	extra.Provides = []string{"com.example/extra@1"}
	result, err := ComposeRuntime(t.Context(), []Contribution{{Reference: "published", Descriptor: descriptor}, {Reference: "extra", Descriptor: extra}}, RuntimeOptions{Select: Supported(CapabilityLifecycle)})
	require.NoError(t, err)
	require.Equal(t, []string{"extra/install", "provider/install", "consumer/install"}, runtimeTestActionLabels(runtimeTestActions(result.Plan, PhaseInstall, StepExecute)))
}
