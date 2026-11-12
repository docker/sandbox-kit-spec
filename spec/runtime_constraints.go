package spec

import (
	"container/heap"
	"context"
	"math/bits"
)

// runtimeBitMatrix stores a relation without allocating one object per pair.
// Only participating effective entries index it. Empty publication scopes and
// skipped declarations never become dimensions of this table.
type runtimeBitMatrix struct {
	words int
	data  []uint64
}

func newRuntimeBitMatrix(size int) runtimeBitMatrix {
	words := (size + 63) / 64
	return runtimeBitMatrix{words: words, data: make([]uint64, size*words)}
}

func (m runtimeBitMatrix) row(node int) []uint64 {
	return m.data[node*m.words : (node+1)*m.words]
}

func (m runtimeBitMatrix) set(from, to int) {
	m.row(from)[to/64] |= uint64(1) << uint(to%64)
}

func (m runtimeBitMatrix) has(from, to int) bool {
	return m.row(from)[to/64]&(uint64(1)<<uint(to%64)) != 0
}

// The transpose makes both orientations of displacement equally bounded.
// Repeated reverse graph scans otherwise turn each reachable edge into another
// full scan of all entries, even when a writer names only a single target.
type runtimeConstraintRelation struct {
	outgoing, incoming runtimeBitMatrix
}

func newRuntimeConstraintRelation(size int) runtimeConstraintRelation {
	return runtimeConstraintRelation{newRuntimeBitMatrix(size), newRuntimeBitMatrix(size)}
}

func (r runtimeConstraintRelation) set(from, to int) {
	r.outgoing.set(from, to)
	r.incoming.set(to, from)
}

// runtimeBoundaryConstraints retains normal, before and after provenance
// separately. Only normal edges may be displaced; a written edge remains even
// when another relationship requests the same pair's normal edge be removed.
type runtimeBoundaryConstraints struct {
	size                  int
	normal, before, after runtimeConstraintRelation
}

func newRuntimeBoundaryConstraints(size int) runtimeBoundaryConstraints {
	return runtimeBoundaryConstraints{size, newRuntimeConstraintRelation(size), newRuntimeConstraintRelation(size), newRuntimeConstraintRelation(size)}
}

// displace implements synchronous writer-only removal. The pending table is a
// snapshot's deletion set, reused between passes rather than a second expanded
// graph. No pass observes another relationship's removals from that same pass.
func (c runtimeBoundaryConstraints) displace(ctx context.Context) error {
	pending := newRuntimeBitMatrix(c.size)
	// Step 2 observes the initial normal relation. All removals are applied
	// together so edge enumeration order cannot affect the resulting constraints.
	displaced := make([]uint64, c.normal.outgoing.words)
	for writer := range c.size {
		if err := ctx.Err(); err != nil {
			return err
		}
		for _, direction := range []struct {
			before  bool
			normal  runtimeBitMatrix
			targets []uint64
		}{
			{true, c.normal.outgoing, c.before.outgoing.row(writer)},
			{false, c.normal.incoming, c.after.incoming.row(writer)},
		} {
			copy(displaced, direction.targets)
			for word, targets := range direction.targets {
				for targets != 0 {
					if err := ctx.Err(); err != nil {
						return err
					}
					target := word*64 + bits.TrailingZeros64(targets)
					targets &= targets - 1
					for at, others := range direction.normal.row(target) {
						displaced[at] |= others
					}
				}
			}
			for word, others := range displaced {
				for others != 0 {
					other := word*64 + bits.TrailingZeros64(others)
					others &= others - 1
					if direction.before {
						pending.set(other, writer)
					} else {
						pending.set(writer, other)
					}
				}
			}
		}
	}
	if err := c.applyRemovals(ctx, pending); err != nil {
		return err
	}
	// Step 3 expands displacement through remaining normal constraints and written
	// edges of the same orientation. Each pass observes one snapshot and applies
	// removals simultaneously. Written edges are never candidates for removal.
	reached := make([]bool, c.size)
	stack := make([]int, 0, c.size)
	for {
		changed := false
		clear(pending.data)
		for writer := range c.size {
			if err := ctx.Err(); err != nil {
				return err
			}
			for _, direction := range []struct {
				before bool
				normal runtimeBitMatrix
				edges  runtimeBitMatrix
			}{
				{true, c.normal.outgoing, c.before.outgoing},
				{false, c.normal.incoming, c.after.incoming},
			} {
				var targets []uint64
				if direction.before {
					targets = c.before.outgoing.row(writer)
				} else {
					targets = c.after.incoming.row(writer)
				}
				clear(reached)
				reached[writer] = true
				stack = stack[:0]
				// One traversal from all this writer's targets is the union of
				// their individual reachable sets under the same snapshot. A type
				// selector must not repeat a dense traversal for every target.
				for word, entries := range targets {
					for entries != 0 {
						target := word*64 + bits.TrailingZeros64(entries)
						entries &= entries - 1
						if !reached[target] {
							reached[target] = true
							stack = append(stack, target)
						}
					}
				}
				for len(stack) > 0 {
					if err := ctx.Err(); err != nil {
						return err
					}
					node := stack[len(stack)-1]
					stack = stack[:len(stack)-1]
					from, to := node, writer
					if !direction.before {
						from, to = writer, node
					}
					if c.normal.outgoing.has(from, to) {
						pending.set(from, to)
						changed = true
					}
					normal, written := direction.normal.row(node), direction.edges.row(node)
					for at := range normal {
						neighbors := normal[at] | written[at]
						for neighbors != 0 {
							next := at*64 + bits.TrailingZeros64(neighbors)
							neighbors &= neighbors - 1
							if !reached[next] {
								reached[next] = true
								stack = append(stack, next)
							}
						}
					}
				}
			}
		}
		if !changed {
			return ctx.Err()
		}
		if err := c.applyRemovals(ctx, pending); err != nil {
			return err
		}
	}
}

