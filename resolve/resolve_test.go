package resolve

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/docker/sandbox-kit-spec/v3/spec"
)

func unit(ref, kind string, mutate ...func(*spec.Descriptor)) *Unit {
	d := &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: kind}
	for _, m := range mutate {
		m(d)
	}
	return &Unit{Reference: ref, Digest: "sha256:" + ref, Descriptor: d}
}

func provides(caps ...string) func(*spec.Descriptor) {
	return func(d *spec.Descriptor) { d.Provides = caps }
}

func requires(caps ...string) func(*spec.Descriptor) {
	return func(d *spec.Descriptor) { d.Requires = caps }
}

func conflicts(caps ...string) func(*spec.Descriptor) {
	return func(d *spec.Descriptor) { d.Conflicts = caps }
}

func integrates(caps ...string) func(*spec.Descriptor) {
	return func(d *spec.Descriptor) { d.Integrates = caps }
}

func TestResolveIntegrates(t *testing.T) {
	sandbox := unit("reg.io/base:1", spec.KindWorkload)

	// Absent provider: the kit functions without it, resolution passes.
	tool := unit("reg.io/zz-tool:1", spec.KindMixin, integrates("gh >= 2.0.0"))
	r, err := Resolve([]*Unit{tool, sandbox})
	require.NoError(t, err)
	require.Equal(t, []*Unit{tool}, r.Mixins)

	// A met entry orders like a require: the provider composes before
	// the kit that integrates with it, despite lexicographic order.
	gh := unit("reg.io/gh-kit:2.98.0", spec.KindMixin, provides("gh@2.98.0"))
	r, err = Resolve([]*Unit{tool, gh, sandbox})
	require.NoError(t, err)
	pos := map[string]int{}
	for i, u := range r.Mixins {
		pos[u.Reference] = i
	}
	require.Less(t, pos["reg.io/gh-kit:2.98.0"], pos["reg.io/zz-tool:1"])

	// A PRESENT provider below the stated minimum is incoherence: the
	// integration the entry names would break.
	oldGh := unit("reg.io/gh-kit:1.0.0", spec.KindMixin, provides("gh@1.0.0"))
	_, err = Resolve([]*Unit{tool, oldGh, sandbox})
	require.ErrorContains(t, err, `integrates with "gh >= 2.0.0" but the set only offers gh@1.0.0`)
}

func TestResolveTwoKitComposition(t *testing.T) {
	claude := unit("reg.io/claude-kit:2.1.0", spec.KindWorkload, provides("claude@2.1.0"))
	gh := unit("reg.io/gh-kit:2.98.0", spec.KindMixin, provides("gh@2.98.0"))

	r, err := Resolve([]*Unit{gh, claude})
	require.NoError(t, err)
	require.Same(t, claude, r.Workload)
	require.Equal(t, []*Unit{gh}, r.Mixins)
}

func TestResolveOrdersProvidersFirst(t *testing.T) {
	sandbox := unit("reg.io/base:1", spec.KindWorkload)
	// zz-tool requires node, so node-kit must compose first despite
	// sorting after aa-tool lexicographically.
	node := unit("reg.io/node-kit:22", spec.KindMixin, provides("node@22.0.0"))
	tool := unit("reg.io/zz-tool:1", spec.KindMixin, requires("node >= 20"))
	aa := unit("reg.io/aa-tool:1", spec.KindMixin)

	r, err := Resolve([]*Unit{tool, aa, node, sandbox})
	require.NoError(t, err)

	pos := map[string]int{}
	for i, u := range r.Mixins {
		pos[u.Reference] = i
	}
	require.Less(t, pos["reg.io/node-kit:22"], pos["reg.io/zz-tool:1"])

	// Determinism: same set, any input order, same output.
	r2, err := Resolve([]*Unit{sandbox, node, aa, tool})
	require.NoError(t, err)
	require.Equal(t, refs(r.Mixins), refs(r2.Mixins))
}

