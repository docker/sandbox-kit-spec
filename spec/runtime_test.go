package spec

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func runtimeContribution(reference string, capabilities ...Capability) Contribution {
	return Contribution{Reference: reference, Descriptor: &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: capabilities}}
}

func runtimeOrder(c Capability, order int32) Capability {
	c.Order = &order
	return c
}

func runtimeConfig(t *testing.T, typ string, config any) Capability {
	t.Helper()
	c, err := capabilityFrom(typ, config)
	require.NoError(t, err)
	return *c
}

func runtimeLifecycle(t *testing.T, config Lifecycle) Capability {
	t.Helper()
	return runtimeConfig(t, CapabilityLifecycle, config)
}

func runtimeStage(t *testing.T, plan ExecutionPlan, stage ExecutionStage) []ExecutionStep {
	t.Helper()
	at := slices.IndexFunc(plan.Stages, func(p StagePlan) bool { return p.Stage == stage })
	require.NotEqual(t, -1, at)
	return plan.Stages[at].Steps
}

func runtimeCompose(t *testing.T, contributions ...Contribution) *RuntimeComposition {
	t.Helper()
	result, err := ComposeRuntime(t.Context(), contributions, RuntimeOptions{Select: Supported(KnownCapabilities()...)})
	require.NoError(t, err)
	require.NotNil(t, result)
	return result
}

func runtimeFailure(t *testing.T, contributions []Contribution, opts RuntimeOptions, message string) RuntimeCompositionError {
	t.Helper()
	result, err := ComposeRuntime(t.Context(), contributions, opts)
	require.Nil(t, result, "an error must never expose an executable partial plan")
	require.ErrorContains(t, err, message)
	var detail *RuntimeCompositionError
	require.ErrorAs(t, err, &detail)
	require.Error(t, errors.Unwrap(detail))
	return *detail
}

func TestRuntimeInterleavesLifecycleAndDockerReadiness(t *testing.T) {
	files := runtimeOrder(runtimeLifecycle(t, Lifecycle{Files: []File{{Path: "/etc/docker/daemon.json", Content: "{}"}}}), 400)
	commands := []StartupHook{{Command: CommandLine{"docker", "info"}}, {Command: CommandLine{"docker", "info"}}}
	late := runtimeLifecycle(t, Lifecycle{Install: []InstallHook{{Command: CommandLine{"prepare"}}}, Startup: commands})
	input := runtimeContribution("kit", late, Capability{Type: CapabilityDockerd}, files, runtimeLifecycle(t, Lifecycle{Startup: commands}))
	result := runtimeCompose(t, input)
	require.Equal(t, []ExecutionStage{StageConfigure, StageSetup, StageInstall, StageStart}, []ExecutionStage{
		result.Plan.Stages[0].Stage, result.Plan.Stages[1].Stage, result.Plan.Stages[2].Stage, result.Plan.Stages[3].Stage,
	})
	setup := runtimeStage(t, result.Plan, StageSetup)
	require.Len(t, setup, 1)
	require.Equal(t, ActionPrepare, setup[0].Action)
	install := runtimeStage(t, result.Plan, StageInstall)
	require.Len(t, install, 1)
	require.Equal(t, ActionInstall, install[0].Action)
	start := runtimeStage(t, result.Plan, StageStart)
	require.Len(t, start, 4)
	require.Equal(t, []string{ActionFiles, ActionStart, ActionStartup, ActionStartup}, []string{start[0].Action, start[1].Action, start[2].Action, start[3].Action})
	require.Equal(t, []int32{400, 500, 1000, 1000}, []int32{start[0].Order, start[1].Order, start[2].Order, start[3].Order})
	require.Equal(t, "capabilities[2]", start[0].Origins[0].Target.Path)
	require.Equal(t, "capabilities[0]", start[2].Origins[0].Target.Path)
	require.Equal(t, "capabilities[3]", start[3].Origins[0].Target.Path)
	require.Equal(t, install[0].EntryID, start[2].EntryID, "one entry retains its identity across stages")
	require.NotEqual(t, start[2].EntryID, start[3].EntryID, "identical lifecycle entries remain independent")
	var lowered Lifecycle
	require.NoError(t, DecodeCapabilityConfig(start[2].Capability, &lowered))
	require.Equal(t, commands, lowered.Startup, "repeated commands and hook-list order are preserved")
	for i, record := range result.Selections[0].Selection.Selected {
		require.Equal(t, fmt.Sprintf("capabilities[%d]", i), record.Path)
	}
	require.Nil(t, input.Descriptor.Capabilities[0].Order, "planning must not assign defaults into caller inputs")
}

