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

// TestVolumePathRefusesNUL walks each descriptor through the gates a
// create crosses: authoring validation, create-argument expansion, and
// the effective gate. A literal NUL is refused while authoring. An entry
// whose config references an argument defers its typed checks, so a NUL
// a create argument supplies, or a literal one beside a parameterized
// size, is refused at the effective gate instead.
func TestVolumePathRefusesNUL(t *testing.T) {
	for _, tc := range []struct {
		name      string
		path      string // YAML spelling of the volume path
		size      string // YAML spelling of the volume size, when set
		hostMount string // a host mount declared before the volume, when set
		dir       string // value of the dir create argument, default cache
		authoring string // error authoring validation reports
		effective string // error the effective gate reports
	}{
		{name: "absolute", path: "/data/cache"},
		{name: "alias compared after cleaning", path: "/data/./cache/"},
		{name: "relative", path: "data/cache", authoring: "must be absolute"},
		{name: "trailing NUL", path: `"/data/cache\0"`, authoring: "without NUL"},
		{name: "interior NUL", path: `"/data/ca\0che"`, authoring: "without NUL"},
		{name: "host mount elsewhere", path: "/data/cache", hostMount: "/data/models"},
		{name: "host mount at the path", path: "/data/cache", hostMount: "/data/cache", authoring: "conflicts with host mount"},
		{name: "NUL beside a host mount at its prefix", path: `"/data/cache\0"`, hostMount: "/data/cache", authoring: "without NUL"},
		{name: "argument", path: `"/data/${{ kit.args.dir }}"`},
		{name: "NUL from an argument", path: `"/data/${{ kit.args.dir }}"`, dir: "cache\x00", effective: "without NUL"},
		{name: "NUL beside a parameterized size", path: `"/data/cache\0"`, size: `"${{ kit.args.size }}"`, hostMount: "/data/cache", effective: "without NUL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := "schemaVersion: \"3\"\nkind: mixin\nargs:\n  dir: {default: cache}\n  size: {default: 1g}\ncapabilities:\n"
			if tc.hostMount != "" {
				raw += "  - type: com.docker.sandbox/host-mount@1\n    config: {path: " + tc.hostMount + "}\n"
			}
			volume := "path: " + tc.path
			if tc.size != "" {
				volume += ", size: " + tc.size
			}
			raw += "  - type: com.docker.sandbox/volume@1\n    config: {" + volume + "}\n"
			d, err := Decode([]byte(raw))
			require.NoError(t, err)
			_, err = ValidateRaw([]byte(raw), d)
			if tc.authoring != "" {
				require.ErrorContains(t, err, tc.authoring)
				return
			}
			require.NoError(t, err)

			supplied := map[string]string{}
			if tc.dir != "" {
				supplied["dir"] = tc.dir
			}
			values, err := KitArgValues(d.Args, supplied)
			require.NoError(t, err)
			expanded, err := ExpandCreateArgs([]byte(raw), d.Args, values)
			require.NoError(t, err)
			effective, err := Decode(expanded)
			require.NoError(t, err)
			_, err = ValidateEffective(expanded, effective)
			if tc.effective != "" {
				require.ErrorContains(t, err, tc.effective)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestVolumePathPredicateIsTheValidator(t *testing.T) {
	for _, p := range shortPaths() {
		d := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{
			{Type: CapabilityVolume, Config: map[string]any{"path": p}},
		}}
		_, err := Validate(d)
		require.Equal(t, volumePath(p), err == nil, "volume path %q: %v", p, err)
	}
}

// shortPaths returns every string of up to five characters over an
// alphabet that spells relative paths, lexical aliases, newlines, and
// NUL. Rules held to one predicate are compared on all of them, not on
// hand-picked samples, so a path class they treat differently cannot
// hide between the samples.
func shortPaths() []string {
	all, level := []string{""}, []string{""}
	for range 5 {
		var next []string
		for _, prefix := range level {
			for _, r := range "/.a\n\x00" {
				next = append(next, prefix+string(r))
			}
		}
		all, level = append(all, next...), next
	}
	return all
}
