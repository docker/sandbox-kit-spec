package spec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

func runtimeTestDescriptor(caps ...Capability) *Descriptor {
	return &Descriptor{SchemaVersion: SchemaVersion, Kind: KindWorkload, Capabilities: caps}
}

func runtimeTestCapability(t *testing.T, typ, id string, config any) Capability {
	t.Helper()
	c, err := CapabilityWithConfig(Capability{Type: typ, ID: id}, config)
	require.NoError(t, err)
	return *c
}

func runtimeTestHook(t *testing.T, id string, install, startup bool) Capability {
	t.Helper()
	config := Lifecycle{}
	if install {
		config.Install = []InstallHook{{Command: CommandLine{"echo", id}}}
	}
	if startup {
		config.Startup = []StartupHook{{Command: CommandLine{"echo", id}}}
	}
	return runtimeTestCapability(t, CapabilityLifecycle, id, config)
}

func runtimeTestCompose(t *testing.T, d *Descriptor, opts RuntimeOptions) *RuntimeComposition {
	t.Helper()
	if opts.Select == nil {
		opts.Select = Supported(KnownCapabilities()...)
	}
	result, err := ComposeRuntime(t.Context(), []Contribution{{Reference: "test-kit:1", Descriptor: d}}, opts)
	require.NoError(t, err)
	return result
}

func runtimeTestEntryIDs(plan ExecutionPlan) []string {
	var ids []string
	for _, entry := range plan.Entries {
		ids = append(ids, entry.Origins[0].ID)
	}
	return ids
}

func runtimeTestActions(plan ExecutionPlan, phase ExecutionPhase, step ExecutionStep) []ExecutionAction {
	for _, phasePlan := range plan.Phases {
		if phasePlan.Phase == phase {
			for _, stepPlan := range phasePlan.Steps {
				if stepPlan.Step == step {
					return stepPlan.Actions
				}
			}
		}
	}
	return nil
}

func runtimeTestActionLabels(actions []ExecutionAction) []string {
	var labels []string
	for _, action := range actions {
		label := action.Action
		if len(action.Origins) > 0 {
			label = action.Origins[0].ID + "/" + label
		}
		labels = append(labels, label)
	}
	return labels
}

func TestRuntimeLifecycleOrderAndFixedBarriers(t *testing.T) {
	setup := runtimeTestHook(t, "setup", true, true)
	setup.Config["files"] = []File{{Path: "/home/agent/config", Content: "ready"}}
	engine := Capability{Type: CapabilityDockerd, ID: "engine"}
	after := runtimeTestHook(t, "after", true, true)
	d := runtimeTestDescriptor(setup, engine, after)
	result := runtimeTestCompose(t, d, RuntimeOptions{WorkspaceAssigned: true})
	require.Equal(t, []string{"setup", "engine", "after"}, runtimeTestEntryIDs(result.Plan))
	require.Equal(t, []string{"workspace-ready"}, runtimeTestActionLabels(runtimeTestActions(result.Plan, PhasePrepare, StepAttach))[1:])
	require.Equal(t, []string{"setup/install", "after/install"}, runtimeTestActionLabels(runtimeTestActions(result.Plan, PhaseInstall, StepExecute)))
	require.Equal(t, []string{"setup/files"}, runtimeTestActionLabels(runtimeTestActions(result.Plan, PhaseStart, StepPlace)))
	require.Equal(t, []string{"setup/startup", "engine/engine", "after/startup"}, runtimeTestActionLabels(runtimeTestActions(result.Plan, PhaseStart, StepExecute)))
	require.Equal(t, []string{"workload"}, runtimeTestActionLabels(runtimeTestActions(result.Plan, PhaseRunning, StepExecute)))
	require.Empty(t, result.Warnings)
	require.Equal(t, "contributions[0].capabilities[0]/effective[0]", result.Plan.Entries[0].ID)
	require.Equal(t, SelectionTarget{Reference: "test-kit:1", Path: "capabilities[0]"}, result.Plan.Entries[0].Origins[0].Target)
	for _, capability := range result.Descriptor.Capabilities {
		require.Empty(t, capability.ID)
		require.Empty(t, capability.Before)
	}
}