func refs(units []*Unit) []string {
	out := make([]string, len(units))
	for i, u := range units {
		out[i] = u.Reference
	}
	return out
}

func TestResolveReportsEveryProblem(t *testing.T) {
	a := unit("reg.io/a:1", spec.KindWorkload, requires("node >= 20"), conflicts("python"))
	b := unit("reg.io/b:1", spec.KindWorkload, provides("python@3.12"))

	_, err := Resolve([]*Unit{a, b})
	require.Error(t, err)
	require.ErrorContains(t, err, "more than one workload")
	require.ErrorContains(t, err, `requires "node >= 20" but nothing in the set provides it`)
	require.ErrorContains(t, err, `conflicts with "python"`)
}

func TestResolveInfersVersionFromReferenceTag(t *testing.T) {
	// An unversioned provide inherits the consumed tag's version.
	sandbox := unit("reg.io/kits/hello:2.1.0", spec.KindWorkload, provides("hello"))
	tool := unit("reg.io/kits/tool:1.0.0", spec.KindMixin, requires("hello >= 2.0.0"))

	r, err := Resolve([]*Unit{tool, sandbox})
	require.NoError(t, err)
	require.Same(t, sandbox, r.Workload)

	// Below the minimum, the inferred version appears in the error.
	older := unit("reg.io/kits/hello:1.0.3", spec.KindWorkload, provides("hello"))
	_, err = Resolve([]*Unit{tool, older})
	require.ErrorContains(t, err, "the set only offers hello@1.0.3")

	// Non-version tags infer nothing and stay unversioned.
	dev := unit("reg.io/kits/hello:dev", spec.KindWorkload, provides("hello"))
	_, err = Resolve([]*Unit{tool, dev})
	require.ErrorContains(t, err, "hello (unversioned, from reg.io/kits/hello:dev)")

	// An explicit @version outranks the tag.
	pinnedOld := unit("reg.io/kits/hello:9.9.9", spec.KindWorkload, provides("hello@1.0.0"))
	_, err = Resolve([]*Unit{tool, pinnedOld})
	require.ErrorContains(t, err, "the set only offers hello@1.0.0")
}

func TestVersionFromReference(t *testing.T) {
	require.Equal(t, "1.0.3", VersionFromReference("docker.io/example/sbx-kit-hello:1.0.3"))
	require.Equal(t, "2.1.0", VersionFromReference("reg.io:5000/kits/hello:v2.1.0"),
		"a conventional v-prefixed tag names the bare version")
	require.Equal(t, "2.1.0", VersionFromReference("reg.io/kits/hello:2.1.0@sha256:abc"))
	require.Empty(t, VersionFromReference("reg.io/kits/hello:dev"))
	require.Empty(t, VersionFromReference("reg.io/kits/hello"))
	require.Empty(t, VersionFromReference("reg.io:5000/kits/hello"), "a registry port is not a tag")
	require.Empty(t, VersionFromReference("./local-kit"))

	// Git references: a version-shaped #ref= is the git analog of a tag.
	require.Equal(t, "2.1.0", VersionFromReference("git+https://github.com/org/kits.git#ref=v2.1.0&dir=gh"))
	require.Empty(t, VersionFromReference("git+https://github.com/org/kits.git#ref=main"))
	require.Empty(t, VersionFromReference("git+https://github.com/org/kits.git"))
}

