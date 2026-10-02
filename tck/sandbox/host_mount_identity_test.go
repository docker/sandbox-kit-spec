package sandbox

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/docker/sandbox-kit-spec/v3/tck/adapter"
)

func TestHostMountVersionListingsResolveOneIdentity(t *testing.T) {
	a := adapter.New(filepath.Join("testdata", "fake-adapter"))
	a.Env = []string{"KIT_TCK_FAKE_STATE=" + t.TempDir(), "KIT_TCK_FAKE_BROKEN=", "KIT_TCK_FAKE_CLAIMS=" + capHostMount}
	e := &Env{Adapter: a, Fixtures: Fixtures(FixtureDir), Claimed: map[string]bool{capHostMount: true}}
	scope := hostMountScope(t.Context(), e, "host-mount-version-v1", "host-mount-version-v2")
	cleanup := sync.OnceFunc(scope.cleanup)
	defer cleanup()
	for _, kit := range []string{"host-mount-version-v1", "host-mount-version-v2"} {
		_, remove, err := scope.create(kit, nil)
		require.NoError(t, err)
		remove()
	}
	first, err := a.HostMounts(t.Context(), e.Fixtures("host-mount-version-v1"))
	require.NoError(t, err)
	second, err := a.HostMounts(t.Context(), e.Fixtures("host-mount-version-v2"))
	require.NoError(t, err)
	require.Len(t, first, 1)
	require.Equal(t, first, second, "each version reference must list the shared repository's directory")
	cleanup()
	require.Empty(t, e.hostMountLeaks, "cleanup must remove one shared handle without reporting its aliases as leaks")
	for _, kit := range []string{"host-mount-version-v1", "host-mount-version-v2"} {
		mounts, err := a.HostMounts(t.Context(), e.Fixtures(kit))
		require.NoError(t, err)
		require.Empty(t, mounts)
	}
}
