package spec

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
)

// ExecutionStage is a barrier in the runtime's boot sequence. Orders are
// compared only within a stage. Configure precedes container preparation;
// Setup establishes mounts and other hook prerequisites. Install runs only
// at create. Start runs on every boot, after install access has ended.
type ExecutionStage string

const (
	StageConfigure ExecutionStage = "configure"
	StageSetup     ExecutionStage = "setup"
	StageInstall   ExecutionStage = "install"
	StageStart     ExecutionStage = "start"
)

// PlannedAction names one effect defined by an exact-version contract.
// A handler executes only this action, not the entry's other actions.
type PlannedAction struct {
	Stage  ExecutionStage
	Action string
}

// ExecutionContract supplies pure planning metadata, not a runtime handler
// or a permission grant. Actions must be non-nil, including for declarative
// types, where it explicitly returns no actions. ValidatePlan is an optional
// pure prerequisite check of the complete plan. Neither callback may mutate
// its inputs or perform capability effects.
type ExecutionContract struct {
	DefaultOrder int32
	Actions      func(Capability) ([]PlannedAction, error)
	ValidatePlan func(ExecutionPlan) error
}

// RuntimeOptions contains the host's selection policy and additional trusted
// exact-version contracts. Contracts cannot replace built-in definitions.
type RuntimeOptions struct {
	Select    SelectCapability
	Contracts map[string]ExecutionContract
}

// ContributionSelection retains an input occurrence's original selection
// records. Repeated references are distinguished by Contribution, not Source.
type ContributionSelection struct {
	Contribution int
	Reference    string
	Selection    Selection
}

// ExecutionOrigin links an effective entry to one selected declaration.
// Source is optional publisher attribution and never determines identity.
type ExecutionOrigin struct {
	Contribution int
	Target       SelectionTarget
	Source       *CapabilitySource
}

// ExecutionStep is one action whose promised readiness must finish before
// the next step. EntryID is assigned before sorting and is independent of
// publisher Source, display labels, and numeric order. Once-only state is
// scoped to sandbox identity plus EntryID, stage, and action; runtimes retain
// it across daemon and host restarts. IDs do not promise continuity across
// descriptor restructuring or Kit updates.
type ExecutionStep struct {
	EntryID    string
	Action     string
	Order      int32
	Capability Capability
	Origins    []ExecutionOrigin
}

// StagePlan contains the final sequential order within one stage.
type StagePlan struct {
	Stage ExecutionStage
	Steps []ExecutionStep
}

// ExecutionPlan contains all four stages in their fixed order. Runtimes
// execute Configure and Setup as needed for the current boot, Install once
// at create, and Start every boot. They must not replay the whole plan on
// restart. Reserved host trust, identity, and workspace initialization is a
// precondition to executing capability effects.
type ExecutionPlan struct {
	Stages []StagePlan
}

// RuntimeComposition is returned only when selection, reconciliation,
// effective validation, and every ordering check have succeeded.
type RuntimeComposition struct {
	Descriptor *Descriptor
	Selections []ContributionSelection
	Plan       ExecutionPlan
	Warnings   []string
}

// RuntimeCompositionError exposes available original selection records and
// the diagnostic without exposing an executable partial composition.
type RuntimeCompositionError struct {
	Selections []ContributionSelection
	Err        error
}

func (e *RuntimeCompositionError) Error() string { return "compose runtime: " + e.Err.Error() }
func (e *RuntimeCompositionError) Unwrap() error { return e.Err }