func TestRuntimeTypeParticipationIsIndependentOfConfig(t *testing.T) {
	for _, startup := range []bool{false, true} {
		hook := runtimeTestHook(t, "hook", true, startup)
		engine := Capability{Type: CapabilityDockerd, ID: "engine"}
		hook.After = []string{CapabilityDockerd}
		consumer := runtimeTestHook(t, "consumer", true, true)
		result := runtimeTestCompose(t, runtimeTestDescriptor(hook, consumer, engine), RuntimeOptions{})
		// The engine is not an Install node. Its edge must not move the hook's
		// installation behind the consumer that uses the installed tools.
		require.Equal(t, []string{"hook/install", "consumer/install"}, runtimeTestActionLabels(runtimeTestActions(result.Plan, PhaseInstall, StepExecute)))
		want := []string{"consumer/startup", "engine/engine"}
		if startup {
			want = append(want, "hook/startup")
		}
		require.Equal(t, want, runtimeTestActionLabels(runtimeTestActions(result.Plan, PhaseStart, StepExecute)))
	}
}

func TestRuntimeWrittenCyclesRequireSharedParticipation(t *testing.T) {
	for _, shared := range []bool{false, true} {
		first := runtimeTestHook(t, "first", false, true)
		first.Before = []string{"#bridge"}
		bridge := Capability{Type: "com.example/bridge@1", ID: "bridge", Before: []string{"#last"}}
		last := runtimeTestHook(t, "last", false, true)
		last.Before = []string{"#first"}
		contract := ExecutionContract{}
		if shared {
			contract.Participation = []ExecutionBoundary{{PhaseStart, StepExecute}}
		}
		result, err := ComposeRuntime(t.Context(), []Contribution{{Descriptor: runtimeTestDescriptor(first, bridge, last)}}, RuntimeOptions{Select: Supported(CapabilityLifecycle, bridge.Type), Contracts: map[string]ExecutionContract{bridge.Type: contract}})
		if shared {
			require.Nil(t, result)
			require.ErrorContains(t, err, "cycle")
			continue
		}
		require.NoError(t, err)
		require.Equal(t, []string{"last/startup", "first/startup"}, runtimeTestActionLabels(runtimeTestActions(result.Plan, PhaseStart, StepExecute)))
	}
}

func TestRuntimeSkippedGroupKeepsTargetsWithoutOrdering(t *testing.T) {
	first := runtimeTestHook(t, "first", true, true)
	first.Before = []string{"#bridge"}
	bridge := Capability{Type: "com.example/unavailable@1", ID: "bridge", Before: []string{"#last"}}
	last := runtimeTestHook(t, "last", true, true)
	for _, grouped := range []bool{false, true} {
		t.Run(fmt.Sprintf("group=%t", grouped), func(t *testing.T) {
			middle := bridge
			if grouped {
				middle = Capability{Group: &CapabilityGroup{Optional: true, Capabilities: []Capability{bridge}}}
			} else {
				middle.Optional = true
			}
			result := runtimeTestCompose(t, runtimeTestDescriptor(last, middle, first), RuntimeOptions{})
			require.Equal(t, []string{"last", "first"}, runtimeTestEntryIDs(result.Plan))
			require.Equal(t, []string{"last/startup", "first/startup"}, runtimeTestActionLabels(runtimeTestActions(result.Plan, PhaseStart, StepExecute)))
			require.Len(t, result.Selections[0].Selection.Skipped, 1)
			require.Len(t, result.Descriptor.Capabilities, 2)
		})
	}
}

func TestRuntimeStableEligibleEntryOrder(t *testing.T) {
	first := runtimeTestHook(t, "first", true, true)
	first.After = []string{"#third"}
	second := runtimeTestHook(t, "second", true, true)
	third := runtimeTestHook(t, "third", true, true)
	fourth := runtimeTestHook(t, "fourth", true, true)
	result := runtimeTestCompose(t, runtimeTestDescriptor(first, second, third, fourth), RuntimeOptions{})
	require.Equal(t, []string{"first", "second", "third", "fourth"}, runtimeTestEntryIDs(result.Plan))
	for _, phase := range []ExecutionPhase{PhaseInstall, PhaseStart} {
		actions := runtimeTestActions(result.Plan, phase, StepExecute)
		var ids []string
		for _, action := range actions {
			ids = append(ids, action.Origins[0].ID)
		}
		require.Equal(t, []string{"second", "third", "first", "fourth"}, ids)
	}
}

