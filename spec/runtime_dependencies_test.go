package spec

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRuntimeDependenciesMatchEveryOfferedVersion(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provides []string
		required string
		matches  bool
	}{
		{"ascending first", []string{"tool@1.0.0", "tool@2.0.0"}, "tool = 1.0.0", true},
		{"ascending last", []string{"tool@1.0.0", "tool@2.0.0"}, "tool = 2.0.0", true},
		{"descending first", []string{"tool@2.0.0", "tool@1.0.0"}, "tool = 2.0.0", true},
		{"descending last", []string{"tool@2.0.0", "tool@1.0.0"}, "tool = 1.0.0", true},
		{"fallback first", []string{"tool", "tool@2.0.0"}, "tool = 1.0.0", true},
		{"fallback last", []string{"tool@2.0.0", "tool"}, "tool = 1.0.0", true},
		{"unmatched version", []string{"tool@1.0.0", "tool@2.0.0"}, "tool = 3.0.0", false},
	} {
		for _, scope := range []string{"implicit", "original", "aggregate"} {
			for _, dependency := range []string{"requires", "integrates"} {
				t.Run(tc.name+"/"+scope+"/"+dependency, func(t *testing.T) {
					hook := runtimeTestHook(t, "provider", true, true)
					hook.After = []string{"extra"}
					provider := runtimeTestDescriptor(hook)
					provider.Kind, provider.Version, provider.Provides = KindMixin, "1.0.0", tc.provides
					if scope != "implicit" {
						provider.CapabilityScopes = []CapabilityScope{{}}
						zero := uint32(0)
						provider.Capabilities[0].Scope = &zero
						if scope == "original" {
							provider.CapabilityScopes[0].Provides = []string{"com.docker.kit/tool"}
						}
					}
					consumer := runtimeTestDescriptor(runtimeTestHook(t, "consumer", true, true))
					if dependency == "requires" {
						consumer.Requires = []string{tc.required}
					} else {
						consumer.Integrates = []string{tc.required}
					}
					// Moving the provider exposes a lost dependency that the input
					// order alone would hide. Its consumer must follow that move.
					extraHook := runtimeTestHook(t, "extra", true, true)
					// Versions are offers from one provider, not distinct ID targets.
					// A binding per version would make this qualified ID ambiguous.
					extraHook.Before = []string{"tool#provider"}
					extra := runtimeTestDescriptor(extraHook)
					extra.Kind, extra.Provides = KindMixin, []string{"extra@1"}
					result, err := ComposeRuntime(t.Context(), []Contribution{
						{Reference: "provider", Descriptor: provider},
						{Reference: "consumer", Descriptor: consumer},
						{Reference: "extra", Descriptor: extra},
					}, RuntimeOptions{Select: Supported(CapabilityLifecycle)})
					require.NoError(t, err)
					want := []string{"extra", "provider", "consumer"}
					if !tc.matches {
						// A present name with no matching version adds no dependency.
						want = []string{"consumer", "extra", "provider"}
					}
					for _, phase := range []ExecutionPhase{PhaseInstall, PhaseStart} {
						var got []string
						for _, action := range runtimeTestActions(result.Plan, phase, StepExecute) {
							got = append(got, action.Origins[0].ID)
						}
						require.Equal(t, want, got, "phase %s", phase)
					}
				})
			}
		}
	}
}
