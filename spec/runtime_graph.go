package spec

import (
	"container/heap"
	"context"
	"fmt"
	"slices"
	"strings"
)

// runtimeNode keeps one original declaration, including declarations skipped by
// selection. Dropping a skipped node would also drop authored edges through it.
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
	nodes     []runtimeNode
	scopes    [][]int
	providers map[string][]int
}

// runtimeEdge retains the authored relationship or contract default responsible
// for an edge, so a cycle can explain which declarations need to change.
type runtimeEdge struct {
	from, to int
	reason   string
}

// runtimeFragment is one reconciled request and its scheduled effects. Several
// fragments can share an entry when reconciliation splits overlapping grants.
// Keeping fragments separate runs each effective request once without inventing
// several ordering positions for the same original declaration.
type runtimeFragment struct {
	capability Capability
	entry      int
	id         string
	effects    []PlannedEffect
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

// runtimeRoot follows the reconciliation union and compresses its lookup path.
// Nil parents is the pre-reconciliation graph, where every declaration is a node.
func runtimeRoot(parents []int, index int) int {
	if parents == nil {
		return index
	}
	root := index
	for parents[root] != root {
		root = parents[root]
	}
	for parents[index] != index {
		next := parents[index]
		parents[index] = root
		index = next
	}
	return root
}

// writtenEdges runs both before and after reconciliation. Broad targets exclude
// their writing entry; an explicit ID edge collapsed into that entry instead
// becomes contradictory. Skipped nodes retain their authored relationships.
func (g *runtimeGraph) writtenEdges(parents []int) ([]runtimeEdge, []string, error) {
	var edges []runtimeEdge
	var warnings []string
	seen := map[[2]int]bool{}
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
					from, to := runtimeRoot(parents, i), runtimeRoot(parents, j)
					if from == to {
						if target.id != "" {
							return nil, nil, fmt.Errorf("%s: %s %q is a self-reference after reconciliation", g.label(i), relation.name, raw)
						}
						continue
					}
					if relation.name == "after" {
						from, to = to, from
					}
					key := [2]int{from, to}
					if !seen[key] {
						seen[key] = true
						edges = append(edges, runtimeEdge{from: from, to: to, reason: fmt.Sprintf("%s %s %q", g.label(i), relation.name, raw)})
					}
				}
			}
		}
	}
	return edges, warnings, nil
}

