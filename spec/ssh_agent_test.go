package spec

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func sshAgentKit(t *testing.T, kind string, entries ...string) *Descriptor {
	t.Helper()
	doc := "schemaVersion: \"3\"\nkind: " + kind + "\ncapabilities:\n"
	for _, entry := range entries {
		doc += entry
	}
	return mustDecode(t, doc)
}

// sshAgentEntry renders one entry; extra is config lines after the phase,
// already indented for the config map.
func sshAgentEntry(phase string, optional bool, extra ...string) string {
	out := "  - type: " + CapabilitySSHAgent + "\n"
	if optional {
		out += "    optional: true\n"
	}
	out += "    config:\n      phase: " + phase + "\n"
	for _, line := range extra {
		out += "      " + line + "\n"
	}
	return out
}

func TestSSHAgentValidation(t *testing.T) {
	for _, kind := range []string{KindWorkload, KindMixin} {
		for _, entry := range []string{
			sshAgentEntry("runtime", false),
			sshAgentEntry("install", true),
			sshAgentEntry("[install, runtime]", false),
			sshAgentEntry("runtime", false, "unrestricted: false", "sign: [git]"),
			sshAgentEntry("runtime", false, "unrestricted: false", "sign: [git, file, release@example.com]"),
			sshAgentEntry("runtime", false, "unrestricted: false", "authenticate: [github.com]"),
			sshAgentEntry("runtime", false, "unrestricted: false", "authenticate: [git@github.com, deploy_bot@ci.example.org]"),
			sshAgentEntry("runtime", false, "unrestricted: false", "sign: [git]", "authenticate: [git@github.com]"),
			// The dash, dot and underscore forms a login name takes.
			sshAgentEntry("runtime", false, "unrestricted: false", "authenticate: [a-b.c_d@x.io]"),
		} {
			_, err := Validate(sshAgentKit(t, kind, entry))
			require.NoError(t, err, "%s:\n%s", kind, entry)
		}
		// One entry per phase: both phases together are two asks.
		_, err := Validate(sshAgentKit(t, kind, sshAgentEntry("install", false, "unrestricted: false", "sign: [git]"), sshAgentEntry("runtime", true)))
		require.NoError(t, err)
	}

	for name, tc := range map[string]struct{ doc, want string }{
		"phase missing":       {"  - type: " + CapabilitySSHAgent + "\n    config: {}\n", "phase must be"},
		"no config":           {"  - type: " + CapabilitySSHAgent + "\n", "phase must be"},
		"empty phases":        {sshAgentEntry("[]", false), "phase must be"},
		"duplicate phases":    {sshAgentEntry("[install, install]", false), "listed twice"},
		"unknown list phase":  {sshAgentEntry("[install, always]", false), "phase must be"},
		"unrestricted bounds": {sshAgentEntry("runtime", false, "sign: [git]"), "require unrestricted: false"},
		"unrestricted true":   {sshAgentEntry("runtime", false, "unrestricted: true", "sign: [git]"), "require unrestricted: false"},
		"bounded empty":       {sshAgentEntry("runtime", false, "unrestricted: false"), "requires sign or authenticate"},
		"null unrestricted":   {sshAgentEntry("runtime", false, "unrestricted: null"), "unrestricted must not be null"},
		"null phase":          {sshAgentEntry("null", false), "phase must not be null"},
		"null sign":           {sshAgentEntry("runtime", false, "unrestricted: false", "sign: null", "authenticate: [github.com]"), "sign must not be null"},
		"null authenticate":   {sshAgentEntry("runtime", false, "unrestricted: false", "sign: [git]", "authenticate: null"), "authenticate must not be null"},
		"invalid phase type":  {sshAgentEntry("42", false), "string or list"},
		"unknown phase":       {sshAgentEntry("always", false), "phase must be"},
		"unknown field":       {sshAgentEntry("runtime", false, "socket: /tmp/agent"), "socket"},
		"empty sign":          {sshAgentEntry("runtime", false, "unrestricted: false", "sign: []"), "empty sign list"},
		"empty authenticate":  {sshAgentEntry("runtime", false, "unrestricted: false", "authenticate: []"), "empty authenticate list"},
		"namespace space":     {sshAgentEntry("runtime", false, "unrestricted: false", `sign: ["my ns"]`), "printable ASCII"},
		"namespace empty":     {sshAgentEntry("runtime", false, "unrestricted: false", `sign: [""]`), "printable ASCII"},
		"namespace twice":     {sshAgentEntry("runtime", false, "unrestricted: false", "sign: [git, git]"), "twice"},
		"wildcard host":       {sshAgentEntry("runtime", false, "unrestricted: false", `authenticate: ["*.github.com"]`), "literal lowercase DNS name"},
		"host with port":      {sshAgentEntry("runtime", false, "unrestricted: false", `authenticate: ["github.com:22"]`), "literal lowercase DNS name"},
		"uppercase host":      {sshAgentEntry("runtime", false, "unrestricted: false", "authenticate: [GitHub.com]"), "literal lowercase DNS name"},
		"ip address":          {sshAgentEntry("runtime", false, "unrestricted: false", "authenticate: [192.0.2.1]"), "IP address"},
		"user with ip":        {sshAgentEntry("runtime", false, "unrestricted: false", "authenticate: [git@192.0.2.1]"), "IP address"},
		"empty user":          {sshAgentEntry("runtime", false, "unrestricted: false", `authenticate: ["@github.com"]`), "literal lowercase DNS name"},
		"destination twice":   {sshAgentEntry("runtime", false, "unrestricted: false", "authenticate: [github.com, github.com]"), "twice"},
		"overlapping phases":  {sshAgentEntry("[install, runtime]", false) + sshAgentEntry("runtime", false), "already declared"},
		"same phase twice":    {sshAgentEntry("runtime", false) + sshAgentEntry("runtime", true, "unrestricted: false", "sign: [git]"), "already declared"},
		"identical same kind": {sshAgentEntry("install", false) + sshAgentEntry("install", false), "identical"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Validate(sshAgentKit(t, KindWorkload, tc.doc))
			require.ErrorContains(t, err, tc.want)
		})
	}
}