func (c runtimeBoundaryConstraints) applyRemovals(ctx context.Context, pending runtimeBitMatrix) error {
	for from := range c.size {
		if err := ctx.Err(); err != nil {
			return err
		}
		for word, entries := range pending.row(from) {
			for entries != 0 {
				to := word*64 + bits.TrailingZeros64(entries)
				entries &= entries - 1
				c.normal.incoming.row(to)[from/64] &^= uint64(1) << uint(from%64)
			}
			c.normal.outgoing.row(from)[word] &^= pending.row(from)[word]
		}
	}
	return nil
}

func (c runtimeBoundaryConstraints) successors(node int, word int) uint64 {
	return c.normal.outgoing.row(node)[word] | c.before.outgoing.row(node)[word] | c.after.outgoing.row(node)[word]
}

// order uses Kahn's algorithm with a min-heap of eligible composition positions.
// A FIFO queue would leave a newly eligible early declaration behind later
// declarations, violating the specified dependency/declaration tie-break.
func (c runtimeBoundaryConstraints) order(ctx context.Context) ([]int, []int, error) {
	indegree := make([]int, c.size)
	for node := range c.size {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		for word := range c.normal.outgoing.words {
			entries := c.successors(node, word)
			for entries != 0 {
				target := word*64 + bits.TrailingZeros64(entries)
				entries &= entries - 1
				indegree[target]++
			}
		}
	}
	var order []int
	visited := make([]bool, c.size)
	eligible := &runtimeEligible{}
	for node := range c.size {
		if indegree[node] == 0 {
			heap.Push(eligible, node)
		}
	}
	for eligible.Len() > 0 {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		next := heap.Pop(eligible).(int)
		visited[next] = true
		order = append(order, next)
		for word := range c.normal.outgoing.words {
			entries := c.successors(next, word)
			for entries != 0 {
				target := word*64 + bits.TrailingZeros64(entries)
				entries &= entries - 1
				indegree[target]--
				if indegree[target] == 0 {
					heap.Push(eligible, target)
				}
			}
		}
	}
	if len(order) != c.size {
		cycle, err := c.cycle(ctx, visited)
		return nil, cycle, err
	}
	return order, nil, nil
}

// cycle walks the compact adjacency directly. An explicit stack also bounds
// the Go call stack when a valid small descriptor contains a long cycle.
func (c runtimeBoundaryConstraints) cycle(ctx context.Context, completed []bool) ([]int, error) {
	type frame struct {
		node, word int
		remaining  uint64
	}
	active := make([]bool, c.size)
	// Iterating nodes, rather than map keys, keeps cycle diagnostics deterministic.
	for start := range c.size {
		if completed[start] {
			continue
		}
		active[start], completed[start] = true, true
		stack := []frame{{node: start}}
		for len(stack) > 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			top := &stack[len(stack)-1]
			if top.remaining == 0 {
				if top.word == c.normal.outgoing.words {
					active[top.node] = false
					stack = stack[:len(stack)-1]
					continue
				}
				top.remaining = c.successors(top.node, top.word)
				top.word++
				continue
			}
			next := (top.word-1)*64 + bits.TrailingZeros64(top.remaining)
			top.remaining &= top.remaining - 1
			if active[next] {
				var cycle []int
				for _, item := range stack {
					if item.node == next || len(cycle) != 0 {
						cycle = append(cycle, item.node)
					}
				}
				return append(cycle, next), nil
			}
			if !completed[next] {
				active[next], completed[next] = true, true
				stack = append(stack, frame{node: next})
			}
		}
	}
	return nil, nil
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
