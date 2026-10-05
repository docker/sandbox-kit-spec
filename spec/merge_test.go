package spec

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// contribute builds a contribution from descriptor YAML, so the tests
// exercise the same decode path a published kit arrives through.
func contribute(t *testing.T, reference, yaml string) Contribution {
	t.Helper()
	return Contribution{Reference: reference, Descriptor: mustDecode(t, yaml)}
}

func mergeOK(t *testing.T, contributions ...Contribution) *MergeResult {
	t.Helper()
	res, err := Merge(contributions, MergeOptions{ContextPath: "/usr/share/sandbox/kit/set/context.md"})
	require.NoError(t, err)
	return res
}

// The merged kit's role is a fact about the kits it lists, not a claim
// its author makes: one workload among them is the base, and a set of
// overlays is itself an overlay.
func TestMergeDerivesTheKind(t *testing.T) {
	workload := contribute(t, "shell", `schemaVersion: "3"
kind: workload
version: "1.0.0"
provides: [shell]
`)
	mixin := contribute(t, "gh", `schemaVersion: "3"
kind: mixin
version: "2.0.0"
provides: [gh]
`)
	other := contribute(t, "tool", `schemaVersion: "3"
kind: mixin
version: "1.0.0"
provides: [tool]
`)

	require.Equal(t, KindWorkload, mergeOK(t, workload, mixin).Descriptor.Kind)
	require.Equal(t, KindMixin, mergeOK(t, mixin, other).Descriptor.Kind,
		"a set of overlays is an overlay, composable onto a workload the way its own kits were")

	_, err := Merge([]Contribution{workload, contribute(t, "hello", `schemaVersion: "3"
kind: workload
version: "1.0.0"
`)}, MergeOptions{})
	require.ErrorContains(t, err, "both workload kits")

	_, err = Merge([]Contribution{workload, contribute(t, "nested", `schemaVersion: "3"
kind: set
kits:
  - ref: reg.example.com/a:1
`)}, MergeOptions{})
	require.ErrorContains(t, err, "has to be merged before it can contribute")
}

// Provides travel in with the content; requires the set answers itself
// do not, because a kit cannot satisfy its own requirement.
func TestMergeRelations(t *testing.T) {
	workload := contribute(t, "shell", `schemaVersion: "3"
kind: workload
version: "1.0.0"
provides: [shell]
conflicts: [podman]
`)
	claude := contribute(t, "claude-mixin", `schemaVersion: "3"
kind: mixin
version: "2.1.6"
provides: ["claude@2.1.6"]
requires: [shell]
integrates: ["docker-engine >= 25.0.0"]
`)
	acp := contribute(t, "claude-acp", `schemaVersion: "3"
kind: mixin
version: "0.5.0"
provides: ["claude-acp@0.5.0"]
requires: ["claude >= 2.0.0"]
`)

	out := mergeOK(t, workload, claude, acp).Descriptor
	require.ElementsMatch(t, []string{"shell@1.0.0", "claude@2.1.6", "claude-acp@0.5.0"}, out.Provides,
		"a bare provide carries the version its own kit declared, not the set's")
	require.Empty(t, out.Requires,
		"shell and claude are provided inside the set, so the merged kit needs neither")
	require.Equal(t, []string{"docker-engine >= 25.0.0"}, out.Integrates,
		"an integration nothing in the set provides stays an open question for the host composition")
	require.Equal(t, []string{"podman"}, out.Conflicts)

	// A minimum the set cannot meet is still a requirement.
	tooNew := contribute(t, "future", `schemaVersion: "3"
kind: mixin
version: "1.0.0"
provides: [future]
requires: ["claude >= 99.0.0"]
`)
	out = mergeOK(t, workload, claude, tooNew).Descriptor
	require.Equal(t, []string{"claude >= 99.0.0"}, out.Requires)
}

// A flattened kit ships every listed kit's content, so its licenses
// list has to name every one of their terms.
func TestMergeUnionsLicenses(t *testing.T) {
	a := contribute(t, "a", `schemaVersion: "3"
kind: workload
version: "1.0.0"
licenses: [Apache-2.0]
`)
	b := contribute(t, "b", `schemaVersion: "3"
kind: mixin
version: "1.0.0"
licenses: [MIT, Apache-2.0]
`)
	require.Equal(t, []string{"Apache-2.0", "MIT"}, mergeOK(t, a, b).Descriptor.Licenses)
}

// Two grants union: the merged kit reaches both sets of hosts, and a
// deny survives from whichever kit stated it.
func TestMergeNetworkPolicyUnion(t *testing.T) {
	a := contribute(t, "a", `schemaVersion: "3"
kind: workload
version: "1.0.0"
capabilities:
  - type: com.docker.sandbox/network-policy@1
    config:
      install:
        allow: [deb.debian.org]
      runtime:
        allow: [github.com]
        deny: [evil.example.com]
`)
	b := contribute(t, "b", `schemaVersion: "3"
kind: mixin
version: "1.0.0"
capabilities:
  - type: com.docker.sandbox/network-policy@1
    config:
      runtime:
        allow: [api.anthropic.com, github.com]
`)

	out := mergeOK(t, a, b).Descriptor
	require.Len(t, out.Capabilities, 1, "one policy entry: the type is a singleton")
	require.Equal(t, CapabilityNetworkPolicy, out.Capabilities[0].Type,
		"a set of @1 kits publishes the version those kits wrote")

	policy, err := NetworkPolicyOf(out.Capabilities)
	require.NoError(t, err)
	require.Equal(t, []string{"deb.debian.org"}, policy.Install.Allow)
	require.Equal(t, []string{"github.com", "api.anthropic.com"}, policy.Runtime.Allow)
	require.Equal(t, []string{"evil.example.com"}, policy.Runtime.Deny)
}

// One kit on @2 moves the merged policy onto @2, and the @1 hosts
// join it as the unbounded entries they already are.
func TestMergeNetworkPolicyUpconvertsToV2(t *testing.T) {
	v1 := contribute(t, "v1", `schemaVersion: "3"
kind: workload
version: "1.0.0"
capabilities:
  - type: com.docker.sandbox/network-policy@1
    config:
      runtime:
        allow: [github.com]
`)
	v2 := contribute(t, "v2", `schemaVersion: "3"
kind: mixin
version: "1.0.0"
capabilities:
  - type: com.docker.sandbox/network-policy@2
    config:
      runtime:
        allow:
          - hosts: [api.example.com]
            methods: [GET]
`)

	out := mergeOK(t, v1, v2).Descriptor
	require.Equal(t, CapabilityNetworkPolicyV2, out.Capabilities[0].Type)

	policy, err := NetworkPolicyV2Of(out.Capabilities)
	require.NoError(t, err)
	require.Len(t, policy.Runtime.Allow, 2)
	require.Equal(t, []string{"github.com"}, policy.Runtime.Allow[0].Hosts)
	require.False(t, policy.Runtime.Allow[0].Bounded(), "an @1 host is an unbounded entry")
	require.Equal(t, []string{"GET"}, policy.Runtime.Allow[1].Methods)
}

// The union of a bounded grant and an unbounded one over the same host
// IS the unbounded one; keeping both is the shape @2 refuses to state.
func TestMergeDropsBoundedEntriesCoveredByAnUnboundedOne(t *testing.T) {
	bounded := contribute(t, "bounded", `schemaVersion: "3"
kind: workload
version: "1.0.0"
capabilities:
  - type: com.docker.sandbox/network-policy@2
    config:
      runtime:
        allow:
          - hosts: [api.github.com]
            methods: [GET]
          - hosts: [api.other.com]
            methods: [POST]
`)
	unbounded := contribute(t, "unbounded", `schemaVersion: "3"
kind: mixin
version: "1.0.0"
capabilities:
  - type: com.docker.sandbox/network-policy@2
    config:
      runtime:
        allow: [api.github.com]
`)

	policy, err := NetworkPolicyV2Of(mergeOK(t, bounded, unbounded).Descriptor.Capabilities)
	require.NoError(t, err)
	require.Len(t, policy.Runtime.Allow, 2)
	require.Equal(t, []string{"api.other.com"}, policy.Runtime.Allow[0].Hosts,
		"the bounded entry for a host nothing else grants outright survives")
	require.Equal(t, []string{"api.github.com"}, policy.Runtime.Allow[1].Hosts)
	require.False(t, policy.Runtime.Allow[1].Bounded())
}