// An arg-referenced entry defers its value checks until expansion, but
// the presence rules hold before it: an empty list is wrong whatever the
// arg later says.
func TestSSHAgentPresenceRulesDoNotWaitForArgs(t *testing.T) {
	for _, extra := range []string{"authenticate: []", "authenticate: null"} {
		doc := "schemaVersion: \"3\"\nkind: mixin\nargs:\n  ns: {default: git}\ncapabilities:\n" +
			sshAgentEntry("runtime", false, "unrestricted: false", "sign: [\"${{ kit.args.ns }}\"]", extra)
		_, err := Validate(mustDecode(t, doc))
		require.ErrorContains(t, err, "authenticate")
	}
}

func boolPtr(v bool) *bool { return &v }

func TestSSHAgentParameterizedUnrestricted(t *testing.T) {
	for _, bounds := range []string{"", "      sign: [git]\n", "      authenticate: [git@github.com]\n"} {
		raw := []byte("schemaVersion: '3'\nkind: mixin\nargs:\n  unrestricted: {required: true}\ncapabilities:\n" +
			sshAgentEntry("runtime", false, "unrestricted: ${{ kit.args.unrestricted }}") + bounds)
		d, err := Decode(raw)
		require.NoError(t, err)
		_, err = ValidateRaw(raw, d)
		require.NoError(t, err)
		for _, value := range []string{"true", "false", "not-a-boolean"} {
			effective, err := ExpandCreateArgs(raw, d.Args, map[string]string{"unrestricted": value})
			require.NoError(t, err)
			expanded, err := Decode(effective)
			require.NoError(t, err)
			_, err = ValidateEffective(effective, expanded)
			if (value == "true" && bounds == "") || (value == "false" && bounds != "") {
				require.NoError(t, err)
			} else {
				require.Error(t, err, "unrestricted=%s bounds=%q", value, bounds)
			}
		}
	}
}