func TestRuntimeEntryActionsHaveDefinedStageOrder(t *testing.T) {
	c := runtimeLifecycle(t, Lifecycle{Install: []InstallHook{{Command: CommandLine{"install"}}}, Files: []File{{Path: "/config", Content: "value"}}, Startup: []StartupHook{{Command: CommandLine{"start"}}}})
	result := runtimeCompose(t, runtimeContribution("entry", c))
	require.Equal(t, ActionInstall, runtimeStage(t, result.Plan, StageInstall)[0].Action)
	start := runtimeStage(t, result.Plan, StageStart)
	require.Len(t, start, 2)
	require.Equal(t, ActionFiles, start[0].Action)
	require.Equal(t, ActionStartup, start[1].Action)
	require.Equal(t, start[0].EntryID, start[1].EntryID)
}

func TestRuntimeStableTiesAcrossTypesAndContributionOccurrences(t *testing.T) {
	const thirdParty = "com.example/prepare@2"
	custom := Capability{Type: thirdParty}
	lifecycle := runtimeOrder(runtimeLifecycle(t, Lifecycle{Startup: []StartupHook{{Command: CommandLine{"echo", "ready"}}}}), 77)
	input := []Contribution{runtimeContribution("same-reference", lifecycle, custom), runtimeContribution("same-reference", lifecycle)}
	opts := RuntimeOptions{Select: Supported(CapabilityLifecycle, thirdParty), Contracts: map[string]ExecutionContract{thirdParty: fixedExecutionContract(77, PlannedAction{StageStart, "ready"})}}
	result, err := ComposeRuntime(t.Context(), input, opts)
	require.NoError(t, err)
	start := runtimeStage(t, result.Plan, StageStart)
	require.Len(t, start, 3)
	require.Equal(t, []string{CapabilityLifecycle, thirdParty, CapabilityLifecycle}, []string{start[0].Capability.Type, start[1].Capability.Type, start[2].Capability.Type})
	require.Equal(t, 0, start[0].Origins[0].Contribution)
	require.Equal(t, 1, start[2].Origins[0].Contribution)
	require.NotEqual(t, start[0].EntryID, start[2].EntryID)
	replanned, err := ComposeRuntime(t.Context(), input, opts)
	require.NoError(t, err)
	require.Equal(t, result.Plan, replanned.Plan, "pinned inputs produce the same plan across boot or host restart")
	beforeID := start[0].EntryID
	input[0].Descriptor.Capabilities[0].Order = new(int32(-20))
	input[0].Descriptor.Capabilities[0].Name = "renamed"
	input[0].Descriptor.Capabilities[0].Source = &CapabilitySource{Kit: "untrusted", Path: "invented"}
	replanned, err = ComposeRuntime(t.Context(), input, opts)
	require.NoError(t, err)
	require.Equal(t, beforeID, runtimeStage(t, replanned.Plan, StageStart)[0].EntryID, "order, display names, and publisher attribution do not define identity")
}

