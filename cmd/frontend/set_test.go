package main

import (
	"encoding/json"
	"fmt"
	"testing"

	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"

	"github.com/docker/sandbox-kit-spec/v3/resolve"
	"github.com/docker/sandbox-kit-spec/v3/spec"
)

func resolved(reference, kind string, config ocispecs.ImageConfig) resolvedKit {
	return resolvedKit{
		reference:  reference,
		descriptor: &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: kind},
		config:     ocispecs.Image{Config: config},
		stem:       reference,
	}
}

// A workload among a set's kits anchors the merged contract, and a
// mixin's own contract fields are ignored — they exist for a standalone
// docker run of it, the assembler's rule at create time too.
func TestMergedImageConfigWorkloadAnchorsTheContract(t *testing.T) {
	workload := resolved("shell", spec.KindWorkload, ocispecs.ImageConfig{
		Entrypoint: []string{"/bin/bash"},
		Cmd:        []string{"-l"},
		User:       "agent",
		WorkingDir: "/home/agent",
		Env:        []string{"PATH=/usr/bin", "BASE=1"},
	})
	mixin := resolved("gh", spec.KindMixin, ocispecs.ImageConfig{
		Entrypoint: []string{"gh"},
		Cmd:        []string{"--help"},
		User:       "root",
		Env:        []string{"PATH=/opt/gh/bin", "GH=1"},
	})

	config := mergedConfig(t, []resolvedKit{workload, mixin}, spec.KindWorkload)
	require.Equal(t, []string{"/bin/bash"}, config.Config.Entrypoint)
	require.Equal(t, []string{"-l"}, config.Config.Cmd)
	require.Equal(t, "agent", config.Config.User)
	require.Equal(t, "/home/agent", config.Config.WorkingDir)
	require.Contains(t, config.Config.Env, "PATH=/usr/bin:/opt/gh/bin",
		"PATH appends the mixin's elements instead of substituting them")
	require.Contains(t, config.Config.Env, "GH=1")
	require.Contains(t, config.Config.Env, "BASE=1")
}

// Nothing anchors a workload-free set, so a contract field survives only
// when exactly one kit states it: that keeps a standalone docker run of
// the merged overlay behaving as its one author wrote, and leaves the
// merged kit with neither rather than with whichever kit sorted last.
func TestMergedImageConfigMixinOnlyContract(t *testing.T) {
	stated := resolved("one", spec.KindMixin, ocispecs.ImageConfig{
		Entrypoint: []string{"/bin/echo"},
		Cmd:        []string{"one"},
		WorkingDir: "/opt",
	})
	silent := resolved("two", spec.KindMixin, ocispecs.ImageConfig{
		Env: []string{"TWO=1"},
	})

	config := mergedConfig(t, []resolvedKit{stated, silent}, spec.KindMixin)
	require.Equal(t, []string{"/bin/echo"}, config.Config.Entrypoint)
	require.Equal(t, []string{"one"}, config.Config.Cmd)
	require.Equal(t, "/opt", config.Config.WorkingDir)
	require.Contains(t, config.Config.Env, "TWO=1")

	// Two authors disagree, so the merged overlay states neither.
	other := resolved("three", spec.KindMixin, ocispecs.ImageConfig{
		Entrypoint: []string{"/bin/cat"},
	})
	config = mergedConfig(t, []resolvedKit{stated, other}, spec.KindMixin)
	require.Empty(t, config.Config.Entrypoint)
	require.Empty(t, config.Config.Cmd)
}

// Two kits setting one variable to different values is the config
// analogue of a file collision: composition order would pick a winner
// silently.
func TestMergedImageConfigRejectsEnvConflicts(t *testing.T) {
	a := resolved("a", spec.KindWorkload, ocispecs.ImageConfig{Env: []string{"MODE=fast"}})
	b := resolved("b", spec.KindMixin, ocispecs.ImageConfig{Env: []string{"MODE=slow"}})

	_, err := mergedImageConfig([]resolvedKit{a, b}, &spec.Descriptor{Kind: spec.KindWorkload}, nil)
	require.ErrorContains(t, err, "env conflict on MODE")
}

