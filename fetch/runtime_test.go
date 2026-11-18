package fetch

import (
	"context"
	"testing"

	"github.com/docker/sandbox-kit-spec/v3/spec"
	"github.com/stretchr/testify/require"
)

func TestResolveDoesNotAttributeCompositionFailureToLastKit(t *testing.T) {
	reg := newRegistry(t)
	first := &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindWorkload,
		Capabilities: []spec.Capability{{Type: spec.CapabilityCredential, Config: map[string]any{
			"service": "example", "phase": "runtime", "apiKey": map[string]any{
				"name": "TOKEN", "inject": []any{map[string]any{"domain": "api.example.com", "header": "Authorization", "format": "Bearer %s"}},
			},
		}}}}
	last := &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindMixin}
	reg.tag("first", "1.0.0", reg.image(t, kitJSON(t, first)))
	reg.tag("last", "1.0.0", reg.image(t, kitJSON(t, last)))
	client, err := New()
	require.NoError(t, err)
	lastRef := reg.ref("last", "1.0.0")
	_, err = client.Resolve(t.Context(), reqs(reg.ref("first", "1.0.0"), lastRef))
	require.ErrorContains(t, err, "not in the network policy")
	require.NotContains(t, err.Error(), lastRef+":", "an effective-composition error has no single input source")
}

func TestAssembleReturnsExecutablePlan(t *testing.T) {
	base, _ := assemblyFixture(t, spec.KindWorkload, "base", "usr/local/bin/base")
	base.Descriptor = kitJSON(t, &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindWorkload,
		Capabilities: []spec.Capability{
			{Type: spec.CapabilityLifecycle, ID: "hook", After: []string{"#service"}, Config: map[string]any{
				"install": []any{map[string]any{"command": "test -r \"$WORKSPACE_DIR/fixture\""}},
				"startup": []any{map[string]any{"command": "use-service"}},
			}},
			{Type: "com.example/service@1", ID: "service"},
		},
	})
	contracts := map[string]spec.ExecutionContract{"com.example/service@1": {
		Participation: []spec.ExecutionBoundary{{Phase: spec.PhaseStart, Step: spec.StepExecute}},
		Effects: func(spec.Capability) ([]spec.PlannedEffect, error) {
			return []spec.PlannedEffect{{Phase: spec.PhaseStart, Step: spec.StepExecute,
				Action: "serve", Kind: spec.EffectProgram, End: true}}, nil
		},
	}}
	result, err := Assemble(t.Context(), fixtureRequests(1), Options{
		Loader: fixtureLoader(base), WorkspaceAssigned: true, ExecutionContracts: contracts,
		CapabilitySelector: spec.Supported(spec.CapabilityLifecycle, "com.example/service@1"),
	})
	require.NoError(t, err)
	var prepare, install, startup, stop []string
	for _, phase := range result.Resolved.Plan.Phases {
		for _, step := range phase.Steps {
			for _, action := range step.Actions {
				switch phase.Phase {
				case spec.PhasePrepare:
					prepare = append(prepare, action.Action)
					require.Equal(t, spec.StepAttach, action.Step)
				case spec.PhaseInstall:
					install = append(install, action.Action)
					require.Equal(t, spec.RepeatOnce, action.Repetition)
				case spec.PhaseStart:
					startup = append(startup, action.Action)
					require.Equal(t, spec.RepeatEveryBoot, action.Repetition)
				}
			}
		}
	}
	for _, action := range result.Resolved.Plan.Stop {
		stop = append(stop, action.Action)
	}
	require.Equal(t, []string{"workspace-ready"}, prepare)
	require.Equal(t, []string{"install"}, install)
	require.Equal(t, []string{"serve", "startup"}, startup)
	require.Equal(t, []string{"workload", "startup", "serve", "install", "workspace-ready"}, stop)
	require.Equal(t, "capabilities[0]", result.Resolved.Selections[0].Selection.Selected[0].Path)
	require.Equal(t, "hook", result.Resolved.Kits[0].Descriptor.Capabilities[0].ID)
}

func TestResolvePlanningFailureHasNoResult(t *testing.T) {
	reg := newRegistry(t)
	d := &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindWorkload,
		Capabilities: []spec.Capability{{Type: "com.example/selected@1", Optional: true}}}
	reg.tag("kit", "1.0.0", reg.image(t, kitJSON(t, d)))
	client, err := New()
	require.NoError(t, err)
	selector := func(context.Context, spec.SelectionTarget, spec.Descriptor, spec.Capability) spec.CapabilityDecision {
		return spec.CapabilityDecision{Accepted: true}
	}
	result, err := client.Resolve(t.Context(), reqs(reg.ref("kit", "1.0.0")), WithCapabilitySelector(selector))
	require.Nil(t, result)
	require.ErrorContains(t, err, "no execution contract")
	var compositionErr *spec.RuntimeCompositionError
	require.ErrorAs(t, err, &compositionErr)
	require.Len(t, compositionErr.Selections, 1)
	require.Len(t, compositionErr.Selections[0].Selection.Selected, 1)
}
