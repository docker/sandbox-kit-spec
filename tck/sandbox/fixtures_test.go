package sandbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/docker/sandbox-kit-spec/v3/spec"
)

// Every fixture the suite composes must be a valid kit, or a conformance
// failure could be the fixture's fault rather than the runtime's.
func TestFixturesAreValidKits(t *testing.T) {
	matches, err := filepath.Glob(filepath.Join("testdata", "fixtures", "*", "*.yaml"))
	require.NoError(t, err)
	require.Len(t, matches, 87, "every fixture directory needs its descriptor")

	for _, path := range matches {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			require.NoError(t, err)
			d, err := spec.Decode(raw)
			require.NoError(t, err)
			_, err = spec.ValidateRaw(raw, d)
			require.NoError(t, err)
		})
	}
}

func TestRepublishedGroupFixtureMatchesPublisher(t *testing.T) {
	base := &spec.Descriptor{Kind: spec.KindMixin, Capabilities: []spec.Capability{
		{Type: spec.CapabilityLifecycle, Config: map[string]any{"startup": []any{map[string]any{"command": "true"}}}},
	}}
	feature := &spec.Descriptor{Kind: spec.KindMixin, Capabilities: []spec.Capability{
		{Group: &spec.CapabilityGroup{Optional: true, Capabilities: []spec.Capability{
			{Type: spec.CapabilityVolume, Config: map[string]any{"path": "/var/tmp/republished-volume"}},
			{Type: spec.CapabilityLifecycle, Config: map[string]any{"files": []any{map[string]any{"path": "/var/tmp/republished-feature", "content": "selected"}}}},
		}}},
	}}
	published, err := spec.Merge([]spec.Contribution{
		{Reference: "registry.example/base:1.0.0", Descriptor: base},
		{Reference: "registry.example/feature:1.0.0", Descriptor: feature},
	}, spec.MergeOptions{})
	require.NoError(t, err)
	republished, err := spec.Merge([]spec.Contribution{{Reference: "registry.example/set:1.0.0", Descriptor: published.Descriptor}}, spec.MergeOptions{})
	require.NoError(t, err)
	raw, err := os.ReadFile(filepath.Join(FixtureDir, "groups-republished", "groups-republished.yaml"))
	require.NoError(t, err)
	fixture, err := spec.Decode(raw)
	require.NoError(t, err)
	want, err := json.Marshal(republished.Descriptor)
	require.NoError(t, err)
	got, err := json.Marshal(fixture)
	require.NoError(t, err)
	require.JSONEq(t, string(want), string(got))
}

func TestVolumeFixturesValidateAfterArgumentExpansion(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join(FixtureDir, "volume-state*", "*.yaml"))
	require.NoError(t, err)
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			require.NoError(t, err)
			d, err := spec.Decode(raw)
			require.NoError(t, err)
			values, err := spec.KitArgValues(d.Args, nil)
			require.NoError(t, err)
			expanded, err := spec.ExpandCreateArgs(raw, d.Args, values)
			require.NoError(t, err)
			effective, err := spec.Decode(expanded)
			require.NoError(t, err)
			_, err = spec.ValidateEffective(expanded, effective)
			require.NoError(t, err)
		})
	}
}

func TestPublishedVolumeFixtureMatchesPublisher(t *testing.T) {
	var contributions []spec.Contribution
	for _, name := range []string{"volume-state", "volume-state-other"} {
		raw, err := os.ReadFile(filepath.Join(FixtureDir, name, name+".yaml"))
		require.NoError(t, err)
		d, err := spec.Decode(raw)
		require.NoError(t, err)
		values, err := spec.KitArgValues(d.Args, nil)
		require.NoError(t, err)
		expanded, err := spec.ExpandCreateArgs(raw, d.Args, values)
		require.NoError(t, err)
		d, err = spec.Decode(expanded)
		require.NoError(t, err)
		// Provenance is diagnostic; the published fixture carries the
		// original first contributor's source rather than claiming it.
		contributions = append(contributions, spec.Contribution{Reference: name, Descriptor: d})
	}
	require.NotEqual(t, contributions[0].Descriptor.Capabilities[0].Source,
		contributions[1].Descriptor.Capabilities[0].Source,
		"matching volume fixtures must exercise distinct provenance")
	require.NotEqual(t, contributions[0].Descriptor.Capabilities[0].Name,
		contributions[1].Descriptor.Capabilities[0].Name,
		"matching volume fixtures must ignore distinct display names")
	require.NotEqual(t, contributions[0].Descriptor.Capabilities[0].Description,
		contributions[1].Descriptor.Capabilities[0].Description,
		"matching volume fixtures must ignore distinct descriptions")
	_, hasTmpfs := contributions[0].Descriptor.Capabilities[0].Config["tmpfs"]
	require.False(t, hasTmpfs)
	require.Equal(t, false, contributions[1].Descriptor.Capabilities[0].Config["tmpfs"],
		"matching volume fixtures must reconcile omitted and explicit false tmpfs")
	require.False(t, contributions[0].Descriptor.Capabilities[0].Optional)
	require.True(t, contributions[1].Descriptor.Capabilities[0].Optional,
		"matching fixtures must reconcile required and optional requests")
	require.NotEqual(t, contributions[0].Descriptor.Capabilities[0].Config["path"],
		contributions[1].Descriptor.Capabilities[0].Config["path"],
		"matching fixtures must exercise distinct path spellings")
	published, err := spec.Merge(contributions, spec.MergeOptions{})
	require.NoError(t, err)
	raw, err := os.ReadFile(filepath.Join(FixtureDir, "volume-state-published", "volume-state-published.yaml"))
	require.NoError(t, err)
	fixture, err := spec.Decode(raw)
	require.NoError(t, err)
	want, err := json.Marshal(published.Descriptor)
	require.NoError(t, err)
	got, err := json.Marshal(fixture)
	require.NoError(t, err)
	require.JSONEq(t, string(want), string(got))
	require.Len(t, fixture.Capabilities, 2)
}