func TestResolveVersionFallbackChain(t *testing.T) {
	tool := unit("reg.io/kits/tool:1.0.0", spec.KindMixin, requires("hello >= 2.0.0"))

	// A directory kit's reference carries no version; the descriptor's
	// version: field fills in.
	dirKit := unit("./hello-kit", spec.KindWorkload, provides("hello"))
	dirKit.Descriptor.Version = "2.1.0"
	_, err := Resolve([]*Unit{tool, dirKit})
	require.NoError(t, err)

	// A version-shaped git ref wins over the descriptor version, and its
	// conventional v prefix normalizes away.
	gitKit := unit("git+https://github.com/org/kits.git#ref=v1.0.0&dir=hello", spec.KindWorkload, provides("hello"))
	gitKit.Descriptor.Version = "9.9.9"
	_, err = Resolve([]*Unit{tool, gitKit})
	require.ErrorContains(t, err, "the set only offers hello@1.0.0",
		"the reference is identity; the descriptor version cannot override it")

	// An OCI version tag likewise overrides a stale descriptor version.
	ociKit := unit("reg.io/kits/hello:3.0.0", spec.KindWorkload, provides("hello"))
	ociKit.Descriptor.Version = "1.0.0"
	_, err = Resolve([]*Unit{tool, ociKit})
	require.NoError(t, err, "the consumed tag 3.0.0 satisfies >= 2.0.0 regardless of the stale version field")

	// Explicit provides@version outranks everything.
	pinned := unit("reg.io/kits/hello:3.0.0", spec.KindWorkload, provides("hello@1.0.0"))
	pinned.Descriptor.Version = "3.0.0"
	_, err = Resolve([]*Unit{tool, pinned})
	require.ErrorContains(t, err, "the set only offers hello@1.0.0")
}

func TestResolveMinimumVersion(t *testing.T) {
	sandbox := unit("reg.io/base:1", spec.KindWorkload, requires("gh >= 2.99"))
	gh := unit("reg.io/gh-kit:2.98.0", spec.KindMixin, provides("gh@2.98.0"))

	_, err := Resolve([]*Unit{sandbox, gh})
	require.ErrorContains(t, err, "the set only offers gh@2.98.0")

	gh299 := unit("reg.io/gh-kit:2.99.1", spec.KindMixin, provides("gh@2.99.1"))
	_, err = Resolve([]*Unit{sandbox, gh299})
	require.NoError(t, err)
}

func TestResolveVersionRange(t *testing.T) {
	sandbox := unit("reg.io/base:1", spec.KindWorkload, requires("node >= 20.0.0, < 21.0.0"))
	node20 := unit("reg.io/node:20.5.0", spec.KindMixin, provides("node@20.5.0"))
	_, err := Resolve([]*Unit{sandbox, node20})
	require.NoError(t, err)

	node21 := unit("reg.io/node:21.0.0", spec.KindMixin, provides("node@21.0.0"))
	_, err = Resolve([]*Unit{sandbox, node21})
	require.ErrorContains(t, err, `requires "node >= 20.0.0, < 21.0.0" but the set only offers node@21.0.0`)

	// Upper-bound-only integrates fails when a present provider is too new.
	tool := unit("reg.io/tool:1", spec.KindMixin, integrates("api <= 1.0.0"))
	api := unit("reg.io/api:2", spec.KindMixin, provides("api@2.0.0"))
	base := unit("reg.io/base:1", spec.KindWorkload)
	_, err = Resolve([]*Unit{base, tool, api})
	require.ErrorContains(t, err, `integrates with "api <= 1.0.0"`)
}

func TestResolveOwnProvideDoesNotSatisfyOwnRequire(t *testing.T) {
	// A kit requiring a capability it also provides is asking for another
	// provider, not vouching for itself.
	sandbox := unit("reg.io/base:1", spec.KindWorkload)
	weird := unit("reg.io/weird:1", spec.KindMixin, provides("cache"), requires("cache"))

	_, err := Resolve([]*Unit{sandbox, weird})
	require.ErrorContains(t, err, "the set only offers cache@1 (from reg.io/weird:1)")
}

func TestResolveCycle(t *testing.T) {
	sandbox := unit("reg.io/base:1", spec.KindWorkload)
	a := unit("reg.io/a:1", spec.KindMixin, provides("a"), requires("b"))
	b := unit("reg.io/b:1", spec.KindMixin, provides("b"), requires("a"))

	_, err := Resolve([]*Unit{sandbox, a, b})
	require.ErrorContains(t, err, "dependency cycle among reg.io/a:1, reg.io/b:1")
}