func mergedConfig(t *testing.T, kits []resolvedKit, kind string) ocispecs.Image {
	t.Helper()
	raw, err := mergedImageConfig(kits, &spec.Descriptor{Kind: kind}, nil)
	require.NoError(t, err)
	var out ocispecs.Image
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

// A set and the kits it lists stage their sources under the same root by
// filename stem, so a collision would leave the merged filesystem
// describing one kit with another's declarations.
func TestStemCollisions(t *testing.T) {
	gh := resolved("reg.io/gh:1", spec.KindMixin, ocispecs.ImageConfig{})
	gh.stem = "gh"
	shell := resolved("reg.io/shell:1", spec.KindWorkload, ocispecs.ImageConfig{})
	shell.stem = "shell"

	require.NoError(t, checkStemCollision("team", []resolvedKit{gh, shell}))
	require.ErrorContains(t, checkStemCollision("gh", []resolvedKit{gh, shell}),
		"which is where this set would stage its own")

	clash := resolved("reg.io/other-gh:1", spec.KindMixin, ocispecs.ImageConfig{})
	clash.stem = "gh"
	require.ErrorContains(t, checkKitCollisions([]resolvedKit{gh, clash}),
		"both stage their sources at")
}

// A kit's descriptor describes the kit, not one of its platforms: the
// annotation is written once for every platform manifest, so two
// platforms' merges disagreeing has to stop the build.
func TestSameDeclarations(t *testing.T) {
	a := &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindWorkload, Provides: []string{"a@1.0.0"}}
	b := &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindWorkload, Provides: []string{"a@1.0.0"}}
	require.NoError(t, sameDeclarations(a, b))

	b.Provides = []string{"a@2.0.0"}
	require.Error(t, sameDeclarations(a, b))
}

// The merged descriptor stages as YAML like any other kit's sources, and
// carries only the fields the merge actually set.
func TestMarshalDescriptorYAML(t *testing.T) {
	raw, err := marshalDescriptorYAML(&spec.Descriptor{
		SchemaVersion: spec.SchemaVersion,
		Kind:          spec.KindWorkload,
		Provides:      []string{"team@1.0.0"},
		Kits:          []spec.Kit{{Ref: "reg.io/base:1", Digest: "sha256:" + testDigestHex}},
	})
	require.NoError(t, err)
	require.NotContains(t, string(raw), "displayName",
		"an unset field stages as nothing, not as an empty value")

	d, err := spec.Decode(raw)
	require.NoError(t, err)
	require.Equal(t, spec.KindWorkload, d.Kind)
	require.Equal(t, "reg.io/base:1", d.Kits[0].Ref)
}

const testDigestHex = "1111111111111111111111111111111111111111111111111111111111111111"

// Stating the derived kind beside kits: is legal and says the same
// thing explicitly — but only when it is true, or the descriptor names
// a role its content does not play.
func TestCheckAuthoredKind(t *testing.T) {
	require.NoError(t, checkAuthoredKind(spec.KindSet, spec.KindWorkload),
		"kind: set asks for the kind to be derived, so it claims nothing")
	require.NoError(t, checkAuthoredKind(spec.KindSet, spec.KindMixin))
	require.NoError(t, checkAuthoredKind(spec.KindWorkload, spec.KindWorkload))
	require.NoError(t, checkAuthoredKind(spec.KindMixin, spec.KindMixin))

	require.ErrorContains(t, checkAuthoredKind(spec.KindWorkload, spec.KindMixin),
		"make it a mixin")
	require.ErrorContains(t, checkAuthoredKind(spec.KindMixin, spec.KindWorkload),
		"make it a workload")
}

