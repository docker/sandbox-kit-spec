package spec

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestDockerValidation(t *testing.T) {
	base := func(kind string, caps ...Capability) *Descriptor {
		d := &Descriptor{SchemaVersion: SchemaVersion, Kind: kind, Capabilities: caps}
		if kind == KindSet {
			d.Kits = []Kit{{Ref: "example.com/workload:1"}}
		}
		return d
	}
	for _, kind := range []string{KindWorkload, KindMixin, KindSet} {
		for _, optional := range []bool{false, true} {
			_, err := Validate(base(kind, Capability{Type: CapabilityDockerd, Optional: optional}))
			require.NoError(t, err, "%s may declare dockerd@1", kind)
		}
	}

	_, err := Validate(base(KindWorkload,
		Capability{Type: CapabilityDockerd},
		Capability{Type: CapabilityDockerd, Optional: true},
	))
	require.ErrorContains(t, err, "already declared")

	for _, tc := range []struct {
		spelling string
		auto     bool
	}{{"{}", true}, {`{"autoStart": true}`, true}, {`{"autoStart": false}`, false}} {
		t.Run(tc.spelling, func(t *testing.T) {
			for _, entry := range dockerdSpellings(t, tc.spelling) {
				_, err := Validate(base(KindWorkload, entry))
				require.NoError(t, err)
				d, err := DockerdOf([]Capability{entry})
				require.NoError(t, err)
				require.Equal(t, tc.auto, d.AutoStarts())
			}
		})
	}
	d, err := DockerdOf([]Capability{{Type: CapabilityDockerd}})
	require.NoError(t, err)
	require.True(t, d.AutoStarts(), "omitted config starts the engine")

	for _, tc := range []struct{ spelling, want string }{
		{`{"autoStart": null}`, "autoStart must be true or false"},
		{`{"autoStart": "false"}`, "cannot unmarshal string"},
		{`{"AutoStart": false}`, `did you mean "autoStart"?`},
		{`{"storage": "/var/lib/docker"}`, `unknown field "storage"`},
	} {
		t.Run(tc.spelling, func(t *testing.T) {
			for _, entry := range dockerdSpellings(t, tc.spelling) {
				_, err := Validate(base(KindWorkload, entry))
				require.ErrorContains(t, err, tc.want)
			}
		})
	}
}

// dockerdSpellings decodes one config spelling from YAML and from JSON.
func dockerdSpellings(t *testing.T, spelling string) []Capability {
	t.Helper()
	var fromYAML, fromJSON Capability
	require.NoError(t, yaml.Unmarshal([]byte("type: "+CapabilityDockerd+"\nconfig: "+spelling+"\n"), &fromYAML))
	require.NoError(t, json.Unmarshal([]byte(`{"type":"`+CapabilityDockerd+`","config":`+spelling+`}`), &fromJSON))
	return []Capability{fromYAML, fromJSON}
}

// Whether the runtime starts the engine grants nothing: the elevation and
// the store are the same either way.
func TestDockerdAutoStartSurface(t *testing.T) {
	auto := SurfaceOf(&Descriptor{Capabilities: []Capability{{Type: CapabilityDockerd}}})
	manual := SurfaceOf(&Descriptor{Capabilities: []Capability{{Type: CapabilityDockerd, Config: map[string]any{"autoStart": false}}}})
	require.Equal(t, auto, manual)
	require.Empty(t, DiffWidenings(auto, manual))
	require.Empty(t, DiffWidenings(manual, auto))
	require.True(t, PrivilegedOf([]Capability{{Type: CapabilityDockerd, Config: map[string]any{"autoStart": false}}}))
}