func TestRuntimeSelectionOriginsAndSkippedGroups(t *testing.T) {
	const unavailable = "com.example/missing@1"
	file := runtimeLifecycle(t, Lifecycle{Files: []File{{Path: "/skipped", Content: "skipped"}}})
	file.Source = &CapabilitySource{Kit: "publisher", Path: "original-file"}
	input := runtimeContribution("consumption", Capability{Group: &CapabilityGroup{Optional: true, Capabilities: []Capability{file, {Type: unavailable}}}}, runtimeOrder(groupHook("first"), 2000), runtimeOrder(groupHook("second"), 1000))
	result := runtimeCompose(t, input)
	require.Len(t, result.Selections, 1)
	selection := result.Selections[0].Selection
	require.Len(t, selection.Skipped, 1)
	require.Equal(t, []string{"capabilities[0].group.capabilities[1]"}, selection.Skipped[0].Rejected)
	require.Equal(t, []string{"capabilities[0].group.capabilities[0]", "capabilities[0].group.capabilities[1]"}, selection.Skipped[0].Members)
	require.Len(t, selection.Selected, 2)
	require.Equal(t, "capabilities[1]", selection.Selected[0].Path)
	require.Equal(t, "capabilities[2]", selection.Selected[1].Path)
	steps := runtimeStage(t, result.Plan, StageStart)
	require.Len(t, steps, 2)
	require.Equal(t, "capabilities[2]", steps[0].Origins[0].Target.Path)
	require.Equal(t, "capabilities[1]", steps[1].Origins[0].Target.Path)

	input.Descriptor.Capabilities = input.Descriptor.Capabilities[:1]
	input.Descriptor.Capabilities[0].Group.Capabilities = []Capability{file}
	result = runtimeCompose(t, input)
	origin := runtimeStage(t, result.Plan, StageStart)[0].Origins[0]
	require.Equal(t, 0, origin.Contribution)
	require.Equal(t, SelectionTarget{Reference: "consumption", Path: "capabilities[0].group.capabilities[0]"}, origin.Target)
	require.Equal(t, file.Source, origin.Source)
	require.NotSame(t, file.Source, origin.Source)
}

func TestRuntimeRetainsSelectionsOnPlanningAndSelectionFailure(t *testing.T) {
	opts := RuntimeOptions{Select: Supported(KnownCapabilities()...)}
	input := []Contribution{runtimeContribution("accepted", groupHook("ready")), runtimeContribution("refused", Capability{Type: "com.example/missing@1"})}
	detail := runtimeFailure(t, input, opts, "required capability selection rejected")
	require.Len(t, detail.Selections, 2)
	require.Len(t, detail.Selections[0].Selection.Selected, 1)
	require.Len(t, detail.Selections[1].Selection.Skipped, 1)

	opts.Select = func(context.Context, SelectionTarget, Descriptor, Capability) CapabilityDecision {
		return CapabilityDecision{Accepted: true}
	}
	input[1].Descriptor.Capabilities[0].Optional = true
	detail = runtimeFailure(t, input, opts, "has no execution contract")
	require.Len(t, detail.Selections[1].Selection.Selected, 1, "missing metadata cannot turn a selected optional request into a skip")
	require.Empty(t, detail.Selections[1].Selection.Skipped)

	input = []Contribution{runtimeContribution("first", runtimeLifecycle(t, Lifecycle{Files: []File{{Path: "/same"}}})), runtimeContribution("second", runtimeLifecycle(t, Lifecycle{Files: []File{{Path: "/same"}}}))}
	detail = runtimeFailure(t, input, opts, "written by two contributions")
	require.Len(t, detail.Selections, 2)
}

func TestRuntimeContractRegistrationsAreExactAndValidated(t *testing.T) {
	const typ = "com.example/custom@2"
	input := []Contribution{runtimeContribution("custom", Capability{Type: typ})}
	selector := Supported(typ)
	for _, tc := range []struct {
		name     string
		key      string
		contract ExecutionContract
		want     string
	}{
		{"builtin override", CapabilityLifecycle, fixedExecutionContract(1), "cannot replace built-in"},
		{"unversioned", "com.example/custom", fixedExecutionContract(1), "not an exact capability type"},
		{"implicit zero actions", typ, ExecutionContract{}, "must explicitly define Actions"},
		{"unknown stage", typ, fixedExecutionContract(1, PlannedAction{"after-start", "run"}), "invalid or duplicate action"},
		{"blank action", typ, fixedExecutionContract(1, PlannedAction{StageStart, " "}), "invalid or duplicate action"},
		{"duplicate action identity", typ, fixedExecutionContract(1, PlannedAction{StageStart, "run"}, PlannedAction{StageStart, "run"}), "invalid or duplicate action"},
		{"lower failure", typ, ExecutionContract{Actions: func(Capability) ([]PlannedAction, error) { return nil, errors.New("cannot lower") }}, "cannot lower"},
		{"prerequisite failure", typ, ExecutionContract{Actions: fixedExecutionContract(1).Actions, ValidatePlan: func(ExecutionPlan) error { return errors.New("missing service") }}, "missing service"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtimeFailure(t, input, RuntimeOptions{Select: selector, Contracts: map[string]ExecutionContract{tc.key: tc.contract}}, tc.want)
		})
	}
	validated := false
	opts := RuntimeOptions{Select: selector, Contracts: map[string]ExecutionContract{typ: {
		DefaultOrder: -50,
		Actions:      fixedExecutionContract(0, PlannedAction{StageSetup, "ready"}, PlannedAction{StageStart, "ready"}).Actions,
		ValidatePlan: func(plan ExecutionPlan) error {
			require.Len(t, plan.Stages, 4)
			require.Len(t, runtimeStage(t, plan, StageSetup), 1)
			require.Len(t, runtimeStage(t, plan, StageStart), 1)
			validated = true
			return nil
		},
	}}}
	result, err := ComposeRuntime(t.Context(), input, opts)
	require.NoError(t, err)
	require.True(t, validated)
	require.Equal(t, int32(-50), runtimeStage(t, result.Plan, StageSetup)[0].Order)
	opts.Contracts[typ] = fixedExecutionContract(0)
	result, err = ComposeRuntime(t.Context(), input, opts)
	require.NoError(t, err)
	require.Len(t, result.Descriptor.Capabilities, 1, "declarative contracts retain configuration")
	for _, stage := range result.Plan.Stages {
		require.Empty(t, stage.Steps)
	}
}

