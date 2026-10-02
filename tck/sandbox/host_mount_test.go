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

func TestHostMountModeDoesNotRequireGuestChmod(t *testing.T) {
	rep := runAgainstFake(t, "host-mount-no-guest-chmod", check{
		requirement: "host-mount@1/initial-mode-applied",
		capability:  capHostMount,
		run:         hostMountRoot("mode", "", "700\n"),
	})
	require.False(t, rep.Failed(), "applying initial mode does not require chmod from the guest: %s", rep)
}

func hostMountTestEnv(t *testing.T, broken string) *Env {
	t.Helper()
	a := adapter.New(filepath.Join("testdata", "fake-adapter"))
	a.Env = []string{"KIT_TCK_FAKE_STATE=" + t.TempDir(), "KIT_TCK_FAKE_BROKEN=" + broken, "KIT_TCK_FAKE_CLAIMS=" + capHostMount}
	return &Env{Adapter: a, Fixtures: Fixtures(FixtureDir), Claimed: map[string]bool{capHostMount: true}}
}

func TestHostMountCleanupReportsHiddenProvision(t *testing.T) {
	e := hostMountTestEnv(t, "host-mount-not-listed")
	scope := hostMountScope(t.Context(), e, "host-mount")
	_, remove, err := scope.create("host-mount", nil)
	require.NoError(t, err)
	remove()
	scope.cleanup()
	require.Len(t, e.hostMountLeaks, 1, "a hidden provisioned directory cannot be treated as removed")
	require.Contains(t, e.hostMountLeaks[0], scope.path)
}

func TestHostMountCleanupWithoutProvision(t *testing.T) {
	for _, secondKit := range []string{"", "host-mount-other", "host-mount-volume"} {
		t.Run(secondKit, func(t *testing.T) {
			e := hostMountTestEnv(t, "")
			scope := hostMountScope(t.Context(), e, "host-mount")
			if secondKit != "" {
				_, remove, err := e.sandbox(t.Context(), []string{fixtureWorkload, "host-mount", secondKit}, map[string]string{"mount_path": scope.path})
				remove()
				var refused *adapter.RefusedError
				require.ErrorAs(t, err, &refused)
			}
			scope.cleanup()
			require.Empty(t, e.hostMountLeaks, "a refused request does not provision a directory")
		})
	}
}

func TestHostMountCleanupForgetsExplicitRemoval(t *testing.T) {
	e := hostMountTestEnv(t, "")
	scope := hostMountScope(t.Context(), e, "host-mount")
	_, remove, err := scope.create("host-mount", nil)
	require.NoError(t, err)
	remove()
	mount, err := hostMountRecord(t.Context(), e, "host-mount", scope.path)
	require.NoError(t, err)
	require.NoError(t, e.Adapter.RemoveHostMount(t.Context(), mount.ID))
	scope.forget("host-mount", scope.path)
	scope.cleanup()
	require.Empty(t, e.hostMountLeaks, "explicitly removed directories do not need another listing")
}

func TestHostMountCleanupPreservesOtherDirectories(t *testing.T) {
	e := hostMountTestEnv(t, "")
	_, removeExisting, err := hostSandbox(t.Context(), e, "host-mount", "/var/tmp/existing-cache")
	require.NoError(t, err)
	removeExisting()
	scope := hostMountScope(t.Context(), e, "host-mount")
	other, removeOther, err := hostSandbox(t.Context(), e, "host-mount-other", scope.path)
	require.NoError(t, err)
	require.Empty(t, hostProbe(t.Context(), e, other, "write", scope.path, "marker", "other-kit-data", ""))
	removeOther()
	_, removeTest, err := scope.create("host-mount", nil)
	require.NoError(t, err)
	removeTest()
	scope.cleanup()
	require.Empty(t, e.hostMountLeaks)
	mounts, err := e.Adapter.HostMounts(t.Context(), e.Fixtures("host-mount"))
	require.NoError(t, err)
	require.Len(t, mounts, 1, "cleanup removes only this run's destination")
	require.Equal(t, "/var/tmp/existing-cache", mounts[0].Path)
	otherMounts, err := e.Adapter.HostMounts(t.Context(), e.Fixtures("host-mount-other"))
	require.NoError(t, err)
	require.Len(t, otherMounts, 1, "another Kit's directory at the same destination is preserved")
	require.Equal(t, scope.path, otherMounts[0].Path)
	res, err := e.Adapter.ReadHostMount(t.Context(), otherMounts[0].ID, "marker")
	require.NoError(t, err)
	require.Zero(t, res.ExitCode)
	require.Equal(t, "other-kit-data", res.Stdout)
}