func TestRuntimeTargetsAndAbsentWarnings(t *testing.T) {
	for _, tc := range []struct {
		name, target string
		provider     bool
		id           bool
		wantError    string
		warnings     int
	}{
		{"exact type", CapabilityDockerd, true, true, "", 0},
		{"whole provider", "com.example/base", true, true, "", 0},
		{"qualified ID", "com.example/base#engine", true, true, "", 0},
		{"absent type", "com.example/missing@1", true, true, "", 1},
		{"absent provider", "com.example/missing", true, true, "", 1},
		{"absent qualified provider", "com.example/missing#engine", true, true, "", 1},
		{"present provider missing ID", "com.example/base#engine", true, false, "no entry", 0},
		{"missing local ID", "#missing", true, true, "missing", 0},
		{"unprovided private ID", "com.example/base#engine", false, true, "", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hook := runtimeTestHook(t, "hook", true, false)
			hook.After = []string{tc.target}
			engine := Capability{Type: CapabilityDockerd}
			if tc.id {
				engine.ID = "engine"
			}
			provider := runtimeTestDescriptor(engine)
			provider.Kind = KindMixin
			if tc.provider {
				provider.Provides = []string{"com.example/base@1"}
			}
			result, err := ComposeRuntime(t.Context(), []Contribution{{Descriptor: runtimeTestDescriptor(hook)}, {Descriptor: provider}}, RuntimeOptions{Select: Supported(KnownCapabilities()...)})
			if tc.wantError != "" {
				require.Nil(t, result)
				require.ErrorContains(t, err, tc.wantError)
				return
			}
			require.NoError(t, err)
			require.Len(t, result.Warnings, tc.warnings)
			if tc.warnings == 0 {
				require.Equal(t, []string{"hook", engine.ID}, runtimeTestEntryIDs(result.Plan))
			}
		})
	}
}

func TestRuntimePresentEmptyProviderAndQualifiedFailure(t *testing.T) {
	for _, target := range []string{"com.example/empty", "com.example/empty#missing"} {
		hook := runtimeTestHook(t, "hook", true, false)
		hook.Before = []string{target}
		empty := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Provides: []string{"com.example/empty"}}
		result, err := ComposeRuntime(t.Context(), []Contribution{{Descriptor: runtimeTestDescriptor(hook)}, {Descriptor: empty}}, RuntimeOptions{Select: Supported(KnownCapabilities()...)})
		if target == "com.example/empty" {
			require.NoError(t, err)
			require.Empty(t, result.Warnings)
		} else {
			require.Nil(t, result)
			require.ErrorContains(t, err, "no entry")
		}
	}
}

func TestRuntimeSingletonAliasesAndPostMergeSelfReference(t *testing.T) {
	for _, self := range []bool{false, true} {
		t.Run(fmt.Sprintf("self=%t", self), func(t *testing.T) {
			engineA := Capability{Type: CapabilityDockerd, ID: "engine-a"}
			engineB := Capability{Type: CapabilityDockerd, ID: "engine-b"}
			if self {
				engineA.Before = []string{"com.example/other#engine-b"}
			}
			hook := runtimeTestHook(t, "hook", true, false)
			hook.After = []string{"com.example/other#engine-b"}
			first := runtimeTestDescriptor(hook, engineA)
			second := runtimeTestDescriptor(engineB)
			second.Kind = KindMixin
			second.Provides = []string{"com.example/other"}
			result, err := ComposeRuntime(t.Context(), []Contribution{{Descriptor: first}, {Descriptor: second}}, RuntimeOptions{Select: Supported(KnownCapabilities()...)})
			require.NoError(t, err)
			require.Equal(t, []string{"hook", "engine-a"}, runtimeTestEntryIDs(result.Plan))
			require.Len(t, result.Plan.Entries[1].Origins, 2)
			require.Equal(t, "engine-b", result.Plan.Entries[1].Origins[1].ID)
			require.Len(t, runtimeTestActions(result.Plan, PhaseStart, StepExecute), 1)
		})
	}
}