func TestRuntimeRejectsContradictoryPrerequisites(t *testing.T) {
	startup := runtimeOrder(groupHook("use engine"), 400)
	install := runtimeOrder(runtimeLifecycle(t, Lifecycle{Install: []InstallHook{{Command: CommandLine{"use-access"}}}}), 0)
	credential := runtimeConfig(t, CapabilityCredential, Credential{Service: "api", Phase: Phases{"install"}, APIKey: &APIKey{Name: "TOKEN"}})
	ssh := runtimeConfig(t, CapabilitySSHAgent, SSHAgent{Phase: Phases{"runtime"}})
	for _, tc := range []struct {
		name  string
		entry Capability
		hook  Capability
		want  string
	}{
		{"Docker", Capability{Type: CapabilityDockerd}, startup, "Docker readiness before startup"},
		{"install credential", credential, install, "must establish"},
		{"runtime SSH", runtimeOrder(ssh, 2000), groupHook("use ssh"), "must establish"},
		{"network policy", runtimeConfig(t, CapabilityNetworkPolicy, PhasedNetwork{}), install, "must establish"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtimeFailure(t, []Contribution{runtimeContribution("contradiction", tc.hook, tc.entry)}, RuntimeOptions{Select: Supported(KnownCapabilities()...)}, tc.want)
		})
	}
	manual := runtimeConfig(t, CapabilityDockerd, Dockerd{AutoStart: new(false)})
	result := runtimeCompose(t, runtimeContribution("manual", startup, manual))
	start := runtimeStage(t, result.Plan, StageStart)
	require.Len(t, start, 1)
	require.Equal(t, ActionStartup, start[0].Action, "manual engine startup has no runtime readiness action")
	file := runtimeOrder(runtimeLifecycle(t, Lifecycle{Files: []File{{Path: "/config", Content: "ready"}}}), math.MinInt32)
	result = runtimeCompose(t, runtimeContribution("file-before-access", file, credential))
	require.Equal(t, ActionFiles, runtimeStage(t, result.Plan, StageStart)[0].Action)
}

