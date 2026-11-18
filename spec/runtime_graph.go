package spec

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// runtimeNode keeps one original declaration, including declarations skipped by
// selection. Skipped declarations bind IDs but never constrain execution order.
// Its position in nodes is the dependency/declaration tie-break position.
type runtimeNode struct {
	capability Capability
	origin     ExecutionOrigin
	scope      int
	selected   bool
}

// runtimeGraph separates declaration identity from effective requests. scopes
// holds original node indexes; providers binds a name to original Kit scope
// ranges. Neither map is rebuilt from merged payloads or publisher attribution.
// Shared ranges and dependency groups keep empty publication scopes linear in
// the input size, even when many aggregate aliases name the whole artifact.
type runtimeGraph struct {
	nodes        []runtimeNode
	scopes       [][]int
	providers    map[string][]runtimeProviderBinding
	dependencies []runtimeScopeDependencies
}

// A whole-artifact alias shares one range instead of copying every scope index
// for every provided name. Explicit original bindings use singleton ranges.
// Range identity also prevents repeated expansion through different aliases.
type runtimeScopeRange struct{ first, end int }

type runtimeProviderBinding struct {
	scopes *runtimeScopeRange
	// One provider can offer several versions of a name. Keep them together
	// so matching any version preserves the dependency without duplicating
	// scope targets or making a qualified entry ID appear ambiguous.
	offers []Provide
}

// Aggregate requirements apply to every scope, but their parsed expressions
// and traversal are shared. Local requirements retain empty intermediate Kits'
// dependencies without constructing a square scope-reachability matrix.
type runtimeDependencyGroup struct{ requirements []Require }
type runtimeScopeDependencies struct{ local, aggregate *runtimeDependencyGroup }

// runtimeRelation retains the written constraint responsible for an edge.
// Writer and before preserve which endpoint authored a relationship, because
// that endpoint alone may be released from its normal ordering constraints.
// One authored relationship is retained instead of expanding a type
// or provider target into one heap object per matched declaration. The target is
// resolved while validating or lowering a boundary, and its temporary matches
// are discarded before processing the next relationship.
type runtimeRelation struct {
	writer int
	before bool
	target orderingTarget
	reason string
}

// runtimeFragment is one reconciled request and its scheduled effects. Each
// request sits at its first declaration; other origins contribute ID aliases
// and written edges without contributing extra declaration constraints.
type runtimeFragment struct {
	capability    Capability
	entry         int
	id            string
	effects       []PlannedEffect
	participation []ExecutionBoundary
}

