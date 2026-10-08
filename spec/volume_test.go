package spec

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVolumeRequestEquivalence(t *testing.T) {
	for _, tc := range []struct {
		name  string
		a, b  Volume
		match bool
	}{
		{"cleaned path", Volume{Path: "/data/cache"}, Volume{Path: "/data/./cache/"}, true},
		{"different path", Volume{Path: "/data/cache"}, Volume{Path: "/data/other"}, false},
		{"octal", Volume{Mode: "0755"}, Volume{Mode: "755"}, true},
		{"zero mode unspecified", Volume{Mode: "0000"}, Volume{}, false},
		{"mode unspecified", Volume{Mode: "0755"}, Volume{}, false},
		{"size unspecified", Volume{Size: "0"}, Volume{}, false},
		{"binary units", Volume{Size: "1g"}, Volume{Size: "1024mib"}, true},
		{"decimal", Volume{Size: "1.5g"}, Volume{Size: "1536m"}, true},
		{"whitespace", Volume{Size: "1 G"}, Volume{Size: "1073741824"}, true},
		{"leading zero", Volume{Size: "001.0k"}, Volume{Size: "1024.00"}, true},
		{"unbounded size", Volume{Size: "999999999999999999999999t"}, Volume{Size: "1023999999999999999999998976g"}, true},
		{"precision", Volume{Size: "9007199254740992"}, Volume{Size: "9007199254740993"}, false},
		{"invalid size", Volume{Size: "bad"}, Volume{Size: "bad"}, false},
		{"backing", Volume{Tmpfs: false}, Volume{Tmpfs: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.match, SameVolumeRequest(tc.a, tc.b))
			require.Equal(t, tc.match, SameVolumeRequest(tc.b, tc.a))
		})
	}
}

func TestVolumeAliasesDoNotBypassDeclarationArityOrWidenTheGate(t *testing.T) {
	d := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{
		{Type: CapabilityVolume, Config: map[string]any{"path": "/data/cache"}},
		{Type: CapabilityVolume, Config: map[string]any{"path": "/data/./cache"}},
	}}
	_, err := Validate(d)
	require.ErrorContains(t, err, "already declared")
	a := SurfaceOf(&Descriptor{Capabilities: d.Capabilities[:1]})
	b := SurfaceOf(&Descriptor{Capabilities: d.Capabilities[1:]})
	require.Equal(t, a, b)
}

func TestEmptyVolumeSettingsRemainUnspecified(t *testing.T) {
	raw := []byte(`schemaVersion: "3"
kind: mixin
capabilities:
  - type: com.docker.sandbox/volume@1
    config: {path: /data, size: "", mode: ""}
`)
	d, err := Decode(raw)
	require.NoError(t, err)
	_, err = ValidateRaw(raw, d)
	require.NoError(t, err)
	volumes, err := VolumesOf(d.Capabilities)
	require.NoError(t, err)
	require.True(t, SameVolumeRequest(volumes[0], Volume{Path: "/data"}))
}
