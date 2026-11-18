package spec

import "slices"

// schedule emits the independently validated order of each phase/step. Entries
// retain declaration order for inspection; they are not a global execution list.
// Repetition belongs to phase applicability, never to sorting or shutdown state.
func (g *runtimeGraph) schedule(orders map[ExecutionBoundary][]int, fragments []runtimeFragment, workspace bool) ExecutionPlan {
	plan := ExecutionPlan{}
	for _, fragment := range fragments {
		entry := ExecutionEntry{ID: g.fragmentIdentity(fragment), Selected: true}
		for _, origin := range fragment.capability.executionOrigins {
			entry.Origins = append(entry.Origins, g.nodes[origin].origin)
		}
		plan.Entries = append(plan.Entries, entry)
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
			for _, index := range orders[ExecutionBoundary{phase, step}] {
				fragment := fragments[index]
				for _, effect := range fragment.effects {
					if effect.Phase != phase || effect.Step != step {
						continue
					}
					capability := fragment.capability
					action := ExecutionAction{EntryID: g.fragmentIdentity(fragment), FragmentID: fragment.id, Phase: phase, Step: step,
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
			// Workspace preparation is the runtime's own attachment, with its
			// own access. It finishes in Prepare before any Kit install grant
			// exists, even when there are no install commands in the plan.
			if workspace && phase == PhasePrepare && step == StepAttach {
				action := runtimeOwnedAction("workspace-ready", PhasePrepare, RepeatEveryBoot)
				stepPlan.Actions = append(stepPlan.Actions, action)
				// Ending the attachment withdraws runtime access and detaches the
				// workspace; it does not delete the repository or its contents.
				action.End = true
				endings[phase] = append(endings[phase], action)
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
	// Ending actions were collected in each boundary's forward order. Reversing
	// each phase reverses its steps, entries, and effect lists too. Stop includes
	// Install for an interrupted boot. The runner filters effects that never
	// began or already ended, including grants withdrawn by InstallEnd.
	plan.InstallEnd = slices.Clone(endings[PhaseInstall])
	slices.Reverse(plan.InstallEnd)
	for _, phase := range []ExecutionPhase{PhaseRunning, PhaseStart, PhaseInstall, PhasePrepare} {
		slices.Reverse(endings[phase])
		plan.Stop = append(plan.Stop, endings[phase]...)
	}
	return plan
}

// Reconciliation can derive distinct effective requests from the same first
// declaration. Keep their identities separate without inventing author IDs.
func (g *runtimeGraph) fragmentIdentity(fragment runtimeFragment) string {
	return g.identity(fragment.entry) + "/" + fragment.id
}

func runtimeOwnedAction(name string, phase ExecutionPhase, repetition ExecutionRepetition) ExecutionAction {
	kind, step := EffectCommand, StepExecute
	if name == "workload" {
		kind = EffectProgram
	}
	if name == "workspace-ready" {
		kind, step = EffectResource, StepAttach
	}
	return ExecutionAction{EntryID: "runtime/" + name, FragmentID: "runtime/" + name, Phase: phase,
		Step: step, Action: name, Kind: kind, Repetition: repetition}
}
