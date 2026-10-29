package spec

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func gitPushKit(t *testing.T, kind, config string) *Descriptor {
	t.Helper()
	return mustDecode(t, "schemaVersion: \"3\"\nkind: "+kind+"\nversion: \"1.0.0\"\ncapabilities:\n  - type: "+CapabilityGitPush+"\n"+config)
}

func TestGitPushValidation(t *testing.T) {
	valid := []string{
		"    config: {remotes: [github.com/docker/*]}\n",
		"    config: {refs: {include: [refs/heads/sbx/*], exclude: [refs/heads/main, refs/tags/*]}}\n",
		"    config: {refs: {exclude: [refs/heads/release/*]}}\n",
		"    config: {delete: deny}\n",
		"    config: {commits: {signed: require, trailer: warn}}\n",
		"    config: {remotes: ['github.com/${{ kit.args.org }}/*'], delete: '${{ kit.args.del }}'}\n    optional: true\n",
	}
	for _, config := range valid {
		for _, kind := range []string{KindWorkload, KindMixin} {
			_, err := Validate(gitPushKit(t, kind, config))
			require.NoError(t, err, "%s: %s", kind, config)
		}
	}

	invalid := []struct{ config, want string }{
		{"", "states no rule"},
		{"    config: {}\n", "states no rule"},
		{"    config: {remotes: []}\n", "remotes is empty"},
		{"    config: {remotes: null}\n", "must not be null"},
		{"    config: {refs: {}}\n", "must state include or exclude"},
		{"    config: {refs: {include: []}}\n", "refs.include is empty"},
		{"    config: {refs: {exclude: null}}\n", "refs.exclude is empty"},
		{"    config: {refs: {include: [main]}}\n", "starts with refs/"},
		{"    config: {refs: {include: ['refs/heads/*/*']}}\n", "at most one *"},
		{"    config: {refs: {include: [refs/heads/a, refs/heads/a]}}\n", "twice"},
		{"    config: {remotes: [GitHub.com/docker/x]}\n", "lowercase DNS host"},
		{"    config: {remotes: [github.com]}\n", "host/owner/repo"},
		{"    config: {delete: never}\n", `must be one of "allow" or "deny"`},
		{"    config: {commits: {}}\n", "must state signed or trailer"},
		{"    config: {commits: {committer: enforce}}\n", "unknown field"},
		{"    config: {commits: {signed: maybe}}\n", `must be one of "require", "prefer" or "ignore"`},
		{"    config: {commits: {trailer: null}}\n", "must not be null"},
		{"    config: {branches: [main]}\n", "unknown field"},
	}
	for _, tt := range invalid {
		_, err := Validate(gitPushKit(t, KindWorkload, tt.config))
		require.ErrorContains(t, err, tt.want, tt.config)
	}

	_, err := Validate(&Descriptor{SchemaVersion: SchemaVersion, Kind: KindWorkload, Capabilities: []Capability{
		{Type: CapabilityGitPush, Config: map[string]any{"delete": "deny"}},
		{Type: CapabilityGitPush, Config: map[string]any{"delete": "allow"}},
	}})
	require.ErrorContains(t, err, "already declared")
}

func TestGitPushPatternMatches(t *testing.T) {
	cases := []struct {
		pattern, value string
		want           bool
	}{
		{"refs/heads/main", "refs/heads/main", true},
		{"refs/heads/main", "refs/heads/main2", false},
		{"refs/heads/*", "refs/heads/feature/x", true},
		{"refs/heads/*", "refs/tags/v1", false},
		{"refs/heads/sbx/*", "refs/heads/sbx/", true},
		{"refs/heads/*-wip", "refs/heads/topic-wip", true},
		{"refs/heads/*-wip", "refs/heads/-wip", true},
		{"refs/heads/*-wip", "refs/heads/wip", false},
		{"github.com/docker/*", "github.com/docker/sandboxes", true},
		{"github.com/docker/*", "github.com/moby/moby", false},
	}
	for _, tt := range cases {
		require.Equal(t, tt.want, GitPushPatternMatches(tt.pattern, tt.value), "%s ~ %s", tt.pattern, tt.value)
	}

	refs := &GitPushRefs{Include: []string{"refs/heads/sbx/*", "refs/heads/feature/*"}, Exclude: []string{"refs/heads/feature/frozen"}}
	require.True(t, GitPushRefAllowed(refs, "refs/heads/sbx/one"))
	require.True(t, GitPushRefAllowed(refs, "refs/heads/feature/new"))
	require.False(t, GitPushRefAllowed(refs, "refs/heads/feature/frozen"), "exclude wins over include")
	require.False(t, GitPushRefAllowed(refs, "refs/heads/main"))
	require.True(t, GitPushRefAllowed(&GitPushRefs{Exclude: []string{"refs/heads/main"}}, "refs/heads/other"), "absent include admits the rest")
	require.True(t, GitPushRefAllowed(nil, "refs/heads/main"))

	require.True(t, GitPushRemoteAllowed(nil, "github.com/x/y"))
	require.True(t, GitPushRemoteAllowed([]string{"github.com/docker/*"}, "github.com/docker/sandboxes.git"), ".git suffix is not part of the name")
	require.False(t, GitPushRemoteAllowed([]string{"github.com/docker/*"}, "gitlab.com/docker/sandboxes"))
}