func TestSSHAgentBounded(t *testing.T) {
	require.False(t, SSHAgent{Phase: SSHAgentPhases{"runtime"}}.Bounded())
	require.True(t, SSHAgent{Phase: SSHAgentPhases{"runtime"}, Unrestricted: boolPtr(false), Sign: []string{"git"}}.Bounded())
	require.True(t, SSHAgent{Phase: SSHAgentPhases{"runtime"}, Unrestricted: boolPtr(false), Authenticate: []string{"github.com"}}.Bounded())
}

func mergedSSHAgents(t *testing.T, inputs ...Contribution) map[string]Capability {
	t.Helper()
	merged := mergeOK(t, inputs...).Descriptor
	_, err := Validate(merged)
	require.NoError(t, err)
	out := map[string]Capability{}
	for _, n := range merged.Capabilities {
		require.Equal(t, CapabilitySSHAgent, n.Type)
		var a SSHAgent
		require.NoError(t, DecodeCapabilityConfig(n, &a))
		out[a.Phase[0]] = n
	}
	return out
}

// One socket per phase serves every kit that asked, so what it signs is
// what any of them may: the union of bounded asks, and everything when
// any ask is unbounded. The required declaration wins in either order.
func TestSSHAgentMergeUnionsBounds(t *testing.T) {
	workload := Contribution{Reference: "workload", Descriptor: sshAgentKit(t, KindWorkload,
		sshAgentEntry("runtime", true, "unrestricted: false", "sign: [git]"))}
	mixin := Contribution{Reference: "mixin", Descriptor: sshAgentKit(t, KindMixin,
		sshAgentEntry("runtime", false, "unrestricted: false", "sign: [file, git]", "authenticate: [git@github.com]"),
		sshAgentEntry("install", true, "unrestricted: false", "authenticate: [github.com]"))}
	for _, inputs := range [][]Contribution{{workload, mixin}, {mixin, workload}} {
		byPhase := mergedSSHAgents(t, inputs...)
		var runtime, install SSHAgent
		require.NoError(t, DecodeCapabilityConfig(byPhase["runtime"], &runtime))
		require.NoError(t, DecodeCapabilityConfig(byPhase["install"], &install))
		require.Equal(t, SSHAgent{Phase: SSHAgentPhases{"runtime"}, Unrestricted: boolPtr(false), Sign: []string{"file", "git"}, Authenticate: []string{"git@github.com"}}, runtime)
		require.False(t, byPhase["runtime"].Optional, "a required declaration wins")
		require.Equal(t, SSHAgent{Phase: SSHAgentPhases{"install"}, Unrestricted: boolPtr(false), Authenticate: []string{"github.com"}}, install)
		require.True(t, byPhase["install"].Optional)
	}

	unbounded := Contribution{Reference: "unbounded", Descriptor: sshAgentKit(t, KindMixin, sshAgentEntry("runtime", true))}
	for _, inputs := range [][]Contribution{{workload, unbounded}, {unbounded, workload}} {
		var runtime SSHAgent
		require.NoError(t, DecodeCapabilityConfig(mergedSSHAgents(t, inputs...)["runtime"], &runtime))
		require.False(t, runtime.Bounded(), "an unbounded ask makes the phase unbounded")
	}
}

func surfaceOf(t *testing.T, entries ...string) Surface {
	t.Helper()
	return SurfaceOf(sshAgentKit(t, KindWorkload, entries...))
}

func TestSSHAgentSurface(t *testing.T) {
	require.Equal(t, []string{"runtime"}, surfaceOf(t, sshAgentEntry("runtime", true)).SSHAgent)
	require.Equal(t,
		[]string{"install", "runtime authenticate git@github.com", "runtime sign file", "runtime sign git"},
		surfaceOf(t,
			sshAgentEntry("runtime", false, "unrestricted: false", "sign: [git, file]", "authenticate: [git@github.com]"),
			sshAgentEntry("install", false)).SSHAgent)
	require.Empty(t, surfaceOf(t, sshAgentEntry("runtime", false, "unrestricted: false", "sign: [git]")).Services,
		"a well-known type never falls back to a service digest")
}