// Hooks concatenate in composition order — the order a runtime would
// have run them in.
func TestMergeLifecycleConcatenatesInOrder(t *testing.T) {
	first := contribute(t, "first", `schemaVersion: "3"
kind: workload
version: "1.0.0"
capabilities:
  - type: com.docker.sandbox/lifecycle@1
    config:
      install:
        - command: echo base
      interactive: ["--interactive"]
`)
	second := contribute(t, "second", `schemaVersion: "3"
kind: mixin
version: "1.0.0"
capabilities:
  - type: com.docker.sandbox/lifecycle@1
    config:
      install:
        - command: echo overlay
      startup:
        - command: echo boot
      files:
        - {path: /etc/one, content: "1"}
`)

	lifecycle, err := LifecycleOf(mergeOK(t, first, second).Descriptor.Capabilities)
	require.NoError(t, err)
	require.Len(t, lifecycle.Install, 2)
	require.Equal(t, CommandLine{"sh", "-c", "echo base"}, lifecycle.Install[0].Command)
	require.Equal(t, CommandLine{"sh", "-c", "echo overlay"}, lifecycle.Install[1].Command)
	require.Len(t, lifecycle.Startup, 1)
	require.Equal(t, []string{"--interactive"}, lifecycle.Interactive)
}

// The interactive tail replaces the launch command's arguments rather
// than adding to them, so two authors cannot both be honored.
func TestMergeRefusesTwoInteractiveTails(t *testing.T) {
	tail := `schemaVersion: "3"
kind: %s
version: "1.0.0"
capabilities:
  - type: com.docker.sandbox/lifecycle@1
    config:
      interactive: ["--chat"]
`
	_, err := Merge([]Contribution{
		contribute(t, "a", fmt.Sprintf(tail, KindWorkload)),
		contribute(t, "b", fmt.Sprintf(tail, KindMixin)),
	}, MergeOptions{})
	require.ErrorContains(t, err, "interactive tail")
}

// Two kits writing one path would each believe they own the file, and
// the later write would silently discard what the earlier one wrote.
func TestMergeRefusesTwoWritersOfOneFile(t *testing.T) {
	file := `schemaVersion: "3"
kind: %s
version: "1.0.0"
capabilities:
  - type: com.docker.sandbox/lifecycle@1
    config:
      files:
        - {path: /home/agent/.config, content: "x"}
`
	_, err := Merge([]Contribution{
		contribute(t, "a", fmt.Sprintf(file, KindWorkload)),
		contribute(t, "b", fmt.Sprintf(file, KindMixin)),
	}, MergeOptions{})
	require.ErrorContains(t, err, "written by two contributions")
}

// Distinct volume destinations union; repeated port requests collapse.
func TestMergeInstanceShapedCapabilities(t *testing.T) {
	a := contribute(t, "a", `schemaVersion: "3"
kind: workload
version: "1.0.0"
capabilities:
  - type: com.docker.sandbox/volume@1
    config: {path: /home/agent/.cache, size: 1g}
  - type: com.docker.sandbox/port@1
    config: {container: 8080}
`)
	b := contribute(t, "b", `schemaVersion: "3"
kind: mixin
version: "1.0.0"
capabilities:
  - type: com.docker.sandbox/volume@1
    config: {path: /home/agent/.state}
  - type: com.docker.sandbox/port@1
    config: {container: 8080}
`)

	out := mergeOK(t, a, b).Descriptor
	volumes, err := VolumesOf(out.Capabilities)
	require.NoError(t, err)
	require.Len(t, volumes, 2)
	ports, err := PortsOf(out.Capabilities)
	require.NoError(t, err)
	require.Len(t, ports, 1, "identical port requests still collapse")
}

// One credential has one owner, which the resolver states across a set
// and the merge has to hold to when it flattens one.
func TestMergeGivesOneCredentialOneOwner(t *testing.T) {
	cred := `schemaVersion: "3"
kind: %s
version: "1.0.0"
capabilities:
  - type: com.docker.sandbox/network-policy@1
    config:
      runtime:
        allow: [api.github.com]
  - type: com.docker.sandbox/credential@1
    config:
      service: github
      phase: runtime
      apiKey:
        name: %s
        inject:
          - {domain: api.github.com, header: Authorization, format: "Bearer %%s"}
`
	_, err := Merge([]Contribution{
		contribute(t, "a", fmt.Sprintf(cred, KindWorkload, "GH_TOKEN")),
		contribute(t, "b", fmt.Sprintf(cred, KindMixin, "GITHUB_TOKEN")),
	}, MergeOptions{})
	require.ErrorContains(t, err, "one credential has one owner")

	// Identical configs are refused too, which is where a credential
	// parts company with every other type: two kits asking for one
	// port is one port, while two kits owning one credential is what
	// Resolve refuses outright — so collapsing them here would publish
	// what the resolver would have turned down, leaving the merged kit
	// with a credential nothing answers for.
	_, err = Merge([]Contribution{
		contribute(t, "a", fmt.Sprintf(cred, KindWorkload, "GH_TOKEN")),
		contribute(t, "b", fmt.Sprintf(cred, KindMixin, "GH_TOKEN")),
	}, MergeOptions{})
	require.ErrorContains(t, err, "one credential has one owner")

	// A different service is a different credential, and unions.
	other := strings.Replace(fmt.Sprintf(cred, KindMixin, "GL_TOKEN"), "service: github", "service: gitlab", 1)
	out := mergeOK(t,
		contribute(t, "a", fmt.Sprintf(cred, KindWorkload, "GH_TOKEN")),
		contribute(t, "b", other),
	).Descriptor
	creds, err := CredentialsOf(out.Capabilities)
	require.NoError(t, err)
	require.Len(t, creds, 2)
}

// An entry one contribution requires is required in the merged kit:
// optional says its asker degrades without it, and one that does not
// degrade decides for the whole.
func TestMergeTakesTheStricterOptionality(t *testing.T) {
	optional := contribute(t, "optional", `schemaVersion: "3"
kind: workload
version: "1.0.0"
capabilities:
  - type: com.docker.sandbox/privileged@1
    optional: true
`)
	required := contribute(t, "required", `schemaVersion: "3"
kind: mixin
version: "1.0.0"
capabilities:
  - type: com.docker.sandbox/privileged@1
`)

	out := mergeOK(t, optional, required).Descriptor
	require.Len(t, out.Capabilities, 1)
	require.False(t, out.Capabilities[0].Optional)
}

// Resources and agent-sessions describe the whole sandbox rather than a
// grant to it, so two different answers cannot both be its.
func TestMergeHoldsWholeSandboxTypesToOneAuthor(t *testing.T) {
	resources := `schemaVersion: "3"
kind: %s
version: "1.0.0"
capabilities:
  - type: com.docker.sandbox/resources@1
    config: {cpu: %s}
`
	_, err := Merge([]Contribution{
		contribute(t, "a", fmt.Sprintf(resources, KindWorkload, "2")),
		contribute(t, "b", fmt.Sprintf(resources, KindMixin, "4")),
	}, MergeOptions{})
	require.ErrorContains(t, err, "one author")

	out := mergeOK(t,
		contribute(t, "a", fmt.Sprintf(resources, KindWorkload, "2")),
		contribute(t, "b", fmt.Sprintf(resources, KindMixin, "2")),
	).Descriptor
	require.Len(t, out.Capabilities, 1, "an identical restatement is the same ask")
}

// An empty newSession is a verb ("the launch argv alone"), so composing
// must hand it on as authored: re-rendering the typed config would let
// omitempty drop it and make the verb read as unsupported.
func TestMergeKeepsEmptyInteractiveVerbs(t *testing.T) {
	workload := contribute(t, "agent", `schemaVersion: "3"
kind: workload
version: "1.0.0"
capabilities:
  - type: com.docker.sandbox/agent-interactive-sessions@1
    config: {newSession: [], continue: [--continue]}
`)
	mixin := contribute(t, "extra", `schemaVersion: "3"
kind: mixin
version: "1.0.0"
capabilities:
  - type: com.docker.sandbox/resources@1
    config: {cpu: 2}
`)
	out := mergeOK(t, workload, mixin).Descriptor
	sessions, err := AgentInteractiveSessionsOf(out.Capabilities)
	require.NoError(t, err)
	require.NotNil(t, sessions)
	require.NotNil(t, sessions.NewSession, "an empty newSession survives composition")
	require.Empty(t, sessions.NewSession)
	require.Nil(t, sessions.SessionPicker)

	other := `schemaVersion: "3"
kind: mixin
version: "1.0.0"
capabilities:
  - type: com.docker.sandbox/agent-interactive-sessions@1
    config: {%s}
`
	_, err = Merge([]Contribution{workload, contribute(t, "b", fmt.Sprintf(other, "continue: [-c]"))}, MergeOptions{})
	require.ErrorContains(t, err, "one author")
	same := mergeOK(t, workload, contribute(t, "b", fmt.Sprintf(other, "newSession: [], continue: [--continue]"))).Descriptor
	require.Len(t, same.Capabilities, 1, "an identical restatement is the same ask")
}