// ComposeRuntime plans ordered, dependency-resolved, fully expanded inputs.
// It performs no registry, filesystem, process, or sandbox operations. Select
// is required and must not apply capabilities. A selected type lacking an
// execution contract fails planning, even when originally optional.
func ComposeRuntime(ctx context.Context, contributions []Contribution, opts RuntimeOptions) (*RuntimeComposition, error) {
	var selections []ContributionSelection
	fail := func(err error) (*RuntimeComposition, error) {
		return nil, &RuntimeCompositionError{Selections: selections, Err: err}
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
	selected := make([]Contribution, len(contributions))
	var selectionErrors ValidationErrors
	for i, contribution := range contributions {
		selection, err := SelectCapabilities(ctx, contribution.Reference, contribution.Descriptor, opts.Select)
		selections = append(selections, ContributionSelection{Contribution: i, Reference: contribution.Reference, Selection: selection})
		selectionErrors.add(err)
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		if contribution.Descriptor == nil {
			continue
		}
		d := *contribution.Descriptor
		d.Capabilities = slices.Clone(selection.Capabilities)
		declarations := map[string]Capability{}
		for _, declaration := range executionDeclarations(contribution.Descriptor) {
			declarations[declaration.path] = declaration.capability
		}
		at := 0
		for _, record := range selection.Selected {
			for _, member := range record.Members {
				c := &d.Capabilities[at]
				var source *CapabilitySource
				// Selection adds a fallback Source for diagnostics. The
				// origin keeps only attribution the publisher actually supplied.
				original := declarations[member]
				if original.Source != nil {
					copy := *original.Source
					source = &copy
				}
				c.executionOrigins = []ExecutionOrigin{{Contribution: i, Target: SelectionTarget{Reference: contribution.Reference, Path: member}, Source: source}}
				at++
			}
		}
		selected[i] = Contribution{Reference: contribution.Reference, Descriptor: &d}
	}
	if err := selectionErrors.err(); err != nil {
		return fail(err)
	}
	for _, contribution := range selected {
		for i := range contribution.Descriptor.Capabilities {
			c := &contribution.Descriptor.Capabilities[i]
			contract, ok := contracts[c.Type]
			if !ok {
				return fail(fmt.Errorf("%s: selected %s has no execution contract", executionLocations(c.executionOrigins), c.Type))
			}
			order := contract.DefaultOrder
			if c.Order != nil {
				order = *c.Order
			}
			c.Order = &order
		}
	}
	d, err := Compose(selected)
	if err != nil {
		return fail(err)
	}
	warnings, err := Validate(d)
	if err != nil {
		return fail(err)
	}
	sizeWarnings, err := checkMergedSize(d)
	if err != nil {
		return fail(err)
	}
	warnings = append(warnings, sizeWarnings...)
	plan, err := planRuntime(ctx, d, contracts, selected)
	if err != nil {
		return fail(err)
	}
	return &RuntimeComposition{Descriptor: d, Selections: selections, Plan: plan, Warnings: warnings}, nil
}

type executionDeclaration struct {
	capability Capability
	path       string
}

// These paths name the consumed descriptor independently of publication
// attribution. Selection accepts only ordinary entries or one-level groups.
func executionDeclarations(d *Descriptor) []executionDeclaration {
	var declarations []executionDeclaration
	for i, c := range d.Capabilities {
		at := fmt.Sprintf("capabilities[%d]", i)
		if c.Group != nil {
			for j, entry := range c.Group.Capabilities {
				declarations = append(declarations, executionDeclaration{entry, fmt.Sprintf("%s.group.capabilities[%d]", at, j)})
			}
		} else {
			declarations = append(declarations, executionDeclaration{c, at})
		}
	}
	return declarations
}

func runtimeContracts(additional map[string]ExecutionContract) (map[string]ExecutionContract, error) {
	contracts := BuiltinExecutionContracts()
	for _, typ := range slices.Sorted(maps.Keys(additional)) {
		contract := additional[typ]
		if _, exists := contracts[typ]; exists {
			return nil, fmt.Errorf("cannot replace built-in execution contract %s", typ)
		}
		if !needType.MatchString(typ) {
			return nil, fmt.Errorf("execution contract %q is not an exact capability type", typ)
		}
		if contract.Actions == nil {
			return nil, fmt.Errorf("execution contract %s must explicitly define Actions, including zero-action declarations", typ)
		}
		contracts[typ] = contract
	}
	return contracts, nil
}

func planRuntime(ctx context.Context, d *Descriptor, contracts map[string]ExecutionContract, contributions []Contribution) (ExecutionPlan, error) {
	plan := ExecutionPlan{Stages: []StagePlan{{Stage: StageConfigure}, {Stage: StageSetup}, {Stage: StageInstall}, {Stage: StageStart}}}
	used := map[string]bool{}
	var usedOrder []string
	for _, c := range d.Capabilities {
		if err := ctx.Err(); err != nil {
			return ExecutionPlan{}, err
		}
		contract := contracts[c.Type]
		actions, err := contract.Actions(c)
		if err != nil {
			return ExecutionPlan{}, fmt.Errorf("%s: lower %s: %w", executionLocations(c.executionOrigins), c.Type, err)
		}
		id, err := executionEntryID(c)
		if err != nil {
			return ExecutionPlan{}, err
		}
		seen := map[PlannedAction]bool{}
		for _, action := range actions {
			stage := slices.IndexFunc(plan.Stages, func(s StagePlan) bool { return s.Stage == action.Stage })
			if stage < 0 || strings.TrimSpace(action.Action) == "" || seen[action] {
				return ExecutionPlan{}, fmt.Errorf("%s: %s has invalid or duplicate action %q in stage %q", executionLocations(c.executionOrigins), c.Type, action.Action, action.Stage)
			}
			seen[action] = true
			plan.Stages[stage].Steps = append(plan.Stages[stage].Steps, ExecutionStep{
				EntryID: id, Action: action.Action, Order: *c.Order, Capability: c, Origins: slices.Clone(c.executionOrigins),
			})
		}
		if !used[c.Type] {
			used[c.Type] = true
			usedOrder = append(usedOrder, c.Type)
		}
	}
	for i := range plan.Stages {
		slices.SortStableFunc(plan.Stages[i].Steps, func(a, b ExecutionStep) int {
			return cmp.Compare(a.Order, b.Order)
		})
	}
	if err := validateHookDependencies(plan, contributions); err != nil {
		return ExecutionPlan{}, err
	}
	for _, typ := range usedOrder {
		if err := ctx.Err(); err != nil {
			return ExecutionPlan{}, err
		}
		if validate := contracts[typ].ValidatePlan; validate != nil {
			if err := validate(plan); err != nil {
				return ExecutionPlan{}, fmt.Errorf("execution contract %s: %w", typ, err)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return ExecutionPlan{}, err
	}
	return plan, nil
}

func executionEntryID(c Capability) (string, error) {
	key, arity, err := InstanceKeyOf(c)
	if err != nil {
		return "", err
	}
	type identityOrigin struct {
		Contribution int
		Target       SelectionTarget
	}
	origins := make([]identityOrigin, len(c.executionOrigins))
	for i, origin := range c.executionOrigins {
		origins[i] = identityOrigin{Contribution: origin.Contribution, Target: origin.Target}
	}
	raw, err := json.Marshal(struct {
		Type    string
		Arity   Arity
		Key     InstanceKey
		Origins []identityOrigin
	}{c.Type, arity, key, origins})
	if err != nil {
		return "", fmt.Errorf("execution identity: %w", err)
	}
	return fmt.Sprintf("entry-%x", sha256.Sum256(raw)), nil
}

func executionLocations(origins []ExecutionOrigin) string {
	locations := make([]string, len(origins))
	for i, origin := range origins {
		locations[i] = fmt.Sprintf("contribution[%d] %s %s", origin.Contribution, origin.Target.Reference, origin.Target.Path)
	}
	return strings.Join(locations, ", ")
}

func validateHookDependencies(plan ExecutionPlan, contributions []Contribution) error {
	providers := map[string][]struct {
		index int
		entry Provide
	}{}
	for i, contribution := range contributions {
		for _, entry := range contribution.Descriptor.Provides {
			p, err := ParseProvide(entry)
			if err != nil {
				return err
			}
			if p.Version == "" {
				p.Version = contribution.Descriptor.Version
			}
			providers[p.Name] = append(providers[p.Name], struct {
				index int
				entry Provide
			}{i, p})
		}
	}
	direct := make([][]int, len(contributions))
	for i, contribution := range contributions {
		for _, entry := range append(slices.Clone(contribution.Descriptor.Requires), contribution.Descriptor.Integrates...) {
			r, err := ParseRequire(entry)
			if err != nil {
				return err
			}
			for _, provider := range providers[r.Name] {
				if provider.index != i && Satisfies(provider.entry, r) {
					direct[provider.index] = append(direct[provider.index], i)
				}
			}
		}
	}
	// Memoized reachability includes dependencies through contributors
	// without hooks, without a cubic scan of unrelated contribution pairs.
	before := make([]map[int]bool, len(contributions))
	visiting := make([]bool, len(contributions))
	var visit func(int) error
	visit = func(from int) error {
		if visiting[from] {
			return fmt.Errorf("dependency cycle includes contribution[%d] %s", from, contributions[from].Reference)
		}
		if before[from] != nil {
			return nil
		}
		visiting[from] = true
		before[from] = map[int]bool{}
		for _, to := range direct[from] {
			if err := visit(to); err != nil {
				return err
			}
			before[from][to] = true
			for descendant := range before[to] {
				before[from][descendant] = true
			}
		}
		visiting[from] = false
		return nil
	}
	for from := range direct {
		if err := visit(from); err != nil {
			return err
		}
	}
	for _, stage := range plan.Stages {
		var hooks []ExecutionStep
		for _, step := range stage.Steps {
			if step.Capability.Type == CapabilityLifecycle && (step.Action == ActionInstall || step.Action == ActionStartup) {
				for _, earlier := range hooks {
					for _, provider := range step.Origins {
						for _, requirer := range earlier.Origins {
							if before[provider.Contribution][requirer.Contribution] {
								return fmt.Errorf("%s order %d must precede dependent hooks at %s order %d in stage %s", executionLocations(step.Origins), step.Order, executionLocations(earlier.Origins), earlier.Order, stage.Stage)
							}
						}
					}
				}
				hooks = append(hooks, step)
			}
		}
	}
	return nil
}

// Publication removes internally satisfied requirements, so contradictory
// hook orders must be refused while those dependency edges are still known.
// Check every declared possibility; selection can remove hooks, never create
// a new inversion between two surviving hooks.
func validatePublicationHookOrder(contributions []Contribution) error {
	plan := ExecutionPlan{Stages: []StagePlan{{Stage: StageInstall}, {Stage: StageStart}}}
	defaultOrder := BuiltinExecutionContracts()[CapabilityLifecycle].DefaultOrder
	for i, contribution := range contributions {
		if contribution.Descriptor == nil {
			return nil // mergeDeclarations reports the missing descriptor.
		}
		for _, declaration := range executionDeclarations(contribution.Descriptor) {
			c := declaration.capability
			if c.Type != CapabilityLifecycle {
				continue
			}
			order := defaultOrder
			if c.Order != nil {
				order = *c.Order
			}
			for _, action := range []PlannedAction{{StageInstall, ActionInstall}, {StageStart, ActionStartup}} {
				// Typed values elsewhere in a publication template may
				// still hold arguments. Only hook-list presence is needed
				// to refuse a potentially reversed dependency order.
				if !hasDeclaredHooks(c, action.Action) {
					continue
				}
				at := slices.IndexFunc(plan.Stages, func(stage StagePlan) bool { return stage.Stage == action.Stage })
				plan.Stages[at].Steps = append(plan.Stages[at].Steps, ExecutionStep{Action: action.Action, Order: order, Capability: c,
					Origins: []ExecutionOrigin{{Contribution: i, Target: SelectionTarget{Reference: contribution.Reference, Path: declaration.path}}}})
			}
		}
	}
	for i := range plan.Stages {
		slices.SortStableFunc(plan.Stages[i].Steps, func(a, b ExecutionStep) int {
			return cmp.Compare(a.Order, b.Order)
		})
	}
	return validateHookDependencies(plan, contributions)
}

func hasDeclaredHooks(c Capability, action string) bool {
	value, null := deref(reflect.ValueOf(c.Config[action]))
	return !null && (value.Kind() != reflect.Slice || value.Len() > 0)
}
