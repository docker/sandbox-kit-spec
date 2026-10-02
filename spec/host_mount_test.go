package spec

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func hostMountDescriptor(config map[string]any) *Descriptor {
	return &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{{Type: CapabilityHostMount, Config: config}}}
}

func TestHostMountGrammar(t *testing.T) {
	for _, mode := range []string{"", "755", "0755", "1777"} {
		d := hostMountDescriptor(map[string]any{"path": "/cache", "mode": mode})
		if mode == "" {
			delete(d.Capabilities[0].Config, "mode")
		}
		_, err := Validate(d)
		require.NoError(t, err)
		mounts, err := HostMountsOf(d.Capabilities)
		require.NoError(t, err)
		require.Equal(t, []HostMount{{Path: "/cache", Mode: mode}}, mounts)
	}
	for _, config := range []map[string]any{
		nil, {}, {"path": "relative"}, {"path": "/"},
		{"path": "/cache/"}, {"path": "/a/../cache"}, {"path": "//cache"}, {"path": "/a/./cache"}, {"path": "/ca\x00che"},
		{"path": "/cache", "mode": "888"}, {"path": "/cache", "mode": 755},
		{"path": "/cache", "mode": ""}, {"path": "/cache", "mode": nil},
		{"path": "/cache", "hostPath": "/etc"}, {"path": "/cache", "size": "2g"}, {"path": "/cache", "tmpfs": true},
	} {
		_, err := Validate(hostMountDescriptor(config))
		require.Error(t, err, "config %#v", config)
	}
	d := hostMountDescriptor(map[string]any{"path": "/cache"})
	d.Capabilities = append(d.Capabilities, Capability{Type: CapabilityHostMount, Config: map[string]any{"path": "/cache", "mode": "0700"}})
	_, err := Validate(d)
	require.ErrorContains(t, err, "already declared")
	_, err = HostMountsOf([]Capability{{Type: CapabilityHostMount, Config: map[string]any{"hostPath": "/etc"}}})
	require.ErrorContains(t, err, "unknown field")
}

func TestHostMountStorageConflicts(t *testing.T) {
	for _, secondType := range []string{CapabilityHostMount, CapabilityVolume} {
		for _, reverse := range []bool{false, true} {
			first := hostMountDescriptor(map[string]any{"path": "/cache"})
			second := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{{Type: secondType, Config: map[string]any{"path": "/cache"}}}}
			if reverse {
				first, second = second, first
			}
			d := *first
			d.Capabilities = append(append([]Capability{}, first.Capabilities...), second.Capabilities...)
			_, err := Validate(&d)
			require.Error(t, err)
			contributions := []Contribution{{Reference: "one", Descriptor: first}, {Reference: "two", Descriptor: second}}
			_, err = Merge(contributions, MergeOptions{})
			require.ErrorContains(t, err, "one owner")
			_, err = Compose(contributions)
			require.ErrorContains(t, err, "one owner")
		}
	}
	// Existing volume spelling aliases cannot evade the shared storage key.
	for _, reverse := range []bool{false, true} {
		host := hostMountDescriptor(map[string]any{"path": "/cache"})
		volume := &Descriptor{Kind: KindMixin, Capabilities: []Capability{{Type: CapabilityVolume, Config: map[string]any{"path": "/data/../cache"}}}}
		parts := []Contribution{{Reference: "host", Descriptor: host}, {Reference: "volume", Descriptor: volume}}
		if reverse {
			parts[0], parts[1] = parts[1], parts[0]
		}
		_, err := Compose(parts)
		require.ErrorContains(t, err, "one owner")
	}
	out, err := Compose([]Contribution{{Reference: "one", Descriptor: hostMountDescriptor(map[string]any{"path": "/cache"})}, {Reference: "two", Descriptor: hostMountDescriptor(map[string]any{"path": "/models"})}})
	require.NoError(t, err)
	mounts, err := HostMountsOf(out.Capabilities)
	require.NoError(t, err)
	require.Len(t, mounts, 2)
}

