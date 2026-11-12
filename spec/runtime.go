package spec

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
)

// ExecutionPhase is a fixed boot boundary. Install completes once per sandbox.
type ExecutionPhase string

const (
	PhasePrepare ExecutionPhase = "prepare"
	PhaseInstall ExecutionPhase = "install"
	PhaseStart   ExecutionPhase = "start"
	PhaseRunning ExecutionPhase = "running"
)

// ExecutionStep is a fixed boundary within a phase. Relationships order
// entries within a step; they cannot move work across these boundaries.
type ExecutionStep string

const (
	StepAttach  ExecutionStep = "attach"
	StepPlace   ExecutionStep = "place"
	StepExecute ExecutionStep = "execute"
)

// EffectKind distinguishes host resources, placed content, services and commands.
type EffectKind string

const (
	EffectResource EffectKind = "resource"
	EffectContent  EffectKind = "content"
	EffectProgram  EffectKind = "program"
	EffectCommand  EffectKind = "command"
)

// PlannedEffect names one part of a capability contract. The handler performs
// only Action, awaits its contract's completion condition, and aborts the phase
// on failure. End requires the matching cleanup action at the phase's end.
type PlannedEffect struct {
	Phase  ExecutionPhase `json:"phase"`
	Step   ExecutionStep  `json:"step"`
	Action string         `json:"action"`
	Kind   EffectKind     `json:"kind"`
	End    bool           `json:"end,omitempty"`
}

// OrderingDefault applies only when both entries have a matching effect in
// Phase and Step. Action and TargetAction narrow those effects when nonempty.
// Exactly one of BeforeType and AfterType names an exact capability version.
type OrderingDefault struct {
	Phase        ExecutionPhase `json:"phase"`
	Step         ExecutionStep  `json:"step"`
	Action       string         `json:"action,omitempty"`
	TargetAction string         `json:"targetAction,omitempty"`
	BeforeType   string         `json:"beforeType,omitempty"`
	AfterType    string         `json:"afterType,omitempty"`
}

// ExecutionContract is trusted runtime registration, never descriptor data.
// Effects must be pure. A nil Effects function explicitly declares no boot
// work, as session-only interfaces do. Builtin contracts cannot be replaced.
type ExecutionContract struct {
	Effects  func(Capability) ([]PlannedEffect, error)
	Defaults []OrderingDefault
}

// ExecutionRepetition tells a runner which actions to skip on later boots.
type ExecutionRepetition string

const (
	RepeatEveryBoot ExecutionRepetition = "every-boot"
	RepeatOnce      ExecutionRepetition = "once"
)

// ExecutionOrigin identifies an original declaration before selection and
// reconciliation. ID is authored; Path and Contribution are consumption identity.
// Publisher-provided source labels are deliberately not used as identity.
type ExecutionOrigin struct {
	Contribution int               `json:"contribution"`
	Reference    string            `json:"reference"`
	Path         string            `json:"path"`
	ID           string            `json:"id,omitempty"`
	Type         string            `json:"type"`
	Scope        uint32            `json:"scope"`
	Target       SelectionTarget   `json:"target"`
	Source       *CapabilitySource `json:"source,omitempty"`
}

// ExecutionEntry is one logical graph node. Reconciled requests share a node,
// while skipped declarations retain their own nodes and ordering relationships.
// ID is an internal consumption identity, never an author-addressable ID.
type ExecutionEntry struct {
	ID       string            `json:"id"`
	Origins  []ExecutionOrigin `json:"origins"`
	Selected bool              `json:"selected"`
}

// ExecutionAction applies or ends one effect of one effective request.
// FragmentID distinguishes effective requests shared by the same logical entry.
// Within a sandbox, (EntryID, FragmentID, Phase, Step, Action) identifies the
// effect. End distinguishes its application from its cleanup. Completion state
// must record those separately: applying an install grant does not complete the
// later action that withdraws it.
// Capability is nil only for runtime-owned workspace readiness and workload
// actions. Their IDs cannot be targeted by descriptor relationships.
type ExecutionAction struct {
	EntryID    string              `json:"entryId"`
	FragmentID string              `json:"fragmentId"`
	Phase      ExecutionPhase      `json:"phase"`
	Step       ExecutionStep       `json:"step"`
	Action     string              `json:"action"`
	Kind       EffectKind          `json:"kind"`
	Capability *Capability         `json:"capability,omitempty"`
	Origins    []ExecutionOrigin   `json:"origins,omitempty"`
	Repetition ExecutionRepetition `json:"repetition"`
	End        bool                `json:"end,omitempty"`
}

// ExecutionStepPlan lists actions in the global entry order for one step.
type ExecutionStepPlan struct {
	Step    ExecutionStep     `json:"step"`
	Actions []ExecutionAction `json:"actions"`
}

// PhasePlan completes every step before advancing to the next one.
type PhasePlan struct {
	Phase ExecutionPhase      `json:"phase"`
	Steps []ExecutionStepPlan `json:"steps"`
}