func TestRuntimeTypeTargetUsesOriginalVersion(t *testing.T) {
	old := Capability{Type: CapabilityNetworkPolicy, ID: "old", Config: map[string]any{"runtime": map[string]any{"allow": []string{"old.example"}}}}
	current := Capability{Type: CapabilityNetworkPolicyV2, ID: "current", Config: map[string]any{"runtime": map[string]any{"allow": []string{"new.example"}}}}
	hook := runtimeTestHook(t, "hook", false, true)
	hook.Before = []string{CapabilityNetworkPolicy}
	first := runtimeTestDescriptor(old, hook)
	second := runtimeTestDescriptor(current)
	second.Kind = KindMixin
	result, err := ComposeRuntime(t.Context(), []Contribution{{Descriptor: first}, {Descriptor: second}}, RuntimeOptions{Select: Supported(KnownCapabilities()...)})
	require.NoError(t, err)
	require.Equal(t, []string{"old", "hook"}, runtimeTestEntryIDs(result.Plan))
	require.Len(t, result.Plan.Entries[0].Origins, 2)
	require.Equal(t, CapabilityNetworkPolicyV2, runtimeTestActions(result.Plan, PhaseStart, StepAttach)[0].Capability.Type)
	require.Empty(t, result.Warnings)
}

func TestRuntimeCancellationAndSelectionFailureAreTyped(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := ComposeRuntime(ctx, nil, RuntimeOptions{})
	require.Nil(t, result)
	require.ErrorIs(t, err, context.Canceled)
	var failure *RuntimeCompositionError
	require.ErrorAs(t, err, &failure)

	d := runtimeTestDescriptor(Capability{Type: CapabilityDockerd, ID: "engine"})
	result, err = ComposeRuntime(t.Context(), []Contribution{{Reference: "declined", Descriptor: d}}, RuntimeOptions{Select: Supported()})
	require.Nil(t, result)
	require.ErrorAs(t, err, &failure)
	require.Len(t, failure.Selections, 1)
	require.Len(t, failure.Selections[0].Selection.Skipped, 1)
	require.Equal(t, "declined", failure.Selections[0].Reference)
}

func TestRuntimeBuiltinContractInventory(t *testing.T) {
	require.ElementsMatch(t, KnownCapabilities(), slices.Collect(maps.Keys(builtinRuntimeContracts())))
}

func TestRuntimeContractsRefuseUnknownOverrideAndInvalidEffects(t *testing.T) {
	custom := "com.example/custom@1"
	selected := func(context.Context, SelectionTarget, Descriptor, Capability) CapabilityDecision {
		return CapabilityDecision{Accepted: true}
	}
	for _, tc := range []struct {
		name      string
		contracts map[string]ExecutionContract
		want      string
	}{
		{"missing", nil, "no execution contract"},
		{"builtin override", map[string]ExecutionContract{CapabilityDockerd: {}}, "cannot be replaced"},
		{"invalid registration", map[string]ExecutionContract{"bad": {}}, "exact capability"},
		{"invalid step", map[string]ExecutionContract{custom: {Effects: func(Capability) ([]PlannedEffect, error) {
			return []PlannedEffect{{Phase: PhasePrepare, Step: StepAttach, Action: "daemon", Kind: EffectProgram}}, nil
		}}}, "does not belong"},
		{"duplicate action", map[string]ExecutionContract{custom: {Participation: []ExecutionBoundary{{PhaseStart, StepExecute}}, Effects: func(Capability) ([]PlannedEffect, error) {
			effect := PlannedEffect{Phase: PhaseStart, Step: StepExecute, Action: "run", Kind: EffectProgram}
			return []PlannedEffect{effect, effect}, nil
		}}}, "repeats effect"},
		{"contract failure", map[string]ExecutionContract{custom: {Effects: func(Capability) ([]PlannedEffect, error) { return nil, errors.New("contract failed") }}}, "contract failed"},
		{"invalid participation", map[string]ExecutionContract{custom: {Participation: []ExecutionBoundary{{Phase: PhaseStart, Step: "invalid"}}}}, "invalid or duplicate participation"},
		{"missing participation", map[string]ExecutionContract{custom: {Effects: func(Capability) ([]PlannedEffect, error) {
			return []PlannedEffect{{Phase: PhaseStart, Step: StepExecute, Action: "run", Kind: EffectCommand}}, nil
		}}}, "outside fixed participation"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := ComposeRuntime(t.Context(), []Contribution{{Descriptor: runtimeTestDescriptor(Capability{Type: custom})}}, RuntimeOptions{Select: selected, Contracts: tc.contracts})
			require.Nil(t, result)
			require.ErrorContains(t, err, tc.want)
			var failure *RuntimeCompositionError
			require.ErrorAs(t, err, &failure)
		})
	}
	result, err := ComposeRuntime(t.Context(), []Contribution{{Descriptor: runtimeTestDescriptor(Capability{Type: custom})}}, RuntimeOptions{Select: selected, Contracts: map[string]ExecutionContract{custom: {}}})
	require.NoError(t, err)
	require.True(t, result.Plan.Entries[0].Selected)
	require.Empty(t, runtimeTestActions(result.Plan, PhaseStart, StepExecute))
}