func TestHostMountSelectionAndExpansion(t *testing.T) {
	d := hostMountDescriptor(map[string]any{"path": "/cache"})
	_, err := SelectCapabilities(t.Context(), d, Supported(CapabilityVolume))
	require.Error(t, err, "support for private volumes cannot satisfy host sharing")
	d.Capabilities[0].Optional = true
	selection, err := SelectCapabilities(t.Context(), d, Supported(CapabilityVolume))
	require.NoError(t, err)
	require.Empty(t, selection.Capabilities)
	require.Len(t, selection.Skipped, 1)

	d.Capabilities = []Capability{{Group: &CapabilityGroup{Optional: true, Capabilities: []Capability{
		{Type: CapabilityHostMount, Config: map[string]any{"path": "${{ kit.env.HOME }}/.cache/pip"}},
		groupHook("populate cache"),
	}}}}
	d, err = ExpandEnvironment(d, map[string]string{"HOME": "/home/agent"})
	require.NoError(t, err)
	selection, err = SelectCapabilities(t.Context(), d, Supported(CapabilityHostMount, CapabilityLifecycle))
	require.NoError(t, err)
	mounts, err := HostMountsOf(selection.Capabilities)
	require.NoError(t, err)
	require.Equal(t, "/home/agent/.cache/pip", mounts[0].Path)
	selection, err = SelectCapabilities(t.Context(), d, Supported(CapabilityLifecycle))
	require.NoError(t, err)
	require.Empty(t, selection.Capabilities, "a skipped sharing group contributes no hooks")

	d.Capabilities[0].Group.Capabilities[0].Config["path"] = "relative"
	_, err = SelectCapabilities(t.Context(), d, Supported(CapabilityLifecycle))
	require.Error(t, err, "unsupported optional groups still validate")
}

func TestHostMountPermissionSurface(t *testing.T) {
	d := hostMountDescriptor(map[string]any{"path": "/cache", "mode": "0755"})
	host := SurfaceOf(d)
	require.Equal(t, []string{"/cache"}, host.HostMountPaths)
	require.Empty(t, host.StoragePaths)
	private := Surface{StoragePaths: []string{"/cache"}}
	widening := DiffWidenings(private, host)
	require.Len(t, widening, 1)
	require.Equal(t, "host-mount", widening[0].Category)
	d.Capabilities[0].Config["mode"] = "0700"
	require.Empty(t, DiffWidenings(host, SurfaceOf(d)))
	d.Capabilities[0].Config["path"] = "/models"
	require.NotEmpty(t, DiffWidenings(host, SurfaceOf(d)))
	d.Capabilities[0].Config["hostPath"] = "/etc"
	require.NotEmpty(t, SurfaceOf(d).Services, "malformed known configs cannot disappear from consent")
}

func TestHostMountConflictDependsOnSelectedGroups(t *testing.T) {
	d := hostMountDescriptor(map[string]any{"path": "/cache"})
	d.Capabilities = append(d.Capabilities, Capability{Group: &CapabilityGroup{Optional: true, Capabilities: []Capability{
		{Type: CapabilityVolume, Config: map[string]any{"path": "/cache"}},
	}}})
	for _, supported := range [][]string{{CapabilityHostMount}, {CapabilityHostMount, CapabilityVolume}} {
		selection, err := SelectCapabilities(t.Context(), d, Supported(supported...))
		require.NoError(t, err)
		selected := *d
		selected.Capabilities = selection.Capabilities
		_, err = Compose([]Contribution{{Reference: "selected", Descriptor: &selected}})
		if len(supported) == 1 {
			require.NoError(t, err, "an unavailable optional volume contributes no conflicting path")
		} else {
			require.ErrorContains(t, err, "one owner", "selecting a conflicting group cannot silently change storage kind")
		}
	}
}