// The agreement rule runs at composition too: a mixin contributes the
// lifecycle tail, so the check compares the decoded asks of the workload
// and the mixin and names both Kits.
func TestComposedInteractiveSessionsAgreeWithLifecycle(t *testing.T) {
	workload := func(newSession string) Contribution {
		return contribute(t, "agent", `schemaVersion: "3"
kind: workload
version: "1.0.0"
capabilities:
  - type: com.docker.sandbox/agent-interactive-sessions@1
    config: {newSession: `+newSession+`}
`)
	}
	mixin := func(interactive string) Contribution {
		return contribute(t, "tui", `schemaVersion: "3"
kind: mixin
version: "1.0.0"
capabilities:
  - type: com.docker.sandbox/lifecycle@1
    config: {interactive: `+interactive+`, startup: [{command: echo ready}]}
`)
	}
	for _, tc := range []struct {
		name, newSession, interactive string
		ok                            bool
	}{
		{"same tail", "[--tui]", "[--tui]", true},
		{"both empty", "[]", "[]", true},
		{"different tails", "[--other]", "[--tui]", false},
		{"newSession empty, lifecycle tail", "[]", "[--tui]", false},
		{"newSession tail, lifecycle empty", "[--tui]", "[]", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			composed, err := Compose([]Contribution{workload(tc.newSession), mixin(tc.interactive)})
			if tc.ok {
				require.NoError(t, err)
				raw, err := json.Marshal(composed)
				require.NoError(t, err)
				_, err = ValidateEffective(raw, composed)
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, "disagrees with lifecycle interactive")
			require.ErrorContains(t, err, "agent capabilities[0]")
			require.ErrorContains(t, err, "tui capabilities[0]")
		})
	}

	t.Run("lifecycle without a tail", func(t *testing.T) {
		noTail := contribute(t, "hooks", `schemaVersion: "3"
kind: mixin
version: "1.0.0"
capabilities:
  - type: com.docker.sandbox/lifecycle@1
    config: {startup: [{command: echo ready}]}
`)
		_, err := Compose([]Contribution{workload("[--tui]"), noTail})
		require.NoError(t, err)
	})
}

// The two session capabilities enumerate the same sessions, so composing
// Kits that declare different list commands is refused with both refs
// named, not left to a caller that happens to validate the output.
func TestComposedSessionListsAgree(t *testing.T) {
	headless := func(list string) Contribution {
		return contribute(t, "headless", `schemaVersion: "3"
kind: workload
version: "1.0.0"
capabilities:
  - type: com.docker.sandbox/agent-sessions@1
    config: {list: `+list+`}
`)
	}
	interactive := func(list string) Contribution {
		return contribute(t, "tui", `schemaVersion: "3"
kind: mixin
version: "1.0.0"
capabilities:
  - type: com.docker.sandbox/agent-interactive-sessions@1
    config: {list: `+list+`}
`)
	}
	_, err := Compose([]Contribution{headless(`[ls, ids]`), interactive(`[ls, ids]`)})
	require.NoError(t, err)
	_, err = Compose([]Contribution{headless(`"a b"`), interactive(`[sh, -c, "a b"]`)})
	require.NoError(t, err, "the string and list spellings of one command are equal")

	_, err = Compose([]Contribution{headless(`[ls, ids]`), interactive(`[ls, all]`)})
	require.ErrorContains(t, err, "differs from agent-interactive-sessions list")
	require.ErrorContains(t, err, "headless capabilities[0]")
	require.ErrorContains(t, err, "tui capabilities[0]")

	oneSided := contribute(t, "tui", `schemaVersion: "3"
kind: mixin
version: "1.0.0"
capabilities:
  - type: com.docker.sandbox/agent-interactive-sessions@1
    config: {continue: [--continue]}
`)
	_, err = Compose([]Contribution{headless(`[ls, ids]`), oneSided})
	require.NoError(t, err, "list on one capability only is fine")

	// Spellings that differ only in an unresolved reference may resolve to
	// one command, so they are left to the expanded descriptor.
	_, err = Compose([]Contribution{headless(`[ls, "${{ kit.args.dir }}"]`), interactive(`[ls, "${{ kit.args.other }}"]`)})
	require.NoError(t, err)
}

// A stated empty interactive tail is the launch argv with nothing
// appended, valid beside a hook as it is at @1 today, and composition
// keeps it rather than letting omitempty turn it into absence.
func TestLifecycleKeepsAStatedEmptyInteractiveTail(t *testing.T) {
	lifecycleOf := func(t *testing.T, config string) *Capability {
		t.Helper()
		c := contribute(t, "kit", `schemaVersion: "3"
kind: workload
version: "1.0.0"
capabilities:
  - type: com.docker.sandbox/lifecycle@1
    config: `+config+`
`)
		composed, err := Compose([]Contribution{c})
		require.NoError(t, err)
		require.Len(t, composed.Capabilities, 1)
		raw, err := json.Marshal(composed)
		require.NoError(t, err)
		_, err = ValidateEffective(raw, composed)
		require.NoError(t, err)
		return &composed.Capabilities[0]
	}

	empty := lifecycleOf(t, "{interactive: [], startup: [{command: echo ready}]}")
	require.Contains(t, empty.Config, "interactive")
	require.Empty(t, empty.Config["interactive"])
	l, err := LifecycleOf([]Capability{*empty})
	require.NoError(t, err)
	require.NotNil(t, l.Interactive)

	absent := lifecycleOf(t, "{startup: [{command: echo ready}]}")
	require.NotContains(t, absent.Config, "interactive")

	tail := lifecycleOf(t, "{interactive: [--tui]}")
	require.Equal(t, []any{"--tui"}, tail.Config["interactive"])
}

// Lifecycle round-trips a stated empty interactive tail through JSON, the
// way CapabilityWithConfig re-renders it, and writes every other field as
// before.
func TestLifecycleMarshalsAStatedEmptyInteractiveTail(t *testing.T) {
	raw, err := json.Marshal(Lifecycle{Interactive: []string{}})
	require.NoError(t, err)
	require.JSONEq(t, `{"interactive":[]}`, string(raw))

	raw, err = json.Marshal(Lifecycle{})
	require.NoError(t, err)
	require.JSONEq(t, `{}`, string(raw))

	raw, err = json.Marshal(Lifecycle{Interactive: []string{"--tui"}})
	require.NoError(t, err)
	require.JSONEq(t, `{"interactive":["--tui"]}`, string(raw))

	// Other fields keep omitempty and their order.
	raw, err = json.Marshal(Lifecycle{Startup: []StartupHook{{Command: CommandLine{"true"}}}, Interactive: []string{}})
	require.NoError(t, err)
	require.Equal(t, `{"startup":[{"command":["true"]}],"interactive":[]}`, string(raw))

	d := decodeValid(t, `schemaVersion: "3"
kind: workload
provides: ["claude@2.1.0"]
capabilities:
  - type: com.docker.sandbox/lifecycle@1
    config: {interactive: [], startup: [{command: echo ready}]}
`)
	first, err := LifecycleOf(d.Capabilities)
	require.NoError(t, err)
	rendered, err := CapabilityWithConfig(d.Capabilities[0], first)
	require.NoError(t, err)
	again, err := LifecycleOf([]Capability{*rendered})
	require.NoError(t, err)
	require.NotNil(t, again.Interactive)
	require.Empty(t, again.Interactive)
}

// A lifecycle tail inside a selected group reaches composition as a plain
// entry, so the same comparison covers it.
func TestSelectedGroupLifecycleTailAgreesWithNewSession(t *testing.T) {
	for name, tc := range map[string]struct {
		newSession string
		ok         bool
	}{"agreeing": {"[--tui]", true}, "disagreeing": {"[--other]", false}} {
		t.Run(name, func(t *testing.T) {
			d := decodeValid(t, `schemaVersion: "3"
kind: workload
provides: ["claude@2.1.0"]
capabilities:
  - type: com.docker.sandbox/agent-interactive-sessions@1
    config: {newSession: `+tc.newSession+`}
  - group:
      capabilities:
        - type: com.docker.sandbox/lifecycle@1
          config: {interactive: [--tui], startup: [{command: echo ready}]}
`)
			selected, err := SelectCapabilities(t.Context(), d, Supported(CapabilityAgentInteractiveSessions, CapabilityLifecycle))
			require.NoError(t, err)
			effective := *d
			effective.Capabilities = selected.Capabilities
			_, err = Compose([]Contribution{{Reference: "agent", Descriptor: &effective}})
			if tc.ok {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, "disagrees with lifecycle interactive")
		})
	}
}

