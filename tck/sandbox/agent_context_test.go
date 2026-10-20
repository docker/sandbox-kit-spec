package sandbox

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/docker/sandbox-kit-spec/v3/tck/adapter"
)

func TestInlineContextNeedsNoHelperCapabilities(t *testing.T) {
	a := adapter.New(filepath.Join("testdata", "fake-adapter"))
	a.Env = []string{"KIT_TCK_FAKE_STATE=" + t.TempDir(), "KIT_TCK_FAKE_CLAIMS=" + capAgentContextV2, "KIT_TCK_FAKE_BROKEN="}
	selected := check{requirement: "agent-context@2/materialized", capability: capAgentContextV2, run: inlineContextMaterialized}
	rep, err := runChecks(t.Context(), &Env{Adapter: a, Fixtures: Fixtures(FixtureDir)}, []check{selected})
	require.NoError(t, err)
	require.False(t, rep.Failed(), "%s", rep)
	require.Empty(t, rep.Findings, "the inline-context check must run without helper claims")
}

func TestInlineContextWaitsForLaunchSnapshots(t *testing.T) {
	a := adapter.New(filepath.Join("testdata", "fake-adapter"))
	a.Env = []string{
		"KIT_TCK_FAKE_STATE=" + t.TempDir(), "KIT_TCK_FAKE_CLAIMS=" + capAgentContextV2,
		"KIT_TCK_FAKE_BROKEN=", "KIT_TCK_FAKE_CONTEXT_READY_DELAY=0.5",
	}
	selected := check{requirement: "agent-context@2/materialized", capability: capAgentContextV2, run: inlineContextMaterialized}
	rep, err := runChecks(t.Context(), &Env{Adapter: a, Fixtures: Fixtures(FixtureDir)}, []check{selected})
	require.NoError(t, err)
	require.False(t, rep.Failed(), "Create may return before launch snapshots are published: %s", rep)
}

func TestInlineContextSnapshotWaitHonorsDeadline(t *testing.T) {
	a := adapter.New(filepath.Join("testdata", "fake-adapter"))
	a.Env = []string{
		"KIT_TCK_FAKE_STATE=" + t.TempDir(), "KIT_TCK_FAKE_CLAIMS=" + capAgentContextV2,
		"KIT_TCK_FAKE_BROKEN=", "KIT_TCK_FAKE_CONTEXT_READY_DELAY=60",
	}
	id, err := a.Create(t.Context(), []string{filepath.Join(FixtureDir, "context-v2-workload")}, adapter.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, a.Remove(context.Background(), id)) })
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	failure := waitInlineContextSnapshots(ctx, &Env{Adapter: a}, id)
	require.NotNil(t, failure)
	require.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
	require.Contains(t, failure.Detail, "wait for launch snapshots")
}

func TestFakeInlineContextMatchesFixtureNotParentPath(t *testing.T) {
	a := adapter.New(filepath.Join("testdata", "fake-adapter"))
	state := t.TempDir()
	a.Env = []string{
		"KIT_TCK_FAKE_STATE=" + state, "KIT_TCK_FAKE_CLAIMS=" + capAgentContext, "KIT_TCK_FAKE_BROKEN=",
	}
	for _, fixture := range []string{"workload", "context-v2-unrelated"} {
		kit := filepath.Join(t.TempDir(), "context-v2-checkout", "fixtures", fixture)
		id, err := a.Create(t.Context(), []string{kit}, adapter.CreateOptions{})
		require.NoError(t, err, "only the four inline-context fixture basenames require @2")
		require.NotEmpty(t, id)
		require.NoFileExists(t, filepath.Join(state, "context-v2", id+".json"))
		require.NoError(t, a.Remove(t.Context(), id))
	}
}
