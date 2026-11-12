package spec

import (
	"container/heap"
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
// holds original node indexes; providers maps a name to its original Kit scopes.
// Neither map is rebuilt from merged payloads or publisher-supplied attribution.
type runtimeGraph struct {
	nodes        []runtimeNode
	scopes       [][]int
	providers    map[string][]int
	dependencies [][]bool
}

// runtimeEdge retains the normal or written constraint responsible for an edge.
// Writer and before preserve which endpoint authored a relationship, because
// that endpoint alone may be released from its normal ordering constraints.
type runtimeEdge struct {
	from, to int
	reason   string
	writer   int
	before   bool
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
	graph := &runtimeGraph{providers: map[string][]int{}}
	for ci, contribution := range contributions {
		d := contribution.Descriptor
		baseScope := len(graph.scopes)
		scopeCount := max(1, len(d.CapabilityScopes))
		graph.scopes = append(graph.scopes, make([][]int, scopeCount)...)
		bound := map[string]bool{}
		for i, scope := range d.CapabilityScopes {
			for _, name := range scope.Provides {
				graph.providers[name] = append(graph.providers[name], baseScope+i)
				bound[name] = true
			}
		}
		// An aggregate provide without an original scope binding belongs to
		// this whole contribution, never to unrelated contributions beside it.
		for _, raw := range d.Provides {
			provided, err := ParseProvide(raw)
			if err != nil {
				return nil, err
			}
			if !bound[provided.Name] {
				for i := range scopeCount {
					graph.providers[provided.Name] = append(graph.providers[provided.Name], baseScope+i)
				}
				bound[provided.Name] = true
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
	if err := graph.buildDependencies(contributions); err != nil {
		return nil, err
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
func (g *runtimeGraph) resolve(writer int, target orderingTarget) ([]int, bool, error) {
	var targets []int
	if target.capabilityType != "" {
		for i, node := range g.nodes {
			if node.capability.Type == target.capabilityType {
				targets = append(targets, i)
			}
		}
		return targets, len(targets) > 0, nil
	}
	var scopes []int
	if target.local {
		scopes = []int{g.nodes[writer].scope}
	} else {
		var present bool
		scopes, present = g.providers[target.provider]
		if !present {
			return nil, false, nil
		}
	}
	for _, scope := range scopes {
		for _, i := range g.scopes[scope] {
			if target.id == "" || g.nodes[i].capability.ID == target.id {
				targets = append(targets, i)
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

// buildDependencies retains Kit dependency constraints independently of the
// caller's chosen order among unrelated Kits. Scope metadata carries internal
// dependencies through publication, where satisfied requirements disappear.
func (g *runtimeGraph) buildDependencies(contributions []Contribution) error {
	n := len(g.scopes)
	g.dependencies = make([][]bool, n)
	for i := range n {
		g.dependencies[i] = make([]bool, n)
	}
	// Published scopes bind names while the enclosing descriptor retains the
	// versions of every offer. Resolve constraints against those versions; a
	// merely present but incompatible integrates provider contributes no edge.
	provided := make([][]Provide, n)
	base := 0
	for _, contribution := range contributions {
		d := contribution.Descriptor
		count := max(1, len(d.CapabilityScopes))
		for _, raw := range d.Provides {
			p, err := ParseProvide(raw)
			if err != nil {
				return err
			}
			if p.Version == "" {
				p.Version = d.Version
			}
			for _, scope := range g.providers[p.Name] {
				if scope >= base && scope < base+count {
					provided[scope] = append(provided[scope], p)
				}
			}
		}
		base += count
	}
	base = 0
	for _, contribution := range contributions {
		d := contribution.Descriptor
		count := max(1, len(d.CapabilityScopes))
		for local := range count {
			relations := append(slices.Clone(d.Requires), d.Integrates...)
			if len(d.CapabilityScopes) > 0 {
				relations = append(relations, d.CapabilityScopes[local].Requires...)
				relations = append(relations, d.CapabilityScopes[local].Integrates...)
			}
			for _, raw := range relations {
				required, err := ParseRequire(raw)
				if err != nil {
					return err
				}
				for _, provider := range g.providers[required.Name] {
					if provider != base+local && slices.ContainsFunc(provided[provider], func(p Provide) bool { return Satisfies(p, required) }) {
						g.dependencies[provider][base+local] = true
					}
				}
			}
		}
		base += count
	}
	// Reachability includes empty intermediate Kits, so their lack of entries does
	// not erase provider-before-requirer constraints on the Kits around them.
	for k := range n {
		for i := range n {
			for j := range n {
				g.dependencies[i][j] = g.dependencies[i][j] || g.dependencies[i][k] && g.dependencies[k][j]
			}
		}
	}
	return nil
}

// writtenEdges validates every declaration target before selection. Later the
// planner maps these original endpoints to selected effective requests. An edge
// collapsed by reconciliation has no effect, including an explicit ID edge.
func (g *runtimeGraph) writtenEdges() ([]runtimeEdge, []string, error) {
	var edges []runtimeEdge
	var warnings []string
	for i, node := range g.nodes {
		for _, relation := range []struct {
			name    string
			targets []string
		}{{"before", node.capability.Before}, {"after", node.capability.After}} {
			for _, raw := range relation.targets {
				target, err := parseOrderingTarget(raw)
				if err != nil {
					return nil, nil, fmt.Errorf("%s: %w", g.label(i), err)
				}
				targets, present, err := g.resolve(i, target)
				if err != nil {
					return nil, nil, err
				}
				if !present {
					warnings = append(warnings, fmt.Sprintf("%s: %s target %q is absent", g.label(i), relation.name, raw))
				}
				for _, j := range targets {
					if i == j {
						if target.id != "" {
							return nil, nil, fmt.Errorf("%s: %s %q is a self-reference", g.label(i), relation.name, raw)
						}
						continue
					}
					from, to := i, j
					if relation.name == "after" {
						from, to = to, from
					}
					edges = append(edges, runtimeEdge{from: from, to: to, writer: i, before: relation.name == "before", reason: fmt.Sprintf("%s %s %q", g.label(i), relation.name, raw)})
				}
			}
		}
	}
	return edges, warnings, nil
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
func (g *runtimeGraph) plan(ctx context.Context, capabilities []Capability, contracts map[string]ExecutionContract, workspace bool) (ExecutionPlan, []string, error) {
	fragments, err := g.fragments(ctx, capabilities, contracts)
	if err != nil {
		return ExecutionPlan{}, nil, err
	}
	written, _, err := g.writtenEdges()
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
			order, err := g.boundaryOrder(boundary, fragments, written)
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
func (g *runtimeGraph) boundaryOrder(boundary ExecutionBoundary, fragments []runtimeFragment, written []runtimeEdge) ([]int, error) {
	var nodes []int
	origins := make([][]int, len(g.nodes))
	for i, fragment := range fragments {
		if !slices.Contains(fragment.participation, boundary) {
			continue
		}
		nodes = append(nodes, i)
		for _, origin := range fragment.capability.executionOrigins {
			origins[origin] = append(origins[origin], i)
		}
	}
	normal := map[[2]int]runtimeEdge{}
	for _, i := range nodes {
		for _, j := range nodes {
			if i == j {
				continue
			}
			left, right := fragments[i].entry, fragments[j].entry
			ls, rs := g.nodes[left].scope, g.nodes[right].scope
			if ls == rs && left < right || ls != rs && g.dependencies[ls][rs] {
				normal[[2]int{i, j}] = runtimeEdge{from: i, to: j, reason: "normal Kit declaration/dependency order"}
			}
		}
	}
	var explicit []runtimeEdge
	for _, edge := range written {
		for _, from := range origins[edge.from] {
			for _, to := range origins[edge.to] {
				if from == to {
					continue
				}
				writer := from
				if !edge.before {
					writer = to
				}
				explicit = append(explicit, runtimeEdge{from: from, to: to, writer: writer, before: edge.before, reason: edge.reason})
			}
		}
	}
	// Step 2 observes the initial normal relation. All removals are applied
	// together so edge enumeration order cannot affect the resulting constraints.
	removals := map[[2]int]bool{}
	for _, edge := range explicit {
		target := edge.to
		if !edge.before {
			target = edge.from
		}
		for _, other := range nodes {
			if edge.before {
				if _, ok := normal[[2]int{target, other}]; target == other || ok {
					removals[[2]int{other, edge.writer}] = true
				}
			} else {
				if _, ok := normal[[2]int{other, target}]; target == other || ok {
					removals[[2]int{edge.writer, other}] = true
				}
			}
		}
	}
	for key := range removals {
		delete(normal, key)
	}
	// Step 3 expands displacement through remaining normal constraints and written
	// edges of the same orientation. Each pass observes one snapshot and applies
	// removals simultaneously. Written edges are never candidates for removal.
	for {
		removals = map[[2]int]bool{}
		for _, edge := range explicit {
			target := edge.to
			if !edge.before {
				target = edge.from
			}
			reached := map[int]bool{}
			var walk func(int)
			walk = func(node int) {
				if node == edge.writer || reached[node] {
					return
				}
				reached[node] = true
				for _, next := range nodes {
					key := [2]int{node, next}
					if !edge.before {
						key = [2]int{next, node}
					}
					if _, ok := normal[key]; ok {
						walk(next)
					}
				}
				for _, other := range explicit {
					if other.before != edge.before {
						continue
					}
					if edge.before && other.from == node {
						walk(other.to)
					}
					if !edge.before && other.to == node {
						walk(other.from)
					}
				}
			}
			walk(target)
			for other := range reached {
				key := [2]int{other, edge.writer}
				if !edge.before {
					key = [2]int{edge.writer, other}
				}
				if _, ok := normal[key]; ok {
					removals[key] = true
				}
			}
		}
		if len(removals) == 0 {
			break
		}
		for key := range removals {
			delete(normal, key)
		}
	}
	edges := slices.Clone(explicit)
	// Iterating nodes, rather than map keys, keeps cycle diagnostics deterministic.
	for _, i := range nodes {
		for _, j := range nodes {
			if edge, ok := normal[[2]int{i, j}]; ok {
				edges = append(edges, edge)
			}
		}
	}
	return g.order(edges, nodes, fragments)
}

// order uses Kahn's algorithm with a min-heap of eligible composition positions.
// A FIFO queue would leave a newly eligible early declaration behind later
// declarations, violating the specified dependency/declaration tie-break.
func (g *runtimeGraph) order(edges []runtimeEdge, nodes []int, fragments []runtimeFragment) ([]int, error) {
	indegree := make([]int, len(fragments))
	outgoing := make([][]int, len(fragments))
	for i, edge := range edges {
		indegree[edge.to]++
		outgoing[edge.from] = append(outgoing[edge.from], i)
	}
	var order []int
	visited := make([]bool, len(fragments))
	eligible := &runtimeEligible{}
	for _, node := range nodes {
		if indegree[node] == 0 {
			heap.Push(eligible, node)
		}
	}
	for eligible.Len() > 0 {
		next := heap.Pop(eligible).(int)
		visited[next] = true
		order = append(order, next)
		for _, index := range outgoing[next] {
			target := edges[index].to
			indegree[target]--
			if indegree[target] == 0 {
				heap.Push(eligible, target)
			}
		}
	}
	if len(order) != len(nodes) {
		return nil, g.cycleError(edges, outgoing, nodes, visited, fragments)
	}
	return order, nil
}

// cycleError reports an actual cycle in the unscheduled remainder. Reporting
// every blocked node would also blame entries merely downstream of the cycle.
func (g *runtimeGraph) cycleError(edges []runtimeEdge, outgoing [][]int, nodes []int, completed []bool, fragments []runtimeFragment) error {
	active := make([]bool, len(fragments))
	visited := slices.Clone(completed)
	var path, cycle []int
	var visit func(int) bool
	visit = func(node int) bool {
		active[node] = true
		visited[node] = true
		for _, index := range outgoing[node] {
			edge := edges[index]
			path = append(path, index)
			if active[edge.to] {
				start := 0
				for edges[path[start]].from != edge.to {
					start++
				}
				cycle = slices.Clone(path[start:])
				return true
			}
			if !visited[edge.to] && visit(edge.to) {
				return true
			}
			path = path[:len(path)-1]
		}
		active[node] = false
		return false
	}
	for _, node := range nodes {
		if !visited[node] && visit(node) {
			break
		}
	}
	details := make([]string, 0, len(cycle))
	for _, index := range cycle {
		edge := edges[index]
		details = append(details, fmt.Sprintf("%s -> %s [%s]", g.label(fragments[edge.from].entry), g.label(fragments[edge.to].entry), edge.reason))
	}
	return fmt.Errorf("execution ordering cycle: %s", strings.Join(details, "; "))
}

type runtimeEligible []int

func (q runtimeEligible) Len() int           { return len(q) }
func (q runtimeEligible) Less(i, j int) bool { return q[i] < q[j] }
func (q runtimeEligible) Swap(i, j int)      { q[i], q[j] = q[j], q[i] }
func (q *runtimeEligible) Push(value any)    { *q = append(*q, value.(int)) }
func (q *runtimeEligible) Pop() any {
	last := len(*q) - 1
	value := (*q)[last]
	*q = (*q)[:last]
	return value
}