// fragments groups connected origins before lowering effective requests. For
// example, one grant can contribute to two phase-specific requests that each
// merge with another grant. All connected origins need one ordering position.
// The smallest original index is always the root, preserving the first
// contributor's tie-break position and every contributor's ID binding.
func (g *runtimeGraph) fragments(ctx context.Context, capabilities []Capability, contracts map[string]ExecutionContract) ([]runtimeFragment, []int, error) {
	parents := make([]int, len(g.nodes))
	for i := range parents {
		parents[i] = i
	}
	for _, c := range capabilities {
		if len(c.executionOrigins) == 0 {
			return nil, nil, fmt.Errorf("effective capability %s has no reconciliation origins", c.Type)
		}
		first := c.executionOrigins[0]
		for _, origin := range c.executionOrigins {
			if origin < 0 || origin >= len(g.nodes) || !g.nodes[origin].selected {
				return nil, nil, fmt.Errorf("effective capability %s has invalid reconciliation origin %d", c.Type, origin)
			}
			left, right := runtimeRoot(parents, first), runtimeRoot(parents, origin)
			if left != right {
				parents[max(left, right)] = min(left, right)
			}
		}
	}
	fragments := make([]runtimeFragment, 0, len(capabilities))
	for i, c := range capabilities {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		contract, known := contracts[c.Type]
		if !known {
			return nil, nil, fmt.Errorf("effective capability %s has no execution contract", c.Type)
		}
		var effects []PlannedEffect
		if contract.Effects != nil {
			// Registrations are trusted to describe execution, but a callback
			// still receives its own copy so it cannot rewrite the composition.
			copy, err := cloneRuntimeCapability(c)
			if err != nil {
				return nil, nil, err
			}
			effects, err = contract.Effects(copy)
			if err != nil {
				return nil, nil, fmt.Errorf("execution contract %s: %w", c.Type, err)
			}
			effects = slices.Clone(effects)
		}
		seen := map[string]bool{}
		for _, effect := range effects {
			if err := validateRuntimeEffect(effect); err != nil {
				return nil, nil, fmt.Errorf("execution contract %s: %w", c.Type, err)
			}
			key := string(effect.Phase) + "/" + string(effect.Step) + "/" + effect.Action
			if seen[key] {
				return nil, nil, fmt.Errorf("execution contract %s repeats effect %s", c.Type, key)
			}
			seen[key] = true
		}
		fragments = append(fragments, runtimeFragment{capability: c, entry: runtimeRoot(parents, c.executionOrigins[0]),
			id: fmt.Sprintf("effective[%d]", i), effects: effects})
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	return fragments, parents, nil
}

// plan validates the reconciled written graph before adding defaults. A written
// cycle is never repaired by defaults; a cycle introduced by defaults is also an
// error. No schedule escapes either failure path.
func (g *runtimeGraph) plan(ctx context.Context, capabilities []Capability, contracts map[string]ExecutionContract, workspace bool) (ExecutionPlan, []string, error) {
	fragments, parents, err := g.fragments(ctx, capabilities, contracts)
	if err != nil {
		return ExecutionPlan{}, nil, err
	}
	written, _, err := g.writtenEdges(parents)
	if err != nil {
		return ExecutionPlan{}, nil, err
	}
	if _, err := g.order(written, parents); err != nil {
		return ExecutionPlan{}, nil, err
	}
	edges, warnings := g.defaultEdges(written, fragments, contracts)
	order, err := g.order(edges, parents)
	if err != nil {
		return ExecutionPlan{}, nil, err
	}
	return g.schedule(order, parents, fragments, workspace), warnings, nil
}

func runtimeHasEffect(fragment runtimeFragment, ordering OrderingDefault, action string) bool {
	for _, effect := range fragment.effects {
		if effect.Phase == ordering.Phase && effect.Step == ordering.Step && (action == "" || effect.Action == action) {
			return true
		}
	}
	return false
}

// defaultEdges only connects active effects in the same phase and step. An
// explicit opposite edge overrides that pair's default. An indirect opposite
// path does not override it: the resulting cycle must be reported to the author.
func (g *runtimeGraph) defaultEdges(written []runtimeEdge, fragments []runtimeFragment, contracts map[string]ExecutionContract) ([]runtimeEdge, []string) {
	edges := slices.Clone(written)
	explicit := map[[2]int]bool{}
	added := map[[2]int]bool{}
	for _, edge := range written {
		explicit[[2]int{edge.from, edge.to}] = true
		added[[2]int{edge.from, edge.to}] = true
	}
	var warnings []string
	warned := map[[2]int]bool{}
	byType := map[string][]runtimeFragment{}
	for _, fragment := range fragments {
		byType[fragment.capability.Type] = append(byType[fragment.capability.Type], fragment)
	}
	for _, source := range fragments {
		for _, ordering := range contracts[source.capability.Type].Defaults {
			if !runtimeHasEffect(source, ordering, ordering.Action) {
				continue
			}
			typ := ordering.BeforeType
			if typ == "" {
				typ = ordering.AfterType
			}
			for _, target := range byType[typ] {
				if source.entry == target.entry || !runtimeHasEffect(target, ordering, ordering.TargetAction) {
					continue
				}
				from, to := source.entry, target.entry
				if ordering.AfterType != "" {
					from, to = to, from
				}
				key := [2]int{from, to}
				if explicit[[2]int{to, from}] {
					if !warned[key] {
						warnings = append(warnings, fmt.Sprintf("written relationship %s before %s overrides %s default in %s/%s", g.label(to), g.label(from), source.capability.Type, ordering.Phase, ordering.Step))
						warned[key] = true
					}
					continue
				}
				if !added[key] {
					added[key] = true
					edges = append(edges, runtimeEdge{from: from, to: to, reason: fmt.Sprintf("%s default in %s/%s", source.capability.Type, ordering.Phase, ordering.Step)})
				}
			}
		}
	}
	return edges, warnings
}

// order uses Kahn's algorithm with a min-heap of eligible original positions.
// A FIFO queue would leave a newly eligible early declaration behind later
// declarations, violating the specified dependency/declaration tie-break.
func (g *runtimeGraph) order(edges []runtimeEdge, parents []int) ([]int, error) {
	indegree := make([]int, len(g.nodes))
	outgoing := make([][]int, len(g.nodes))
	for i, edge := range edges {
		indegree[edge.to]++
		outgoing[edge.from] = append(outgoing[edge.from], i)
	}
	var roots []int
	for i := range g.nodes {
		if runtimeRoot(parents, i) == i {
			roots = append(roots, i)
		}
	}
	var order []int
	visited := make([]bool, len(g.nodes))
	eligible := &runtimeEligible{}
	for _, root := range roots {
		if indegree[root] == 0 {
			heap.Push(eligible, root)
		}
	}
	for eligible.Len() > 0 {
		next := heap.Pop(eligible).(int)
		visited[next] = true
		order = append(order, next)
		for _, edge := range outgoing[next] {
			target := edges[edge].to
			indegree[target]--
			if indegree[target] == 0 {
				heap.Push(eligible, target)
			}
		}
	}
	if len(order) != len(roots) {
		return nil, g.cycleError(edges, outgoing, roots, visited)
	}
	return order, nil
}

// cycleError reports an actual cycle in the unscheduled remainder. Reporting
// every blocked node would also blame entries merely downstream of the cycle.
func (g *runtimeGraph) cycleError(edges []runtimeEdge, outgoing [][]int, roots []int, completed []bool) error {
	active := make([]bool, len(g.nodes))
	visited := slices.Clone(completed)
	var path []int
	var cycle []int
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
	for _, root := range roots {
		if !visited[root] && visit(root) {
			break
		}
	}
	details := make([]string, 0, len(cycle))
	for _, index := range cycle {
		edge := edges[index]
		details = append(details, fmt.Sprintf("%s -> %s [%s]", g.label(edge.from), g.label(edge.to), edge.reason))
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