// The profile belongs to the workload; the bodies become one staged
// file, which the descriptor cannot carry and the caller has to write.
func TestMergeCollectsAgentContextBodies(t *testing.T) {
	workload := contribute(t, "shell", `schemaVersion: "3"
kind: workload
version: "1.0.0"
capabilities:
  - type: com.docker.sandbox/agent-context@1
    config:
      filename: AGENTS.md
      contentFile: /usr/share/sandbox/kit/shell/context.md
`)
	mixin := contribute(t, "gh", `schemaVersion: "3"
kind: mixin
version: "1.0.0"
capabilities:
  - type: com.docker.sandbox/agent-context@1
    config:
      content: "gh is available"
`)

	res := mergeOK(t, workload, mixin)
	context, err := AgentContextOf(res.Descriptor.Capabilities)
	require.NoError(t, err)
	require.Equal(t, "AGENTS.md", context.Filename)
	require.Equal(t, "/usr/share/sandbox/kit/set/context.md", context.ContentFile)
	require.Empty(t, context.Content, "the bodies are staged, not inlined")

	require.Equal(t, []ContextSource{
		{Reference: "shell", Path: "/usr/share/sandbox/kit/shell/context.md"},
		{Reference: "gh", Content: "gh is available"},
	}, res.ContextSources)

	_, err = Merge([]Contribution{workload, contribute(t, "other", `schemaVersion: "3"
kind: mixin
version: "1.0.0"
capabilities:
  - type: com.docker.sandbox/agent-context@1
    config:
      filename: CLAUDE.md
`)}, MergeOptions{ContextPath: "/x"})
	require.ErrorContains(t, err, "agent-context filename")
}

// A merged descriptor is a published descriptor: it has to pass the
// rules every consumer holds one to.
func TestMergedDescriptorValidatesAsPublished(t *testing.T) {
	workload := contribute(t, "reg.example.com/shell:1.0.0", `schemaVersion: "3"
kind: workload
version: "1.0.0"
provides: ["shell@1.0.0"]
capabilities:
  - type: com.docker.sandbox/network-policy@1
    config:
      runtime:
        allow: [api.github.com]
  - type: com.docker.sandbox/credential@1
    config:
      service: github
      phase: runtime
      apiKey:
        name: GH_TOKEN
        inject:
          - {domain: api.github.com, header: Authorization, format: "Bearer %s"}
`)
	mixin := contribute(t, "reg.example.com/tool:2.0.0", `schemaVersion: "3"
kind: mixin
version: "2.0.0"
provides: ["tool@2.0.0"]
requires: ["shell >= 1.0.0"]
capabilities:
  - type: com.docker.sandbox/volume@1
    config: {path: /work}
`)

	out := mergeOK(t, workload, mixin).Descriptor
	out.Version = "9.9.9"
	raw, err := json.Marshal(out)
	require.NoError(t, err)
	_, err = ValidatePublished(raw, out)
	require.NoError(t, err)
	require.Equal(t, KindWorkload, out.Kind)
}

// A listed kit's create-phase args are supplied by the set: a literal
// value pins it into the artifact, and a reference re-exports the input
// under the set's own declaration.
func TestKitArgValues(t *testing.T) {
	decls := map[string]Arg{
		"version": {Default: strptr("1.0.0"), Pattern: `^[0-9]+\.[0-9]+\.[0-9]+$`},
		"team":    {Required: true, Enum: []string{"alpha", "beta"}},
		"baked":   {BuildArg: "BAKED"},
	}

	values, err := KitArgValues(decls, map[string]string{"version": "2.1.6", "team": "alpha"})
	require.NoError(t, err)
	require.Equal(t, "2.1.6", values["version"])
	require.Equal(t, "alpha", values["team"])
	require.NotContains(t, values, "baked", "a build-phase arg is already baked into that kit's published descriptor")

	// A reference has no value yet, so that kit's own pattern cannot
	// judge it; the set's declaration bounds it at create instead.
	values, err = KitArgValues(decls, map[string]string{
		"version": "${{ kit.args.claudeVersion }}",
		"team":    "beta",
	})
	require.NoError(t, err)
	require.Equal(t, "${{ kit.args.claudeVersion }}", values["version"])

	_, err = KitArgValues(decls, map[string]string{"team": "gamma"})
	require.ErrorContains(t, err, "gamma")

	_, err = KitArgValues(decls, map[string]string{"team": "alpha", "nope": "x"})
	require.ErrorContains(t, err, `no arg "nope"`)

	_, err = KitArgValues(decls, map[string]string{"team": "alpha", "baked": "x"})
	require.ErrorContains(t, err, "already baked")

	_, err = KitArgValues(decls, nil)
	require.ErrorContains(t, err, "team", "a required arg the set never supplies has no other source")
}

func strptr(s string) *string { return &s }

// The set form's grammar: a set lists kits, a kits list excludes the two
// Dockerfile recipes, and a published descriptor never carries kind: set.
func TestSetFormValidation(t *testing.T) {
	const good = `schemaVersion: "3"
kind: set
version: "1.0.0"
kits:
  - ref: reg.example.com/base:1.0.0
  - ref: reg.example.com/gh:2.72.0
    args: {version: "2.72.0"}
`
	raw := []byte(good)
	d, err := Decode(raw)
	require.NoError(t, err)
	_, err = ValidateRaw(raw, d)
	require.NoError(t, err)

	reject := func(t *testing.T, yaml, want string) {
		t.Helper()
		raw := []byte(yaml)
		d, err := Decode(raw)
		require.NoError(t, err)
		_, err = ValidateRaw(raw, d)
		require.ErrorContains(t, err, want)
	}

	reject(t, `schemaVersion: "3"
kind: set
version: "1.0.0"
`, "lists no kits")

	reject(t, `schemaVersion: "3"
kind: set
version: "1.0.0"
kits: [{ref: reg.example.com/base:1.0.0}]
build: |
  FROM scratch
`, "a set's content is the kits it lists")

	reject(t, `schemaVersion: "3"
kind: set
version: "1.0.0"
kits: [{ref: ./base}]
`, "is a local path")

	reject(t, `schemaVersion: "3"
kind: set
version: "1.0.0"
kits: [{ref: "git+https://github.com/me/kits#dir=base"}]
`, "is a git reference")

	reject(t, `schemaVersion: "3"
kind: set
version: "1.0.0"
kits:
  - ref: reg.example.com/base:1.0.0
  - ref: reg.example.com/base:1.0.0
`, "already listed at kits[0]")

	reject(t, `schemaVersion: "3"
kind: set
version: "1.0.0"
kits: [{ref: reg.example.com/base:1.0.0, digest: sha256:short}]
`, "is not a sha256 manifest digest")

	// A descriptor that validates has to be one the frontend can
	// resolve, so the reference is held to the image grammar itself
	// rather than to a handful of prefix rules.
	reject(t, `schemaVersion: "3"
kind: set
version: "1.0.0"
kits: [{ref: "https://reg.example.com/base:1.0.0"}]
`, "is not an image reference")

	reject(t, `schemaVersion: "3"
kind: set
version: "1.0.0"
kits: [{ref: "Reg.Example.COM/Base:1.0.0"}]
`, "is not an image reference")

	// A digest is a literal pin whatever the reference looks like, so
	// it is judged even when the reference itself defers.
	reject(t, `schemaVersion: "3"
kind: set
version: "1.0.0"
args:
  registry:
    default: reg.example.com
    buildArg: REGISTRY
kits: [{ref: "${{ kit.args.registry }}/base:1.0.0", digest: sha256:short}]
`, "is not a sha256 manifest digest")

	// Two pins that disagree cannot both be what the author meant, and
	// an artifact this frontend never built reaches consumers through
	// ValidatePublished alone.
	conflicting := `schemaVersion: "3"
kind: workload
version: "1.0.0"
kits:
  - ref: reg.example.com/base@sha256:` + strings.Repeat("a", 64) + `
    digest: sha256:` + strings.Repeat("b", 64) + `
`
	cd, err := Decode([]byte(conflicting))
	require.NoError(t, err)
	_, err = ValidatePublished([]byte(conflicting), cd)
	require.ErrorContains(t, err, "one kit has one pin")

	// Agreeing pins are a belt-and-braces spelling, not a conflict.
	agreeing := `schemaVersion: "3"
kind: workload
version: "1.0.0"
kits:
  - ref: reg.example.com/base@sha256:` + strings.Repeat("a", 64) + `
    digest: sha256:` + strings.Repeat("a", 64) + `
`
	ad, err := Decode([]byte(agreeing))
	require.NoError(t, err)
	_, err = ValidatePublished([]byte(agreeing), ad)
	require.NoError(t, err)

	// A kit reference may name a build-phase arg — a registry namespace
	// is exactly that — judged after expansion.
	const parameterized = `schemaVersion: "3"
kind: set
version: "1.0.0"
args:
  registry:
    default: reg.example.com
    buildArg: REGISTRY
kits:
  - ref: ${{ kit.args.registry }}/base:1.0.0
`
	raw = []byte(parameterized)
	d, err = Decode(raw)
	require.NoError(t, err)
	_, err = ValidateRaw(raw, d)
	require.NoError(t, err)

	// Published form: the derived kind, pinned kits, nothing left to expand.
	unmerged := []byte(good)
	ud, err := Decode(unmerged)
	require.NoError(t, err)
	_, err = ValidatePublished(unmerged, ud)
	require.ErrorContains(t, err, "kind: set",
		"kind: set reaching a consumer means the merge never ran")

	published := `schemaVersion: "3"
kind: workload
version: "1.0.0"
kits:
  - ref: reg.example.com/base:1.0.0
`
	pd, err := Decode([]byte(published))
	require.NoError(t, err)
	_, err = ValidatePublished([]byte(published), pd)
	require.ErrorContains(t, err, "with no digest")

	pinned := `schemaVersion: "3"
kind: workload
version: "1.0.0"
args:
  registry:
    default: reg.example.com
    buildArg: REGISTRY
kits:
  - ref: ${{ kit.args.registry }}/base:1.0.0
    digest: sha256:1111111111111111111111111111111111111111111111111111111111111111
`
	pd, err = Decode([]byte(pinned))
	require.NoError(t, err)
	_, err = ValidatePublished([]byte(pinned), pd)
	require.ErrorContains(t, err, "kits[0].ref: published descriptor still references an arg",
		"a set's kits resolve at build, so a surviving reference means expansion did not run")
}

