package spec

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRuntimeInstallCleanupAndReverseStop(t *testing.T) {
	grant := runtimeTestCapability(t, CapabilityCredential, "token", Credential{Service: "github", Phase: Phases{"install", "runtime"}, APIKey: &APIKey{Name: "TOKEN"}})
	policy := Capability{Type: CapabilityNetworkPolicy, ID: "policy"}
	volume := runtimeTestCapability(t, CapabilityVolume, "store", Volume{Path: "/work"})
	engine := Capability{Type: CapabilityDockerd, ID: "engine"}
	first := runtimeTestCapability(t, CapabilityLifecycle, "first", Lifecycle{Startup: []StartupHook{{Command: CommandLine{"first"}, Background: true}}})
	second := runtimeTestCapability(t, CapabilityLifecycle, "second", Lifecycle{Startup: []StartupHook{{Command: CommandLine{"second"}, Background: true}}})
	second.After = []string{"#first"}
	d := runtimeTestDescriptor(volume, grant, policy, second, first, engine)
	result := runtimeTestCompose(t, d, RuntimeOptions{})
	require.Equal(t, []string{"token/credential-material", "policy/network-policy", "token/credential-access"}, runtimeTestActionLabels(result.Plan.InstallEnd))
	require.Equal(t, []string{"workload", "engine/engine", "second/startup", "first/startup", "token/credential-material", "policy/network-policy", "token/credential-access", "token/credential-material", "policy/network-policy", "token/credential-access", "engine/engine-resources", "store/volume"}, runtimeTestActionLabels(result.Plan.Stop))
	for _, action := range result.Plan.InstallEnd {
		require.True(t, action.End)
		require.Equal(t, PhaseInstall, action.Phase)
		require.Equal(t, RepeatOnce, action.Repetition)
	}
	for _, action := range result.Plan.Stop {
		require.True(t, action.End)
		want := RepeatEveryBoot
		if action.Phase == PhaseInstall {
			want = RepeatOnce
		}
		require.Equal(t, want, action.Repetition)
	}
	for _, phase := range result.Plan.Phases {
		for _, step := range phase.Steps {
			for _, action := range step.Actions {
				require.False(t, action.End)
				want := RepeatEveryBoot
				if phase.Phase == PhaseInstall {
					want = RepeatOnce
				}
				require.Equal(t, want, action.Repetition)
			}
		}
	}
}

func TestRuntimeWorkspaceReadyBeforeInstallAccessAndHooks(t *testing.T) {
	custom := "com.example/clone-service@1"
	service := Capability{Type: custom, ID: "service"}
	hook := runtimeTestHook(t, "hook", true, false)
	hook.After = []string{"#service"}
	grant := runtimeTestCapability(t, CapabilitySSHAgent, "ssh", SSHAgent{Phase: Phases{"install"}})
	for _, hooks := range []bool{false, true} {
		t.Run(map[bool]string{false: "no hooks", true: "with hooks"}[hooks], func(t *testing.T) {
			capabilities := []Capability{service, grant}
			if hooks {
				capabilities = append(capabilities, hook)
			}
			result := runtimeTestCompose(t, runtimeTestDescriptor(capabilities...), RuntimeOptions{
				Select: func(context.Context, SelectionTarget, Descriptor, Capability) CapabilityDecision {
					return CapabilityDecision{Accepted: true}
				},
				WorkspaceAssigned: true,
				Contracts: map[string]ExecutionContract{custom: {Participation: []ExecutionBoundary{{PhaseInstall, StepExecute}}, Effects: func(Capability) ([]PlannedEffect, error) {
					return []PlannedEffect{{Phase: PhaseInstall, Step: StepExecute, Action: "clone-service", Kind: EffectProgram, End: true}}, nil
				}}},
			})
			require.Equal(t, []string{"workspace-ready"}, runtimeTestActionLabels(runtimeTestActions(result.Plan, PhasePrepare, StepAttach)))
			want := []string{"service/clone-service"}
			if hooks {
				want = append(want, "hook/install")
			}
			require.Equal(t, want, runtimeTestActionLabels(runtimeTestActions(result.Plan, PhaseInstall, StepExecute)))
			require.Equal(t, []string{"ssh/ssh-agent-access"}, runtimeTestActionLabels(runtimeTestActions(result.Plan, PhaseInstall, StepAttach)))
			require.Equal(t, []string{"ssh/ssh-agent-material"}, runtimeTestActionLabels(runtimeTestActions(result.Plan, PhaseInstall, StepPlace)))
			end := []string{"service/clone-service", "ssh/ssh-agent-material", "ssh/ssh-agent-access"}
			if hooks {
				end = append([]string{"hook/install"}, end...)
			}
			require.Equal(t, end, runtimeTestActionLabels(result.Plan.InstallEnd))
			require.Equal(t, "workspace-ready", result.Plan.Stop[len(result.Plan.Stop)-1].Action)
		})
	}
}

