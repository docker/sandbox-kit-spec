package sandbox

import (
	"path/filepath"
	"testing"

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