// A published kit may offer a bare name and carry its version in
// version:, which §9.2 permits. The merge has to materialize that
// fallback: an internal requirement stating a minimum is satisfied only
// if the provide it matches has a version, and the emitted entry has to
// keep the version of the kit that offered it rather than inheriting
// the set's own once the merged descriptor is read.
func TestMergeMaterializesTheProvidesVersionFallback(t *testing.T) {
	bare := contribute(t, "base", `schemaVersion: "3"
kind: workload
version: "1.4.2"
provides: [base]
`)
	needs := contribute(t, "tool", `schemaVersion: "3"
kind: mixin
version: "2.0.0"
provides: ["tool@2.0.0"]
requires: ["base >= 1.0.0"]
`)

	out := mergeOK(t, bare, needs).Descriptor
	require.ElementsMatch(t, []string{"base@1.4.2", "tool@2.0.0"}, out.Provides)
	require.Empty(t, out.Requires,
		"the minimum is met by base@1.4.2; an unversioned provide would have left it unsatisfied")

	// A minimum the fallback does not reach is still a requirement.
	tooNew := contribute(t, "future", `schemaVersion: "3"
kind: mixin
version: "1.0.0"
provides: [future]
requires: ["base >= 9.0.0"]
`)
	out = mergeOK(t, bare, tooNew).Descriptor
	require.Equal(t, []string{"base >= 9.0.0"}, out.Requires)
}

// Two spellings of one path are one file: lifecycle validation asks only
// that a path be absolute, so the one-writer rule cannot compare the
// authored strings.
func TestMergeRefusesTwoWritersOfOnePathUnderDifferentSpellings(t *testing.T) {
	file := `schemaVersion: "3"
kind: %s
version: "1.0.0"
capabilities:
  - type: com.docker.sandbox/lifecycle@1
    config:
      files:
        - {path: "%s", content: x}
`
	_, err := Merge([]Contribution{
		contribute(t, "a", fmt.Sprintf(file, KindWorkload, "/etc/tool.conf")),
		contribute(t, "b", fmt.Sprintf(file, KindMixin, "/etc/./tool.conf")),
	}, MergeOptions{})
	require.ErrorContains(t, err, "/etc/tool.conf is written by two contributions")
}

// optional says its asker degrades without the grant; the merged policy
// is required as soon as one contributor does not degrade, and stays
// optional when they all do.
func TestMergeNetworkPolicyOptionality(t *testing.T) {
	policy := `schemaVersion: "3"
kind: %s
version: "1.0.0"
capabilities:
  - type: com.docker.sandbox/network-policy@1
    optional: %s
    config:
      runtime:
        allow: [%s]
`
	out := mergeOK(t,
		contribute(t, "a", fmt.Sprintf(policy, KindWorkload, "true", "a.example.com")),
		contribute(t, "b", fmt.Sprintf(policy, KindMixin, "true", "b.example.com")),
	).Descriptor
	require.True(t, out.Capabilities[0].Optional, "every contributor degrades without it")

	out = mergeOK(t,
		contribute(t, "a", fmt.Sprintf(policy, KindWorkload, "true", "a.example.com")),
		contribute(t, "b", fmt.Sprintf(policy, KindMixin, "false", "b.example.com")),
	).Descriptor
	require.False(t, out.Capabilities[0].Optional, "one that does not degrade decides for the set")
}

// The resolver counts owners, not entries: two kits providing one name
// is refused, but a single kit may offer it at several versions. The
// merge has to keep them all, or the version that satisfied another
// kit's constraint during resolution is gone by the time the merge
// judges the same constraint.
func TestMergeKeepsEveryVersionOneKitOffers(t *testing.T) {
	both := contribute(t, "base", `schemaVersion: "3"
kind: workload
version: "1.0.0"
provides: ["foo@1.0.0", "foo@2.0.0"]
`)
	needs := contribute(t, "tool", `schemaVersion: "3"
kind: mixin
version: "1.0.0"
provides: ["tool@1.0.0"]
requires: ["foo >= 2.0.0"]
`)

	out := mergeOK(t, both, needs).Descriptor
	require.ElementsMatch(t, []string{"foo@1.0.0", "foo@2.0.0", "tool@1.0.0"}, out.Provides,
		"both versions the owner offers carry over")
	require.Empty(t, out.Requires,
		"foo@2.0.0 meets the minimum; keeping only foo@1.0.0 would have retained it")
}

// A comma is legal inside a path, so joining the slices with one made
// two different grants collide and the union came out narrower than
// its authors asked for.
func TestMergeNetworkEntryKeysAreUnambiguous(t *testing.T) {
	policy := `schemaVersion: "3"
kind: %s
version: "1.0.0"
capabilities:
  - type: com.docker.sandbox/network-policy@2
    config:
      runtime:
        allow:
          - hosts: [api.example.com]
            methods: [GET]
            paths: %s
`
	out := mergeOK(t,
		contribute(t, "a", fmt.Sprintf(policy, KindWorkload, `["/a,/b"]`)),
		contribute(t, "b", fmt.Sprintf(policy, KindMixin, `["/a", "/b"]`)),
	).Descriptor

	p, err := NetworkPolicyV2Of(out.Capabilities)
	require.NoError(t, err)
	require.Len(t, p.Runtime.Allow, 2, "one path containing a comma is not two paths")
	require.Equal(t, []string{"/a,/b"}, p.Runtime.Allow[0].Paths)
	require.Equal(t, []string{"/a", "/b"}, p.Runtime.Allow[1].Paths)

	// An exact repeat still collapses.
	out = mergeOK(t,
		contribute(t, "a", fmt.Sprintf(policy, KindWorkload, `["/a", "/b"]`)),
		contribute(t, "b", fmt.Sprintf(policy, KindMixin, `["/a", "/b"]`)),
	).Descriptor
	p, err = NetworkPolicyV2Of(out.Capabilities)
	require.NoError(t, err)
	require.Len(t, p.Runtime.Allow, 1)
}