// Several Kits may ask for the engine; it starts when any of them wants
// it started, whichever order they compose in.
func TestDockerdAutoStartMerge(t *testing.T) {
	ask := func(config map[string]any) Capability {
		return Capability{Type: CapabilityDockerd, Config: config}
	}
	manual := map[string]any{"autoStart": false}
	explicit := map[string]any{"autoStart": true}
	for _, tc := range []struct {
		name string
		a, b map[string]any
		want map[string]any
	}{
		{"false and omitted", manual, nil, nil},
		{"false and true", manual, explicit, nil},
		{"true and omitted", explicit, nil, nil},
		{"false and false", manual, manual, manual},
		{"true and true", explicit, explicit, explicit},
		{"omitted and omitted", nil, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, order := range [][2]map[string]any{{tc.a, tc.b}, {tc.b, tc.a}} {
				merged := mergeOK(t,
					Contribution{Reference: "workload", Descriptor: &Descriptor{SchemaVersion: SchemaVersion, Kind: KindWorkload,
						Capabilities: []Capability{{Type: CapabilityLongRunning}, ask(order[0])}}},
					Contribution{Reference: "mixin", Descriptor: &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin,
						Capabilities: []Capability{ask(order[1])}}},
				).Descriptor
				require.Equal(t, []Capability{{Type: CapabilityLongRunning}, ask(tc.want)}, merged.Capabilities)
				d, err := DockerdOf(merged.Capabilities)
				require.NoError(t, err)
				require.Equal(t, tc.want == nil || tc.want["autoStart"] == true, d.AutoStarts())
				_, err = Validate(merged)
				require.NoError(t, err)
			}
		})
	}

	// Required wins and a name is kept, as for any merged entry.
	merged := mergeOK(t,
		Contribution{Reference: "workload", Descriptor: &Descriptor{SchemaVersion: SchemaVersion, Kind: KindWorkload,
			Capabilities: []Capability{{Type: CapabilityDockerd, Optional: true, Config: manual}}}},
		Contribution{Reference: "mixin", Descriptor: &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin,
			Capabilities: []Capability{{Type: CapabilityDockerd, Name: "engine"}}}},
	).Descriptor
	require.Equal(t, []Capability{{Type: CapabilityDockerd, Name: "engine"}}, merged.Capabilities)

	_, err := Merge([]Contribution{
		{Reference: "workload", Descriptor: &Descriptor{SchemaVersion: SchemaVersion, Kind: KindWorkload,
			Capabilities: []Capability{ask(nil)}}},
		{Reference: "mixin", Descriptor: &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin,
			Capabilities: []Capability{ask(map[string]any{"autoStart": "no"})}}},
	}, MergeOptions{})
	require.ErrorContains(t, err, "merge: mixin capabilities[0]: "+CapabilityDockerd)
}

// The engine needs privileged@1's elevation and brings persistent storage
// at its store, so the gate reads it as privileged@1 plus a volume@1
// there: replacing that pair widens nothing, and adding the engine to a
// Kit granted privileged@1 alone widens storage.
func TestDockerSurface(t *testing.T) {
	docker := SurfaceOf(&Descriptor{Capabilities: []Capability{{Type: CapabilityDockerd}}})
	require.Equal(t, Surface{Privileged: true, StoragePaths: []string{DockerStoragePath}}, docker)
	require.True(t, PrivilegedOf([]Capability{{Type: CapabilityDockerd}}))

	ws := DiffWidenings(SurfaceOf(&Descriptor{}), docker)
	require.Len(t, ws, 2)
	require.Equal(t, []string{"storage", "privileged"}, []string{ws[0].Category, ws[1].Category})

	privileged := SurfaceOf(&Descriptor{Capabilities: []Capability{{Type: CapabilityPrivileged}}})
	ws = DiffWidenings(privileged, docker)
	require.Len(t, ws, 1)
	require.Equal(t, "storage", ws[0].Category)
	require.Contains(t, ws[0].Detail, DockerStoragePath)
	require.Empty(t, DiffWidenings(docker, privileged))

	assembled := SurfaceOf(&Descriptor{Capabilities: []Capability{
		{Type: CapabilityPrivileged},
		{Type: CapabilityVolume, Config: map[string]any{"path": "/var/lib/docker/", "size": "20g"}},
	}})
	require.Empty(t, DiffWidenings(assembled, docker))
	require.Empty(t, DiffWidenings(docker, assembled))
}

func dockerStorage(typ, path string) Capability {
	return Capability{Type: typ, Config: map[string]any{"path": path}}
}