func TestResolveNoSandbox(t *testing.T) {
	_, err := Resolve([]*Unit{unit("reg.io/gh:1", spec.KindMixin)})
	require.ErrorContains(t, err, "no workload kit")
}

func TestLockRoundTripAndRecreate(t *testing.T) {
	claude := unit("reg.io/claude-kit:2.1.0", spec.KindWorkload, provides("claude@2.1.0"))
	claude.Args = map[string]string{"timeout": "60000"}
	claude.Descriptor.Capabilities = []spec.Capability{{
		Type:   spec.CapabilityNetworkPolicy,
		Config: map[string]any{"runtime": map[string]any{"allow": []any{"api.anthropic.com"}}},
	}}
	gh := unit("reg.io/gh-kit:2.98.0", spec.KindMixin, provides("gh@2.98.0"))

	r, err := Resolve([]*Unit{gh, claude})
	require.NoError(t, err)

	lock := LockFrom(r)
	data, err := lock.Marshal()
	require.NoError(t, err)

	parsed, err := ParseLock(data)
	require.NoError(t, err)
	require.Len(t, parsed.Kits, 2)
	require.Equal(t, "reg.io/claude-kit:2.1.0", parsed.Kits[0].Reference, "sandbox first")
	require.Equal(t, map[string]string{"timeout": "60000"}, parsed.Kits[0].Args)
	require.Equal(t, []string{"api.anthropic.com"}, parsed.Kits[0].Permissions.NetworkRuntimeAllow)

	// Bit-identical recreate verifies.
	require.NoError(t, parsed.VerifyRecreate(r))

	// A moved tag (different digest) is refused.
	movedClaude := unit("reg.io/claude-kit:2.1.0", spec.KindWorkload, provides("claude@2.1.0"))
	movedClaude.Digest = "sha256:moved"
	movedClaude.Descriptor.Capabilities = claude.Descriptor.Capabilities
	moved, err := Resolve([]*Unit{gh, movedClaude})
	require.NoError(t, err)
	require.ErrorContains(t, parsed.VerifyRecreate(moved), "the tag moved")
}

func TestGateOnVersionMovement(t *testing.T) {
	old := unit("reg.io/gh-kit:latest", spec.KindMixin, provides("gh@2.98.0"))
	old.Descriptor.Capabilities = []spec.Capability{{
		Type:   spec.CapabilityNetworkPolicy,
		Config: map[string]any{"runtime": map[string]any{"allow": []any{"api.github.com", "github.com"}}},
	}}
	sandbox := unit("reg.io/base:1", spec.KindWorkload)

	r, err := Resolve([]*Unit{sandbox, old})
	require.NoError(t, err)
	lock := LockFrom(r)

	// Same surface, new version: silent.
	same := unit("reg.io/gh-kit:latest", spec.KindMixin, provides("gh@2.99.0"))
	same.Digest = "sha256:new"
	same.Descriptor.Capabilities = old.Descriptor.Capabilities
	rSame, err := Resolve([]*Unit{sandbox, same})
	require.NoError(t, err)
	require.Empty(t, lock.Gate(rSame))

	// Widened surface: gated with the specific delta.
	wide := unit("reg.io/gh-kit:latest", spec.KindMixin, provides("gh@3.0.0"))
	wide.Digest = "sha256:wider"
	wide.Descriptor.Capabilities = []spec.Capability{
		{
			Type:   spec.CapabilityNetworkPolicy,
			Config: map[string]any{"runtime": map[string]any{"allow": []any{"api.github.com", "github.com", "telemetry.example.com"}}},
		},
		{
			Type:   spec.CapabilityCredential,
			Config: map[string]any{"service": "github", "phase": "runtime", "apiKey": map[string]any{"name": "GH_TOKEN"}},
		},
	}
	rWide, err := Resolve([]*Unit{sandbox, wide})
	require.NoError(t, err)
	widenings := lock.Gate(rWide)
	require.Len(t, widenings, 2)
	require.Equal(t, "network.runtime.allow: telemetry.example.com", widenings[0].String())
	require.Equal(t, "credentials.runtime: github", widenings[1].String())

	// A kit not in the lock at all: its whole surface is the widening.
	extra := unit("reg.io/new-kit:1", spec.KindMixin)
	extra.Descriptor.Capabilities = []spec.Capability{{Type: spec.CapabilityPrivileged}}
	rExtra, err := Resolve([]*Unit{sandbox, old, extra})
	require.NoError(t, err)
	require.Equal(t, "privileged: requests privileged execution", lock.Gate(rExtra)[0].String())

	// A runtime-provided capability is a grant like privileged:
	// requesting one is a widening the gate must surface, by type name.
	reg := unit("reg.io/gh-kit:latest", spec.KindMixin, provides("gh@3.1.0"))
	reg.Digest = "sha256:registry"
	reg.Descriptor.Capabilities = append([]spec.Capability{}, old.Descriptor.Capabilities...)
	reg.Descriptor.Capabilities = append(reg.Descriptor.Capabilities, spec.Capability{Type: spec.CapabilityKitRegistry})
	rReg, err := Resolve([]*Unit{sandbox, reg})
	require.NoError(t, err)
	regWidenings := lock.Gate(rReg)
	require.Len(t, regWidenings, 1)
	require.Equal(t, "services: "+spec.CapabilityKitRegistry, regWidenings[0].String())
}