// A version-shaped tag is what the resolver judges a kit by, so it has
// to be what the merge records too: a kit consumed as base:2.0.0 whose
// descriptor still says 1.0.0 satisfies an internal `base >= 2.0.0`
// during ordering, and a merge reading the descriptor alone would emit
// base@1.0.0 and keep the requirement the set already answered.
func TestListedKitTakesItsVersionFromTheReference(t *testing.T) {
	stale := &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindWorkload, Version: "1.0.0"}

	require.Equal(t, "2.0.0", resolve.EffectiveProvideVersion("reg.io/base:2.0.0", stale))
	require.Equal(t, "1.0.0", resolve.EffectiveProvideVersion("reg.io/base:dev", stale),
		"a tag that is not version-shaped leaves the descriptor as the only home")
}

// A set lists kits; a companion Dockerfile beside it is found by naming
// convention rather than declared, so only the frontend can catch it.
// Left alone it would be staged as the kit's recipe and never built.
func TestSetRejectsAConventionalCompanion(t *testing.T) {
	d := &spec.Descriptor{
		SchemaVersion: spec.SchemaVersion,
		Kind:          spec.KindSet,
		Kits:          []spec.Kit{{Ref: "reg.io/base:1.0.0"}},
	}
	// The grammar catches the declared forms; this is the pair it
	// cannot see, so the frontend's own guard is what refuses it.
	_, err := spec.Validate(d)
	require.NoError(t, err, "a companion file is invisible to the grammar")

	d.Dockerfile = "team.dockerfile"
	_, err = spec.Validate(d)
	require.ErrorContains(t, err, "a set's content is the kits it lists",
		"the declared form is the grammar's to refuse")
}

// A set of mixins derives to a mixin, which is a shape no member
// authored and no consumer would otherwise refuse.
func TestASetMayNotDeriveToAnUnpublishableDescriptor(t *testing.T) {
	derived := &spec.Descriptor{
		SchemaVersion: spec.SchemaVersion,
		Kind:          spec.KindMixin,
		DisplayName:   "Derived",
		Version:       "1.0.0",
		Provides:      []string{"demo@1.0.0"},
		Capabilities:  []spec.Capability{{Type: spec.CapabilitySbx}},
	}
	_, err := publishedSetDescriptor(derived)
	require.ErrorContains(t, err, "not publishable")
	require.ErrorContains(t, err, "workload-only")

	derived.Kind = spec.KindWorkload
	_, err = publishedSetDescriptor(derived)
	require.NoError(t, err)
}

// The resolver is handed the listed kits; the set's own declarations
// join afterwards as one more contribution. A relation the set states
// about something it contains would therefore go unjudged and flatten
// into a self-relation nothing can ever satisfy.
func TestCheckSetRelations(t *testing.T) {
	kits := []spec.Contribution{
		{Reference: "reg.io/shell:1.0.0", Descriptor: &spec.Descriptor{
			SchemaVersion: spec.SchemaVersion, Kind: spec.KindWorkload,
			Version: "1.0.0", Provides: []string{"shell"},
		}},
		{Reference: "reg.io/gh:2.72.0", Descriptor: &spec.Descriptor{
			SchemaVersion: spec.SchemaVersion, Kind: spec.KindMixin,
			Provides: []string{"gh@2.72.0"},
		}},
	}
	set := func(mutate func(*spec.Descriptor)) *spec.Descriptor {
		d := &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindMixin}
		mutate(d)
		return d
	}

	// An ask of the composition the merged kit lands in survives: it is
	// the only kind that can still be met.
	require.NoError(t, checkSetRelations(set(func(d *spec.Descriptor) {
		d.Requires = []string{"node >= 20.0.0"}
	}), kits))
	require.NoError(t, checkSetRelations(set(func(d *spec.Descriptor) {
		d.Requires = []string{"shell >= 1.0.0"}
	}), kits), "a constraint its own kits meet is merely redundant")

	require.ErrorContains(t, checkSetRelations(set(func(d *spec.Descriptor) {
		d.Requires = []string{"shell >= 2.0.0"}
	}), kits), "could never resolve")
	require.ErrorContains(t, checkSetRelations(set(func(d *spec.Descriptor) {
		d.Integrates = []string{"gh >= 3.0.0"}
	}), kits), "could never resolve")
	require.ErrorContains(t, checkSetRelations(set(func(d *spec.Descriptor) {
		d.Conflicts = []string{"gh"}
	}), kits), "would exclude itself")
	require.ErrorContains(t, checkSetRelations(set(func(d *spec.Descriptor) {
		d.Provides = []string{"shell@9.0.0"}
	}), kits), "one name has one owner")
}