// ExecutionPlan is executable only after ComposeRuntime returns without error.
// InstallEnd runs immediately after Install and before Start, once per sandbox.
// Stop covers ordinary shutdown after a successful start. It ends Running, then
// Start and Prepare in reverse step and entry order, without repeating InstallEnd.
type ExecutionPlan struct {
	Entries    []ExecutionEntry  `json:"entries"`
	Phases     []PhasePlan       `json:"phases"`
	InstallEnd []ExecutionAction `json:"installEnd"`
	Stop       []ExecutionAction `json:"stop"`
}

// RuntimeOptions supplies the permission decision and trusted execution
// contracts. WorkspaceAssigned schedules the runtime's mandatory readiness gate
// before install hooks, after install attachments and settings are available.
type RuntimeOptions struct {
	Select            SelectCapability
	Contracts         map[string]ExecutionContract
	WorkspaceAssigned bool
}

// ContributionSelection retains decisions in original contribution order.
type ContributionSelection struct {
	Contribution int       `json:"contribution"`
	Reference    string    `json:"reference"`
	Selection    Selection `json:"selection"`
}

// RuntimeComposition keeps effective inspection data separate from executable
// order and original permission records. Descriptor is not an execution schedule.
type RuntimeComposition struct {
	Descriptor *Descriptor             `json:"descriptor"`
	Selections []ContributionSelection `json:"selections"`
	Plan       ExecutionPlan           `json:"plan"`
	Warnings   []string                `json:"warnings,omitempty"`
}

// RuntimeCompositionError retains available permission records without exposing
// a partial executable result. Unwrap preserves validation and cancellation errors.
type RuntimeCompositionError struct {
	Selections []ContributionSelection
	// Contribution identifies an input validation or selection failure.
	// Nil denotes a failure in the combined declarations or execution plan.
	Contribution *int
	Err          error
}

func (e *RuntimeCompositionError) Error() string { return "compose runtime: " + e.Err.Error() }
func (e *RuntimeCompositionError) Unwrap() error { return e.Err }