// Resolve skips self, so a contribution's own provide never satisfies
// its own requirement. A merge that let it would drop a requirement
// the resolver would have failed on — quietly repairing an incoherent
// contribution instead of carrying the incoherence where it shows.
func TestMergeWillNotLetAContributionSatisfyItself(t *testing.T) {
	selfish := contribute(t, "selfish", `schemaVersion: "3"
kind: workload
version: "1.0.0"
provides: ["team@1.0.0"]
requires: ["team >= 1.0.0"]
`)
	other := contribute(t, "other", `schemaVersion: "3"
kind: mixin
version: "1.0.0"
provides: ["tool@1.0.0"]
`)

	out := mergeOK(t, selfish, other).Descriptor
	require.Equal(t, []string{"team >= 1.0.0"}, out.Requires,
		"its own provide does not answer it, so the requirement stands")

	// Another contribution offering the name does answer it.
	provider := contribute(t, "provider", `schemaVersion: "3"
kind: mixin
version: "2.0.0"
provides: ["needed@2.0.0"]
`)
	asker := contribute(t, "asker", `schemaVersion: "3"
kind: workload
version: "1.0.0"
provides: ["asker@1.0.0"]
requires: ["needed >= 2.0.0"]
`)
	out = mergeOK(t, provider, asker).Descriptor
	require.Empty(t, out.Requires)
}

// A set supplies a listed kit's arg as a literal, or as exactly one
// reference re-exporting it. A reference embedded in a larger value is
// bounded by neither declaration: the kit's cannot judge a value that
// does not exist yet, and the set's bounds only the substituted part.
func TestKitArgValuesRefusesACompositeReference(t *testing.T) {
	decls := map[string]Arg{"version": {Default: strptr("1.0.0"), Pattern: `^[0-9]+\.[0-9]+\.[0-9]+$`}}

	values, err := KitArgValues(decls, map[string]string{"version": "${{ kit.args.v }}"})
	require.NoError(t, err, "a whole-value reference re-exports the input")
	require.Equal(t, "${{ kit.args.v }}", values["version"])

	_, err = KitArgValues(decls, map[string]string{"version": "v${{ kit.args.v }}"})
	require.ErrorContains(t, err, "embeds a reference in a larger value")

	_, err = KitArgValues(decls, map[string]string{"version": "${{ kit.args.v }}-rc1"})
	require.ErrorContains(t, err, "embeds a reference in a larger value")
}

// Absent means a kit with an ordinary recipe; present and empty means
// an author who meant to list something. The schema refuses the second
// through minItems, so the validator has to agree.
func TestAnEmptyKitsListIsRefusedUnderAnyKind(t *testing.T) {
	for _, kind := range []string{KindMixin, KindWorkload} {
		raw := []byte("schemaVersion: \"3\"\nkind: " + kind + "\nversion: \"1.0.0\"\nkits: []\n")
		d, err := Decode(raw)
		require.NoError(t, err)
		_, err = ValidateRaw(raw, d)
		require.ErrorContains(t, err, "present but empty", "kind: %s", kind)
	}

	// Absent is an ordinary kit and stays legal.
	raw := []byte("schemaVersion: \"3\"\nkind: mixin\nversion: \"1.0.0\"\n")
	d, err := Decode(raw)
	require.NoError(t, err)
	_, err = ValidateRaw(raw, d)
	require.NoError(t, err)
}

// port@1 defines a publication by container port and transport, so an
// omitted transport and an explicit tcp are one ask, and a name is
// informational rather than part of the request.
func TestMergeNormalizesPortRequests(t *testing.T) {
	port := `schemaVersion: "3"
kind: %s
version: "1.0.0"
capabilities:
  - type: com.docker.sandbox/port@1
    config: {container: 8080%s}
`
	out := mergeOK(t,
		contribute(t, "a", fmt.Sprintf(port, KindWorkload, "")),
		contribute(t, "b", fmt.Sprintf(port, KindMixin, ", transport: tcp")),
	).Descriptor
	ports, err := PortsOf(out.Capabilities)
	require.NoError(t, err)
	require.Len(t, ports, 1, "an omitted transport and an explicit tcp are one publication")

	out = mergeOK(t,
		contribute(t, "a", fmt.Sprintf(port, KindWorkload, ", name: api")),
		contribute(t, "b", fmt.Sprintf(port, KindMixin, ", name: http")),
	).Descriptor
	ports, err = PortsOf(out.Capabilities)
	require.NoError(t, err)
	require.Len(t, ports, 1, "a name describes the port, it does not make a second one")

	// A different port is still a different ask.
	out = mergeOK(t,
		contribute(t, "a", fmt.Sprintf(port, KindWorkload, "")),
		contribute(t, "b", `schemaVersion: "3"
kind: mixin
version: "1.0.0"
capabilities:
  - type: com.docker.sandbox/port@1
    config: {container: 9090}
`),
	).Descriptor
	ports, err = PortsOf(out.Capabilities)
	require.NoError(t, err)
	require.Len(t, ports, 2)
}

// A lifecycle path that resolves at create cannot be held to the
// one-writer rule: two contributions could write one file with the
// contributions gone and nothing left to notice.
func TestMergeRefusesADeferredLifecyclePath(t *testing.T) {
	file := `schemaVersion: "3"
kind: %s
version: "1.0.0"
args:
  target:
    default: /etc/tool.conf
capabilities:
  - type: com.docker.sandbox/lifecycle@1
    config:
      files:
        - {path: "${{ kit.args.target }}", content: x}
`
	_, err := Merge([]Contribution{
		contribute(t, "a", fmt.Sprintf(file, KindWorkload)),
		contribute(t, "b", `schemaVersion: "3"
kind: mixin
version: "1.0.0"
capabilities:
  - type: com.docker.sandbox/lifecycle@1
    config:
      files:
        - {path: /etc/other.conf, content: y}
`),
	}, MergeOptions{})
	require.ErrorContains(t, err, "resolves at create")
}

// §6 gives a reference its type only when it occupies the whole value,
// so padding makes it an ordinary string — and re-exporting it as
// though it were typed would resolve a number into a padded string.
func TestOnlyAnExactReferenceIsAReExport(t *testing.T) {
	require.True(t, IsWholeArgRef("${{ kit.args.version }}"))
	require.True(t, IsWholeArgRef("${{kit.args.version}}"))
	require.False(t, IsWholeArgRef(" ${{ kit.args.version }} "), "padding makes it text")
	require.False(t, IsWholeArgRef("v${{ kit.args.version }}"))
	require.False(t, IsWholeArgRef("${{ kit.args.a }}${{ kit.args.b }}"))

	decls := map[string]Arg{"port": {Default: strptr("8080")}}
	_, err := KitArgValues(decls, map[string]string{"port": " ${{ kit.args.p }} "})
	require.ErrorContains(t, err, "embeds a reference in a larger value")
}

// volume@1/no-silent-merge forbids sharing a destination even when
// both Kits ask for the same storage. Equivalent path spellings must
// not bypass the conflict in publication or runtime composition.
func TestCompositionRefusesSharedVolumePaths(t *testing.T) {
	for _, tc := range []struct {
		name, path, extra string
	}{
		{"identical", "/data/cache", ""},
		{"different size", "/data/cache", ", size: 10g"},
		{"different mode", "/data/cache", ", mode: '0755'"},
		{"tmpfs", "/data/cache", ", tmpfs: true"},
		{"dot", "/data/./cache", ""},
		{"parent", "/data/other/../cache", ""},
		{"trailing slash", "/data/cache/", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, optional := range []bool{false, true} {
				t.Run(fmt.Sprintf("optional=%t", optional), func(t *testing.T) {
					vol := `schemaVersion: "3"
kind: mixin
capabilities:
  - type: com.docker.sandbox/volume@1
    optional: %t
    config: {path: %s%s}
`
					inputs := []Contribution{
						contribute(t, "first-kit", fmt.Sprintf(vol, optional, "/data/cache", "")),
						contribute(t, "second-kit", fmt.Sprintf(vol, optional, tc.path, tc.extra)),
					}
					for _, input := range inputs {
						_, err := Validate(input.Descriptor)
						require.NoError(t, err)
					}
					published, err := Merge(inputs, MergeOptions{})
					require.Nil(t, published)
					require.ErrorContains(t, err, "first-kit capabilities[0]")
					require.ErrorContains(t, err, "second-kit capabilities[0]")
					require.ErrorContains(t, err, CapabilityVolume)
					require.ErrorContains(t, err, "/data/cache")
					composed, composeErr := Compose(inputs)
					require.Nil(t, composed)
					require.EqualError(t, composeErr, err.Error())
				})
			}
		})
	}
}