// Flattening erases a relation in either direction, so both are
// checked: a listed kit that conflicts with — or cannot integrate
// against — what the set itself provides would publish a kit whose
// incompatibility names itself, which every consumer skips.
func TestCheckSetRelationsJudgesTheReverseDirection(t *testing.T) {
	kit := func(mutate func(*spec.Descriptor)) []spec.Contribution {
		d := &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindMixin, Version: "1.0.0"}
		mutate(d)
		return []spec.Contribution{{Reference: "reg.io/tool:1.0.0", Descriptor: d}}
	}
	set := &spec.Descriptor{
		SchemaVersion: spec.SchemaVersion, Kind: spec.KindMixin,
		Version: "1.0.0", Provides: []string{"team@1.0.0"},
	}

	require.NoError(t, checkSetRelations(set, kit(func(d *spec.Descriptor) {
		d.Requires = []string{"node >= 20.0.0"}
	})), "a name neither side provides is somebody else's to answer")
	require.NoError(t, checkSetRelations(set, kit(func(d *spec.Descriptor) {
		d.Integrates = []string{"team >= 1.0.0"}
	})), "a constraint the set meets is merely redundant")

	require.ErrorContains(t, checkSetRelations(set, kit(func(d *spec.Descriptor) {
		d.Conflicts = []string{"team"}
	})), "would exclude itself")
	require.ErrorContains(t, checkSetRelations(set, kit(func(d *spec.Descriptor) {
		d.Integrates = []string{"team >= 2.0.0"}
	})), "could never resolve")
}

// A set requirement only the set itself provides is unresolvable by
// construction, since every consumer skips self-provides. The merge
// carries it through honestly; this is what makes it fixable.
func TestCheckSetRelationsRefusesASelfRequirement(t *testing.T) {
	kits := []spec.Contribution{{Reference: "reg.io/shell:1.0.0", Descriptor: &spec.Descriptor{
		SchemaVersion: spec.SchemaVersion, Kind: spec.KindWorkload,
		Version: "1.0.0", Provides: []string{"shell@1.0.0"},
	}}}

	require.ErrorContains(t, checkSetRelations(&spec.Descriptor{
		SchemaVersion: spec.SchemaVersion, Kind: spec.KindMixin, Version: "1.0.0",
		Provides: []string{"team@1.0.0"},
		Requires: []string{"team >= 1.0.0"},
	}, kits), "the only thing providing")

	// A requirement something else answers is fine, and so is one
	// nobody in the set answers — that lands on the host composition.
	require.NoError(t, checkSetRelations(&spec.Descriptor{
		SchemaVersion: spec.SchemaVersion, Kind: spec.KindMixin, Version: "1.0.0",
		Provides: []string{"team@1.0.0"},
		Requires: []string{"shell >= 1.0.0", "node >= 20.0.0"},
	}, kits))
}

// The merged config takes user and working directory from the sole
// contract too, so a kit stating only one of those states a contract:
// reading it as none would overwrite it with nothing, and a second
// such kit would not register as the disagreement it is.
func TestMixinOnlyContractCountsEveryField(t *testing.T) {
	userOnly := resolved("one", spec.KindMixin, ocispecs.ImageConfig{User: "agent"})
	silent := resolved("two", spec.KindMixin, ocispecs.ImageConfig{Env: []string{"X=1"}})

	config := mergedConfig(t, []resolvedKit{userOnly, silent}, spec.KindMixin)
	require.Equal(t, "agent", config.Config.User, "the one kit that stated it keeps it")

	workdirOnly := resolved("three", spec.KindMixin, ocispecs.ImageConfig{WorkingDir: "/work"})
	config = mergedConfig(t, []resolvedKit{workdirOnly, silent}, spec.KindMixin)
	require.Equal(t, "/work", config.Config.WorkingDir)

	// Two authors disagree even when neither states an entrypoint.
	config = mergedConfig(t, []resolvedKit{userOnly, workdirOnly}, spec.KindMixin)
	require.Empty(t, config.Config.User)
	require.Empty(t, config.Config.WorkingDir)
}

