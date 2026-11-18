package spec

import (
	"context"
	"encoding/json"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRuntimeGraphKeepsPairwiseConstraintsCompact(t *testing.T) {
	const count = 1000
	hook := runtimeTestHook(t, "", true, false)
	descriptor := runtimeTestDescriptor()
	for range count {
		descriptor.Capabilities = append(descriptor.Capabilities, hook)
	}
	raw, err := json.Marshal(descriptor)
	require.NoError(t, err)
	require.Less(t, len(raw), SizeErrorBytes)
	_, err = ValidateExpandedDeclarations(raw, descriptor)
	require.NoError(t, err)
	graph, err := newRuntimeGraph([]Contribution{{Descriptor: descriptor}})
	require.NoError(t, err)
	boundary := ExecutionBoundary{PhaseInstall, StepExecute}
	fragments := make([]runtimeFragment, count)
	for i, node := range graph.nodes {
		fragments[i] = runtimeFragment{entry: i, capability: node.capability, participation: []ExecutionBoundary{boundary}}
	}
	// Selection intentionally supplies a detached descriptor to each callback.
	// Measure only graph planning here, so that safety boundary does not obscure
	// the pairwise-edge amplification this regression protects against.
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	order, err := graph.boundaryOrder(t.Context(), boundary, fragments, nil)
	runtime.ReadMemStats(&after)
	require.NoError(t, err)
	require.Len(t, order, count)
	for i, node := range order {
		require.Equal(t, i, node)
	}
	// This generous ceiling permits representation changes while rejecting the
	// hundreds of megabytes consumed by expanded edge records for this input.
	require.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(32*1024*1024))
}

func TestRuntimeGraphWrittenTypeTargetsStayCompact(t *testing.T) {
	const count = 1000
	hook := runtimeTestHook(t, "", true, false)
	hook.Before = []string{CapabilityLifecycle}
	descriptor := runtimeTestDescriptor()
	for range count {
		descriptor.Capabilities = append(descriptor.Capabilities, hook)
	}
	graph, err := newRuntimeGraph([]Contribution{{Descriptor: descriptor}})
	require.NoError(t, err)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	relations, _, err := graph.writtenEdges(t.Context())
	runtime.ReadMemStats(&after)
	require.NoError(t, err)
	require.Len(t, relations, count)
	// A type selector remains one authored relationship even though it names
	// every lifecycle declaration. Validation must not retain its Cartesian
	// expansion before selection has had a chance to skip any of the entries.
	require.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(32*1024*1024))
}

func TestRuntimeGraphHonorsCancellationDuringDisplacement(t *testing.T) {
	const count = 1000
	constraints := newRuntimeBoundaryConstraints(count)
	for from := range count {
		for to := range count {
			if from < to {
				constraints.normal.set(from, to)
			} else if from > to {
				constraints.before.set(from, to)
			}
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Millisecond)
	defer cancel()
	// Dense target lists exercise the expensive part after graph construction.
	// Cancellation must stop that work instead of waiting for the fixed point.
	err := constraints.displace(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestRuntimeBroadSelfTargetDoesNotOrderSplitGrants(t *testing.T) {
	for _, before := range []bool{true, false} {
		t.Run(map[bool]string{true: "before", false: "after"}[before], func(t *testing.T) {
			both := runtimeTestCapability(t, CapabilitySSHAgent, "both", SSHAgent{Phase: Phases{"install", "runtime"}})
			if before {
				both.Before = []string{CapabilitySSHAgent}
			} else {
				both.After = []string{CapabilitySSHAgent}
			}
			runtimeOnly := runtimeTestCapability(t, CapabilitySSHAgent, "runtime", SSHAgent{Phase: Phases{"runtime"}})
			first := runtimeTestDescriptor(both)
			second := runtimeTestDescriptor(runtimeOnly)
			second.Kind = KindMixin
			// Reconciliation splits the first declaration into install and
			// runtime requests. Its broad target excludes its original self,
			// rather than inventing two contradictory edges between those requests.
			result, err := ComposeRuntime(t.Context(), []Contribution{{Descriptor: first}, {Descriptor: second}}, RuntimeOptions{Select: Supported(KnownCapabilities()...)})
			require.NoError(t, err)
			require.Len(t, runtimeTestActions(result.Plan, PhaseInstall, StepAttach), 1)
			require.Len(t, runtimeTestActions(result.Plan, PhaseStart, StepAttach), 1)
		})
	}
}