func TestRuntimeCallbacksCannotMutatePlanningInputs(t *testing.T) {
	custom := Capability{Type: "com.example/custom@1", ID: "custom", Config: map[string]any{"value": "original"}}
	hook := runtimeTestHook(t, "hook", true, false)
	hook.After = []string{"#custom"}
	d := runtimeTestDescriptor(hook, custom)
	before, err := json.Marshal(d)
	require.NoError(t, err)
	result, err := ComposeRuntime(t.Context(), []Contribution{{Descriptor: d}}, RuntimeOptions{
		Select: func(_ context.Context, _ SelectionTarget, copy Descriptor, c Capability) CapabilityDecision {
			copy.Capabilities[0].After[0] = "#missing"
			copy.Capabilities[1].Config["value"] = "descriptor mutated"
			if c.Type == custom.Type {
				c.Config["value"] = "capability mutated"
			}
			return CapabilityDecision{Accepted: true}
		},
		Contracts: map[string]ExecutionContract{custom.Type: {Effects: func(c Capability) ([]PlannedEffect, error) {
			require.Equal(t, "original", c.Config["value"])
			c.Config["value"] = "contract mutated"
			return nil, nil
		}}},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"hook", "custom"}, runtimeTestEntryIDs(result.Plan))
	after, err := json.Marshal(d)
	require.NoError(t, err)
	require.JSONEq(t, string(before), string(after))
	require.Equal(t, "original", result.Descriptor.Capabilities[1].Config["value"])
}

func TestRuntimeSelectorRetainsOriginalPresenceAndTypedValues(t *testing.T) {
	d, err := Decode([]byte(`schemaVersion: "3"
kind: workload
capabilities:
  - type: com.example/first@1
    config: {}
  - type: com.example/second@1
    config: {typed: [a, b]}
`))
	require.NoError(t, err)
	d.Capabilities[1].Config["typed"] = []string{"a", "b"}
	calls := 0
	result, err := ComposeRuntime(t.Context(), []Contribution{{Descriptor: d}}, RuntimeOptions{
		Select: func(_ context.Context, _ SelectionTarget, owner Descriptor, c Capability) CapabilityDecision {
			require.Equal(t, *d, owner)
			require.Equal(t, d.Capabilities[calls], c)
			calls++
			if typed, ok := c.Config["typed"].([]string); ok {
				typed[0] = "changed"
			}
			return CapabilityDecision{Accepted: true}
		},
		Contracts: map[string]ExecutionContract{"com.example/first@1": {}, "com.example/second@1": {}},
	})
	require.NoError(t, err)
	require.Equal(t, 2, calls)
	require.Equal(t, []string{"a", "b"}, d.Capabilities[1].Config["typed"])
	require.Equal(t, []string{"a", "b"}, result.Descriptor.Capabilities[1].Config["typed"])
}