func TestRuntimeDependencyHooksRejectReversedOrderIncludingTransitiveEdges(t *testing.T) {
	provider := runtimeContribution("provider", runtimeOrder(groupHook("provide"), 2000))
	provider.Descriptor.Provides = []string{"service@1"}
	middle := runtimeContribution("middle")
	middle.Descriptor.Provides, middle.Descriptor.Requires = []string{"middle@1"}, []string{"service >= 1"}
	requirer := runtimeContribution("requirer", groupHook("require"))
	requirer.Descriptor.Requires = []string{"middle"}
	runtimeFailure(t, []Contribution{provider, middle, requirer}, RuntimeOptions{Select: Supported(CapabilityLifecycle)}, "must precede dependent hooks")
	provider.Descriptor.Capabilities[0].Order = nil
	result := runtimeCompose(t, provider, middle, requirer)
	steps := runtimeStage(t, result.Plan, StageStart)
	require.Equal(t, "provider", steps[0].Origins[0].Target.Reference)
	require.Equal(t, "requirer", steps[1].Origins[0].Target.Reference)
	requirer.Descriptor.Requires = nil
	requirer.Descriptor.Integrates = []string{"service >= 1"}
	provider.Descriptor.Capabilities[0].Order = new(int32(2000))
	runtimeFailure(t, []Contribution{provider, requirer}, RuntimeOptions{Select: Supported(CapabilityLifecycle)}, "must precede dependent hooks")
	requirer.Descriptor.Integrates = []string{"absent"}
	runtimeCompose(t, provider, requirer)
}

func TestRuntimeBundledSkillWaitsForEveryDiscoveryMount(t *testing.T) {
	mount := Capability{Type: CapabilityAgentSkills, Config: map[string]any{"path": "/home/agent/.agents/skills"}}
	bundle := Capability{Type: CapabilityAgentSkill, Config: map[string]any{"path": "/usr/share/skills/review"}}
	opts := RuntimeOptions{Select: Supported(CapabilityAgentSkill, CapabilityAgentSkills)}
	for _, order := range []int32{-1, 100} {
		t.Run(strconv.FormatInt(int64(order), 10), func(t *testing.T) {
			runtimeFailure(t, []Contribution{runtimeContribution("skills", runtimeOrder(bundle, order), mount)}, opts, "must mount discovery paths before bundled skills")
		})
	}
	result := runtimeCompose(t, runtimeContribution("skills", bundle, mount))
	setup := runtimeStage(t, result.Plan, StageSetup)
	require.Len(t, setup, 2)
	require.Equal(t, CapabilityAgentSkills, setup[0].Capability.Type)
	require.Equal(t, CapabilityAgentSkill, setup[1].Capability.Type)
	runtimeFailure(t, []Contribution{runtimeContribution("skills", mount, bundle, runtimeOrder(Capability{Type: CapabilityAgentSkills, Config: map[string]any{"path": "/other/discovery"}}, 400))}, opts, "must mount discovery paths before bundled skills")
}

func TestRuntimeMergedRequestsRetainAllOriginsAndPhases(t *testing.T) {
	volume := runtimeConfig(t, CapabilityVolume, Volume{Path: "/cache"})
	first := runtimeContribution("first", volume, runtimeConfig(t, CapabilitySSHAgent, SSHAgent{Phase: Phases{"install", "runtime"}}))
	second := runtimeContribution("second", runtimeOrder(volume, 100), runtimeConfig(t, CapabilitySSHAgent, SSHAgent{Phase: Phases{"runtime"}}))
	result := runtimeCompose(t, first, second)
	setup := runtimeStage(t, result.Plan, StageSetup)
	require.Len(t, setup, 1)
	require.Len(t, setup[0].Origins, 2)
	require.Equal(t, "first", setup[0].Origins[0].Target.Reference)
	require.Equal(t, "second", setup[0].Origins[1].Target.Reference)
	install := runtimeStage(t, result.Plan, StageInstall)
	require.Len(t, install, 1)
	require.Len(t, install[0].Origins, 1)
	start := runtimeStage(t, result.Plan, StageStart)
	require.Len(t, start, 1)
	require.Len(t, start[0].Origins, 2)
	require.NotEqual(t, install[0].EntryID, start[0].EntryID, "phase-split effective requests have distinct keys")
	require.Nil(t, setup[0].Capability.Source, "effective Source cannot pretend a reconciled request has one publisher")
	second.Descriptor.Capabilities[0].Order = new(int32(101))
	detail := runtimeFailure(t, []Contribution{first, second}, RuntimeOptions{Select: Supported(KnownCapabilities()...)}, "order")
	require.Contains(t, detail.Error(), "first")
	require.Contains(t, detail.Error(), "second")
}