// An integration names something a kit works with when present and
// functions without, and the resolver judges only present OTHER
// providers — so a capability the set provides itself is absent as far
// as the rule goes, which is a working kit rather than a stuck one.
func TestASetMayIntegrateWithWhatItProvides(t *testing.T) {
	kits := []spec.Contribution{{Reference: "reg.io/shell:1.0.0", Descriptor: &spec.Descriptor{
		SchemaVersion: spec.SchemaVersion, Kind: spec.KindWorkload,
		Version: "1.0.0", Provides: []string{"shell@1.0.0"},
	}}}
	set := func(mutate func(*spec.Descriptor)) *spec.Descriptor {
		d := &spec.Descriptor{
			SchemaVersion: spec.SchemaVersion, Kind: spec.KindMixin,
			Version: "1.0.0", Provides: []string{"team@1.0.0"},
		}
		mutate(d)
		return d
	}

	require.NoError(t, checkSetRelations(set(func(d *spec.Descriptor) {
		d.Integrates = []string{"team >= 1.0.0"}
	}), kits), "an integration with its own capability is simply absent")

	// A requirement is still refused: nothing can ever answer it.
	require.ErrorContains(t, checkSetRelations(set(func(d *spec.Descriptor) {
		d.Requires = []string{"team >= 1.0.0"}
	}), kits), "the only thing providing")
}

// The set contributes last, because it is the author with the whole
// composition in view — unless an edge says otherwise. Edges run both
// ways: a kit integrating with what the set provides makes the set its
// provider, and one each way is a circle nothing can order.
func TestOrderContributions(t *testing.T) {
	set := &spec.Descriptor{
		SchemaVersion: spec.SchemaVersion, Kind: spec.KindMixin,
		Version: "1.0.0", Provides: []string{"team@1.0.0"},
	}
	kit := func(ref string, mutate func(*spec.Descriptor)) spec.Contribution {
		d := &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindMixin, Version: "1.0.0"}
		mutate(d)
		return spec.Contribution{Reference: ref, Descriptor: d}
	}
	provider := func(name string) func(*spec.Descriptor) {
		return func(d *spec.Descriptor) { d.Provides = []string{name + "@1.0.0"} }
	}

	ordered, err := orderContributions(set, []spec.Contribution{
		kit("a", provider("a")), kit("b", provider("b")),
	})
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b", "the set descriptor"}, references(ordered))

	// A kit integrating with what the set provides puts the set first.
	ordered, err = orderContributions(set, []spec.Contribution{
		kit("a", provider("a")),
		kit("b", func(d *spec.Descriptor) {
			provider("b")(d)
			d.Integrates = []string{"team >= 1.0.0"}
		}),
	})
	require.NoError(t, err)
	require.Equal(t, []string{"a", "the set descriptor", "b"}, references(ordered))

	// And the reverse edge orders the other way.
	withRequire := &spec.Descriptor{
		SchemaVersion: spec.SchemaVersion, Kind: spec.KindMixin, Version: "1.0.0",
		Provides: []string{"team@1.0.0"}, Requires: []string{"a >= 1.0.0"},
	}
	ordered, err = orderContributions(withRequire, []spec.Contribution{kit("a", provider("a"))})
	require.NoError(t, err)
	require.Equal(t, []string{"a", "the set descriptor"}, references(ordered))

	// One edge each way is a circle.
	_, err = orderContributions(withRequire, []spec.Contribution{
		kit("a", func(d *spec.Descriptor) {
			provider("a")(d)
			d.Integrates = []string{"team >= 1.0.0"}
		}),
	})
	require.ErrorContains(t, err, "depend on each other in a circle")
}