// An omitted config and one written as {} are the only two spellings a
// config-less type has, and they are the same request.
func TestMergeTreatsAnEmptyConfigAsNone(t *testing.T) {
	out := mergeOK(t,
		contribute(t, "a", `schemaVersion: "3"
kind: workload
version: "1.0.0"
capabilities:
  - type: com.docker.sandbox/privileged@1
`),
		contribute(t, "b", `schemaVersion: "3"
kind: mixin
version: "1.0.0"
capabilities:
  - type: com.docker.sandbox/privileged@1
    config: {}
`),
	).Descriptor
	require.Len(t, out.Capabilities, 1, "two spellings of one request are one entry")
}

// A version segment has no length bound, so comparison cannot go
// through a machine-sized integer.
func TestVersionsCompareBeyondMachineIntegers(t *testing.T) {
	require.Equal(t, -1, CompareVersions("99999999999999999999", "100000000000000000000"))
	require.Equal(t, 1, CompareVersions("100000000000000000000", "99999999999999999999"))
	require.Equal(t, 0, CompareVersions("00123", "123"), "leading zeros do not change the point named")

	// And the range that bounds them parses rather than being refused
	// as unsatisfiable.
	_, err := ParseRequire("big >= 99999999999999999999, < 100000000000000000000")
	require.NoError(t, err)
}

// A set's kits are resolved during the build, so a reference may name
// a build-phase arg and nothing else: a create-phase one would still
// be a placeholder when the frontend tried to parse it as an image
// reference.
func TestAKitReferenceOnlyDefersToABuildPhaseArg(t *testing.T) {
	ok := []byte(`schemaVersion: "3"
kind: set
version: "1.0.0"
args:
  registry:
    default: reg.example.com
    buildArg: REGISTRY
kits: [{ref: "${{ kit.args.registry }}/base:1.0.0"}]
`)
	d, err := Decode(ok)
	require.NoError(t, err)
	_, err = ValidateRaw(ok, d)
	require.NoError(t, err)

	createPhase := []byte(`schemaVersion: "3"
kind: set
version: "1.0.0"
args:
  registry:
    default: reg.example.com
kits: [{ref: "${{ kit.args.registry }}/base:1.0.0"}]
`)
	d, err = Decode(createPhase)
	require.NoError(t, err)
	_, err = ValidateRaw(createPhase, d)
	require.ErrorContains(t, err, "resolves at create")

	// An undeclared name is the whole-document check's to report, and
	// saying it twice differently would only confuse the author.
	undeclared := []byte(`schemaVersion: "3"
kind: set
version: "1.0.0"
kits: [{ref: "${{ kit.args.nope }}/base:1.0.0"}]
`)
	d, err = Decode(undeclared)
	require.NoError(t, err)
	_, err = ValidateRaw(undeclared, d)
	require.ErrorContains(t, err, `declares no arg "nope"`)
}

// A re-exported arg leaves a placeholder, which is a string. Where the
// field holds text it rides through to create; where it holds a number
// the merge cannot read it, and the author needs to be told that
// rather than shown a JSON type error.
func TestMergeExplainsAReExportItCannotCarry(t *testing.T) {
	typed := contribute(t, "reg.io/tool:1", `schemaVersion: "3"
kind: workload
version: "1.0.0"
capabilities:
  - type: com.docker.sandbox/port@1
    config: {container: "${{ kit.args.port }}"}
`)
	_, err := Merge([]Contribution{typed}, MergeOptions{})
	require.ErrorContains(t, err, "still parameterized where the merge has to read it")
	require.ErrorContains(t, err, "pin the arg in kits[].args")

	// A text field carries one through, which is what re-export is for.
	text := contribute(t, "reg.io/net:1", `schemaVersion: "3"
kind: workload
version: "1.0.0"
capabilities:
  - type: com.docker.sandbox/network-policy@1
    config:
      runtime:
        allow: ["${{ kit.args.host }}"]
`)
	out := mergeOK(t, text)
	policy, err := NetworkPolicyOf(out.Descriptor.Capabilities)
	require.NoError(t, err)
	require.Equal(t, []string{"${{ kit.args.host }}"}, policy.Runtime.Allow)
}

// Two spellings of one relation are one relation: the conformance
// check compares what an entry means, so restating an input without
// its spaces cannot make a retained requirement look dropped.
func TestCanonicalRequireIgnoresSpelling(t *testing.T) {
	spaced, err := ParseRequire("shell >= 1.0.0")
	require.NoError(t, err)
	tight, err := ParseRequire("shell>=1.0.0")
	require.NoError(t, err)
	qualified, err := ParseRequire("com.docker.kit/shell >= 1.0.0")
	require.NoError(t, err)
	require.Equal(t, CanonicalRequire(spaced), CanonicalRequire(tight))
	require.Equal(t, CanonicalRequire(spaced), CanonicalRequire(qualified))

	// And order within a constraint set is not part of what it says.
	ab, err := ParseRequire("node >= 20.0.0, < 21.0.0")
	require.NoError(t, err)
	ba, err := ParseRequire("node < 21.0.0, >= 20.0.0")
	require.NoError(t, err)
	require.Equal(t, CanonicalRequire(ab), CanonicalRequire(ba))

	different, err := ParseRequire("shell >= 2.0.0")
	require.NoError(t, err)
	require.NotEqual(t, CanonicalRequire(spaced), CanonicalRequire(different))
}

// Every version begins with a nonnegative numeric segment, so 0 is a
// lower bound every constraint set carries whether or not one was
// written — and an upper bound beneath it is dead on arrival.
func TestAnUpperBoundBelowZeroIsRefused(t *testing.T) {
	_, err := ParseRequire("thing < 0")
	require.ErrorContains(t, err, "unsatisfiable")

	_, err = ParseRequire("thing <= 0")
	require.NoError(t, err, "0 itself is a version something could provide")

	_, err = ParseRequire("thing >= 1.0.0, < 2.0.0")
	require.NoError(t, err)
}

// Canonical means simplified: a pin answers everything, and otherwise
// only the tightest bound in each direction says anything. Two
// spellings of one range have to render alike, or a check comparing
// relations by meaning would pass one and fail the other.
func TestCanonicalRequireSimplifiesBounds(t *testing.T) {
	same := func(a, b string) {
		t.Helper()
		ra, err := ParseRequire(a)
		require.NoError(t, err)
		rb, err := ParseRequire(b)
		require.NoError(t, err)
		require.Equal(t, CanonicalRequire(ra), CanonicalRequire(rb), "%q and %q say the same thing", a, b)
	}
	same("shell >= 1.0.0, >= 2.0.0", "shell >= 2.0.0")
	same("shell < 3.0.0, < 2.0.0", "shell < 2.0.0")
	same("shell >= 1.0.0, >= 1.0.0", "shell >= 1.0.0")
	same("shell > 1.0.0, >= 1.0.0", "shell > 1.0.0")
	same("shell = 1.0.0, >= 1.0.0, < 2.0.0", "shell = 1.0.0")

	loose, err := ParseRequire("shell >= 1.0.0")
	require.NoError(t, err)
	tight, err := ParseRequire("shell >= 2.0.0")
	require.NoError(t, err)
	require.NotEqual(t, CanonicalRequire(loose), CanonicalRequire(tight))
}

// A rule about which fields are present does not wait for their
// values: deferring it let a contradictory pair reach a merge that
// normalizes one away, after which the strict pass sees nothing wrong.
func TestPresenceRulesDoNotDeferOnAPlaceholder(t *testing.T) {
	raw := []byte(`schemaVersion: "3"
kind: workload
version: "1.0.0"
args:
  note:
    default: hello
capabilities:
  - type: com.docker.sandbox/agent-context@1
    config:
      filename: AGENTS.md
      contentFile: ./notes.md
      content: "${{ kit.args.note }}"
`)
	d, err := Decode(raw)
	require.NoError(t, err)
	_, err = ValidateRaw(raw, d)
	require.ErrorContains(t, err, "mutually exclusive")
}

// ParseProvide accepts surrounding whitespace, so materializing the
// fallback by extending the original text would publish "shell @1.0.0".
func TestTheProvidesFallbackTrimsBeforeAppending(t *testing.T) {
	out := mergeOK(t, contribute(t, "reg.io/shell:1", `schemaVersion: "3"
kind: workload
version: "1.0.0"
provides: ["shell "]
`)).Descriptor
	require.Equal(t, []string{"shell@1.0.0"}, out.Provides)
}

// Bounds that meet inclusively name one version, which is what a pin
// says.
func TestCanonicalRequireCollapsesMeetingBounds(t *testing.T) {
	pinned, err := ParseRequire("shell = 1.0.0")
	require.NoError(t, err)
	bounded, err := ParseRequire("shell >= 1.0.0, <= 1.0.0")
	require.NoError(t, err)
	require.Equal(t, CanonicalRequire(pinned), CanonicalRequire(bounded))
}

