package sandbox

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/docker/sandbox-kit-spec/v3/tck/adapter"
)

func TestHostMountChecks(t *testing.T) {
	rep := runAgainstFake(t, "", hostMountChecks...)
	require.False(t, rep.Failed(), "%s", rep)
}

func TestHostMountDoesNotRequirePrivateVolumeSupport(t *testing.T) {
	for _, claims := range []string{capHostMount, capVolume} {
		t.Run(claims, func(t *testing.T) {
			a := adapter.New(filepath.Join("testdata", "fake-adapter"))
			a.Env = []string{"KIT_TCK_FAKE_STATE=" + t.TempDir(), "KIT_TCK_FAKE_CLAIMS=" + claims, "KIT_TCK_FAKE_BROKEN="}
			rep, err := runChecks(t.Context(), &Env{Adapter: a, Fixtures: Fixtures(FixtureDir)}, hostMountChecks)
			require.NoError(t, err)
			require.False(t, rep.Failed(), "%s", rep)
		})
	}
}

func TestHostMountCleanupPreservesOtherDirectories(t *testing.T) {
	a := adapter.New(filepath.Join("testdata", "fake-adapter"))
	a.Env = []string{"KIT_TCK_FAKE_STATE=" + t.TempDir(), "KIT_TCK_FAKE_BROKEN=", "KIT_TCK_FAKE_CLAIMS=" + capHostMount}
	e := &Env{Adapter: a, Fixtures: Fixtures(FixtureDir)}
	_, removeExisting, err := hostSandbox(t.Context(), e, "host-mount", "/var/tmp/existing-cache")
	require.NoError(t, err)
	removeExisting()
	path, cleanup := hostMountScope(t.Context(), e, "host-mount")
	_, removeTest, err := hostSandbox(t.Context(), e, "host-mount", path)
	require.NoError(t, err)
	removeTest()
	cleanup()
	require.Empty(t, e.hostMountLeaks)
	mounts, err := a.HostMounts(t.Context(), e.Fixtures("host-mount"))
	require.NoError(t, err)
	require.Len(t, mounts, 1, "cleanup removes only this run's destination")
	require.Equal(t, "/var/tmp/existing-cache", mounts[0].Path)
}