func TestGateReportsUnknownTypeConfigChange(t *testing.T) {
	sandbox := unit("reg.io/base:1", spec.KindWorkload)
	thing := func(version, path string) *Unit {
		u := unit("reg.io/thing-kit:latest", spec.KindMixin, provides("thing@"+version))
		u.Digest = "sha256:" + version
		u.Descriptor.Capabilities = []spec.Capability{{
			Type:   "com.example/thing@1",
			Config: map[string]any{"path": path},
		}}
		return u
	}

	r, err := Resolve([]*Unit{sandbox, thing("1.0.0", "/data")})
	require.NoError(t, err)
	lock := LockFrom(r)

	moved, err := Resolve([]*Unit{sandbox, thing("1.1.0", "/other")})
	require.NoError(t, err)
	widenings := lock.Gate(moved)
	require.Len(t, widenings, 1)
	require.Equal(t, "services", widenings[0].Category)
	require.Equal(t, spec.SurfaceOf(moved.Ordered()[1].Descriptor).Services[0], widenings[0].Detail)
}

func TestResolveRefusesTwoProvidersOfOneName(t *testing.T) {
	// The canonical mistake: a workload that carries its agent composed
	// with the mixin that installs the same agent.
	claude := unit("reg.io/claude:2.1.0", spec.KindWorkload, provides("claude@2.1.0"))
	mixin := unit("reg.io/claude-mixin:2.1.4", spec.KindMixin, provides("claude@2.1.4"))

	_, err := Resolve([]*Unit{claude, mixin})
	require.ErrorContains(t, err, `capability "com.docker.kit/claude" is provided by more than one kit`)
	require.ErrorContains(t, err, "reg.io/claude:2.1.0 (2.1.0)")
	require.ErrorContains(t, err, "reg.io/claude-mixin:2.1.4 (2.1.4)")

	// Equal versions are no better: one name, one owner.
	twin := unit("reg.io/claude-twin:2.1.0", spec.KindMixin, provides("claude@2.1.0"))
	_, err = Resolve([]*Unit{claude, twin})
	require.ErrorContains(t, err, "provided by more than one kit")

	// Normalization applies: a bare name and its qualified spelling are
	// the same capability.
	qualified := unit("reg.io/claude-q:2.1.0", spec.KindMixin, provides("com.docker.kit/claude@2.1.0"))
	_, err = Resolve([]*Unit{claude, qualified})
	require.ErrorContains(t, err, "provided by more than one kit")

	// One kit repeating a name — bare plus qualified — is redundancy
	// within one owner, not two owners; the rule counts kits.
	repeated := unit("reg.io/solo:1.0.0", spec.KindWorkload,
		provides("foo@1.0.0", "com.docker.kit/foo@1.0.0"))
	_, err = Resolve([]*Unit{repeated})
	require.NoError(t, err)
}

