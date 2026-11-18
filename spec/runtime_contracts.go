package spec

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// runtimeContracts extends the library's exact-version contracts with trusted
// host registrations. Built-ins are authoritative, so replacement is an error
// even when no input happens to use the replaced type. Sorted keys keep errors
// deterministic when several registrations are invalid.
func runtimeContracts(additional map[string]ExecutionContract) (map[string]ExecutionContract, error) {
	contracts := builtinRuntimeContracts()
	for _, typ := range slices.Sorted(maps.Keys(additional)) {
		if _, builtin := contracts[typ]; builtin {
			return nil, fmt.Errorf("execution contract %s is builtin and cannot be replaced", typ)
		}
		target, err := parseOrderingTarget(typ)
		if err != nil || target.capabilityType == "" {
			return nil, fmt.Errorf("execution contract %q must name an exact capability type and version", typ)
		}
		contract := additional[typ]
		contract.Participation = slices.Clone(contract.Participation)
		seen := map[ExecutionBoundary]bool{}
		for _, boundary := range contract.Participation {
			if !runtimePhaseStep(boundary.Phase, boundary.Step) || seen[boundary] {
				return nil, fmt.Errorf("execution contract %s has invalid or duplicate participation %s/%s", typ, boundary.Phase, boundary.Step)
			}
			seen[boundary] = true
		}
		contracts[typ] = contract
	}
	return contracts, nil
}

// builtinRuntimeContracts is the executable counterpart of SPEC-v3 section 12's
// placement table. Effects describe independently handled work; config-only and
// session-only capabilities explicitly have no boot action. End schedules the
// contract's cleanup, not unconditional deletion of the resource or placed data.
func builtinRuntimeContracts() map[string]ExecutionContract {
	attach := func(phase ExecutionPhase, action string) PlannedEffect {
		return PlannedEffect{Phase: phase, Step: StepAttach, Action: action, Kind: EffectResource, End: true}
	}
	place := func(phase ExecutionPhase, action string) PlannedEffect {
		return PlannedEffect{Phase: phase, Step: StepPlace, Action: action, Kind: EffectContent}
	}
	fixed := func(effects ...PlannedEffect) ExecutionContract {
		participation := make([]ExecutionBoundary, 0, len(effects))
		for _, effect := range effects {
			boundary := ExecutionBoundary{Phase: effect.Phase, Step: effect.Step}
			if !slices.Contains(participation, boundary) {
				participation = append(participation, boundary)
			}
		}
		return ExecutionContract{Participation: participation, Effects: func(Capability) ([]PlannedEffect, error) {
			return slices.Clone(effects), nil
		}}
	}
	return map[string]ExecutionContract{
		CapabilityResources:       fixed(attach(PhasePrepare, "resources")),
		CapabilityPrivileged:      fixed(attach(PhasePrepare, "privilege")),
		CapabilityUSBDevice:       fixed(attach(PhasePrepare, "device")),
		CapabilityVolume:          fixed(attach(PhasePrepare, "volume")),
		CapabilityHostMount:       fixed(attach(PhasePrepare, "host-mount")),
		CapabilityAgentSkills:     fixed(attach(PhasePrepare, "skills-store")),
		CapabilityGitIdentity:     fixed(place(PhasePrepare, "git-identity")),
		CapabilityDockerAuth:      fixed(attach(PhasePrepare, "docker-auth-route"), place(PhasePrepare, "docker-auth-config")),
		CapabilityKitRegistry:     fixed(attach(PhasePrepare, "registry-route"), place(PhasePrepare, "registry-address")),
		CapabilityHostPort:        fixed(attach(PhasePrepare, "host-port")),
		CapabilityOpenURL:         fixed(attach(PhasePrepare, "opener-channel"), place(PhasePrepare, "opener-config")),
		CapabilityPort:            fixed(attach(PhaseStart, "port")),
		CapabilityNetworkPolicy:   fixed(attach(PhaseInstall, "network-policy"), attach(PhaseStart, "network-policy")),
		CapabilityNetworkPolicyV2: fixed(attach(PhaseInstall, "network-policy"), attach(PhaseStart, "network-policy")),
		CapabilityCredential:      {Participation: runtimeGrantParticipation(), Effects: runtimeGrantEffects},
		CapabilitySSHAgent:        {Participation: runtimeGrantParticipation(), Effects: runtimeGrantEffects},
		CapabilityAgentSkill:      fixed(place(PhaseStart, "skill")),
		CapabilityAgentContext:    fixed(place(PhaseStart, "agent-context")),
		CapabilityAgentContextV2:  fixed(place(PhaseStart, "agent-context")),
		CapabilitySbx:             fixed(place(PhasePrepare, "sandbox-environment")),
		CapabilityLifecycle:       {Participation: []ExecutionBoundary{{PhaseInstall, StepExecute}, {PhaseStart, StepPlace}, {PhaseStart, StepExecute}}, Effects: runtimeLifecycleEffects},
		CapabilityDockerd: {
			Participation: []ExecutionBoundary{{PhasePrepare, StepAttach}, {PhaseStart, StepExecute}},
			Effects:       runtimeDockerdEffects,
		},
		CapabilityAgentSessions:            {},
		CapabilityAgentInteractiveSessions: {},
		CapabilityACP:                      {},
		CapabilityLongRunning:              {},
		CapabilityAutoRestart:              {},
	}
}