// ComposeRuntime selects, reconciles and orders expanded contributions without
// applying capability effects. Dependencies and final environment expansion are
// the caller's responsibility. Every error returns a nil composition.
func ComposeRuntime(ctx context.Context, contributions []Contribution, opts RuntimeOptions) (*RuntimeComposition, error) {
	var selections []ContributionSelection
	fail := func(err error) (*RuntimeComposition, error) {
		return nil, &RuntimeCompositionError{Selections: selections, Err: err}
	}
	failContribution := func(index int, err error) (*RuntimeComposition, error) {
		return nil, &RuntimeCompositionError{Selections: selections, Contribution: &index, Err: err}
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if opts.Select == nil {
		return fail(fmt.Errorf("no selector"))
	}
	contracts, err := runtimeContracts(opts.Contracts)
	if err != nil {
		return fail(err)
	}
	if len(contributions) == 0 {
		return fail(fmt.Errorf("no contributions"))
	}
	inputs := make([]Contribution, len(contributions))
	var warnings []string
	for i, contribution := range contributions {
		if contribution.Descriptor == nil {
			return failContribution(i, fmt.Errorf("contribution %d (%s) has no descriptor", i, contribution.Reference))
		}
		d, raw, err := cloneRuntimeDescriptor(contribution.Descriptor)
		if err != nil {
			return failContribution(i, err)
		}
		advisories, err := ValidateExpandedDeclarations(raw, d)
		if err != nil {
			return failContribution(i, fmt.Errorf("contribution %d (%s): %w", i, contribution.Reference, err))
		}
		warnings = append(warnings, advisories...)
		inputs[i] = Contribution{Reference: contribution.Reference, Descriptor: d}
	}
	// Capture declarations before selection so skipped entries can still bind
	// IDs and carry ordering edges without gaining any executable effects.
	graph, err := newRuntimeGraph(inputs)
	if err != nil {
		return fail(err)
	}
	written, advisories, err := graph.writtenEdges(nil)
	if err != nil {
		return fail(err)
	}
	warnings = append(warnings, advisories...)
	if _, err := graph.order(written, nil); err != nil {
		return fail(err)
	}

	selected := make([]Contribution, len(inputs))
	for i, contribution := range inputs {
		selection, err := selectRuntimeCapabilities(ctx, contribution, opts.Select)
		selections = append(selections, ContributionSelection{Contribution: i, Reference: contribution.Reference, Selection: selection})
		if err != nil {
			return failContribution(i, err)
		}
		for _, capability := range selection.Capabilities {
			for _, origin := range capability.executionOrigins {
				graph.nodes[origin].selected = true
			}
		}
		d := *contribution.Descriptor
		d.Capabilities = selection.Capabilities
		selected[i] = Contribution{Reference: contribution.Reference, Descriptor: &d}
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	effective, err := composeWithOrigins(selected)
	if err != nil {
		return fail(err)
	}
	// Validate the combined serialized size as well as effective semantics:
	// individually valid contributions can exceed the descriptor budget together.
	raw, err := json.Marshal(effective)
	if err != nil {
		return fail(err)
	}
	advisories, err = ValidateEffective(raw, effective)
	if err != nil {
		return fail(err)
	}
	warnings = append(warnings, advisories...)
	// Check selected original types as well as effective ones. Reconciliation
	// must not hide a selected version lacking registered execution semantics.
	for _, contribution := range selected {
		for _, capability := range contribution.Descriptor.Capabilities {
			if _, ok := contracts[capability.Type]; !ok {
				return fail(fmt.Errorf("selected capability %s has no execution contract", capability.Type))
			}
		}
	}
	plan, advisories, err := graph.plan(ctx, effective.Capabilities, contracts, opts.WorkspaceAssigned)
	if err != nil {
		return fail(err)
	}
	warnings = append(warnings, advisories...)
	return &RuntimeComposition{Descriptor: effective, Selections: selections, Plan: plan, Warnings: warnings}, nil
}

// cloneRuntimeDescriptor isolates caller-owned values without a JSON round trip.
// JSON supplies validation bytes, but decoding it would lose private presence
// bits and change concrete config value types observed by selectors.
func cloneRuntimeDescriptor(d *Descriptor) (*Descriptor, []byte, error) {
	raw, err := json.Marshal(d)
	if err != nil {
		return nil, nil, fmt.Errorf("copy descriptor: %w", err)
	}
	out := cloneRuntimeValue(reflect.ValueOf(d)).Interface().(*Descriptor)
	return out, raw, nil
}

func cloneRuntimeCapability(c Capability) (Capability, error) {
	if _, err := json.Marshal(c); err != nil {
		return Capability{}, err
	}
	out := cloneRuntimeValue(reflect.ValueOf(c)).Interface().(Capability)
	out.executionOrigins = slices.Clone(c.executionOrigins)
	return out, nil
}

// cloneRuntimeValue copies exported nested data while retaining scalar presence
// flags in structs. Private executionOrigins slices are owned by the planner;
// cloneRuntimeCapability copies them explicitly, and selector copies clear them.
// Callers first marshal the value, rejecting cycles and non-serializable configs.
func cloneRuntimeValue(value reflect.Value) reflect.Value {
	switch value.Kind() {
	case reflect.Interface, reflect.Pointer, reflect.Map, reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
	}
	switch value.Kind() {
	case reflect.Interface:
		out := reflect.New(value.Type()).Elem()
		out.Set(cloneRuntimeValue(value.Elem()))
		return out
	case reflect.Pointer:
		out := reflect.New(value.Type().Elem())
		out.Elem().Set(cloneRuntimeValue(value.Elem()))
		return out
	case reflect.Map:
		out := reflect.MakeMapWithSize(value.Type(), value.Len())
		iter := value.MapRange()
		for iter.Next() {
			out.SetMapIndex(iter.Key(), cloneRuntimeValue(iter.Value()))
		}
		return out
	case reflect.Slice:
		out := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for i := range value.Len() {
			out.Index(i).Set(cloneRuntimeValue(value.Index(i)))
		}
		return out
	case reflect.Array:
		out := reflect.New(value.Type()).Elem()
		for i := range value.Len() {
			out.Index(i).Set(cloneRuntimeValue(value.Index(i)))
		}
		return out
	case reflect.Struct:
		out := reflect.New(value.Type()).Elem()
		out.Set(value)
		for i := range value.NumField() {
			if value.Type().Field(i).IsExported() {
				out.Field(i).Set(cloneRuntimeValue(value.Field(i)))
			}
		}
		return out
	default:
		return value
	}
}

func clearRuntimeOrigins(capabilities []Capability) {
	for i := range capabilities {
		capabilities[i].executionOrigins = nil
		if capabilities[i].Group != nil {
			clearRuntimeOrigins(capabilities[i].Group.Capabilities)
		}
	}
}

// selectRuntimeCapabilities gives each callback independent descriptor/config
// copies. A selector can inspect and decide, but cannot mutate another entry or
// carry the planner's private origin tags into a separate composition.
func selectRuntimeCapabilities(ctx context.Context, contribution Contribution, selectCapability SelectCapability) (Selection, error) {
	var callbackErr error
	selection, err := SelectCapabilities(ctx, contribution.Reference, contribution.Descriptor,
		func(ctx context.Context, target SelectionTarget, d Descriptor, c Capability) CapabilityDecision {
			if callbackErr != nil {
				return CapabilityDecision{}
			}
			copied, _, copyErr := cloneRuntimeDescriptor(&d)
			if copyErr != nil {
				callbackErr = copyErr
				return CapabilityDecision{}
			}
			capability, copyErr := cloneRuntimeCapability(c)
			if copyErr != nil {
				callbackErr = copyErr
				return CapabilityDecision{}
			}
			clearRuntimeOrigins(copied.Capabilities)
			capability.executionOrigins = nil
			return selectCapability(ctx, target, *copied, capability)
		})
	if callbackErr != nil {
		return selection, callbackErr
	}
	return selection, err
}