func TestRuntimeContextBodiesCarryOriginsToEveryProfile(t *testing.T) {
	profile := func(directory, content string) Capability {
		return runtimeConfig(t, CapabilityAgentContextV2, AgentContextV2{Directory: directory, Filename: "AGENTS.md", Content: content})
	}
	result := runtimeCompose(t, runtimeContribution("first", profile("/agent-a", "first")), runtimeContribution("second", profile("/agent-b", "second")))
	setup := runtimeStage(t, result.Plan, StageSetup)
	require.Len(t, setup, 2)
	for _, step := range setup {
		require.Len(t, step.Origins, 2)
		require.Equal(t, "first", step.Origins[0].Target.Reference)
		require.Equal(t, "second", step.Origins[1].Target.Reference)
	}
	require.NotEqual(t, setup[0].EntryID, setup[1].EntryID, "different profile destinations remain separate effective entries")
}

func TestRuntimeDirectPublishedAndNestedPlansPreserveExecution(t *testing.T) {
	const custom = "com.example/service@1"
	first := runtimeContribution("first", groupHook("first"), Capability{Type: custom})
	second := runtimeContribution("second", groupHook("second"))
	opts := RuntimeOptions{Select: Supported(CapabilityLifecycle, custom), Contracts: map[string]ExecutionContract{custom: fixedExecutionContract(1000, PlannedAction{StageStart, "ready"})}}
	compose := func(inputs ...Contribution) []string {
		t.Helper()
		result, err := ComposeRuntime(t.Context(), inputs, opts)
		require.NoError(t, err)
		var effects []string
		for _, step := range runtimeStage(t, result.Plan, StageStart) {
			if step.Capability.Type == CapabilityLifecycle {
				var l Lifecycle
				require.NoError(t, DecodeCapabilityConfig(step.Capability, &l))
				effects = append(effects, l.Startup[0].Command[len(l.Startup[0].Command)-1])
			} else {
				effects = append(effects, step.Action)
			}
		}
		return effects
	}
	want := compose(first, second)
	require.Equal(t, []string{"first", "ready", "second"}, want)
	published, err := Merge([]Contribution{first, second}, MergeOptions{})
	require.NoError(t, err)
	require.Equal(t, want, compose(Contribution{Reference: "published", Descriptor: published.Descriptor}))
	nested, err := Merge([]Contribution{{Reference: "nested", Descriptor: published.Descriptor}}, MergeOptions{})
	require.NoError(t, err)
	require.Equal(t, want, compose(Contribution{Reference: "nested-published", Descriptor: nested.Descriptor}))
}

func TestRuntimeEffectiveValidationAndUnexpandedInputs(t *testing.T) {
	opts := RuntimeOptions{Select: Supported(KnownCapabilities()...)}
	credential := runtimeConfig(t, CapabilityCredential, Credential{Service: "api", Phase: Phases{"runtime"}, APIKey: &APIKey{ProxyManaged: true, Inject: []Inject{{Domain: "api.example.com", Header: "Authorization"}}}})
	detail := runtimeFailure(t, []Contribution{runtimeContribution("effective", credential)}, opts, "allow")
	require.Len(t, detail.Selections[0].Selection.Selected, 1, "effective cross-entry checks run after successful selection")
	unexpanded := groupHook("echo ${{ kit.env.HOME }}")
	runtimeFailure(t, []Contribution{runtimeContribution("unexpanded", unexpanded)}, opts, "expand the final container environment")
	unexpanded = groupHook("echo ${{ kit.args.message }}")
	runtimeFailure(t, []Contribution{runtimeContribution("unexpanded", unexpanded)}, opts, "unresolved arguments")
}

func TestRuntimeDependencyCyclesAndPublicationOrderingDiagnostics(t *testing.T) {
	provider := runtimeContribution("provider", Capability{Group: &CapabilityGroup{Capabilities: []Capability{runtimeOrder(groupHook("provide"), 2000)}}})
	provider.Descriptor.Provides = []string{"service@1"}
	requirer := runtimeContribution("requirer", groupHook("use"))
	requirer.Descriptor.Provides, requirer.Descriptor.Requires = []string{"consumer@1"}, []string{"service"}
	published, err := Merge([]Contribution{provider, requirer}, MergeOptions{})
	require.Nil(t, published)
	require.ErrorContains(t, err, "capabilities[0].group.capabilities[0]")
	require.ErrorContains(t, err, "must precede dependent hooks")
	provider.Descriptor.Requires = []string{"consumer"}
	runtimeFailure(t, []Contribution{provider, requirer}, RuntimeOptions{Select: Supported(CapabilityLifecycle)}, "dependency cycle")
}

