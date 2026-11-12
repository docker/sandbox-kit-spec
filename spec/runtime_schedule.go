package spec

import "slices"

// schedule projects the one validated entry order into fixed phase/step walks.
// Entries without an effect at a boundary remain in the graph but emit no action.
// Sorting never changes a phase, and repetition is carried on each action so a
// runner can skip completed installation without skipping later boot work.
func (g *runtimeGraph) schedule(order, parents []int, fragments []runtimeFragment, workspace bool) ExecutionPlan {
	plan := ExecutionPlan{}
	origins := map[int][]ExecutionOrigin{}
	selected := map[int]bool{}
	for i, node := range g.nodes {
		root := runtimeRoot(parents, i)
		origins[root] = append(origins[root], node.origin)
		selected[root] = selected[root] || node.selected
	}
	byEntry := map[int][]runtimeFragment{}
	for _, fragment := range fragments {
		byEntry[fragment.entry] = append(byEntry[fragment.entry], fragment)
	}
	for _, index := range order {
		plan.Entries = append(plan.Entries, ExecutionEntry{ID: g.identity(index), Origins: origins[index], Selected: selected[index]})
	}
	endings := map[ExecutionPhase][]ExecutionAction{}
	for _, phase := range []ExecutionPhase{PhasePrepare, PhaseInstall, PhaseStart, PhaseRunning} {
		phasePlan := PhasePlan{Phase: phase}
		steps := []ExecutionStep{StepAttach, StepPlace, StepExecute}
		if phase == PhaseRunning {
			steps = []ExecutionStep{StepExecute}
		}
		for _, step := range steps {
			stepPlan := ExecutionStepPlan{Step: step}
			for _, entry := range order {
				for _, fragment := range byEntry[entry] {
					for _, effect := range fragment.effects {
						if effect.Phase != phase || effect.Step != step {
							continue
						}
						// Initial workspace preparation may need install-only
						// access. Insert its gate after Attach/Place, immediately
						// before the first lifecycle install command. Authored
						// relationships cannot target or move this runtime action.
						if workspace && fragment.capability.Type == CapabilityLifecycle && phase == PhaseInstall && step == StepExecute {
							stepPlan.Actions = append(stepPlan.Actions, runtimeOwnedAction("workspace-ready", PhaseInstall, RepeatOnce))
							workspace = false
						}
						capability := fragment.capability
						action := ExecutionAction{EntryID: g.identity(entry), FragmentID: fragment.id, Phase: phase, Step: step,
							Action: effect.Action, Kind: effect.Kind, Capability: &capability, Repetition: RepeatEveryBoot}
						if phase == PhaseInstall {
							action.Repetition = RepeatOnce
						}
						for _, origin := range fragment.capability.executionOrigins {
							action.Origins = append(action.Origins, g.nodes[origin].origin)
						}
						stepPlan.Actions = append(stepPlan.Actions, action)
						if effect.End {
							action.End = true
							endings[phase] = append(endings[phase], action)
						}
					}
				}
			}
			// A sandbox with no install hooks still needs its initial workspace.
			if workspace && phase == PhaseInstall && step == StepExecute {
				stepPlan.Actions = append(stepPlan.Actions, runtimeOwnedAction("workspace-ready", PhaseInstall, RepeatOnce))
				workspace = false
			}
			if phase == PhaseRunning {
				action := runtimeOwnedAction("workload", PhaseRunning, RepeatEveryBoot)
				stepPlan.Actions = append(stepPlan.Actions, action)
				action.End = true
				endings[phase] = append(endings[phase], action)
			}
			phasePlan.Steps = append(phasePlan.Steps, stepPlan)
		}
		plan.Phases = append(plan.Phases, phasePlan)
	}
	// Ending actions were collected in forward execution order. Reversing
	// each phase therefore reverses its steps, entries, and effect lists too.
	// Install cleanup belongs before Start and must never be replayed by Stop.
	plan.InstallEnd = endings[PhaseInstall]
	slices.Reverse(plan.InstallEnd)
	for _, phase := range []ExecutionPhase{PhaseRunning, PhaseStart, PhasePrepare} {
		slices.Reverse(endings[phase])
		plan.Stop = append(plan.Stop, endings[phase]...)
	}
	return plan
}

func runtimeOwnedAction(name string, phase ExecutionPhase, repetition ExecutionRepetition) ExecutionAction {
	kind := EffectCommand
	if name == "workload" {
		kind = EffectProgram
	}
	return ExecutionAction{EntryID: "runtime/" + name, FragmentID: "runtime/" + name, Phase: phase,
		Step: StepExecute, Action: name, Kind: kind, Repetition: repetition}
}
