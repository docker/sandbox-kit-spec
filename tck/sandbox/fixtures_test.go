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
	require.Len(t, matches, 68, "every fixture directory needs its descriptor")

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