func references(cs []spec.Contribution) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Reference)
	}
	return out
}

// An arg's env binding is an exact value, so it overrides what the
// image already says rather than sitting beside it — a duplicate key
// otherwise, and for PATH the additive treatment a mixin's PATH gets.
func TestApplyArgExportsOverrides(t *testing.T) {
	config := ocispecs.ImageConfig{Env: []string{"PATH=/usr/bin", "KEEP=yes"}}
	kits := []resolvedKit{{reference: "reg.io/tool:1", env: map[string]string{
		"PATH": "/opt/tool/bin", "TOOL_MODE": "fast",
	}}}
	require.NoError(t, applyArgExports(&config, kits))
	require.Equal(t, []string{"PATH=/opt/tool/bin", "KEEP=yes", "TOOL_MODE=fast"}, config.Env,
		"the exact value replaces the image's own, and a new one is appended")

	// Deterministic: the same inputs write the same config.
	for range 8 {
		fresh := ocispecs.ImageConfig{}
		require.NoError(t, applyArgExports(&fresh, []resolvedKit{{reference: "reg.io/tool:1", env: map[string]string{
			"A": "1", "B": "2", "C": "3", "D": "4",
		}}}))
		require.Equal(t, []string{"A=1", "B=2", "C=3", "D=4"}, fresh.Env)
	}

	// Two kits disagreeing about one variable is the disagreement the
	// config merge would have caught, had these been contributions.
	err := applyArgExports(&ocispecs.ImageConfig{}, []resolvedKit{
		{reference: "reg.io/a:1", env: map[string]string{"TOOL_MODE": "fast"}},
		{reference: "reg.io/b:1", env: map[string]string{"TOOL_MODE": "slow"}},
	})
	require.ErrorContains(t, err, "both export TOOL_MODE")

	// Agreeing is not a disagreement.
	require.NoError(t, applyArgExports(&ocispecs.ImageConfig{}, []resolvedKit{
		{reference: "reg.io/a:1", env: map[string]string{"TOOL_MODE": "fast"}},
		{reference: "reg.io/b:1", env: map[string]string{"TOOL_MODE": "fast"}},
	}))
}

// Layers and declarations follow one graph. orderKits knows the set
// only by what it provides, so a kit the set itself requires could
// land after a kit that requires the set — the content order is taken
// from the full contribution order instead.
func TestKitsFollowTheContributionOrder(t *testing.T) {
	kit := func(ref string) resolvedKit {
		return resolvedKit{reference: ref, descriptor: &spec.Descriptor{
			SchemaVersion: spec.SchemaVersion, Kind: spec.KindMixin, Version: "1.0.0",
		}}
	}
	// The resolver's reduced order, which put B ahead of Z.
	reduced := []resolvedKit{kit("reg.io/b:1"), kit("reg.io/z:1")}

	// The full graph knows the set requires Z, so Z precedes the set,
	// and B — which requires what the set provides — comes last.
	contributions := []spec.Contribution{
		{Reference: "reg.io/z:1"},
		{Reference: "the set descriptor"},
		{Reference: "reg.io/b:1"},
	}
	ordered := kitsInContributionOrder(contributions, reduced)
	require.Equal(t, []string{"reg.io/z:1", "reg.io/b:1"}, kitReferences(ordered),
		"transitively, Z precedes B; the set itself is not a kit")
}

func kitReferences(kits []resolvedKit) []string {
	out := make([]string, 0, len(kits))
	for _, k := range kits {
		out = append(out, k.reference)
	}
	return out
}

// An OCI config may carry a key twice, and a runtime reading the last
// one would see the value the override was meant to replace.
func TestApplyArgExportsClearsDuplicateKeys(t *testing.T) {
	config := ocispecs.ImageConfig{Env: []string{"MODE=old1", "KEEP=yes", "MODE=old2"}}
	require.NoError(t, applyArgExports(&config, []resolvedKit{
		{reference: "reg.io/tool:1", env: map[string]string{"MODE": "new"}},
	}))
	require.Equal(t, []string{"MODE=new", "KEEP=yes"}, config.Env,
		"one entry, in the first position the key held")

	// An entry that is not an assignment is left alone.
	config = ocispecs.ImageConfig{Env: []string{"BARE", "MODE=old"}}
	require.NoError(t, applyArgExports(&config, []resolvedKit{
		{reference: "reg.io/tool:1", env: map[string]string{"MODE": "new"}},
	}))
	require.Equal(t, []string{"BARE", "MODE=new"}, config.Env)
}

