package sandbox

import (
	"path/filepath"
	"slices"
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

func TestVolumeDataObservationsSkipInaccessibleInitialRoots(t *testing.T) {
	var allVolumeChecks []check
	for _, c := range checks {
		if c.capability == capVolume {
			allVolumeChecks = append(allVolumeChecks, c)
		}
	}
	for _, variant := range []string{"volume-root-not-writable", "volume-root-not-readable"} {
		t.Run(variant, func(t *testing.T) {
			rep := runAgainstFake(t, variant, allVolumeChecks...)
			require.False(t, rep.Failed(), "%s", rep)
			skippedLifetime := false
			skippedPersistence := false
			for _, finding := range rep.Findings {
				require.Equal(t, report.Skip, finding.Severity)
				if finding.Requirement == "volume@1/composition-independent" {
					skippedLifetime = true
				}
				if finding.Requirement == "volume@1/persists-across-recreate" {
					skippedPersistence = true
				}
			}
			require.True(t, skippedLifetime, "unobservable persistence must skip, not pass")
			require.True(t, skippedPersistence, "legacy persistence check must also skip")
		})
	}
}

func TestFakeVolumeInstallHooksRunOnlyAtCreate(t *testing.T) {
	a := adapter.New(filepath.Join("testdata", "fake-adapter"))
	a.Env = []string{"KIT_TCK_FAKE_STATE=" + t.TempDir(), "KIT_TCK_FAKE_CLAIMS=" + capVolume + "," + capLifecycle, "KIT_TCK_FAKE_BROKEN="}
	fixtures := Fixtures(FixtureDir)
	id, err := a.Create(t.Context(), []string{fixtures(fixtureWorkload), fixtures("volume-state-hooks")}, adapter.CreateOptions{})
	require.NoError(t, err)
	defer func() { require.NoError(t, a.Remove(t.Context(), id)) }()
	probe := func(operation, value string) string {
		result, err := a.Exec(t.Context(), id, "kit-tck-volume", operation, volumePath, "install", value)
		require.NoError(t, err)
		require.Zero(t, result.ExitCode, result.Stderr)
		return result.Stdout
	}
	require.Equal(t, "mounted", probe("read", ""))
	probe("write", "already-installed")
	require.NoError(t, a.Stop(t.Context(), id))
	require.NoError(t, a.Start(t.Context(), id))
	require.Equal(t, "already-installed", probe("read", ""))
	require.NoError(t, a.Recreate(t.Context(), id))
	require.Equal(t, "already-installed", probe("read", ""))
}

func TestVolumeTmpfsObservationsSkipInaccessibleFreshRoots(t *testing.T) {
	index := slices.IndexFunc(volumeChecks, func(c check) bool {
		return c.requirement == "volume@1/tmpfs-cleared"
	})
	require.GreaterOrEqual(t, index, 0)
	for _, event := range []string{"stop", "recreate"} {
		t.Run(event, func(t *testing.T) {
			rep := runAgainstFake(t, "volume-fresh-tmpfs-unreadable-"+event, volumeChecks[index])
			require.False(t, rep.Failed(), "%s", rep)
			require.Len(t, rep.Findings, 1)
			require.Equal(t, "volume@1/tmpfs-cleared", rep.Findings[0].Requirement)
			require.Equal(t, report.Skip, rep.Findings[0].Severity)
		})
	}
}

func TestVolumeRetainedContinuesPastUnreadableRenamedRoot(t *testing.T) {
	index := slices.IndexFunc(volumeChecks, func(c check) bool {
		return c.requirement == "volume@1/undeclared-retained"
	})
	require.GreaterOrEqual(t, index, 0)
	rep := runAgainstFake(t, "volume-renamed-root-not-readable", volumeChecks[index])
	require.False(t, rep.Failed(), "%s", rep)
	require.Len(t, rep.Findings, 1)
	require.Equal(t, report.Skip, rep.Findings[0].Severity)
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