// dockerd@1 owns the engine store, so a descriptor stating both is refused
// whichever entry comes first, on the cleaned path storage keys use. A
// path that only resembles it is ordinary storage.
func TestDockerEngineStoreValidation(t *testing.T) {
	sizedByArg := Capability{Type: CapabilityVolume, Config: map[string]any{"path": "/var/lib/docker", "size": "${{ kit.args.size }}"}}
	for _, tc := range []struct {
		name    string
		storage Capability
		refused bool
	}{
		{"volume", dockerStorage(CapabilityVolume, "/var/lib/docker"), true},
		{"volume with a trailing slash", dockerStorage(CapabilityVolume, "/var/lib/docker/"), true},
		{"volume with a dot segment", dockerStorage(CapabilityVolume, "/var/lib/./docker"), true},
		{"volume sized by an arg", sizedByArg, true},
		{"host mount", dockerStorage(CapabilityHostMount, "/var/lib/docker"), true},
		{"volume under the store", dockerStorage(CapabilityVolume, "/var/lib/docker/volumes"), true},
		{"volume deep under the store", dockerStorage(CapabilityVolume, "/var/lib/docker/overlay2/cache/"), true},
		{"host mount under the store", dockerStorage(CapabilityHostMount, "/var/lib/docker/volumes"), true},
		{"volume escaping the store", dockerStorage(CapabilityVolume, "/var/lib/docker/../docker2"), false},
		{"volume at a sibling", dockerStorage(CapabilityVolume, "/var/lib/docker2"), false},
		{"volume at a hyphenated sibling", dockerStorage(CapabilityVolume, "/var/lib/docker-cache"), false},
		{"host mount at a sibling", dockerStorage(CapabilityHostMount, "/var/lib/dockerd/volumes"), false},
		{"volume at the parent", dockerStorage(CapabilityVolume, "/var/lib"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, order := range [][]Capability{
				{{Type: CapabilityDockerd}, tc.storage},
				{tc.storage, {Type: CapabilityDockerd}},
			} {
				d := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindWorkload, Args: map[string]Arg{"size": {Required: true}}, Capabilities: order}
				_, err := Validate(d)
				if !tc.refused {
					require.NoError(t, err)
					continue
				}
				require.ErrorContains(t, err, "is inside the engine store "+CapabilityDockerd)
			}
			// The same storage without the engine is only storage.
			alone := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindWorkload, Args: map[string]Arg{"size": {Required: true}}, Capabilities: []Capability{tc.storage}}
			_, err := Validate(alone)
			require.NoError(t, err)
		})
	}
}