func TestRuntimeSelectedConflictsPrecedeUnknownContractFailure(t *testing.T) {
	first := runtimeTestCapability(t, CapabilityCredential, "one", Credential{Service: "github", Phase: Phases{"runtime"}, APIKey: &APIKey{Name: "TOKEN_A"}})
	second := runtimeTestCapability(t, CapabilityCredential, "two", Credential{Service: "github", Phase: Phases{"runtime"}, APIKey: &APIKey{Name: "TOKEN_B"}})
	base := runtimeTestDescriptor(first, Capability{Type: "com.example/unregistered@1"})
	other := runtimeTestDescriptor(second)
	other.Kind = KindMixin
	result, err := ComposeRuntime(t.Context(), []Contribution{{Descriptor: base}, {Descriptor: other}}, RuntimeOptions{
		Select: func(context.Context, SelectionTarget, Descriptor, Capability) CapabilityDecision {
			return CapabilityDecision{Accepted: true}
		},
	})
	require.Nil(t, result)
	require.ErrorContains(t, err, "credential")
	require.NotContains(t, err.Error(), "no execution contract")
	var failure *RuntimeCompositionError
	require.ErrorAs(t, err, &failure)
	require.Len(t, failure.Selections, 2)
}

func TestRuntimeSelectedLifecycleConflictsAcrossGroups(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		t.Run(fmt.Sprintf("interactive=%t", interactive), func(t *testing.T) {
			config := Lifecycle{Files: []File{{Path: "/shared", Content: "one"}}}
			if interactive {
				config = Lifecycle{Interactive: []string{}, Startup: []StartupHook{{Command: CommandLine{"echo", "ready"}}}}
			}
			first := runtimeTestCapability(t, CapabilityLifecycle, "one", config)
			second := runtimeTestCapability(t, CapabilityLifecycle, "two", config)
			d := runtimeTestDescriptor(Capability{Group: &CapabilityGroup{Optional: true, Capabilities: []Capability{first}}}, Capability{Group: &CapabilityGroup{Optional: true, Capabilities: []Capability{second}}})
			result, err := ComposeRuntime(t.Context(), []Contribution{{Descriptor: d}}, RuntimeOptions{Select: Supported(KnownCapabilities()...)})
			require.Nil(t, result)
			require.Error(t, err)
			var failure *RuntimeCompositionError
			require.ErrorAs(t, err, &failure)
			require.Len(t, failure.Selections, 1, "error: %v", err)
			require.Len(t, failure.Selections[0].Selection.Selected, 2)
			result, err = ComposeRuntime(t.Context(), []Contribution{{Descriptor: d}}, RuntimeOptions{Select: func(_ context.Context, _ SelectionTarget, _ Descriptor, c Capability) CapabilityDecision {
				return CapabilityDecision{Accepted: c.ID == "one"}
			}})
			require.NoError(t, err)
			require.Len(t, result.Descriptor.Capabilities, 1)
		})
	}
}

// These cases exercise the normal constraints, rather than just stable sorting.
// In particular, an inserted entry may move itself across a dependency but must
// never make the provider's untouched setup and launch swap places.
func TestRuntimeWrittenEdgesMoveOnlyTheirWriter(t *testing.T) {
	for _, tc := range []struct {
		name          string
		before, after []string
		want          []string
		refuse        bool
	}{
		{"before setup", []string{"base#setup"}, nil, []string{"inserted/startup", "setup/startup", "launch/startup", "requirer/startup"}, false},
		{"between", []string{"base#launch"}, []string{"base#setup"}, []string{"setup/startup", "inserted/startup", "launch/startup", "requirer/startup"}, false},
		{"contradiction", []string{"base#setup"}, []string{"base#launch"}, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := runtimeTestDescriptor(runtimeTestHook(t, "setup", true, true), runtimeTestHook(t, "launch", true, true))
			base.Kind = KindMixin
			base.Provides = []string{"base@1"}
			inserted := runtimeTestHook(t, "inserted", true, true)
			inserted.Before = tc.before
			inserted.After = tc.after
			mixin := runtimeTestDescriptor(inserted, runtimeTestHook(t, "requirer", true, true))
			mixin.Requires = []string{"base"}
			result, err := ComposeRuntime(t.Context(), []Contribution{{Reference: "base", Descriptor: base}, {Reference: "mixin", Descriptor: mixin}}, RuntimeOptions{Select: Supported(KnownCapabilities()...)})
			if tc.refuse {
				require.Nil(t, result)
				require.ErrorContains(t, err, "cycle")
				require.ErrorContains(t, err, "#setup")
				require.ErrorContains(t, err, "#launch")
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, runtimeTestActionLabels(runtimeTestActions(result.Plan, PhaseStart, StepExecute)))
		})
	}
}