func TestSSHAgentWidenings(t *testing.T) {
	var (
		none         = surfaceOf(t)
		unbounded    = surfaceOf(t, sshAgentEntry("runtime", false))
		installOnly  = surfaceOf(t, sshAgentEntry("install", false))
		git          = surfaceOf(t, sshAgentEntry("runtime", false, "unrestricted: false", "sign: [git]"))
		gitAndFile   = surfaceOf(t, sshAgentEntry("runtime", false, "unrestricted: false", "sign: [git, file]"))
		anyGitHub    = surfaceOf(t, sshAgentEntry("runtime", false, "unrestricted: false", "authenticate: [github.com]"))
		gitAtGitHub  = surfaceOf(t, sshAgentEntry("runtime", false, "unrestricted: false", "authenticate: [git@github.com]"))
		rootAtGitHub = surfaceOf(t, sshAgentEntry("runtime", false, "unrestricted: false", "authenticate: [root@github.com]"))
	)
	details := func(granted, candidate Surface) []string {
		var out []string
		for _, w := range DiffWidenings(granted, candidate) {
			require.Equal(t, "ssh-agent", w.Category)
			out = append(out, w.Detail)
		}
		return out
	}

	require.Equal(t, []string{"runtime sign git"}, details(none, git))
	require.Equal(t, []string{"runtime sign file"}, details(git, gitAndFile), "a new namespace widens")
	require.Equal(t, []string{"runtime"}, details(git, unbounded), "dropping the bounds widens")
	require.Empty(t, details(unbounded, gitAndFile), "bounding an unbounded grant narrows it")
	require.Empty(t, details(gitAndFile, git))
	require.Empty(t, details(anyGitHub, gitAtGitHub), "host covers user@host")
	require.Equal(t, []string{"runtime authenticate github.com"}, details(gitAtGitHub, anyGitHub),
		"dropping the user widens")
	require.Equal(t, []string{"runtime authenticate root@github.com"}, details(gitAtGitHub, rootAtGitHub),
		"another user is another grant")
	// Moving from install to runtime keeps the agent reachable for the
	// workload's whole life: a widening, even though the count is equal.
	require.Equal(t, []string{"runtime"}, details(installOnly, unbounded))
	// The reverse is a widening too: install hooks gain an agent they
	// were never granted. Only dropping a phase needs no approval.
	require.Equal(t, []string{"install"}, details(unbounded, installOnly))
	bothPhases := surfaceOf(t, sshAgentEntry("runtime", false), sshAgentEntry("install", false))
	require.Empty(t, details(bothPhases, installOnly), "giving up a phase needs no approval")

	for _, w := range DiffWidenings(none, gitAtGitHub) {
		require.False(t, strings.Contains(w.Detail, "  "), "surface entries are single-spaced tokens: %q", w.Detail)
	}
}

func TestSSHAgentsOf(t *testing.T) {
	d := sshAgentKit(t, KindMixin,
		sshAgentEntry("[install, runtime]", true),
		sshAgentEntry("runtime", false, "unrestricted: false", "sign: [git]"))
	d.Capabilities[0].Name = "SSH agent"
	d.Capabilities[0].Description = "User-selected keys"
	all, err := SSHAgentsOf(d.Capabilities)
	require.NoError(t, err)
	require.Len(t, all, 2)
	require.Equal(t, SSHAgentPhases{"install", "runtime"}, all[0].Phase)
	require.True(t, all[0].Optional)
	require.Equal(t, "SSH agent", all[0].Name)
	require.Equal(t, "User-selected keys", all[0].Description)
	require.False(t, all[1].Optional)
	require.True(t, all[1].Bounded())
	for phase, count := range map[string]int{"install": 1, "runtime": 2, "unknown": 0} {
		got, err := SSHAgentsOfPhase(d.Capabilities, phase)
		require.NoError(t, err)
		require.Len(t, got, count)
	}
	bad := Capability{Type: CapabilitySSHAgent, Config: map[string]any{"phase": 42}}
	_, err = SSHAgentsOf([]Capability{bad})
	require.Error(t, err)
	_, err = SSHAgentsOfPhase([]Capability{bad}, "runtime")
	require.Error(t, err)
	all, err = SSHAgentsOf(nil)
	require.NoError(t, err)
	require.Empty(t, all)
}