func TestResolveRefusesTwoCredentialOwners(t *testing.T) {
	// Both kits declare the (github, runtime) credential; the page says
	// one credential, one owner.
	cfg := map[string]any{
		"service": "github", "phase": "runtime",
		"apiKey": map[string]any{
			"name": "GITHUB_TOKEN",
			"inject": []any{map[string]any{
				"domain": "api.github.com", "header": "Authorization", "format": "Bearer %s"}},
		},
	}
	policy := map[string]any{"runtime": map[string]any{"allow": []any{"api.github.com:443"}}}
	base := unit("reg.io/base:1", spec.KindWorkload)
	base.Descriptor.Capabilities = []spec.Capability{
		{Type: spec.CapabilityNetworkPolicy, Config: policy},
		{Type: spec.CapabilityCredential, Config: cfg},
	}
	tool := unit("reg.io/tool:1", spec.KindMixin)
	tool.Descriptor.Capabilities = []spec.Capability{
		{Type: spec.CapabilityNetworkPolicy, Config: policy},
		{Type: spec.CapabilityCredential, Config: cfg},
	}
	_, err := Resolve([]*Unit{base, tool})
	require.ErrorContains(t, err, `credential (github, runtime) is declared by more than one kit`)
	require.ErrorContains(t, err, "reg.io/base:1, reg.io/tool:1")
}

func TestResolveCredentialPhaseLists(t *testing.T) {
	for _, phase := range []string{"install", "runtime"} {
		base := unit("reg.io/base:1", spec.KindWorkload)
		base.Descriptor.Capabilities = []spec.Capability{{
			Type: spec.CapabilityCredential,
			Config: map[string]any{
				"service": "github", "phase": []string{"install", "runtime"},
				"apiKey": map[string]any{"name": "GH_TOKEN"},
			},
		}}
		tool := unit("reg.io/tool:1", spec.KindMixin)
		tool.Descriptor.Capabilities = []spec.Capability{{
			Type: spec.CapabilityCredential,
			Config: map[string]any{
				"service": "github", "phase": phase,
				"apiKey": map[string]any{"name": "GH_TOKEN"},
			},
		}}
		_, err := Resolve([]*Unit{base, tool})
		require.ErrorContains(t, err, "credential (github, "+phase+") is declared by more than one kit")
		tool.Descriptor.Capabilities[0].Config["service"] = "gitlab"
		_, err = Resolve([]*Unit{base, tool})
		require.NoError(t, err)
	}
}

// A set of overlays is a legitimate thing to publish: it composes onto a
// workload later, exactly as its own kits would have. ResolvePartial
// judges it complete while holding every other rule.
func TestResolvePartialAllowsNoWorkload(t *testing.T) {
	gh := unit("reg.io/gh:2.98.0", spec.KindMixin, provides("gh@2.98.0"))
	tool := unit("reg.io/tool:1", spec.KindMixin, provides("tool"), requires("gh >= 2.0.0"))

	_, err := Resolve([]*Unit{gh, tool})
	require.ErrorContains(t, err, "no workload kit in the set")

	r, err := ResolvePartial([]*Unit{tool, gh})
	require.NoError(t, err)
	require.Nil(t, r.Workload)
	require.Equal(t, []*Unit{gh, tool}, r.Ordered(),
		"the provider still composes before its requirer, and Ordered omits the absent base")

	// Every other rule still holds.
	_, err = ResolvePartial([]*Unit{gh, unit("reg.io/gh-again:1", spec.KindMixin, provides("gh@1.0.0"))})
	require.ErrorContains(t, err, "provided by more than one kit")

	_, err = ResolvePartial([]*Unit{
		unit("reg.io/a:1", spec.KindWorkload),
		unit("reg.io/b:1", spec.KindWorkload),
	})
	require.ErrorContains(t, err, "more than one workload kit",
		"two bases are incoherent whether or not anything runs")

	_, err = ResolvePartial([]*Unit{tool})
	require.ErrorContains(t, err, "nothing in the set provides it",
		"partial means no workload required, not that requirements go unjudged")
}