func TestRuntimeTransitiveDependenciesFollowMovedProvider(t *testing.T) {
	toolHook := runtimeTestHook(t, "tool", true, false)
	toolHook.After = []string{"extra"}
	tool := runtimeTestDescriptor(toolHook)
	tool.Kind = KindMixin
	tool.Provides = []string{"tool@1"}
	app := runtimeTestDescriptor(runtimeTestHook(t, "app", true, false))
	app.Requires = []string{"tool"}
	extra := runtimeTestDescriptor(runtimeTestHook(t, "extra", true, false))
	extra.Kind = KindMixin
	extra.Provides = []string{"extra@1"}
	for _, inputs := range [][]Contribution{
		{{Reference: "tool", Descriptor: tool}, {Reference: "app", Descriptor: app}, {Reference: "extra", Descriptor: extra}},
		{{Reference: "extra", Descriptor: extra}, {Reference: "tool", Descriptor: tool}, {Reference: "app", Descriptor: app}},
	} {
		result, err := ComposeRuntime(t.Context(), inputs, RuntimeOptions{Select: Supported(KnownCapabilities()...)})
		require.NoError(t, err)
		require.Equal(t, []string{"extra/install", "tool/install", "app/install"}, runtimeTestActionLabels(runtimeTestActions(result.Plan, PhaseInstall, StepExecute)))
	}
}

func TestRuntimeEmptyLifecycleStillParticipates(t *testing.T) {
	first := runtimeTestHook(t, "first", true, true)
	first.After = []string{"#config"}
	second := runtimeTestHook(t, "second", true, false)
	config := runtimeTestHook(t, "config", false, true)
	result := runtimeTestCompose(t, runtimeTestDescriptor(first, second, config), RuntimeOptions{})
	require.Equal(t, []string{"second/install", "first/install"}, runtimeTestActionLabels(runtimeTestActions(result.Plan, PhaseInstall, StepExecute)))
	require.Equal(t, []string{"config/startup", "first/startup"}, runtimeTestActionLabels(runtimeTestActions(result.Plan, PhaseStart, StepExecute)))
}

func TestRuntimeQualifiedOwnIDIsNotAReconciliationAlias(t *testing.T) {
	hook := runtimeTestHook(t, "setup", false, true)
	hook.Before = []string{"kit#setup"}
	d := runtimeTestDescriptor(hook)
	d.Provides = []string{"kit@1"}
	result, err := ComposeRuntime(t.Context(), []Contribution{{Descriptor: d}}, RuntimeOptions{Select: Supported(KnownCapabilities()...)})
	require.Nil(t, result)
	require.ErrorContains(t, err, "self-reference")
}

func TestRuntimeWrittenDisplacementFollowsOtherBeforeEdges(t *testing.T) {
	base := runtimeTestDescriptor(Capability{Type: CapabilityDockerd, ID: "engine"}, runtimeTestHook(t, "observe", false, true))
	base.Kind, base.Provides = KindMixin, []string{"base@1"}
	proxy := runtimeTestHook(t, "proxy", false, true)
	proxy.Before = []string{CapabilityDockerd}
	first := runtimeTestDescriptor(proxy)
	first.Kind, first.Provides, first.Requires = KindMixin, []string{"proxy@1"}, []string{"base"}
	auth := runtimeTestHook(t, "auth", false, true)
	auth.Before = []string{"proxy"}
	second := runtimeTestDescriptor(auth)
	second.Requires = []string{"proxy"}
	result, err := ComposeRuntime(t.Context(), []Contribution{{Descriptor: base}, {Descriptor: first}, {Descriptor: second}}, RuntimeOptions{Select: Supported(KnownCapabilities()...)})
	require.NoError(t, err)
	// Auth names only the proxy. Its displacement must also reach the engine
	// through the proxy's before edge, without disturbing engine then observe.
	require.Equal(t, []string{"auth/startup", "proxy/startup", "engine/engine", "observe/startup"}, runtimeTestActionLabels(runtimeTestActions(result.Plan, PhaseStart, StepExecute)))
}