func TestSSHAgentMultiPhaseMergeAndSurface(t *testing.T) {
	both := sshAgentEntry("[install, runtime]", false, "unrestricted: false", "sign: [git]")
	required := Contribution{Reference: "both", Descriptor: sshAgentKit(t, KindWorkload, both)}
	other := Contribution{Reference: "runtime", Descriptor: sshAgentKit(t, KindMixin, sshAgentEntry("runtime", true))}
	for _, inputs := range [][]Contribution{{required, other}, {other, required}} {
		byPhase := mergedSSHAgents(t, inputs...)
		require.Len(t, byPhase, 2)
		var install, runtime SSHAgent
		require.NoError(t, DecodeCapabilityConfig(byPhase["install"], &install))
		require.NoError(t, DecodeCapabilityConfig(byPhase["runtime"], &runtime))
		require.True(t, install.Bounded())
		require.False(t, runtime.Bounded())
		require.Equal(t, []string{"install sign git", "runtime"}, SurfaceOf(mergeOK(t, inputs...).Descriptor).SSHAgent)
	}
	require.Equal(t, []string{"install sign git", "runtime sign git"}, surfaceOf(t, both).SSHAgent)
	raw, err := json.Marshal(SSHAgent{Phase: SSHAgentPhases{"runtime"}})
	require.NoError(t, err)
	require.Contains(t, string(raw), `"phase":"runtime"`)
}

func TestSSHAgentGroupSelectionAndComposition(t *testing.T) {
	d := sshAgentKit(t, KindWorkload,
		sshAgentEntry("[install, runtime]", false, "unrestricted: false", "sign: [git]"),
		sshAgentEntry("runtime", false))
	d.Capabilities[1] = Capability{Group: &CapabilityGroup{
		Optional: true, Capabilities: []Capability{d.Capabilities[1], groupHook("signing enabled")},
	}}
	_, err := Validate(d)
	require.NoError(t, err, "phase overlap across declaration blocks is composed after selection")
	_, err = Compose([]Contribution{{Reference: "agent", Descriptor: d}})
	require.ErrorContains(t, err, "select groups before composition")
	published, err := Merge([]Contribution{{Reference: "agent", Descriptor: d}}, MergeOptions{})
	require.NoError(t, err)
	require.True(t, HasGroups(published.Descriptor.Capabilities))

	for _, tt := range []struct {
		name     string
		selector SelectCapability
		want     []string
		skipped  int
	}{
		{"accept", Supported(KnownCapabilities()...), []string{"install sign git", "runtime"}, 0},
		{"skip without lifecycle", Supported(CapabilitySSHAgent), []string{"install sign git", "runtime sign git"}, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			selection, err := SelectCapabilities(t.Context(), published.Descriptor, tt.selector)
			require.NoError(t, err)
			require.Len(t, selection.Skipped, tt.skipped)
			selected := *published.Descriptor
			selected.Capabilities = selection.Capabilities
			effective, err := Compose([]Contribution{{Reference: "published", Descriptor: &selected}})
			require.NoError(t, err)
			_, err = Validate(effective)
			require.NoError(t, err)
			require.Equal(t, tt.want, SurfaceOf(effective).SSHAgent)
			require.Equal(t, tt.skipped == 0, HasCapability(effective.Capabilities, CapabilityLifecycle))
			agents, err := SSHAgentsOf(effective.Capabilities)
			require.NoError(t, err)
			require.Len(t, agents, 2)
			for _, a := range agents {
				require.False(t, a.Optional, "the ordinary required request wins in each phase")
			}
		})
	}

	// A skipped group's invalid config must still be diagnosed before
	// runtime selection can discard it.
	d.Capabilities[1].Group.Capabilities[0].Config["phase"] = nil
	_, err = SelectCapabilities(t.Context(), d, func(context.Context, Descriptor, Capability) CapabilityDecision {
		t.Fatal("invalid declarations reached selection")
		return CapabilityDecision{Accepted: false}
	})
	require.ErrorContains(t, err, "capabilities[1].group.capabilities[0].config.phase")
}