func TestRuntimeRegistryCoversEveryKnownVersionAndIsIndependent(t *testing.T) {
	registry := BuiltinExecutionContracts()
	require.ElementsMatch(t, KnownCapabilities(), slices.Collect(maps.Keys(registry)))
	for typ, contract := range registry {
		require.NotNil(t, contract.Actions, typ)
	}
	registry[CapabilityVolume] = fixedExecutionContract(-100)
	require.Equal(t, int32(100), BuiltinExecutionContracts()[CapabilityVolume].DefaultOrder)
	contract := BuiltinExecutionContracts()[CapabilityVolume]
	first, err := contract.Actions(Capability{Type: CapabilityVolume})
	require.NoError(t, err)
	first[0].Action = "changed"
	second, err := contract.Actions(Capability{Type: CapabilityVolume})
	require.NoError(t, err)
	require.Equal(t, ActionMount, second[0].Action)
}

func TestRuntimeDefaultsDocumentationMatchesContracts(t *testing.T) {
	raw, err := os.ReadFile("../docs/spec/SPEC-v3.md")
	require.NoError(t, err)
	start := strings.Index(string(raw), "#### Execution defaults")
	require.NotEqual(t, -1, start)
	rows := map[string][]string{}
	section := strings.SplitN(string(raw)[start:], "\n###", 2)[0]
	for _, line := range strings.Split(section, "\n") {
		cells := strings.Split(line, "|")
		if len(cells) != 5 {
			continue
		}
		typ := strings.Trim(strings.TrimSpace(cells[1]), "`")
		if needType.MatchString(typ) {
			rows[typ] = []string{strings.TrimSpace(cells[2]), strings.TrimSpace(cells[3])}
		}
	}
	registry := BuiltinExecutionContracts()
	require.Len(t, rows, len(registry))
	for typ, contract := range registry {
		row := rows[typ]
		require.Len(t, row, 2, "%s is missing from the reference table", typ)
		require.Equal(t, strconv.FormatInt(int64(contract.DefaultOrder), 10), row[0], typ)
		c := Capability{Type: typ}
		switch typ {
		case CapabilityCredential:
			c = runtimeConfig(t, typ, Credential{Phase: Phases{"install", "runtime"}})
		case CapabilitySSHAgent:
			c = runtimeConfig(t, typ, SSHAgent{Phase: Phases{"install", "runtime"}})
		case CapabilityLifecycle:
			c = runtimeLifecycle(t, Lifecycle{Install: []InstallHook{{Command: CommandLine{"install"}}}, Files: []File{{Path: "/file"}}, Startup: []StartupHook{{Command: CommandLine{"start"}}}})
		}
		actions, err := contract.Actions(c)
		require.NoError(t, err)
		var names []string
		for _, action := range actions {
			names = append(names, string(action.Stage)+":"+action.Action)
		}
		want := strings.Join(names, ", ")
		if len(names) == 0 {
			want = "-"
		}
		require.Equal(t, want, row[1], typ)
	}
}

func TestRuntimeCancellationAndRequiredInputs(t *testing.T) {
	runtimeFailure(t, nil, RuntimeOptions{}, "no selector")
	runtimeFailure(t, nil, RuntimeOptions{Select: Supported()}, "no contributions")
	runtimeFailure(t, []Contribution{{Reference: "nil"}}, RuntimeOptions{Select: Supported()}, "no descriptor")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := ComposeRuntime(ctx, nil, RuntimeOptions{Select: Supported()})
	require.Nil(t, result)
	require.ErrorIs(t, err, context.Canceled)
	ctx, cancel = context.WithCancel(t.Context())
	result, err = ComposeRuntime(ctx, []Contribution{runtimeContribution("cancel", groupHook("hook"))}, RuntimeOptions{Select: func(context.Context, SelectionTarget, Descriptor, Capability) CapabilityDecision {
		cancel()
		return CapabilityDecision{Accepted: true}
	}})
	require.Nil(t, result)
	require.ErrorIs(t, err, context.Canceled)
}
