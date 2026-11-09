package spec

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRuntimeHookDependenciesUseConsumptionReferenceVersion(t *testing.T) {
	for _, reference := range []string{
		"reg.example/provider:2.0.0",
		"reg.example/provider:v2.0.0@sha256:abc",
		"git+https://github.com/example/provider.git#ref=v2.0.0",
	} {
		t.Run(reference, func(t *testing.T) {
			for _, relation := range []string{"requires", "integrates"} {
				t.Run(relation, func(t *testing.T) {
					provider := runtimeContribution(reference, runtimeOrder(groupHook("provider"), 2000))
					provider.Descriptor.Version = "1.0.0"
					provider.Descriptor.Provides = []string{"service"}
					requirer := runtimeContribution("requirer", groupHook("requirer"))
					if relation == "requires" {
						requirer.Descriptor.Requires = []string{"service >= 2.0.0"}
					} else {
						requirer.Descriptor.Integrates = []string{"service >= 2.0.0"}
					}
					inputs := []Contribution{provider, requirer}
					t.Run("runtime", func(t *testing.T) {
						_, err := ComposeRuntime(t.Context(), inputs, RuntimeOptions{Select: Supported(CapabilityLifecycle)})
						require.ErrorContains(t, err, "must precede dependent hooks")
					})
					t.Run("publication", func(t *testing.T) {
						_, err := Merge(inputs, MergeOptions{})
						require.ErrorContains(t, err, "must precede dependent hooks")
					})
				})
			}
		})
	}
}

func TestRuntimePublicationPreservesConsumptionReferenceVersion(t *testing.T) {
	provider := runtimeContribution("reg.example/provider:2.0.0", runtimeOrder(groupHook("provider"), 500))
	provider.Descriptor.Version = "1.0.0"
	provider.Descriptor.Provides = []string{"service"}
	requirer := runtimeContribution("requirer", groupHook("requirer"))
	requirer.Descriptor.Requires = []string{"service >= 2.0.0"}
	inputs := []Contribution{provider, requirer}
	runtimeCompose(t, inputs...)
	merged, err := Merge(inputs, MergeOptions{})
	require.NoError(t, err)
	require.Equal(t, []string{"service@2.0.0"}, merged.Descriptor.Provides)
	require.Empty(t, merged.Descriptor.Requires)

	provider.Descriptor.Provides = []string{"service@3.0.0"}
	merged, err = Merge(inputs, MergeOptions{})
	require.NoError(t, err)
	require.Equal(t, []string{"service@3.0.0"}, merged.Descriptor.Provides, "an explicitly versioned provide outranks the reference")
}