// A re-exported arg is answered by the set's declaration, and the
// kit's own is gone by the time an installer reads anything — so the
// set's has to say at least as much.
func TestCheckReExportHoldsTheSetToTheKitsContract(t *testing.T) {
	enum := Arg{Enum: []string{"fast", "slow"}, Required: true}
	pattern := Arg{Pattern: `^v[0-9]+$`}
	defaulted := Arg{Default: strptr("1.0.0")}

	require.ErrorContains(t,
		CheckReExport("mode", enum, "mode", Arg{}, false),
		"which the set does not declare")

	require.ErrorContains(t,
		CheckReExport("mode", enum, "mode", Arg{Enum: []string{"fast", "slow"}}, true),
		"an installer supplying nothing would leave the reference unresolved")

	require.ErrorContains(t,
		CheckReExport("mode", enum, "mode", Arg{Required: true}, true),
		"does not restate its enum")

	require.ErrorContains(t,
		CheckReExport("mode", enum, "mode", Arg{Required: true, Enum: []string{"fast", "instant"}}, true),
		`allows "instant"`)

	require.ErrorContains(t,
		CheckReExport("tag", pattern, "tag", Arg{Pattern: `^.*$`}, true),
		"does not restate its pattern")

	require.ErrorContains(t,
		CheckReExport("version", defaulted, "version", Arg{}, true),
		"optional with no default")

	// Restating the contract is what makes it legal.
	require.NoError(t, CheckReExport("mode", enum, "mode",
		Arg{Required: true, Enum: []string{"fast", "slow"}}, true))
	require.NoError(t, CheckReExport("mode", enum, "speed",
		Arg{Required: true, Enum: []string{"fast"}}, true), "a narrower enum says more, not less")
	require.NoError(t, CheckReExport("tag", pattern, "tag", Arg{Pattern: `^v[0-9]+$`}, true))
	require.NoError(t, CheckReExport("version", defaulted, "version", Arg{Default: strptr("2.0.0")}, true))
	require.NoError(t, CheckReExport("free", Arg{}, "anything", Arg{}, true), "an unconstrained arg asks nothing")
}

// The version order is not dense: a further segment always sorts above
// the version without it, and "-" is the smallest segment that can be
// written, so v and v + ".-" are adjacent points with nothing between
// them. A range that only spans those two admits nothing.
func TestAdjacentVersionPointsAreUnsatisfiable(t *testing.T) {
	_, err := ParseRequire("thing > 0, < 0.-")
	require.ErrorContains(t, err, "unsatisfiable")

	_, err = ParseRequire("thing > 1.0.0, <= 1.0.0.-")
	require.NoError(t, err, "1.0.0.- itself satisfies an inclusive upper bound")

	_, err = ParseRequire("thing > 1.0.0, < 1.0.0.-")
	require.ErrorContains(t, err, "unsatisfiable")

	// And a range with room in it still parses: 0.- lies between them.
	_, err = ParseRequire("thing > 0, < 0.0")
	require.NoError(t, err)

	require.Equal(t, -1, CompareVersions("0", "0.-"))
	require.Equal(t, -1, CompareVersions("0.-", "0.0"), "every digit and letter sorts above -")
	_, err = ParseRequire("thing > 1.0.0, < 1.0.1")
	require.NoError(t, err)
}

// An exclusive lower bound names the version below the range, so
// "> 1.0.0" and ">= 1.0.0.-" admit the same set and canonicalize alike
// — otherwise a relation restated the second way would evade a check
// that compares relations by meaning.
func TestCanonicalRequireNormalizesExclusiveLowerBounds(t *testing.T) {
	exclusive, err := ParseRequire("shell > 1.0.0")
	require.NoError(t, err)
	inclusive, err := ParseRequire("shell >= 1.0.0.-")
	require.NoError(t, err)
	require.Equal(t, CanonicalRequire(exclusive), CanonicalRequire(inclusive))

	// A range admitting exactly one version is that pin.
	single, err := ParseRequire("shell > 1.0.0, <= 1.0.0.-")
	require.NoError(t, err)
	pinned, err := ParseRequire("shell = 1.0.0.-")
	require.NoError(t, err)
	require.Equal(t, CanonicalRequire(pinned), CanonicalRequire(single))

	// And an inclusive bound still says something different.
	inclusiveSame, err := ParseRequire("shell >= 1.0.0")
	require.NoError(t, err)
	require.NotEqual(t, CanonicalRequire(exclusive), CanonicalRequire(inclusiveSame))
}

// An exclusive upper bound converts when it names a successor:
// "< next(x)" admits up to x. An arbitrary one does not, because the
// largest version below it has no finite spelling.
func TestCanonicalRequireNormalizesSuccessorUpperBounds(t *testing.T) {
	exclusive, err := ParseRequire("shell < 1.0.0.-")
	require.NoError(t, err)
	inclusive, err := ParseRequire("shell <= 1.0.0")
	require.NoError(t, err)
	require.Equal(t, CanonicalRequire(exclusive), CanonicalRequire(inclusive))

	// And an ordinary exclusive bound still says its own thing.
	ordinary, err := ParseRequire("shell < 1.0.1")
	require.NoError(t, err)
	below, err := ParseRequire("shell <= 1.0.1")
	require.NoError(t, err)
	require.NotEqual(t, CanonicalRequire(ordinary), CanonicalRequire(below))

	// The range admitting one version is still that pin, spelled
	// either way round.
	open, err := ParseRequire("shell > 1.0.0, < 1.0.0.-.-")
	require.NoError(t, err)
	pinned, err := ParseRequire("shell = 1.0.0.-")
	require.NoError(t, err)
	require.Equal(t, CanonicalRequire(pinned), CanonicalRequire(open))
}

// Shape checks must run before expansion or merge normalization can
// discard an invalid entry alongside a valid one.
func TestAParameterizedPolicyIsHeldToItsShape(t *testing.T) {
	for _, test := range []struct{ name, entries, field, want string }{
		{"empty methods", `{hosts: ["${{ kit.args.host }}"], methods: []}`, "methods", "empty methods list"},
		{"null methods", `{hosts: ["${{ kit.args.host }}"], methods: null}`, "methods", "empty methods list"},
		{"empty paths", `{hosts: ["${{ kit.args.host }}"], paths: []}`, "paths", "empty paths list"},
		{"empty paths with methods", `{hosts: ["${{ kit.args.host }}"], methods: [GET], paths: []}`, "paths", "empty paths list"},
		{"null paths", `{hosts: ["${{ kit.args.host }}"], methods: [GET], paths: null}`, "paths", "empty paths list"},
		{"paths without methods", `{hosts: ["${{ kit.args.host }}"], paths: ["/v1"]}`, "paths", "paths without methods"},
		{"missing hosts", `{methods: ["${{ kit.args.method }}"]}`, "hosts", "declares no hosts"},
		{"empty hosts", `{hosts: [], methods: ["${{ kit.args.method }}"]}`, "hosts", "declares no hosts"},
		{"null hosts", `{hosts: null, methods: ["${{ kit.args.method }}"]}`, "hosts", "declares no hosts"},
		{"empty host", `{hosts: [""], methods: ["${{ kit.args.method }}"]}`, "hosts", "has an empty host"},
		{"literal entry beside parameterized entry", `{hosts: [api.example.com], methods: []}, "${{ kit.args.host }}"`, "methods", "empty methods list"},
	} {
		for _, phase := range []string{"install", "runtime"} {
			for _, group := range []string{"allow", "deny"} {
				t.Run(test.name+"/"+phase+"/"+group, func(t *testing.T) {
					raw := []byte(fmt.Sprintf(`schemaVersion: "3"
kind: mixin
args:
  host:
    default: api.example.com
  method:
    default: GET
capabilities:
  - type: com.docker.sandbox/network-policy@2
    config:
      %s:
        %s: [%s]
`, phase, group, test.entries))
					d, err := Decode(raw)
					require.NoError(t, err)
					_, err = ValidateRaw(raw, d)
					require.ErrorContains(t, err, test.want)
					var fieldErr *FieldError
					require.ErrorAs(t, err, &fieldErr)
					require.Equal(t, fmt.Sprintf("capabilities[0].config.%s.%s[0].%s", phase, group, test.field), fieldErr.Path)
					_, err = ValidatePublished(raw, d)
					require.ErrorContains(t, err, test.want)
				})
			}
		}
	}
}