func TestKitDeclarationsRejectsMalformedReExportedNetworkPolicy(t *testing.T) {
	published, err := spec.Decode([]byte(`schemaVersion: "3"
kind: mixin
args:
  host: {required: true}
capabilities:
  - type: com.docker.sandbox/network-policy@2
    config:
      runtime:
        allow:
          - "${{ kit.args.host }}"
          - hosts: ["${{ kit.args.host }}"]
            methods: []
`))
	require.NoError(t, err)

	_, _, err = kitDeclarations(published, spec.Kit{
		Args: map[string]string{"host": "${{ kit.args.target }}"},
	}, map[string]spec.Arg{"target": {Required: true}})
	require.ErrorContains(t, err, "declarations are invalid once the set's args are applied")
	require.ErrorContains(t, err, "empty methods list")
}

func TestStageSetContextRejectsReExportedBodyBeforeWriting(t *testing.T) {
	for _, grouped := range []bool{false, true} {
		name := "combined"
		if grouped {
			name = "separate"
		}
		t.Run(name, func(t *testing.T) {
			entry := spec.Capability{Type: spec.CapabilityAgentContext, Config: map[string]any{"content": "Team: ${{ kit.args.team }}"}}
			if grouped {
				entry = spec.Capability{Group: &spec.CapabilityGroup{Optional: true, Capabilities: []spec.Capability{entry}}}
			}
			published := &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindMixin,
				Args: map[string]spec.Arg{"team": {Required: true}}, Capabilities: []spec.Capability{entry}}
			declarations, _, err := kitDeclarations(published, spec.Kit{Args: map[string]string{"team": "${{ kit.args.target }}"}}, map[string]spec.Arg{"target": {Required: true}})
			require.NoError(t, err)
			merged, err := spec.Merge([]spec.Contribution{{Reference: "source-kit", Descriptor: declarations}}, spec.MergeOptions{ContextPath: "/set/context.md"})
			require.NoError(t, err)
			require.Len(t, merged.ContextSources, 1)
			// A valid body first proves validation completes before any staging.
			first := spec.ContextSource{Reference: "first-kit", Content: "already resolved"}
			if grouped {
				first.Target = "/set/first.md"
				require.NotEmpty(t, merged.ContextSources[0].Target)
			}
			sources := append([]spec.ContextSource{first}, merged.ContextSources...)
			_, err = stageSetContext(t.Context(), nil, nil, nil, "/set/context.md", sources)
			require.ErrorContains(t, err, "source-kit references [target]")
			require.ErrorContains(t, err, "create-phase expansion never reaches")
		})
	}
}

func TestKitDeclarationsPreserveFinalEnvironment(t *testing.T) {
	for _, reference := range []string{"${{ kit.env.HOME }}", "${{\nkit.env.HOME}}"} {
		t.Run(reference, func(t *testing.T) {
			published := &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindMixin,
				Capabilities: []spec.Capability{{Type: spec.CapabilityLifecycle, Config: map[string]any{
					"files": []any{map[string]any{"path": reference + "/.config/tool", "content": "configured"}},
				}}},
			}
			d, _, err := kitDeclarations(published, spec.Kit{}, nil)
			require.NoError(t, err)
			merged, err := spec.Merge([]spec.Contribution{{Reference: "tool", Descriptor: d}}, spec.MergeOptions{})
			require.NoError(t, err)
			members := spec.DeclaredCapabilities(merged.Descriptor.Capabilities)
			lc, err := spec.LifecycleOf(members)
			require.NoError(t, err)
			require.Equal(t, reference+"/.config/tool", lc.Files[0].Path)
		})
	}
}