// Composition holds the rule across Kits: the engine asked for by one
// and storage at its path asked for by another cannot both be granted.
// Publication refuses it too when another request defers the merge,
// including when the volume's own settings wait for create.
func TestDockerEngineStoreComposition(t *testing.T) {
	kit := func(kind string, caps ...Capability) *Descriptor {
		return &Descriptor{SchemaVersion: SchemaVersion, Kind: kind, Capabilities: caps}
	}
	docker := Capability{Type: CapabilityDockerd}
	deferring := Capability{Group: &CapabilityGroup{Optional: true, Capabilities: []Capability{{Type: CapabilityLongRunning}}}}
	sizedByArg := Capability{Type: CapabilityVolume, Config: map[string]any{"path": "/var/lib/docker", "size": "${{ kit.args.size }}"}}
	for _, tc := range []struct {
		name    string
		storage Capability
		refused bool
	}{
		{"volume", dockerStorage(CapabilityVolume, "/var/lib/docker/"), true},
		{"host mount", dockerStorage(CapabilityHostMount, "/var/lib/./docker"), true},
		{"volume under the store", dockerStorage(CapabilityVolume, "/var/lib/docker/volumes"), true},
		{"host mount under the store", dockerStorage(CapabilityHostMount, "/var/lib/docker/buildkit/"), true},
		{"volume at a sibling", dockerStorage(CapabilityVolume, "/var/lib/docker-cache"), false},
		{"volume at a numbered sibling", dockerStorage(CapabilityVolume, "/var/lib/docker2"), false},
		{"host mount at a sibling", dockerStorage(CapabilityHostMount, "/var/lib/docker2/volumes"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workload := Contribution{Reference: "workload", Descriptor: kit(KindWorkload, tc.storage)}
			engine := Contribution{Reference: "engine", Descriptor: kit(KindMixin, docker)}
			for _, contributions := range [][]Contribution{{workload, engine}, {engine, workload}} {
				_, err := Compose(contributions)
				published, mergeErr := Merge(contributions, MergeOptions{})
				deferred, deferredErr := Merge(append([]Contribution{{Reference: "deferring", Descriptor: kit(KindMixin, deferring)}}, contributions...), MergeOptions{})
				if !tc.refused {
					require.NoError(t, err)
					require.NoError(t, mergeErr)
					_, err = Validate(published.Descriptor)
					require.NoError(t, err)
					require.NoError(t, deferredErr)
					require.NotNil(t, deferred)
					continue
				}
				for _, err := range []error{err, mergeErr, deferredErr} {
					require.ErrorContains(t, err, "inside the engine store "+CapabilityDockerd)
					require.ErrorContains(t, err, "workload capabilities[0]")
					require.ErrorContains(t, err, "engine")
				}
			}
		})
	}

	t.Run("volume sized by an arg", func(t *testing.T) {
		workload := Contribution{Reference: "workload", Descriptor: &Descriptor{SchemaVersion: SchemaVersion, Kind: KindWorkload,
			Args: map[string]Arg{"size": {Required: true}}, Capabilities: []Capability{sizedByArg}}}
		engine := Contribution{Reference: "engine", Descriptor: kit(KindMixin, docker)}
		_, err := Merge([]Contribution{workload, engine}, MergeOptions{})
		require.ErrorContains(t, err, "inside the engine store "+CapabilityDockerd)

		under := sizedByArg
		under.Config = map[string]any{"path": "/var/lib/docker/volumes", "size": "${{ kit.args.size }}"}
		workload.Descriptor.Capabilities = []Capability{under}
		_, err = Merge([]Contribution{workload, engine}, MergeOptions{})
		require.ErrorContains(t, err, "at /var/lib/docker/volumes, inside the engine store "+CapabilityDockerd)

		sibling := sizedByArg
		sibling.Config = map[string]any{"path": "/var/lib/docker2", "size": "${{ kit.args.size }}"}
		workload.Descriptor.Capabilities = []Capability{sibling}
		_, err = Merge([]Contribution{workload, engine}, MergeOptions{})
		require.NoError(t, err)
	})

	// Presence unions, and a required declaration wins.
	merged := mergeOK(t,
		Contribution{Reference: "workload", Descriptor: kit(KindWorkload, Capability{Type: CapabilityDockerd, Optional: true})},
		Contribution{Reference: "mixin", Descriptor: kit(KindMixin, docker)},
	).Descriptor
	require.Equal(t, []Capability{{Type: CapabilityDockerd}}, merged.Capabilities)
}

// An optional request declares the engine all the same, so the store is
// refused beside it.
func TestDockerEngineStoreOptional(t *testing.T) {
	d := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindWorkload, Capabilities: []Capability{
		{Type: CapabilityDockerd, Optional: true},
		dockerStorage(CapabilityVolume, "/var/lib/docker/volumes"),
	}}
	_, err := Validate(d)
	require.ErrorContains(t, err, "is inside the engine store "+CapabilityDockerd)

	engine := Contribution{Reference: "engine", Descriptor: &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin,
		Capabilities: []Capability{{Type: CapabilityDockerd, Optional: true}}}}
	workload := Contribution{Reference: "workload", Descriptor: &Descriptor{SchemaVersion: SchemaVersion, Kind: KindWorkload,
		Capabilities: []Capability{dockerStorage(CapabilityVolume, "/var/lib/docker")}}}
	_, err = Compose([]Contribution{workload, engine})
	require.ErrorContains(t, err, "inside the engine store "+CapabilityDockerd)
}

// A volume inside a group is not a concrete request until selection
// settles the group, so validation of the one descriptor lets it stand
// and composition of the selected requests refuses it.
func TestDockerEngineStoreAfterSelection(t *testing.T) {
	for _, tc := range []struct {
		name    string
		path    string
		refused bool
	}{
		{"at the store", "/var/lib/docker", true},
		{"under the store", "/var/lib/docker/volumes", true},
		{"at a sibling", "/var/lib/docker-cache", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindWorkload, Capabilities: []Capability{
				{Type: CapabilityDockerd},
				{Group: &CapabilityGroup{Optional: true, Capabilities: []Capability{dockerStorage(CapabilityVolume, tc.path)}}},
			}}
			_, err := Validate(d)
			require.NoError(t, err)

			selection, err := SelectCapabilities(t.Context(), "workload", d, Supported(KnownCapabilities()...))
			require.NoError(t, err)
			selected := *d
			selected.Capabilities = selection.Capabilities
			_, err = Compose([]Contribution{{Reference: "workload", Descriptor: &selected}})
			if !tc.refused {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, "at "+tc.path+", inside the engine store "+CapabilityDockerd)
		})
	}
}