// A kit's unversioned provides take the version its reference carries,
// and only fall back to the descriptor when it carries none. The rule
// has a second caller — a published set records what its kits offered
// — so it is exported and pinned here.
func TestEffectiveProvideVersion(t *testing.T) {
	stale := &spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindWorkload, Version: "1.0.0"}

	require.Equal(t, "2.0.0", EffectiveProvideVersion("reg.io/base:2.0.0", stale),
		"a version-shaped tag wins, so a stale descriptor cannot lie about it")
	require.Equal(t, "1.0.0", EffectiveProvideVersion("reg.io/base:dev", stale),
		"a non-version tag leaves the descriptor as the only home")
	require.Equal(t, "1.0.0", EffectiveProvideVersion("reg.io/base@sha256:abc", stale))
	require.Empty(t, EffectiveProvideVersion("reg.io/base:dev",
		&spec.Descriptor{SchemaVersion: spec.SchemaVersion, Kind: spec.KindMixin}))
}

// Ordered and Topological answer different questions. A workload's
// layers are the base whatever it requires, so Ordered always starts
// with it; declarations follow the graph, so a provider the workload
// requires comes first there.
func TestTopologicalKeepsTheWorkloadInGraphOrder(t *testing.T) {
	node := unit("reg.io/node:20", spec.KindMixin, provides("node@20.0.0"))
	workload := unit("reg.io/app:1", spec.KindWorkload, provides("app"), requires("node >= 20.0.0"))

	r, err := Resolve([]*Unit{workload, node})
	require.NoError(t, err)

	require.Equal(t, []*Unit{workload, node}, r.Ordered(),
		"layers: the workload is the filesystem the overlay lands on")
	require.Equal(t, []*Unit{node, workload}, r.Topological(),
		"declarations: the provider the workload requires comes first")
}

func TestCapabilityResolutionRequiresSelectedGroups(t *testing.T) {
	for _, partial := range []bool{false, true} {
		for _, optional := range []bool{false, true} {
			t.Run(fmt.Sprintf("partial=%t/optional=%t", partial, optional), func(t *testing.T) {
				credential := spec.Capability{Type: spec.CapabilityCredential, Config: map[string]any{"service": "github.com", "phase": "runtime"}}
				grouped := unit("grouped", spec.KindMixin, func(d *spec.Descriptor) {
					d.Capabilities = []spec.Capability{{Group: &spec.CapabilityGroup{Optional: optional, Capabilities: []spec.Capability{credential}}}}
				})
				ordinary := unit("ordinary", spec.KindMixin, func(d *spec.Descriptor) { d.Capabilities = []spec.Capability{credential} })
				units := []*Unit{grouped, ordinary}
				resolveSelected := ResolvePartial
				if !partial {
					units = append(units, unit("base", spec.KindWorkload))
					resolveSelected = Resolve
				}
				_, err := Dependencies(units, partial)
				require.NoError(t, err)
				_, err = resolveSelected(units)
				require.ErrorContains(t, err, "grouped capabilities[0]: select groups")
				grouped.Descriptor.Capabilities = []spec.Capability{credential}
				_, err = resolveSelected(units)
				require.ErrorContains(t, err, "one credential, one owner")
				grouped.Descriptor.Capabilities = nil
				_, err = resolveSelected(units)
				require.NoError(t, err)
			})
		}
	}
}