// newRuntimeGraph enumerates inputs before selection or sorting. Each input
// occurrence gets fresh scope indexes, even when its reference or digest repeats.
// Empty scopes remain addressable: an absent provider is a warning, while a
// present provider missing a requested ID is an error.
func newRuntimeGraph(contributions []Contribution) (*runtimeGraph, error) {
	graph := &runtimeGraph{providers: map[string][]runtimeProviderBinding{}}
	for ci, contribution := range contributions {
		d := contribution.Descriptor
		baseScope := len(graph.scopes)
		scopeCount := max(1, len(d.CapabilityScopes))
		graph.scopes = append(graph.scopes, make([][]int, scopeCount)...)
		aggregate, err := runtimeDependencies(d.Requires, d.Integrates)
		if err != nil {
			return nil, err
		}
		aggregateRange := &runtimeScopeRange{first: baseScope, end: baseScope + scopeCount}
		graph.dependencies = append(graph.dependencies, make([]runtimeScopeDependencies, scopeCount)...)
		for local := range scopeCount {
			graph.dependencies[baseScope+local].aggregate = aggregate
		}
		// Published scopes bind names while the enclosing descriptor retains
		// their versions. Dependencies match the offered version, never merely
		// a present name, including the descriptor's version fallback.
		offers := map[string][]Provide{}
		for _, raw := range d.Provides {
			offer, err := ParseProvide(raw)
			if err != nil {
				return nil, err
			}
			if offer.Version == "" {
				offer.Version = d.Version
			}
			offers[offer.Name] = append(offers[offer.Name], offer)
		}
		bound := map[string]bool{}
		for i, scope := range d.CapabilityScopes {
			local, err := runtimeDependencies(scope.Requires, scope.Integrates)
			if err != nil {
				return nil, err
			}
			graph.dependencies[baseScope+i].local = local
			scopeRange := &runtimeScopeRange{first: baseScope + i, end: baseScope + i + 1}
			for _, name := range scope.Provides {
				graph.providers[name] = append(graph.providers[name], runtimeProviderBinding{scopes: scopeRange, offers: offers[name]})
				bound[name] = true
			}
		}
		// An aggregate provide without an original scope binding belongs to
		// this whole contribution, never to unrelated contributions beside it.
		for _, raw := range d.Provides {
			offer, err := ParseProvide(raw)
			if err != nil {
				return nil, err
			}
			if !bound[offer.Name] {
				graph.providers[offer.Name] = append(graph.providers[offer.Name], runtimeProviderBinding{scopes: aggregateRange, offers: offers[offer.Name]})
				bound[offer.Name] = true
			}
		}
		add := func(c *Capability, path string) {
			localScope := uint32(0)
			if c.Scope != nil {
				localScope = *c.Scope
			}
			index := len(graph.nodes)
			c.executionOrigins = []int{index}
			origin := ExecutionOrigin{Contribution: ci, Reference: contribution.Reference, Path: path,
				ID: c.ID, Type: c.Type, Scope: localScope,
				Target: SelectionTarget{Reference: contribution.Reference, Path: path}}
			if c.Source != nil {
				source := *c.Source
				origin.Source = &source
			}
			scope := baseScope + int(localScope)
			graph.nodes = append(graph.nodes, runtimeNode{capability: *c, origin: origin, scope: scope})
			graph.scopes[scope] = append(graph.scopes[scope], index)
		}
		for i := range d.Capabilities {
			item := &d.Capabilities[i]
			path := fmt.Sprintf("capabilities[%d]", i)
			if item.Group == nil {
				add(item, path)
				continue
			}
			for j := range item.Group.Capabilities {
				add(&item.Group.Capabilities[j], fmt.Sprintf("%s.group.capabilities[%d]", path, j))
			}
		}
	}
	return graph, nil
}

func (g *runtimeGraph) label(index int) string {
	origin := g.nodes[index].origin
	name := fmt.Sprintf("contributions[%d].%s", origin.Contribution, origin.Path)
	if origin.Reference != "" {
		name += " (" + origin.Reference + ")"
	}
	if origin.ID != "" {
		name += " #" + origin.ID
	}
	return name
}

// identity remains unchanged when edges reorder entries. It is stable for the
// same pinned inputs and occurrence order, not a migration key across Kit updates.
func (g *runtimeGraph) identity(index int) string {
	origin := g.nodes[index].origin
	return fmt.Sprintf("contributions[%d].%s", origin.Contribution, origin.Path)
}

// resolve uses original types and scopes, before reconciliation changes either.
// The bool distinguishes an absent provider/type from an existing empty scope.
// Aggregate provider aliases may span scopes, but an ID target must remain unique.
func (g *runtimeGraph) resolve(ctx context.Context, writer int, target orderingTarget) ([]int, bool, error) {
	var targets []int
	if target.capabilityType != "" {
		for i, node := range g.nodes {
			if err := ctx.Err(); err != nil {
				return nil, false, err
			}
			if node.capability.Type == target.capabilityType {
				targets = append(targets, i)
			}
		}
		return targets, len(targets) > 0, nil
	}
	var bindings []runtimeProviderBinding
	if target.local {
		scope := g.nodes[writer].scope
		bindings = []runtimeProviderBinding{{scopes: &runtimeScopeRange{first: scope, end: scope + 1}}}
	} else {
		var present bool
		bindings, present = g.providers[target.provider]
		if !present {
			return nil, false, nil
		}
	}
	for _, binding := range bindings {
		for scope := binding.scopes.first; scope < binding.scopes.end; scope++ {
			if err := ctx.Err(); err != nil {
				return nil, false, err
			}
			for _, i := range g.scopes[scope] {
				if err := ctx.Err(); err != nil {
					return nil, false, err
				}
				if target.id == "" || g.nodes[i].capability.ID == target.id {
					targets = append(targets, i)
				}
			}
		}
	}
	if target.id != "" {
		switch len(targets) {
		case 0:
			return nil, true, fmt.Errorf("%s: present scope has no entry with id %q", g.label(writer), target.id)
		case 1:
		default:
			return nil, true, fmt.Errorf("%s: id %q is ambiguous across provider %q scopes", g.label(writer), target.id, target.provider)
		}
	}
	return targets, true, nil
}