// A stated null config is not the omitted one: the schema refuses it, so
// validation does too, in both decoders and through ValidateRaw.
func TestDockerdNullConfig(t *testing.T) {
	yamlDoc := "schemaVersion: \"3\"\nkind: workload\ncapabilities:\n  - type: " + CapabilityDockerd + "\n    config: null\n"
	jsonDoc := `{"schemaVersion":"3","kind":"workload","capabilities":[{"type":"` + CapabilityDockerd + `","config":null}]}`
	for _, raw := range []string{yamlDoc, jsonDoc} {
		d, err := Decode([]byte(raw))
		require.NoError(t, err)
		_, err = Validate(d)
		require.ErrorContains(t, err, "capabilities[0]: config must be an object")
		_, err = ValidateRaw([]byte(raw), d)
		require.ErrorContains(t, err, "capabilities[0]: config must be an object")
	}
	for _, entry := range dockerdSpellings(t, "null") {
		_, err := Validate(&Descriptor{SchemaVersion: SchemaVersion, Kind: KindWorkload, Capabilities: []Capability{entry}})
		require.ErrorContains(t, err, "config must be an object")
	}
}

// Whatever order the Kits compose in, the merged entry keeps the same
// name and description.
func TestDockerdMergedLabelsOrderIndependent(t *testing.T) {
	first := Capability{Type: CapabilityDockerd, Name: "engine-b", Description: "Builds images"}
	second := Capability{Type: CapabilityDockerd, Name: "engine-a", Description: "Runs containers", Config: map[string]any{"autoStart": false}}
	unnamed := Capability{Type: CapabilityDockerd}
	for _, order := range [][]Capability{{first, second, unnamed}, {second, unnamed, first}, {unnamed, first, second}} {
		var contributions []Contribution
		for i, c := range order {
			contributions = append(contributions, Contribution{Reference: fmt.Sprintf("kit-%d", i),
				Descriptor: &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{c}}})
		}
		merged := mergeOK(t, contributions...).Descriptor
		require.Equal(t, []Capability{{Type: CapabilityDockerd, Name: "engine-a", Description: "Builds images"}}, merged.Capabilities)
	}
}

// When another request defers publication, an arg in autoStart leaves
// the engine request unresolved, but not who owns the store: a literal
// volume there from another Kit is refused all the same, and a sibling
// path still passes.
func TestDockerEngineStoreDeferredAutoStart(t *testing.T) {
	deferring := Contribution{Reference: "deferring", Descriptor: &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin,
		Capabilities: []Capability{{Group: &CapabilityGroup{Optional: true, Capabilities: []Capability{{Type: CapabilityLongRunning}}}}}}}
	reexported := Contribution{Reference: "engine", Descriptor: &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin,
		Args:         map[string]Arg{"m": {Required: true}},
		Capabilities: []Capability{{Type: CapabilityDockerd, Config: map[string]any{"autoStart": "${{ kit.args.m }}"}}}}}
	for _, tc := range []struct {
		path    string
		refused bool
	}{
		{"/var/lib/docker", true},
		{"/var/lib/docker/volumes", true},
		{"/var/lib/docker-cache", false},
	} {
		t.Run(tc.path, func(t *testing.T) {
			workload := Contribution{Reference: "workload", Descriptor: &Descriptor{SchemaVersion: SchemaVersion, Kind: KindWorkload,
				Capabilities: []Capability{dockerStorage(CapabilityVolume, tc.path)}}}
			for _, contributions := range [][]Contribution{{deferring, workload, reexported}, {deferring, reexported, workload}} {
				_, err := Merge(contributions, MergeOptions{})
				if !tc.refused {
					require.NoError(t, err)
					continue
				}
				require.ErrorContains(t, err, "at "+tc.path+", inside the engine store "+CapabilityDockerd)
				require.ErrorContains(t, err, "workload")
				require.ErrorContains(t, err, "engine")
			}
		})
	}
}