func TestRuntimeCrossBoundaryWrittenCycleDoesNotConstrainOtherSteps(t *testing.T) {
	alpha, beta := "com.example/alpha@1", "com.example/beta@1"
	contract := func(phase ExecutionPhase) ExecutionContract {
		return ExecutionContract{Participation: []ExecutionBoundary{{phase, StepExecute}}, Effects: func(Capability) ([]PlannedEffect, error) {
			return []PlannedEffect{{Phase: phase, Step: StepExecute, Action: "run", Kind: EffectCommand}}, nil
		}}
	}
	result, err := ComposeRuntime(t.Context(), []Contribution{{Descriptor: runtimeTestDescriptor(Capability{Type: alpha, ID: "alpha", Before: []string{"#beta"}}, Capability{Type: beta, ID: "beta", Before: []string{"#alpha"}})}}, RuntimeOptions{Select: Supported(alpha, beta), Contracts: map[string]ExecutionContract{alpha: contract(PhaseInstall), beta: contract(PhaseStart)}})
	require.NoError(t, err)
	require.Equal(t, []string{"alpha/run"}, runtimeTestActionLabels(runtimeTestActions(result.Plan, PhaseInstall, StepExecute)))
	require.Equal(t, []string{"beta/run"}, runtimeTestActionLabels(runtimeTestActions(result.Plan, PhaseStart, StepExecute)))
}

func TestRuntimeSplitsSharedGrantsWithoutDuplicatingEffects(t *testing.T) {
	both := runtimeTestCapability(t, CapabilitySSHAgent, "both", SSHAgent{Phase: Phases{"install", "runtime"}})
	runtimeOnly := runtimeTestCapability(t, CapabilitySSHAgent, "runtime", SSHAgent{Phase: Phases{"runtime"}})
	first := runtimeTestDescriptor(both)
	second := runtimeTestDescriptor(runtimeOnly)
	second.Kind = KindMixin
	result, err := ComposeRuntime(t.Context(), []Contribution{{Reference: "one", Descriptor: first}, {Reference: "two", Descriptor: second}}, RuntimeOptions{Select: Supported(KnownCapabilities()...)})
	require.NoError(t, err)
	require.Len(t, result.Plan.Entries, 2)
	require.Len(t, result.Plan.Entries[0].Origins, 1)
	require.Len(t, result.Plan.Entries[1].Origins, 2)
	install := runtimeTestActions(result.Plan, PhaseInstall, StepAttach)
	start := runtimeTestActions(result.Plan, PhaseStart, StepAttach)
	require.Len(t, install, 1)
	require.Len(t, start, 1)
	require.NotEqual(t, install[0].EntryID, start[0].EntryID)
	require.NotEqual(t, install[0].FragmentID, start[0].FragmentID)
	require.Len(t, install[0].Origins, 1)
	require.Len(t, start[0].Origins, 2)
}

func TestRuntimeRepeatedActionNamesHaveDistinctFragmentIDs(t *testing.T) {
	contextA := runtimeTestCapability(t, CapabilityAgentContextV2, "first", AgentContextV2{Directory: "/first", Filename: "AGENTS.md", Content: "first"})
	contextB := runtimeTestCapability(t, CapabilityAgentContextV2, "second", AgentContextV2{Directory: "/second", Filename: "AGENTS.md", Content: "second"})
	first := runtimeTestDescriptor(contextA)
	second := runtimeTestDescriptor(contextB)
	second.Kind = KindMixin
	result, err := ComposeRuntime(t.Context(), []Contribution{{Descriptor: first}, {Descriptor: second}}, RuntimeOptions{Select: Supported(KnownCapabilities()...)})
	require.NoError(t, err)
	require.Len(t, result.Plan.Entries, 2)
	actions := runtimeTestActions(result.Plan, PhaseStart, StepPlace)
	require.Len(t, actions, 2)
	require.NotEqual(t, actions[0].EntryID, actions[1].EntryID)
	require.Equal(t, actions[0].Action, actions[1].Action)
	require.NotEqual(t, actions[0].FragmentID, actions[1].FragmentID)
}

func TestRuntimeInjectOnlyCredentialsHaveNoPlacement(t *testing.T) {
	grant := runtimeTestCapability(t, CapabilityCredential, "injection", Credential{Service: "github", Phase: Phases{"install", "runtime"}, APIKey: &APIKey{Inject: []Inject{{Domain: "github.com"}}}})
	policy := Capability{Type: CapabilityNetworkPolicy, ID: "policy", Config: map[string]any{"install": map[string]any{"allow": []string{"github.com"}}, "runtime": map[string]any{"allow": []string{"github.com"}}}}
	result := runtimeTestCompose(t, runtimeTestDescriptor(grant, policy), RuntimeOptions{})
	for _, phase := range []ExecutionPhase{PhaseInstall, PhaseStart} {
		require.Empty(t, runtimeTestActions(result.Plan, phase, StepPlace))
		require.Equal(t, []string{"injection/credential-access", "policy/network-policy"}, runtimeTestActionLabels(runtimeTestActions(result.Plan, phase, StepAttach)))
	}
	require.Equal(t, []string{"policy/network-policy", "injection/credential-access"}, runtimeTestActionLabels(result.Plan.InstallEnd))
}

func TestRuntimeStopHasCandidatesForInFlightForegroundCommands(t *testing.T) {
	hook := runtimeTestHook(t, "foreground", true, true)
	result := runtimeTestCompose(t, runtimeTestDescriptor(hook), RuntimeOptions{WorkspaceAssigned: true})
	// These are candidates, not unconditional cleanup calls. A runner withdraws
	// only effects still active at Stop, so an interrupted command can be ended
	// while a naturally completed command is neither replayed nor undone.
	require.Equal(t, []string{"foreground/install"}, runtimeTestActionLabels(result.Plan.InstallEnd))
	require.Equal(t, []string{"workload", "foreground/startup", "foreground/install", "workspace-ready"}, runtimeTestActionLabels(result.Plan.Stop))
}