// runtimeDependencies retains Kit dependency constraints independently of the
// caller's chosen order among unrelated Kits. Scope metadata carries internal
// dependencies through publication, where satisfied requirements disappear.
func runtimeDependencies(groups ...[]string) (*runtimeDependencyGroup, error) {
	var parsed []Require
	for _, group := range groups {
		for _, raw := range group {
			required, err := ParseRequire(raw)
			if err != nil {
				return nil, err
			}
			parsed = append(parsed, required)
		}
	}
	if len(parsed) == 0 {
		return nil, nil
	}
	return &runtimeDependencyGroup{requirements: parsed}, nil
}

// scopePredecessors traverses only when a participating entry needs its normal
// order. Empty intermediate Kits still connect providers to requirers, while
// an artifact containing only empty scopes never computes any reachability.
// Groups and ranges are expanded once per query, so shared aggregate metadata
// cannot multiply work by the number of scopes. Nothing is cached quadratically.
func (g *runtimeGraph) scopePredecessors(ctx context.Context, scope int) (map[int]bool, error) {
	visited := map[int]bool{scope: true}
	groups := map[*runtimeDependencyGroup]bool{}
	ranges := map[*runtimeScopeRange]bool{}
	pending := []int{scope}
	for len(pending) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		dependencies := g.dependencies[current]
		for _, group := range []*runtimeDependencyGroup{dependencies.local, dependencies.aggregate} {
			if group == nil || groups[group] {
				continue
			}
			groups[group] = true
			for _, required := range group.requirements {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				for _, binding := range g.providers[required.Name] {
					if err := ctx.Err(); err != nil {
						return nil, err
					}
					if ranges[binding.scopes] || !slices.ContainsFunc(binding.offers, func(offer Provide) bool { return Satisfies(offer, required) }) {
						continue
					}
					ranges[binding.scopes] = true
					for provider := binding.scopes.first; provider < binding.scopes.end; provider++ {
						if err := ctx.Err(); err != nil {
							return nil, err
						}
						if !visited[provider] {
							visited[provider] = true
							pending = append(pending, provider)
						}
					}
				}
			}
		}
	}
	return visited, nil
}

// writtenEdges validates every declaration target before selection. Later the
// planner maps these original endpoints to selected effective requests. An edge
// collapsed by reconciliation has no effect, including an explicit ID edge.
func (g *runtimeGraph) writtenEdges(ctx context.Context) ([]runtimeRelation, []string, error) {
	var relations []runtimeRelation
	var warnings []string
	for i, node := range g.nodes {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		for _, relation := range []struct {
			name    string
			targets []string
		}{{"before", node.capability.Before}, {"after", node.capability.After}} {
			for _, raw := range relation.targets {
				target, err := parseOrderingTarget(raw)
				if err != nil {
					return nil, nil, fmt.Errorf("%s: %w", g.label(i), err)
				}
				targets, present, err := g.resolve(ctx, i, target)
				if err != nil {
					return nil, nil, err
				}
				if !present {
					warnings = append(warnings, fmt.Sprintf("%s: %s target %q is absent", g.label(i), relation.name, raw))
				}
				for _, j := range targets {
					if err := ctx.Err(); err != nil {
						return nil, nil, err
					}
					if i == j && target.id != "" {
						return nil, nil, fmt.Errorf("%s: %s %q is a self-reference", g.label(i), relation.name, raw)
					}
				}
				relations = append(relations, runtimeRelation{writer: i, before: relation.name == "before", target: target, reason: fmt.Sprintf("%s %s %q", g.label(i), relation.name, raw)})
			}
		}
	}
	return relations, warnings, ctx.Err()
}