func TestGitPushMergeIntersects(t *testing.T) {
	workload := contribute(t, "agent", `schemaVersion: "3"
kind: workload
version: "1.0.0"
capabilities:
  - type: com.docker.sandbox/git-push@1
    optional: true
    config:
      remotes: [github.com/docker/*]
      refs: {include: [refs/heads/sbx/*], exclude: [refs/heads/main]}
      delete: allow
      commits: {signed: prefer, trailer: warn}
`)
	mixin := contribute(t, "policy", `schemaVersion: "3"
kind: mixin
version: "1.0.0"
capabilities:
  - type: com.docker.sandbox/git-push@1
    config:
      refs: {exclude: [refs/tags/*]}
      delete: deny
      commits: {trailer: require}
`)
	out := mergeOK(t, workload, mixin).Descriptor
	require.Len(t, out.Capabilities, 1)
	require.False(t, out.Capabilities[0].Optional, "required wins")
	merged, err := GitPushOf(out.Capabilities)
	require.NoError(t, err)
	require.Equal(t, &GitPush{
		Remotes: []string{"github.com/docker/*"},
		Refs:    &GitPushRefs{Include: []string{"refs/heads/sbx/*"}, Exclude: []string{"refs/heads/main", "refs/tags/*"}},
		Delete:  GitPushDeny,
		Commits: &GitPushCommits{Signed: GitPushPrefer, Trailer: GitPushRequire},
	}, merged)
	_, err = Validate(out)
	require.NoError(t, err)

	other := contribute(t, "other", `schemaVersion: "3"
kind: mixin
version: "1.0.0"
capabilities:
  - type: com.docker.sandbox/git-push@1
    config: {refs: {include: [refs/heads/feature/*]}}
`)
	_, err = Merge([]Contribution{workload, other}, MergeOptions{})
	require.ErrorContains(t, err, "refs.include")
	require.ErrorContains(t, err, "agent capabilities[0] and other capabilities[0]")

	same := contribute(t, "same", `schemaVersion: "3"
kind: mixin
version: "1.0.0"
capabilities:
  - type: com.docker.sandbox/git-push@1
    config: {remotes: [github.com/docker/*]}
`)
	out = mergeOK(t, workload, same).Descriptor
	require.Len(t, out.Capabilities, 1, "an identical list is the same bound")
}

func TestWorkloadIdentityValidationAndMerge(t *testing.T) {
	kit := func(kind, config string) *Descriptor {
		return mustDecode(t, "schemaVersion: \"3\"\nkind: "+kind+"\nversion: \"1.0.0\"\ncapabilities:\n  - type: "+CapabilityWorkloadIdentity+"\n"+config)
	}
	for _, config := range []string{"", "    config: {}\n", "    config: {trailers: short}\n", "    config: {ledger: required}\n", "    config: {trailers: '${{ kit.args.t }}'}\n"} {
		_, err := Validate(kit(KindMixin, config))
		require.NoError(t, err, config)
	}
	for _, tt := range []struct{ config, want string }{
		{"    config: {trailers: none}\n", `must be one of "full" or "short"`},
		{"    config: {ledger: off}\n", `must be one of "required" or "preferred"`},
		{"    config: {org: docker}\n", "unknown field"},
	} {
		_, err := Validate(kit(KindWorkload, tt.config))
		require.ErrorContains(t, err, tt.want, tt.config)
	}

	out := mergeOK(t,
		Contribution{Reference: "agent", Descriptor: kit(KindWorkload, "    optional: true\n    config: {trailers: short, ledger: preferred}\n")},
		Contribution{Reference: "audit", Descriptor: kit(KindMixin, "    config: {ledger: required}\n")},
		Contribution{Reference: "bare", Descriptor: kit(KindMixin, "")},
	).Descriptor
	require.Len(t, out.Capabilities, 1)
	require.False(t, out.Capabilities[0].Optional)
	merged, err := WorkloadIdentityOf(out.Capabilities)
	require.NoError(t, err)
	require.Equal(t, &WorkloadIdentity{Trailers: WorkloadIdentityTrailersShort, Ledger: WorkloadIdentityLedgerRequired}, merged)
	_, err = Validate(out)
	require.NoError(t, err)

	out = mergeOK(t,
		Contribution{Reference: "a", Descriptor: kit(KindWorkload, "    config: {trailers: short}\n")},
		Contribution{Reference: "b", Descriptor: kit(KindMixin, "    config: {trailers: full}\n")},
	).Descriptor
	merged, err = WorkloadIdentityOf(out.Capabilities)
	require.NoError(t, err)
	require.Equal(t, WorkloadIdentityTrailersFull, merged.Trailers, "full trailers win")
}

func TestWorkloadIdentityAndGitPushSurface(t *testing.T) {
	d := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindWorkload, Capabilities: []Capability{
		{Type: CapabilityWorkloadIdentity},
		{Type: CapabilityGitPush, Config: map[string]any{"delete": "deny"}},
	}}
	s := SurfaceOf(d)
	require.Len(t, s.Services, 2, "both are permission surface: one grants an identity, the other's bounds gate on change")
	wider := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindWorkload, Capabilities: []Capability{
		{Type: CapabilityWorkloadIdentity},
		{Type: CapabilityGitPush, Config: map[string]any{"delete": "allow"}},
	}}
	require.NotEmpty(t, DiffWidenings(s, SurfaceOf(wider)), "a changed push bound stops for approval")
}
