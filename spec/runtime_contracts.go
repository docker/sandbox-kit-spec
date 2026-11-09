package spec

import (
	"fmt"
	"slices"
)

// Built-in action names have the readiness meanings specified in §7.1.2.
// A repeated name in different stages is a distinct action. In particular,
// provide and policy apply only to the phase represented by their stage.
const (
	ActionConfigure   = "configure"
	ActionMount       = "mount"
	ActionBind        = "bind"
	ActionProvide     = "provide"
	ActionMaterialize = "materialize"
	ActionPrepare     = "prepare"
	ActionPolicy      = "policy"
	ActionFiles       = "files"
	ActionInstall     = "install"
	ActionStartup     = "startup"
	ActionStart       = "start"
)

// BuiltinExecutionContracts returns independent registration metadata for
// every exact capability version this library defines. Altering this map
// does not change future plans; RuntimeOptions.Contracts cannot override it.
func BuiltinExecutionContracts() map[string]ExecutionContract {
	configure := fixedExecutionContract(100, PlannedAction{StageConfigure, ActionConfigure})
	mount := fixedExecutionContract(100, PlannedAction{StageSetup, ActionMount})
	provide := fixedExecutionContract(200, PlannedAction{StageSetup, ActionProvide})
	materialize := fixedExecutionContract(300, PlannedAction{StageSetup, ActionMaterialize})
	skill := materialize
	skill.ValidatePlan = validateAgentSkillPlan
	policy := fixedExecutionContract(100, PlannedAction{StageInstall, ActionPolicy}, PlannedAction{StageStart, ActionPolicy})
	access := ExecutionContract{DefaultOrder: 200, Actions: phasedAccessActions}
	declarative := fixedExecutionContract(0)
	return map[string]ExecutionContract{
		CapabilityResources:                configure,
		CapabilityPrivileged:               configure,
		CapabilityUSBDevice:                configure,
		CapabilityPort:                     configure,
		CapabilityHostPort:                 fixedExecutionContract(100, PlannedAction{StageSetup, ActionBind}),
		CapabilityVolume:                   mount,
		CapabilityHostMount:                mount,
		CapabilityAgentSkills:              mount,
		CapabilityGitIdentity:              provide,
		CapabilityKitRegistry:              provide,
		CapabilityCredential:               access,
		CapabilitySSHAgent:                 access,
		CapabilityDockerAuth:               fixedExecutionContract(200, PlannedAction{StageInstall, ActionProvide}, PlannedAction{StageStart, ActionProvide}),
		CapabilityNetworkPolicy:            policy,
		CapabilityNetworkPolicyV2:          policy,
		CapabilityAgentContext:             materialize,
		CapabilityAgentContextV2:           materialize,
		CapabilityAgentSkill:               skill,
		CapabilityDockerd:                  {DefaultOrder: 500, Actions: dockerdActions, ValidatePlan: validateDockerPlan},
		CapabilityLifecycle:                {DefaultOrder: 1000, Actions: lifecycleActions, ValidatePlan: validateLifecycleAccess},
		CapabilityACP:                      declarative,
		CapabilityAgentSessions:            declarative,
		CapabilityAgentInteractiveSessions: declarative,
		CapabilitySbx:                      declarative,
		CapabilityLongRunning:              declarative,
	}
}

func fixedExecutionContract(order int32, actions ...PlannedAction) ExecutionContract {
	return ExecutionContract{DefaultOrder: order, Actions: func(Capability) ([]PlannedAction, error) {
		return slices.Clone(actions), nil
	}}
}

func phasedAccessActions(c Capability) ([]PlannedAction, error) {
	phases, err := phasesOf(c)
	if err != nil {
		return nil, err
	}
	var actions []PlannedAction
	if slices.Contains(phases, "install") {
		actions = append(actions, PlannedAction{StageInstall, ActionProvide})
	}
	if slices.Contains(phases, "runtime") {
		actions = append(actions, PlannedAction{StageStart, ActionProvide})
	}
	return actions, nil
}

func dockerdActions(c Capability) ([]PlannedAction, error) {
	var d Dockerd
	if err := DecodeCapabilityConfig(c, &d); err != nil {
		return nil, err
	}
	actions := []PlannedAction{{StageConfigure, ActionConfigure}, {StageSetup, ActionPrepare}}
	if d.AutoStarts() {
		actions = append(actions, PlannedAction{StageStart, ActionStart})
	}
	return actions, nil
}

func lifecycleActions(c Capability) ([]PlannedAction, error) {
	var l Lifecycle
	if err := DecodeCapabilityConfig(c, &l); err != nil {
		return nil, err
	}
	var actions []PlannedAction
	if len(l.Install) > 0 {
		actions = append(actions, PlannedAction{StageInstall, ActionInstall})
	}
	if len(l.Files) > 0 {
		actions = append(actions, PlannedAction{StageStart, ActionFiles})
	}
	if len(l.Startup) > 0 {
		actions = append(actions, PlannedAction{StageStart, ActionStartup})
	}
	return actions, nil
}

func validateLifecycleAccess(plan ExecutionPlan) error {
	for _, stage := range plan.Stages {
		var firstHook *ExecutionStep
		for i := range stage.Steps {
			step := &stage.Steps[i]
			if step.Capability.Type == CapabilityLifecycle && (step.Action == ActionInstall || step.Action == ActionStartup) {
				if firstHook == nil {
					firstHook = step
				}
				continue
			}
			switch step.Capability.Type {
			case CapabilityNetworkPolicy, CapabilityNetworkPolicyV2, CapabilityCredential, CapabilitySSHAgent, CapabilityDockerAuth:
				if firstHook != nil {
					return fmt.Errorf("%s order %d must establish %s before hooks at %s order %d in stage %s", executionLocations(step.Origins), step.Order, step.Capability.Type, executionLocations(firstHook.Origins), firstHook.Order, stage.Stage)
				}
			}
		}
	}
	return nil
}

func validateDockerPlan(plan ExecutionPlan) error {
	for _, stage := range plan.Stages {
		if stage.Stage != StageStart {
			continue
		}
		var firstHook *ExecutionStep
		for i := range stage.Steps {
			step := &stage.Steps[i]
			if step.Capability.Type == CapabilityLifecycle && step.Action == ActionStartup && firstHook == nil {
				firstHook = step
			}
			if step.Capability.Type == CapabilityDockerd && step.Action == ActionStart && firstHook != nil {
				return fmt.Errorf("%s order %d must await Docker readiness before startup hooks at %s order %d", executionLocations(step.Origins), step.Order, executionLocations(firstHook.Origins), firstHook.Order)
			}
		}
	}
	return nil
}

func validateAgentSkillPlan(plan ExecutionPlan) error {
	for _, stage := range plan.Stages {
		if stage.Stage != StageSetup {
			continue
		}
		var firstSkill *ExecutionStep
		for i := range stage.Steps {
			step := &stage.Steps[i]
			if step.Capability.Type == CapabilityAgentSkill && firstSkill == nil {
				firstSkill = step
			}
			if step.Capability.Type == CapabilityAgentSkills && firstSkill != nil {
				return fmt.Errorf("%s order %d must mount discovery paths before bundled skills at %s order %d", executionLocations(step.Origins), step.Order, executionLocations(firstSkill.Origins), firstSkill.Order)
			}
		}
	}
	return nil
}