// fragments lowers effective requests without joining connected origin sets.
// A shared grant can reconcile into different requests in different phases;
// connecting their origins would invent constraints between those requests.
func (g *runtimeGraph) fragments(ctx context.Context, capabilities []Capability, contracts map[string]ExecutionContract) ([]runtimeFragment, error) {
	fragments := make([]runtimeFragment, 0, len(capabilities))
	for i, c := range capabilities {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(c.executionOrigins) == 0 {
			return nil, fmt.Errorf("effective capability %s has no reconciliation origins", c.Type)
		}
		first := len(g.nodes)
		for _, origin := range c.executionOrigins {
			if origin < 0 || origin >= len(g.nodes) || !g.nodes[origin].selected {
				return nil, fmt.Errorf("effective capability %s has invalid reconciliation origin %d", c.Type, origin)
			}
			first = min(first, origin)
		}
		contract, known := contracts[c.Type]
		if !known {
			return nil, fmt.Errorf("effective capability %s has no execution contract", c.Type)
		}
		var effects []PlannedEffect
		if contract.Effects != nil {
			// Pure callbacks still receive detached copies, protecting inspection data
			// and all neighboring requests from accidental mutation.
			copy, err := cloneRuntimeCapability(c)
			if err != nil {
				return nil, err
			}
			effects, err = contract.Effects(copy)
			if err != nil {
				return nil, fmt.Errorf("execution contract %s: %w", c.Type, err)
			}
			effects = slices.Clone(effects)
		}
		seen := map[string]bool{}
		for _, effect := range effects {
			if err := validateRuntimeEffect(effect); err != nil {
				return nil, fmt.Errorf("execution contract %s: %w", c.Type, err)
			}
			if !slices.Contains(contract.Participation, ExecutionBoundary{effect.Phase, effect.Step}) {
				return nil, fmt.Errorf("execution contract %s effect %s is outside fixed participation %s/%s", c.Type, effect.Action, effect.Phase, effect.Step)
			}
			key := string(effect.Phase) + "/" + string(effect.Step) + "/" + effect.Action
			if seen[key] {
				return nil, fmt.Errorf("execution contract %s repeats effect %s", c.Type, key)
			}
			seen[key] = true
		}
		fragments = append(fragments, runtimeFragment{capability: c, entry: first, id: fmt.Sprintf("effective[%d]", i), effects: effects, participation: contract.Participation})
	}
	return fragments, ctx.Err()
}

// plan computes an independent constraint graph at each fixed boundary. Types
// participate even when config lowers no work there; absent effects never let
// a config edit reshuffle another entry. No partial schedule escapes a cycle.
func (g *runtimeGraph) plan(ctx context.Context, capabilities []Capability, contracts map[string]ExecutionContract, written []runtimeRelation, workspace bool) (ExecutionPlan, []string, error) {
	fragments, err := g.fragments(ctx, capabilities, contracts)
	if err != nil {
		return ExecutionPlan{}, nil, err
	}
	orders := map[ExecutionBoundary][]int{}
	for _, phase := range []ExecutionPhase{PhasePrepare, PhaseInstall, PhaseStart, PhaseRunning} {
		for _, step := range []ExecutionStep{StepAttach, StepPlace, StepExecute} {
			if !runtimePhaseStep(phase, step) {
				continue
			}
			boundary := ExecutionBoundary{phase, step}
			order, err := g.boundaryOrder(ctx, boundary, fragments, written)
			if err != nil {
				return ExecutionPlan{}, nil, fmt.Errorf("%s/%s: %w", phase, step, err)
			}
			orders[boundary] = order
		}
	}
	return g.schedule(orders, fragments, workspace), nil, nil
}

