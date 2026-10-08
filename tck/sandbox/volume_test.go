package sandbox

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/docker/sandbox-kit-spec/v3/tck/adapter"
	"github.com/docker/sandbox-kit-spec/v3/tck/report"
)

func TestVolumeChecks(t *testing.T) {
	rep := runAgainstFake(t, "", volumeChecks...)
	require.False(t, rep.Failed(), "%s", rep)
}

func TestVolumeChecksDoNotRequireMountRootOwnership(t *testing.T) {
	rep := runAgainstFake(t, "volume-root-not-owned", volumeChecks...)
	require.False(t, rep.Failed(), "%s", rep)
}

func TestVolumeLifetimeNeedsNoOtherCapabilities(t *testing.T) {
	a := adapter.New(filepath.Join("testdata", "fake-adapter"))
	a.Env = []string{"KIT_TCK_FAKE_STATE=" + t.TempDir(), "KIT_TCK_FAKE_CLAIMS=" + capVolume, "KIT_TCK_FAKE_BROKEN="}
	rep, err := runChecks(t.Context(), &Env{Adapter: a, Fixtures: Fixtures(FixtureDir)}, volumeChecks)
	require.NoError(t, err)
	require.False(t, rep.Failed(), "%s", rep)
	for _, finding := range rep.Findings {
		require.Equal(t, "volume@1/mounted-before-hooks", finding.Requirement)
		require.Equal(t, report.Skip, finding.Severity)
	}
}