func TestSSHAgentParameterizedLiteralValidation(t *testing.T) {
	for _, boolean := range []string{"false", `"${{ kit.args.bound }}"`} {
		for _, tc := range []struct{ phase, extra, want string }{
			{`[always, "${{ kit.args.value }}"]`, "sign: [git]", "phase must be"},
			{`["${{ kit.args.value }}", "${{ kit.args.value }}"]`, "sign: [git]", "twice"},
			{"runtime", `sign: ["bad namespace", "${{ kit.args.value }}"]`, "printable ASCII"},
			{"runtime", `sign: ["${{ kit.args.value }}", "${{ kit.args.value }}"]`, "twice"},
			{"runtime", `authenticate: [192.0.2.1, "${{ kit.args.value }}"]`, "IP address"},
			{"runtime", `authenticate: ["*.example.com", "${{ kit.args.value }}"]`, "literal lowercase"},
			{"runtime", `authenticate: ["${{ kit.args.value }}", "${{ kit.args.value }}"]`, "twice"},
			{`[runtime, "${{ kit.args.value }}"]`, `sign: [git, "${{ kit.args.value }}"]`, ""},
		} {
			doc := "schemaVersion: \"3\"\nkind: mixin\nargs:\n  value: {default: git}\n  bound: {default: false}\ncapabilities:\n" + sshAgentEntry(tc.phase, false, "unrestricted: "+boolean, tc.extra)
			_, err := Validate(mustDecode(t, doc))
			if tc.want == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.want)
			}
		}
	}
}

func TestSSHAgentParameterizedPhaseOverlap(t *testing.T) {
	for _, phase := range []string{"runtime", `[install, runtime]`, `[runtime, "${{ kit.args.phase }}"]`} {
		for _, bounds := range [][]string{
			{"unrestricted: false", `sign: ["${{ kit.args.ns }}"]`},
			{`unrestricted: "${{ kit.args.unrestricted }}"`, "sign: [git]"},
		} {
			for _, reverse := range []bool{false, true} {
				entries := []string{sshAgentEntry(phase, false, bounds...), sshAgentEntry("runtime", true)}
				if reverse {
					entries[0], entries[1] = entries[1], entries[0]
				}
				d := mustDecode(t, "schemaVersion: '3'\nkind: mixin\nargs:\n  ns: {default: git}\n  phase: {default: install}\n  unrestricted: {default: false}\ncapabilities:\n"+strings.Join(entries, ""))
				_, err := Validate(d)
				require.ErrorContains(t, err, "already declared")
				d.Capabilities = []Capability{{Group: &CapabilityGroup{Optional: true, Capabilities: d.Capabilities}}}
				_, err = Validate(d)
				require.ErrorContains(t, err, "already declared", "group members are a declaration block too")
			}
		}
	}
}

func TestSSHAgentEnvironmentExpansion(t *testing.T) {
	for _, tc := range []struct {
		name, phase, sign, destination, wantError string
	}{
		{"valid", "install", "git", "git@github.com", ""},
		{"invalid phase", "always", "git", "git@github.com", "phase must be"},
		{"invalid namespace", "install", "has space", "git@github.com", "printable ASCII"},
		{"invalid destination", "install", "git", "*.github.com", "literal lowercase DNS"},
		{"IP destination", "install", "git", "git@192.0.2.1", "IP address"},
		{"overlapping phases", "runtime", "git", "git@github.com", "already declared"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := sshAgentKit(t, KindMixin,
				sshAgentEntry(`"${{ kit.env.PHASE }}"`, false, "unrestricted: false",
					`sign: ["${{ kit.env.SIGN }}"]`, `authenticate: ["${{ kit.env.DESTINATION }}"]`),
				sshAgentEntry("runtime", false))
			raw, err := json.Marshal(d)
			require.NoError(t, err)
			_, err = ValidatePublished(raw, d)
			require.NoError(t, err)
			expanded, err := ExpandEnvironment(d, map[string]string{
				"PHASE": tc.phase, "SIGN": tc.sign, "DESTINATION": tc.destination,
			})
			require.NoError(t, err)
			raw, err = json.Marshal(expanded)
			require.NoError(t, err)
			_, err = ValidateEffective(raw, expanded)
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