// boundaryOrder implements the normative computation. Normal constraints are
// complete pairwise Kit declaration/dependency constraints, not just adjacent
// edges: moving one entry must not release the order of untouched neighbors.
func (g *runtimeGraph) boundaryOrder(ctx context.Context, boundary ExecutionBoundary, fragments []runtimeFragment, written []runtimeRelation) ([]int, error) {
	var nodes []int
	origins := make([][]int, len(g.nodes))
	for i, fragment := range fragments {
		if !slices.Contains(fragment.participation, boundary) {
			continue
		}
		local := len(nodes)
		nodes = append(nodes, i)
		for _, origin := range fragment.capability.executionOrigins {
			origins[origin] = append(origins[origin], local)
		}
	}
	// Effective validation has already bounded the serialized selected descriptor.
	// Store each possible constraint in one bit, rather than multiplying a small
	// descriptor into millions of edge structs, map buckets and adjacency indexes.
	constraints := newRuntimeBoundaryConstraints(len(nodes))
	precedingScope := -1
	var predecessors map[int]bool
	for j, fragment := range nodes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		right := fragments[fragment].entry
		rs := g.nodes[right].scope
		if rs != precedingScope {
			var err error
			predecessors, err = g.scopePredecessors(ctx, rs)
			if err != nil {
				return nil, err
			}
			precedingScope = rs
		}
		for i, other := range nodes {
			if i == j {
				continue
			}
			left := fragments[other].entry
			ls := g.nodes[left].scope
			if ls == rs && left < right || ls != rs && predecessors[ls] {
				constraints.normal.set(i, j)
			}
		}
	}
	for _, relation := range written {
		if len(origins[relation.writer]) == 0 {
			continue
		}
		targets, _, err := g.resolve(ctx, relation.writer, relation.target)
		if err != nil {
			return nil, err
		}
		for _, writer := range origins[relation.writer] {
			for _, target := range targets {
				if target == relation.writer {
					continue
				}
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				for _, other := range origins[target] {
					if writer == other {
						continue
					}
					if relation.before {
						constraints.before.set(writer, other)
					} else {
						constraints.after.set(other, writer)
					}
				}
			}
		}
	}
	if err := constraints.displace(ctx); err != nil {
		return nil, err
	}
	ordered, cycle, err := constraints.order(ctx)
	if err != nil {
		return nil, err
	}
	if len(cycle) != 0 {
		return nil, g.cycleError(ctx, cycle, nodes, fragments, written)
	}
	for i, local := range ordered {
		ordered[i] = nodes[local]
	}
	return ordered, nil
}

// cycleError reports an actual cycle in the unscheduled remainder. Reporting
// every blocked node would also blame entries merely downstream of the cycle.
func (g *runtimeGraph) cycleError(ctx context.Context, cycle []int, nodes []int, fragments []runtimeFragment, written []runtimeRelation) error {
	details := make([]string, 0, len(cycle)-1)
	for i, from := range cycle[:len(cycle)-1] {
		to := cycle[i+1]
		left, right := fragments[nodes[from]], fragments[nodes[to]]
		reason := "normal Kit declaration/dependency order"
		// Reasons are reconstructed only for a real cycle. Carrying one reason
		// string or relation index for every possible edge would undo the compact
		// graph's memory bound. Written constraints take diagnostic precedence.
		for _, relation := range written {
			if err := ctx.Err(); err != nil {
				return err
			}
			writer, target := left, right
			if !relation.before {
				writer, target = right, left
			}
			if !slices.Contains(writer.capability.executionOrigins, relation.writer) {
				continue
			}
			targets, _, err := g.resolve(ctx, relation.writer, relation.target)
			if err != nil {
				return err
			}
			matched := false
			for _, original := range targets {
				if original == relation.writer {
					continue
				}
				if slices.Contains(target.capability.executionOrigins, original) {
					matched = true
					break
				}
			}
			if matched {
				reason = relation.reason
				break
			}
		}
		details = append(details, fmt.Sprintf("%s -> %s [%s]", g.label(left.entry), g.label(right.entry), reason))
	}
	return fmt.Errorf("execution ordering cycle: %s", strings.Join(details, "; "))
}
