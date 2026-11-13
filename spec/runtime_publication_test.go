package spec

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func runtimeTestPublish(t *testing.T, contributions ...Contribution) *Descriptor {
	t.Helper()
	merged, err := Merge(contributions, MergeOptions{})
	require.NoError(t, err)
	raw, err := json.Marshal(merged.Descriptor)
	require.NoError(t, err)
	decoded, err := Decode(raw)
	require.NoError(t, err)
	_, err = ValidatePublished(raw, decoded)
	require.NoError(t, err)
	return decoded
}

func TestRuntimePublicationRetainsRepeatedNestedScopes(t *testing.T) {
	setup := runtimeTestHook(t, "setup", true, true)
	launch := runtimeTestHook(t, "launch", true, true)
	launch.After = []string{"#setup"}
	component := runtimeTestDescriptor(launch, setup)
	component.Kind = KindMixin
	component.Capabilities[0].Source = &CapabilitySource{Kit: "same-label", Path: "same-path"}
	component.Capabilities[1].Source = &CapabilitySource{Kit: "same-label", Path: "same-path"}
	inner := runtimeTestPublish(t, Contribution{Reference: "same:tag", Descriptor: component}, Contribution{Reference: "same:tag", Descriptor: component})
	outer := runtimeTestPublish(t, Contribution{Reference: "nested:tag", Descriptor: inner}, Contribution{Reference: "base", Descriptor: runtimeTestDescriptor()})
	result := runtimeTestCompose(t, outer, RuntimeOptions{})
	require.Equal(t, []string{"launch", "setup", "launch", "setup"}, runtimeTestEntryIDs(result.Plan))
	require.Equal(t, []string{"setup/startup", "launch/startup", "setup/startup", "launch/startup"}, runtimeTestActionLabels(runtimeTestActions(result.Plan, PhaseStart, StepExecute)))
	require.Len(t, result.Descriptor.Capabilities, 4)
	ids, scopes := map[string]bool{}, map[uint32]bool{}
	for _, entry := range result.Plan.Entries {
		require.False(t, ids[entry.ID])
		ids[entry.ID] = true
		scopes[entry.Origins[0].Scope] = true
		require.Equal(t, "same-label", entry.Origins[0].Source.Kit)
		require.Equal(t, "test-kit:1", entry.Origins[0].Target.Reference)
		require.NotEqual(t, entry.Origins[0].Source.Path, entry.Origins[0].Target.Path)
	}
	require.Len(t, scopes, 2)
}

func TestRuntimePublishedProviderBindingsAndSkippedLocalBridge(t *testing.T) {
	setup := runtimeTestHook(t, "setup", false, true)
	launch := runtimeTestHook(t, "launch", false, true)
	bridge := Capability{Type: "com.example/unavailable@1", ID: "bridge", Before: []string{"#launch"}, After: []string{"#setup"}}
	provider := runtimeTestDescriptor(launch, Capability{Group: &CapabilityGroup{Optional: true, Capabilities: []Capability{bridge}}}, setup)
	provider.Kind = KindMixin
	provider.Provides = []string{"com.example/agent@1"}
	hook := runtimeTestHook(t, "inserted", false, true)
	hook.After = []string{"com.example/agent#setup"}
	hook.Before = []string{"com.example/agent#bridge"}
	outer := runtimeTestPublish(t, Contribution{Reference: "agent:1", Descriptor: provider}, Contribution{Reference: "base", Descriptor: runtimeTestDescriptor(hook)})
	nested := runtimeTestPublish(t, Contribution{Reference: "outer:1", Descriptor: outer})
	result := runtimeTestCompose(t, nested, RuntimeOptions{})
	require.Equal(t, []string{"launch", "setup", "inserted"}, runtimeTestEntryIDs(result.Plan))
	require.Equal(t, []string{"launch/startup", "setup/startup", "inserted/startup"}, runtimeTestActionLabels(runtimeTestActions(result.Plan, PhaseStart, StepExecute)))
}

func TestRuntimeAggregateProviderAliasesRemainWithinPublishedContribution(t *testing.T) {
	innerHook := runtimeTestHook(t, "inside", true, false)
	inner := runtimeTestPublish(t, Contribution{Reference: "inside", Descriptor: runtimeTestDescriptor(innerHook)})
	inner.Kind = KindMixin
	inner.Provides = append(inner.Provides, "com.example/aggregate@1")
	outside := runtimeTestHook(t, "outside", true, false)
	outside.Before = []string{"com.example/aggregate"}
	outer := runtimeTestPublish(t, Contribution{Reference: "nested", Descriptor: inner}, Contribution{Reference: "outside", Descriptor: runtimeTestDescriptor(outside)})
	result := runtimeTestCompose(t, outer, RuntimeOptions{})
	require.Equal(t, []string{"inside", "outside"}, runtimeTestEntryIDs(result.Plan))
	require.Equal(t, []string{"outside/install", "inside/install"}, runtimeTestActionLabels(runtimeTestActions(result.Plan, PhaseInstall, StepExecute)))
}

func TestRuntimeAggregateQualifiedIDsRejectAmbiguity(t *testing.T) {
	component := runtimeTestDescriptor(runtimeTestHook(t, "same", true, false))
	component.Kind = KindMixin
	published := runtimeTestPublish(t, Contribution{Descriptor: component}, Contribution{Descriptor: component})
	published.Provides = append(published.Provides, "com.example/aggregate@1")
	hook := runtimeTestHook(t, "outside", true, false)
	hook.After = []string{"com.example/aggregate#same"}
	result, err := ComposeRuntime(t.Context(), []Contribution{{Descriptor: published}, {Descriptor: runtimeTestDescriptor(hook)}}, RuntimeOptions{Select: Supported(KnownCapabilities()...)})
	require.Nil(t, result)
	require.ErrorContains(t, err, "ambiguous")
}
