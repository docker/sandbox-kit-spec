package sandbox

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/docker/sandbox-kit-spec/v3/tck/adapter"
)

func TestExactVersionSelectionForPartialRuntimes(t *testing.T) {
	for _, claims := range [][]string{{capAgentContext}, {capAgentContextV2}, {capAgentContext, capAgentContextV2}, {groupVolume}} {
		t.Run(strings.Join(claims, ","), func(t *testing.T) {
			a := adapter.New(filepath.Join("testdata", "fake-adapter"))
			a.Env = []string{"KIT_TCK_FAKE_STATE=" + t.TempDir(), "KIT_TCK_FAKE_CLAIMS=" + strings.Join(claims, ","), "KIT_TCK_FAKE_BROKEN="}
			rep, err := runChecks(t.Context(), &Env{Adapter: a, Fixtures: Fixtures(FixtureDir)}, []check{{requirement: exactVersionRequirement, run: exactVersionSelection}})
			require.NoError(t, err)
			require.False(t, rep.Failed(), "%s", rep)
			require.Empty(t, rep.Findings, "the core selection check must run for every support combination")
		})
	}
}