// Grants participate in both phases even when a particular config grants only
// one. Optional guest material also keeps its Place position when absent.
func runtimeGrantParticipation() []ExecutionBoundary {
	return []ExecutionBoundary{{PhaseInstall, StepAttach}, {PhaseInstall, StepPlace}, {PhaseStart, StepAttach}, {PhaseStart, StepPlace}}
}

// runtimeGrantEffects separates access from material placed inside the sandbox.
// Inject-only credentials have access effects without a guest credential file
// or environment value. Install and runtime grants get separate action lifetimes
// so ending installation cannot retain its credentials through startup.
func runtimeGrantEffects(c Capability) ([]PlannedEffect, error) {
	phases, err := phasesOf(c)
	if err != nil {
		return nil, err
	}
	action := "credential"
	material := true
	if c.Type == CapabilitySSHAgent {
		action = "ssh-agent"
	} else {
		var credential Credential
		if err := DecodeCapabilityConfig(c, &credential); err != nil {
			return nil, err
		}
		material = credential.APIKey != nil && credential.APIKey.Name != "" || credential.OAuth != nil && credential.OAuth.CredentialFile != nil
	}
	var effects []PlannedEffect
	for _, value := range phases {
		phase := PhaseInstall
		if value == "runtime" {
			phase = PhaseStart
		}
		effects = append(effects, PlannedEffect{Phase: phase, Step: StepAttach, Action: action + "-access", Kind: EffectResource, End: true})
		if material {
			effects = append(effects, PlannedEffect{Phase: phase, Step: StepPlace, Action: action + "-material", Kind: EffectContent, End: true})
		}
	}
	return effects, nil
}

// runtimeLifecycleEffects preserves each declaration as separate actions. Files
// belong to Start Place, so they precede startup services but cannot supply an
// earlier install command. Every command has a cleanup candidate so Stop can
// end an in-flight foreground command too. Completed commands leave no active
// effect to end; earlier commands are never undone. The handler executes each
// list in its original order and retains any deliberate background processes.
func runtimeLifecycleEffects(c Capability) ([]PlannedEffect, error) {
	var lifecycle Lifecycle
	if err := DecodeCapabilityConfig(c, &lifecycle); err != nil {
		return nil, err
	}
	var effects []PlannedEffect
	if len(lifecycle.Install) > 0 {
		effects = append(effects, PlannedEffect{Phase: PhaseInstall, Step: StepExecute, Action: "install", Kind: EffectCommand, End: true})
	}
	if len(lifecycle.Files) > 0 {
		effects = append(effects, PlannedEffect{Phase: PhaseStart, Step: StepPlace, Action: "files", Kind: EffectContent})
	}
	if len(lifecycle.Startup) > 0 {
		startup := PlannedEffect{Phase: PhaseStart, Step: StepExecute, Action: "startup", Kind: EffectCommand, End: true}
		for _, hook := range lifecycle.Startup {
			if hook.Background {
				startup.Kind = EffectProgram
			}
		}
		effects = append(effects, startup)
	}
	return effects, nil
}

// runtimeDockerdEffects separates elevation/storage from engine execution.
// Manual startup still needs those resources, but emits no automatic engine
// action. Its entry retains its Start Execute position even when it does no work.
func runtimeDockerdEffects(c Capability) ([]PlannedEffect, error) {
	var dockerd Dockerd
	if err := DecodeCapabilityConfig(c, &dockerd); err != nil {
		return nil, err
	}
	effects := []PlannedEffect{{Phase: PhasePrepare, Step: StepAttach, Action: "engine-resources", Kind: EffectResource, End: true}}
	if dockerd.AutoStarts() {
		effects = append(effects, PlannedEffect{Phase: PhaseStart, Step: StepExecute, Action: "engine", Kind: EffectProgram, End: true})
	}
	return effects, nil
}

func validateRuntimeEffect(effect PlannedEffect) error {
	if !runtimePhaseStep(effect.Phase, effect.Step) {
		return fmt.Errorf("effect %q has invalid phase/step %q/%q", effect.Action, effect.Phase, effect.Step)
	}
	if !runtimeActionName(effect.Action) {
		return fmt.Errorf("effect action must be a nonempty literal name, got %q", effect.Action)
	}
	validKind := effect.Step == StepAttach && effect.Kind == EffectResource ||
		effect.Step == StepPlace && effect.Kind == EffectContent ||
		effect.Step == StepExecute && (effect.Kind == EffectProgram || effect.Kind == EffectCommand)
	if !validKind {
		return fmt.Errorf("effect %q kind %q does not belong in step %q", effect.Action, effect.Kind, effect.Step)
	}
	return nil
}

func runtimePhaseStep(phase ExecutionPhase, step ExecutionStep) bool {
	switch phase {
	case PhasePrepare, PhaseInstall, PhaseStart:
		return step == StepAttach || step == StepPlace || step == StepExecute
	case PhaseRunning:
		return step == StepExecute
	default:
		return false
	}
}

func runtimeActionName(action string) bool {
	return action != "" && strings.TrimSpace(action) == action && !strings.ContainsAny(action, "\x00\r\n")
}