// Exercise the publisher's re-export path, not default expansion: two
// independently published Kits feed text references into the set merge.
func TestSetReExportsVolumeRequests(t *testing.T) {
	for _, tc := range []struct {
		name, field, first, second, value, other string
		match                                    bool
	}{
		{"shared size", "size", "${{ kit.args.shared }}", "${{ kit.args.shared }}", "2g", "", true},
		{"equivalent independent sizes", "size", "${{ kit.args.shared }}", "${{ kit.args.other }}", "1g", "1024m", true},
		{"conflicting independent sizes", "size", "${{ kit.args.shared }}", "${{ kit.args.other }}", "1g", "2g", false},
		{"equivalent literal size", "size", "${{ kit.args.shared }}", "1024m", "1g", "", true},
		{"conflicting literal size", "size", "${{ kit.args.shared }}", "2g", "1g", "", false},
		{"shared mode", "mode", "${{ kit.args.shared }}", "${{ kit.args.shared }}", "0755", "", true},
		{"shared destination", "path", "${{ kit.args.shared }}", "${{ kit.args.shared }}", "/cache", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setArgs := map[string]spec.Arg{"shared": {Required: true}, "other": {Required: true}}
			var inputs []spec.Contribution
			for i, supplied := range []string{tc.first, tc.second} {
				config := map[string]any{"path": "/cache", tc.field: "${{ kit.args.request }}"}
				published := &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindMixin,
					Args:         map[string]spec.Arg{"request": {Required: true}},
					Capabilities: []spec.Capability{{Type: spec.CapabilityVolume, Config: config}},
				}
				raw, err := json.Marshal(published)
				require.NoError(t, err)
				_, err = spec.ValidatePublished(raw, published)
				require.NoError(t, err)
				declarations, _, err := kitDeclarations(published, spec.Kit{
					Args: map[string]string{"request": supplied},
				}, setArgs)
				require.NoError(t, err)
				inputs = append(inputs, spec.Contribution{Reference: fmt.Sprintf("cache-kit-%d", i), Descriptor: declarations})
			}
			merged, err := spec.Merge(inputs, spec.MergeOptions{})
			require.NoError(t, err, "publication cannot judge sizes that still depend on create-phase inputs")
			merged.Descriptor.Args = setArgs
			raw, err := publishedSetDescriptor(merged.Descriptor)
			require.NoError(t, err)
			members := spec.DeclaredCapabilities(merged.Descriptor.Capabilities)
			require.Len(t, members, 2, "keep both requests until their concrete settings can reconcile")
			require.Equal(t, tc.first, members[0].Config[tc.field])
			require.Equal(t, "cache-kit-0", members[0].Source.Kit)
			require.Equal(t, "cache-kit-1", members[1].Source.Kit)

			expanded, err := spec.ExpandCreateArgs(raw, setArgs, map[string]string{"shared": tc.value, "other": tc.other})
			require.NoError(t, err)
			d, err := spec.Decode(expanded)
			require.NoError(t, err)
			_, err = spec.ValidateExpandedDeclarations(expanded, d)
			require.NoError(t, err)
			selected, err := spec.SelectCapabilities(t.Context(), d, spec.Supported(spec.CapabilityVolume))
			require.NoError(t, err)
			d.Capabilities = selected.Capabilities
			composed, err := spec.Compose([]spec.Contribution{{Reference: "published-set", Descriptor: d}})
			if !tc.match {
				require.Nil(t, composed)
				require.ErrorContains(t, err, "different com.docker.sandbox/volume@1 configurations at /cache")
				require.ErrorContains(t, err, "cache-kit-0")
				require.ErrorContains(t, err, "cache-kit-1")
				return
			}
			require.NoError(t, err)
			volumes, err := spec.VolumesOf(composed.Capabilities)
			require.NoError(t, err)
			require.Len(t, volumes, 1)
			require.Equal(t, tc.value, composed.Capabilities[0].Config[tc.field])
		})
	}
}
