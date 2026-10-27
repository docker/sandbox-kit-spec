package spec

import (
	"fmt"
	"maps"
	"math/rand"
	"path"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/quick"

	"github.com/stretchr/testify/require"
)

func instance(typ string, config map[string]any) Capability {
	return Capability{Type: typ, Config: config}
}

func TestInstanceKeyOf(t *testing.T) {
	claude := InstanceKey{Space: CapabilityACP, Parts: []string{"com.docker.kit/claude"}}
	cache := InstanceKey{Space: CapabilityVolume, Parts: []string{"/data/cache"}}
	http := InstanceKey{Space: CapabilityPort, Parts: []string{"8080/tcp"}}
	review := InstanceKey{Space: CapabilityAgentSkill, Parts: []string{"review"}}
	for _, tc := range []struct {
		name  string
		entry Capability
		want  InstanceKey
		err   string
	}{
		{name: "acp bare agent", entry: instance(CapabilityACP, map[string]any{"agent": "claude", "command": "claude-acp"}), want: claude},
		{name: "acp default namespace", entry: instance(CapabilityACP, map[string]any{"agent": "com.docker.kit/claude", "command": "claude-acp"}), want: claude},
		{name: "acp other namespace", entry: instance(CapabilityACP, map[string]any{"agent": "example.com/claude", "command": "claude-acp"}),
			want: InstanceKey{Space: CapabilityACP, Parts: []string{"example.com/claude"}}},
		{name: "credential", entry: instance(CapabilityCredential, map[string]any{"service": "github", "phase": "runtime"}),
			want: InstanceKey{Space: CapabilityCredential, Parts: []string{"github", "runtime"}}},
		{name: "credential phase list of one", entry: instance(CapabilityCredential, map[string]any{"service": "github", "phase": []any{"runtime"}}),
			want: InstanceKey{Space: CapabilityCredential, Parts: []string{"github", "runtime"}}},
		{name: "credential naming two phases", entry: instance(CapabilityCredential, map[string]any{"service": "github", "phase": []any{"install", "runtime"}}),
			err: `keys on exactly one named phase, got ["install" "runtime"]`},
		{name: "credential naming an empty phase", entry: instance(CapabilityCredential, map[string]any{"service": "github", "phase": nil}),
			err: `keys on exactly one named phase, got [""]`},
		{name: "ssh-agent", entry: instance(CapabilitySSHAgent, map[string]any{"phase": "runtime", "unrestricted": false, "sign": []any{"git"}}),
			want: InstanceKey{Space: CapabilitySSHAgent, Parts: []string{"runtime"}}},
		{name: "ssh-agent naming two phases", entry: instance(CapabilitySSHAgent, map[string]any{"phase": []any{"install", "runtime"}}),
			err: `keys on exactly one named phase, got ["install" "runtime"]`},
		{name: "ssh-agent naming no phase", entry: instance(CapabilitySSHAgent, map[string]any{"phase": []any{}}),
			err: "keys on exactly one named phase, got []"},
		{name: "volume", entry: instance(CapabilityVolume, map[string]any{"path": "/data/cache", "size": "1g"}), want: cache},
		{name: "volume dot segment", entry: instance(CapabilityVolume, map[string]any{"path": "/data/./cache"}), want: cache},
		{name: "host mount keys into the volume space", entry: instance(CapabilityHostMount, map[string]any{"path": "/data/cache", "mode": "0755"}), want: cache},
		{name: "agent-skills", entry: instance(CapabilityAgentSkills, map[string]any{"path": "/home/agent/.claude/skills", "mode": SkillsReadWrite}),
			want: InstanceKey{Space: CapabilityAgentSkills, Parts: []string{"/home/agent/.claude/skills"}}},
		{name: "agent-skill basename", entry: instance(CapabilityAgentSkill, map[string]any{"path": "/opt/skills/review"}), want: review},
		{name: "agent-skill explicit name", entry: instance(CapabilityAgentSkill, map[string]any{"path": "/opt/skills/review-v2", "name": "review"}), want: review},
		{name: "port omitted transport", entry: instance(CapabilityPort, map[string]any{"container": 8080}), want: http},
		{name: "port explicit tcp", entry: instance(CapabilityPort, map[string]any{"container": 8080, "transport": "tcp", "name": "api"}), want: http},
		{name: "port udp", entry: instance(CapabilityPort, map[string]any{"container": 8080, "transport": "udp"}),
			want: InstanceKey{Space: CapabilityPort, Parts: []string{"8080/udp"}}},
		{name: "agent-context profile", entry: instance(CapabilityAgentContext, map[string]any{"directory": "/home/agent/.claude", "filename": "CLAUDE.md", "content": "Be brief."}),
			want: InstanceKey{Space: CapabilityAgentContext, Parts: []string{"/home/agent/.claude/CLAUDE.md"}}},
		{name: "agent-context@2 profile", entry: instance(CapabilityAgentContextV2, map[string]any{"directory": "/home/agent/.claude", "filename": "CLAUDE.md"}),
			want: InstanceKey{Space: CapabilityAgentContextV2, Parts: []string{"/home/agent/.claude/CLAUDE.md"}}},
		{name: "agent-context without a directory", entry: instance(CapabilityAgentContext, map[string]any{"filename": "AGENTS.md"}), want: InstanceKey{Space: CapabilityAgentContext}},
		{name: "agent-context@2 body only", entry: instance(CapabilityAgentContextV2, map[string]any{"content": "Be brief."}), want: InstanceKey{Space: CapabilityAgentContextV2}},
		{name: "agent-context@2 refuses an @1 field", entry: instance(CapabilityAgentContextV2, map[string]any{"directory": "/home/agent/.claude", "filename": "CLAUDE.md", "contentFile": "context.md"}),
			err: "unknown field"},
		{name: "singleton", entry: instance(CapabilityResources, map[string]any{"cpu": 2})},
		{name: "config-less singleton", entry: Capability{Type: CapabilityPrivileged}},
		{name: "usb-device", entry: instance(CapabilityUSBDevice, map[string]any{"vendorId": "1d6b", "productId": "0002"})},
		{name: "unknown type", entry: instance("example.com/thing@1", map[string]any{"anything": 1})},
		{name: "config that does not decode", entry: instance(CapabilityACP, map[string]any{"agent": "claude", "colour": "blue"}), err: "unknown field"},
		{name: "unexpanded arg reference", entry: instance(CapabilityVolume, map[string]any{"path": "/data/${{ kit.args.cache }}/.."}),
			err: "expand it before keying"},
		{name: "unexpanded arg reference spaced with tabs", entry: instance(CapabilityVolume, map[string]any{"path": "/data/${{\tkit.args.cache\t}}"}),
			err: "expand it before keying"},
		{name: "unexpanded arg reference spaced with newlines", entry: instance(CapabilityACP, map[string]any{"agent": "${{\nkit.args.agent\n}}", "command": "acp"}),
			err: "expand it before keying"},
		{name: "unexpanded environment reference", entry: instance(CapabilityACP, map[string]any{"agent": "${{ kit.env.AGENT }}", "command": "acp"}),
			err: "expand it before keying"},
		{name: "reference in a type without a key", entry: instance("example.com/thing@1", map[string]any{"anything": "${{ kit.args.thing }}"})},
		{name: "group", entry: Capability{Group: &CapabilityGroup{Capabilities: []Capability{instance(CapabilityPort, map[string]any{"container": 8080})}}},
			err: "select it first"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key, arity, err := InstanceKeyOf(tc.entry)
			if tc.err != "" {
				require.ErrorContains(t, err, tc.err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, ArityOf(tc.entry.Type), arity, "an entry has its type's arity")
			require.Equal(t, tc.want, key)
		})
	}
}

// Every type this library knows has the arity §7.1 gives it, and a type
// it does not know is unknown rather than guessed at. The table has to
// name every known type, so a new one cannot arrive without a decision.
func TestArityOf(t *testing.T) {
	want := map[string]Arity{
		CapabilityNetworkPolicy:            AritySingleton,
		CapabilityNetworkPolicyV2:          AritySingleton,
		CapabilityResources:                AritySingleton,
		CapabilityPrivileged:               AritySingleton,
		CapabilityKitRegistry:              AritySingleton,
		CapabilityAgentSessions:            AritySingleton,
		CapabilityAgentInteractiveSessions: AritySingleton,
		CapabilityLifecycle:                AritySingleton,
		CapabilitySbx:                      AritySingleton,
		CapabilityLongRunning:              AritySingleton,
		CapabilityGitIdentity:              AritySingleton,
		CapabilityCredential:               ArityInstance,
		CapabilitySSHAgent:                 ArityInstance,
		CapabilityVolume:                   ArityInstance,
		CapabilityHostMount:                ArityInstance,
		CapabilityAgentSkills:              ArityInstance,
		CapabilityAgentSkill:               ArityInstance,
		CapabilityACP:                      ArityInstance,
		CapabilityPort:                     ArityInstance,
		CapabilityAgentContext:             ArityInstance,
		CapabilityAgentContextV2:           ArityInstance,
		CapabilityUSBDevice:                ArityWholeEntry,
	}
	require.ElementsMatch(t, KnownCapabilities(), slices.Collect(maps.Keys(want)))
	for typ, arity := range want {
		require.Equal(t, arity, ArityOf(typ), typ)
	}
	for _, typ := range []string{"", "example.com/thing@1", "com.docker.sandbox/volume@2"} {
		require.Equal(t, ArityUnknown, ArityOf(typ), typ)
	}
}

// Spelling never reaches a key. These hold for whatever quick generates,
// not only for the spellings the table above thought of.
func TestInstanceKeysIgnoreSpelling(t *testing.T) {
	// A fixed source keeps a failure reproducible; quick seeds from the
	// clock otherwise.
	config := &quick.Config{MaxCount: 500, Rand: rand.New(rand.NewSource(1))}
	key := func(t *testing.T, c Capability) InstanceKey {
		k, _, err := InstanceKeyOf(c)
		require.NoError(t, err)
		return k
	}

	t.Run("storage paths", func(t *testing.T) {
		require.NoError(t, quick.Check(func(segments []string) bool {
			want := InstanceKey{Space: CapabilityVolume, Parts: []string{path.Clean("/" + strings.Join(segments, "/"))}}
			for _, spelled := range []string{
				"/" + strings.Join(segments, "/./") + "/",
				"//" + strings.Join(segments, "//"),
				"/detour/../" + strings.Join(segments, "/"),
			} {
				for _, typ := range []string{CapabilityVolume, CapabilityHostMount} {
					if !reflect.DeepEqual(key(t, instance(typ, map[string]any{"path": spelled})), want) {
						return false
					}
				}
			}
			return true
		}, config))
	})

	t.Run("agent names", func(t *testing.T) {
		require.NoError(t, quick.Check(func(name string) bool {
			bare := strings.ReplaceAll(name, "/", "")
			acp := func(agent string) InstanceKey {
				return key(t, instance(CapabilityACP, map[string]any{"agent": agent, "command": "acp"}))
			}
			return reflect.DeepEqual(acp(bare), acp(DefaultCapabilityNamespace+"/"+bare)) &&
				!reflect.DeepEqual(acp(bare), acp("example.com/"+bare))
		}, config))
	})

	t.Run("port transports", func(t *testing.T) {
		require.NoError(t, quick.Check(func(container uint16) bool {
			port := func(fields map[string]any) InstanceKey {
				fields["container"] = int(container)
				return key(t, instance(CapabilityPort, fields))
			}
			omitted := port(map[string]any{})
			return reflect.DeepEqual(omitted, port(map[string]any{"transport": "tcp"})) &&
				!reflect.DeepEqual(omitted, port(map[string]any{"transport": "udp"}))
		}, config))
	})
}

// Composition reconciles on InstanceKeyOf, so an integration keying the
// composed entries cannot disagree with it: two entries become one, or a
// conflict, exactly when their keys are equal. Every ordered pair from
// the pool is composed, not a sample, and the pool mixes spellings of one
// request, different requests under one key, and different keys.
func TestInstanceKeysAgreeWithComposition(t *testing.T) {
	pool := []Capability{
		instance(CapabilityACP, map[string]any{"agent": "claude", "command": "claude-acp"}),
		instance(CapabilityACP, map[string]any{"agent": "com.docker.kit/claude", "command": "claude-acp"}),
		instance(CapabilityACP, map[string]any{"agent": "com.docker.kit/claude", "command": "other-acp"}),
		instance(CapabilityACP, map[string]any{"agent": "example.com/claude", "command": "claude-acp"}),
		instance(CapabilityCredential, map[string]any{"service": "github", "phase": "runtime"}),
		instance(CapabilityCredential, map[string]any{"service": "github", "phase": "install"}),
		instance(CapabilityCredential, map[string]any{"service": "gitlab", "phase": "runtime"}),
		instance(CapabilitySSHAgent, map[string]any{"phase": "runtime"}),
		instance(CapabilitySSHAgent, map[string]any{"phase": "runtime", "unrestricted": false, "sign": []any{"git"}}),
		instance(CapabilitySSHAgent, map[string]any{"phase": "install"}),
		instance(CapabilityVolume, map[string]any{"path": "/data/cache"}),
		instance(CapabilityVolume, map[string]any{"path": "/data/./cache"}),
		instance(CapabilityVolume, map[string]any{"path": "/data/cache", "size": "1g"}),
		instance(CapabilityVolume, map[string]any{"path": "/data/state"}),
		instance(CapabilityHostMount, map[string]any{"path": "/data/cache"}),
		instance(CapabilityHostMount, map[string]any{"path": "/data/shared"}),
		instance(CapabilityAgentSkills, map[string]any{"path": "/home/agent/.claude/skills"}),
		instance(CapabilityAgentSkills, map[string]any{"path": "/home/agent/.codex/skills"}),
		instance(CapabilityAgentSkill, map[string]any{"path": "/opt/skills/review"}),
		instance(CapabilityAgentSkill, map[string]any{"path": "/opt/skills/review-v2", "name": "review"}),
		instance(CapabilityAgentSkill, map[string]any{"path": "/opt/skills/lint"}),
		instance(CapabilityPort, map[string]any{"container": 8080}),
		instance(CapabilityPort, map[string]any{"container": 8080, "transport": "tcp"}),
		instance(CapabilityPort, map[string]any{"container": 8080, "transport": "udp"}),
		instance(CapabilityPort, map[string]any{"container": 9090}),
		// Explicit @1 profiles only. @1 and @2 refuse to compose together
		// whatever their keys, and an entry without a directory folds into
		// whichever profile another contribution states.
		instance(CapabilityAgentContext, map[string]any{"directory": "/home/agent/.claude", "filename": "CLAUDE.md"}),
		instance(CapabilityAgentContext, map[string]any{"directory": "/home/agent/.codex", "filename": "AGENTS.md"}),
	}
	keys := make([]InstanceKey, len(pool))
	for i, c := range pool {
		key, arity, err := InstanceKeyOf(c)
		require.NoError(t, err)
		require.Equal(t, ArityInstance, arity, c.Type)
		keys[i] = key
	}
	contribution := func(reference string, c Capability) Contribution {
		return Contribution{Reference: reference, Descriptor: &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{c}}}
	}
	for i, a := range pool {
		for j, b := range pool {
			composed, err := Compose([]Contribution{contribution("first", a), contribution("second", b)})
			pair := fmt.Sprintf("%s %v and %s %v, keys %v and %v", a.Type, a.Config, b.Type, b.Config, keys[i], keys[j])
			if !reflect.DeepEqual(keys[i], keys[j]) {
				require.NoError(t, err, pair)
				require.Len(t, composed.Capabilities, 2, pair)
				continue
			}
			if err != nil {
				// A conflict between the two, not some unrelated failure.
				require.ErrorContains(t, err, "first capabilities[0]", pair)
				require.ErrorContains(t, err, "second capabilities[0]", pair)
				continue
			}
			require.Len(t, composed.Capabilities, 1, pair)
		}
	}
}

// A composed entry keeps its key when the composed descriptor is composed
// again, so an integration that stored keys recognizes every entry the
// next time. Multi-phase entries arrive split, spellings arrive merged,
// and each key appears once.
func TestInstanceKeysSurviveRecomposition(t *testing.T) {
	workload := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindWorkload, Capabilities: []Capability{
		instance(CapabilityCredential, map[string]any{"service": "github", "phase": []any{"install", "runtime"}}),
		instance(CapabilitySSHAgent, map[string]any{"phase": []any{"install", "runtime"}}),
		instance(CapabilityACP, map[string]any{"agent": "claude", "command": "claude-acp"}),
		instance(CapabilityVolume, map[string]any{"path": "/data/./cache"}),
		instance(CapabilityHostMount, map[string]any{"path": "/data/shared"}),
		instance(CapabilityPort, map[string]any{"container": 8080}),
		instance(CapabilityAgentContext, map[string]any{"directory": "/home/agent/.claude", "filename": "CLAUDE.md"}),
	}}
	mixin := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{
		instance(CapabilityACP, map[string]any{"agent": "com.docker.kit/claude", "command": "claude-acp"}),
		instance(CapabilityVolume, map[string]any{"path": "/data/cache"}),
		instance(CapabilityPort, map[string]any{"container": 8080, "transport": "tcp"}),
		instance(CapabilityAgentContext, map[string]any{"directory": "/home/agent/.codex", "filename": "AGENTS.md"}),
	}}
	keysOf := func(d *Descriptor) []InstanceKey {
		var keys []InstanceKey
		seen := map[string]bool{}
		for _, c := range d.Capabilities {
			key, arity, err := InstanceKeyOf(c)
			require.NoError(t, err, c.Type)
			require.Equal(t, ArityInstance, arity, c.Type)
			require.False(t, seen[fmt.Sprintf("%q", key)], "%v appears twice", key)
			seen[fmt.Sprintf("%q", key)] = true
			keys = append(keys, key)
		}
		return keys
	}
	composed, err := Compose([]Contribution{{Reference: "workload", Descriptor: workload}, {Reference: "mixin", Descriptor: mixin}})
	require.NoError(t, err)
	keys := keysOf(composed)
	require.Len(t, keys, 10, "two phases each for the credential and the agent, one ACP, volume, host mount and port, two profiles")
	recomposed, err := Compose([]Contribution{{Reference: "composed", Descriptor: composed}})
	require.NoError(t, err)
	require.Equal(t, keys, keysOf(recomposed))
}

func ExampleInstanceKeyOf() {
	mount := Capability{Type: CapabilityHostMount, Config: map[string]any{"path": "/data/./cache"}}
	key, arity, err := InstanceKeyOf(mount)
	if err != nil {
		panic(err)
	}
	// Store the space and parts together, joined however your storage can
	// carry them unambiguously.
	fmt.Println(arity, key.Space, key.Parts)

	_, arity, _ = InstanceKeyOf(Capability{Type: CapabilityPrivileged})
	fmt.Println(arity)
	// Output:
	// instance com.docker.sandbox/volume@1 [/data/cache]
	// singleton
}

// Quoted YAML and JSON escape the whitespace an arg reference may hold
// inside its braces, so the gates that refuse a leftover reference read
// decoded strings: such an entry defers typed validation like any
// parameterized one, and only these gates stand between it and a port
// whose container is still a string.
func TestLeftoverArgReferencesSpelledWithWhitespaceAreRefused(t *testing.T) {
	const entry = `type: com.docker.sandbox/port@1
    config: {container: "${{\tkit.args.port\n}}"}`
	for name, capabilities := range map[string]string{
		"ordinary entry": "  - " + entry,
		"group member":   "  - group:\n      optional: true\n      capabilities:\n        - " + strings.ReplaceAll(entry, "\n    ", "\n          "),
	} {
		t.Run(name, func(t *testing.T) {
			raw := []byte("schemaVersion: \"3\"\nkind: mixin\nversion: \"1.0.0\"\nargs:\n  port:\n    default: \"8080\"\ncapabilities:\n" + capabilities + "\n")
			require.NotContains(t, ReferencedArgs(raw), "port", "the raw bytes hide the reference")
			d, err := Decode(raw)
			require.NoError(t, err)

			_, err = ValidateEffective(raw, d)
			require.ErrorContains(t, err, "still references kit args [port]")

			_, err = SelectCapabilities(t.Context(), "kit", d, Supported(CapabilityPort))
			require.ErrorContains(t, err, "still contains unresolved arguments")
		})
	}
}

// What validation keys an authored entry on where a block cannot show it,
// because the entry is invalid on its own or gives no key at all: another
// field that is invalid does not hide the key, and a key field that is
// missing, null, or undecodable leaves the entry unkeyed. A reference in a
// key field is keyed on its text, one spelling for each, only where the
// key reads the field as written; a key that reworks the field leaves the
// entry unkeyed.
func TestLiteralInstanceKeys(t *testing.T) {
	github := InstanceKey{Space: CapabilityCredential, Parts: []string{"github", "runtime"}}
	transport := InstanceKey{Space: CapabilityPort, Parts: []string{"8080/${{ kit.args.transport }}"}}
	for _, tc := range []struct {
		name  string
		entry Capability
		want  []InstanceKey
	}{
		{name: "credential phase listed twice", entry: instance(CapabilityCredential, map[string]any{"service": "github", "phase": []any{"runtime", "runtime"}}),
			want: []InstanceKey{github, github}},
		{name: "credential beside an invalid field", entry: instance(CapabilityCredential, map[string]any{"service": "github", "phase": "runtime", "apiKey": 5}),
			want: []InstanceKey{github}},
		{name: "credential with an empty phase", entry: instance(CapabilityCredential, map[string]any{"service": "github", "phase": []any{"", "runtime"}}),
			want: []InstanceKey{github}},
		{name: "credential with a null phase", entry: instance(CapabilityCredential, map[string]any{"service": "github", "phase": nil})},
		{name: "credential phase that does not decode", entry: instance(CapabilityCredential, map[string]any{"service": "github", "phase": 7})},
		{name: "credential without a service", entry: instance(CapabilityCredential, map[string]any{"phase": "runtime"})},
		{name: "ssh-agent beside an invalid bound", entry: instance(CapabilitySSHAgent, map[string]any{"phase": "runtime", "sign": 5}),
			want: []InstanceKey{{Space: CapabilitySSHAgent, Parts: []string{"runtime"}}}},
		{name: "ssh-agent without a phase", entry: instance(CapabilitySSHAgent, map[string]any{})},
		{name: "acp agent that is not a string", entry: instance(CapabilityACP, map[string]any{"agent": 5, "command": "acp"})},
		{name: "volume beside an invalid size", entry: instance(CapabilityVolume, map[string]any{"path": "/data/cache", "size": "huge"}),
			want: []InstanceKey{{Space: CapabilityVolume, Parts: []string{"/data/cache"}}}},
		{name: "volume path reference spelled with whitespace", entry: instance(CapabilityVolume, map[string]any{"path": "/data/${{\tkit.args.dir\n}}/.."})},
		{name: "port transport reference", entry: instance(CapabilityPort, map[string]any{"container": 8080, "transport": "${{ kit.args.transport }}"}),
			want: []InstanceKey{transport}},
		{name: "port transport reference spelled with whitespace", entry: instance(CapabilityPort, map[string]any{"container": 8080, "transport": "${{\tkit.args.transport\n}}"}),
			want: []InstanceKey{transport}},
		// The long s sorts later, so decoding reads it over the reference.
		{name: "port transport reference beside a case variant", entry: instance(CapabilityPort, map[string]any{"container": 8080, "transport": "${{ kit.args.transport }}", "tranſport": "udp"})},
		// The key reads a container as a number, which a reference is not.
		{name: "port container reference", entry: instance(CapabilityPort, map[string]any{"container": "${{ kit.args.port }}", "transport": "tcp"})},
		{name: "credential service reference", entry: instance(CapabilityCredential, map[string]any{"service": "${{kit.env.SERVICE}}", "phase": "runtime"}),
			want: []InstanceKey{{Space: CapabilityCredential, Parts: []string{"${{ kit.env.SERVICE }}", "runtime"}}}},
		{name: "ssh-agent phase reference beside a literal phase", entry: instance(CapabilitySSHAgent, map[string]any{"phase": []any{"${{ kit.args.phase}}", "install"}}),
			want: []InstanceKey{{Space: CapabilitySSHAgent, Parts: []string{"${{ kit.args.phase }}"}}, {Space: CapabilitySSHAgent, Parts: []string{"install"}}}},
		{name: "agent-skills path reference", entry: instance(CapabilityAgentSkills, map[string]any{"path": "/home/agent/${{ kit.args.agent }}/skills"}),
			want: []InstanceKey{{Space: CapabilityAgentSkills, Parts: []string{"/home/agent/${{ kit.args.agent }}/skills"}}}},
		// Each of these keys reworks the field: a namespace joins a bare
		// agent name, an empty skill name falls back to the path's base,
		// and a profile's directory and filename are joined and cleaned.
		{name: "acp agent reference", entry: instance(CapabilityACP, map[string]any{"agent": "${{ kit.args.agent }}", "command": "acp"})},
		{name: "agent-skill name reference", entry: instance(CapabilityAgentSkill, map[string]any{"path": "/opt/skills/review", "name": "${{ kit.args.name }}"})},
		{name: "agent-context directory reference", entry: instance(CapabilityAgentContextV2, map[string]any{"directory": "/home/agent/${{ kit.args.agent }}", "filename": "AGENTS.md"})},
		{name: "agent-context with an empty directory names no profile", entry: instance(CapabilityAgentContextV2, map[string]any{"directory": "", "filename": "AGENTS.md"}),
			want: []InstanceKey{{Space: CapabilityAgentContextV2}}},
		{name: "port transport spelled in another case", entry: instance(CapabilityPort, map[string]any{"container": 8080, "Transport": "udp"})},
		{name: "port transport a referenced key may name", entry: instance(CapabilityPort, map[string]any{"container": 8080, "${{ kit.args.field }}": "udp"})},
		{name: "agent-skill with a null name reads its default", entry: instance(CapabilityAgentSkill, map[string]any{"path": "/opt/skills/review", "name": nil}),
			want: []InstanceKey{{Space: CapabilityAgentSkill, Parts: []string{"review"}}}},
		{name: "agent-skill name spelled in another case", entry: instance(CapabilityAgentSkill, map[string]any{"path": "/opt/skills/lint", "Name": "review"})},
		{name: "singleton", entry: instance(CapabilityResources, map[string]any{"cpu": 2})},
		{name: "unknown type", entry: instance("example.com/thing@1", map[string]any{"path": "/data"})},
		{name: "group", entry: Capability{Group: &CapabilityGroup{Capabilities: []Capability{instance(CapabilityVolume, map[string]any{"path": "/data/cache"})}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, literalInstanceKeys(tc.entry))
		})
	}
}

// A type's key reads only the fields its definition names, so an
// authored entry keyed on those alone has the key InstanceKeyOf gives the
// whole entry, and nothing in its other fields moves it. Each type's entry
// fills every field its config has, key fields with values no default
// would give, so a key reading a field it does not name shows; then each
// other field is garbled in turn. A field the type defaults, left out or
// stated null, reads its default as decoding does.
func TestLiteralInstanceKeysReadOnlyKeyFields(t *testing.T) {
	// Each type's entry, and the fields its key does not read.
	entries := map[string]struct {
		config map[string]any
		others []string
	}{
		CapabilityACP: {map[string]any{"agent": "claude", "command": "claude-acp", "env": map[string]any{"MODE": "fast"}, "protocolVersion": 2},
			[]string{"command", "env", "protocolVersion"}},
		CapabilityCredential: {map[string]any{"service": "github", "phase": "runtime", "apiKey": map[string]any{"name": "GH_TOKEN", "proxyManaged": true}},
			[]string{"apiKey", "oauth"}},
		CapabilitySSHAgent: {map[string]any{"phase": "install", "unrestricted": false, "sign": []any{"git"}, "authenticate": []any{"git@github.com"}},
			[]string{"unrestricted", "sign", "authenticate"}},
		CapabilityVolume: {map[string]any{"path": "/data/./cache/", "size": "1g", "tmpfs": true, "mode": "1777"},
			[]string{"size", "tmpfs", "mode"}},
		CapabilityHostMount:   {map[string]any{"path": "//data/shared", "mode": "0700"}, []string{"mode"}},
		CapabilityAgentSkill:  {map[string]any{"path": "/opt/skills/review-v2", "name": "review"}, nil},
		CapabilityAgentSkills: {map[string]any{"path": "/home/agent/.claude/skills", "mode": SkillsReadWrite}, []string{"mode"}},
		CapabilityPort:        {map[string]any{"container": 8080, "transport": "udp", "name": "api"}, []string{"name"}},
		CapabilityAgentContext: {map[string]any{"directory": "/home/agent/.claude", "filename": "CLAUDE.md", "content": "Be brief."},
			[]string{"content", "contentFile"}},
		CapabilityAgentContextV2: {map[string]any{"directory": "/home/agent/.claude", "filename": "CLAUDE.md", "content": "Be brief."},
			[]string{"content"}},
	}
	var keyed []string
	for _, typ := range KnownCapabilities() {
		if ArityOf(typ) == ArityInstance {
			keyed = append(keyed, typ)
		}
	}
	require.ElementsMatch(t, keyed, slices.Collect(maps.Keys(entries)), "every instance-shaped type needs an entry")
	garbage := []any{"${{ kit.args.other }}", "${{ kit.env.OTHER }}", 5, nil, map[string]any{"unknown": true}, []any{false}}

	for typ, e := range entries {
		t.Run(typ, func(t *testing.T) {
			want, _, err := InstanceKeyOf(instance(typ, e.config))
			require.NoError(t, err)
			require.Equal(t, []InstanceKey{want}, literalInstanceKeys(instance(typ, e.config)))
			for _, field := range append(e.others, "colour") {
				for _, value := range garbage {
					garbled := maps.Clone(e.config)
					garbled[field] = value
					require.Equal(t, []InstanceKey{want}, literalInstanceKeys(instance(typ, garbled)), "%s: %v", field, value)
				}
			}
			for _, field := range instanceKeys[typ].defaulted {
				omitted, null := maps.Clone(e.config), maps.Clone(e.config)
				delete(omitted, field)
				null[field] = nil
				for _, config := range []map[string]any{omitted, null} {
					want, _, err := InstanceKeyOf(instance(typ, config))
					require.NoError(t, err)
					require.Equal(t, []InstanceKey{want}, literalInstanceKeys(instance(typ, config)), "%v", config)
				}
			}
		})
	}
}

// Validation holds a declaration block to one entry per §7.1 key, so two
// entries in one block collide exactly when composition would key them as
// one request once expanded: an instance-shaped entry by the keys
// InstanceKeyOf gives it per phase, a singleton by its type alone, and
// anything else as the whole entry. Two entries collide when they share a
// key however their references expand: references beside a key leave it
// known, and a reference spelled alike in a key field of both expands
// alike in both. A key that reworks a field can fold a reference into its
// surroundings, so validation leaves such a field unkeyed while it holds
// a reference, and no two pool entries spell one alike. Every ordered
// pair from the pool is validated as a block, not a sample.
func TestValidationAgreesWithInstanceKeys(t *testing.T) {
	token := map[string]any{"name": "GH_TOKEN"}
	pool := []Capability{
		instance(CapabilityACP, map[string]any{"agent": "claude", "command": "claude-acp"}),
		instance(CapabilityACP, map[string]any{"agent": "com.docker.kit/claude", "command": "other-acp"}),
		instance(CapabilityACP, map[string]any{"agent": "example.com/claude", "command": "claude-acp"}),
		instance(CapabilityACP, map[string]any{"agent": "claude", "command": "${{ kit.args.command }}"}),
		instance(CapabilityACP, map[string]any{"agent": "${{ kit.env.AGENT }}", "command": "claude-acp"}),
		instance(CapabilityCredential, map[string]any{"service": "github", "phase": "runtime", "apiKey": token}),
		instance(CapabilityCredential, map[string]any{"service": "github", "phase": []any{"runtime"}, "apiKey": map[string]any{"name": "GITHUB_TOKEN"}}),
		instance(CapabilityCredential, map[string]any{"service": "github", "phase": "install", "apiKey": token}),
		instance(CapabilityCredential, map[string]any{"service": "github", "phase": []any{"install", "runtime"}, "apiKey": token}),
		instance(CapabilityCredential, map[string]any{"service": "github", "phase": []any{"${{ kit.args.phase }}", "install"}, "apiKey": token}),
		instance(CapabilityCredential, map[string]any{"service": "github", "phase": "runtime", "apiKey": map[string]any{"name": "${{ kit.args.token }}"}}),
		instance(CapabilityCredential, map[string]any{"service": "${{ kit.args.service }}", "phase": "runtime", "apiKey": token}),
		instance(CapabilityCredential, map[string]any{"service": "${{ kit.args.service }}", "phase": []any{"runtime"}, "apiKey": map[string]any{"name": "GITHUB_TOKEN"}}),
		instance(CapabilityCredential, map[string]any{"service": "gitlab", "phase": "runtime", "apiKey": token}),
		instance(CapabilitySSHAgent, map[string]any{"phase": "runtime"}),
		instance(CapabilitySSHAgent, map[string]any{"phase": "runtime", "unrestricted": false, "sign": []any{"git"}}),
		instance(CapabilitySSHAgent, map[string]any{"phase": "install"}),
		instance(CapabilitySSHAgent, map[string]any{"phase": []any{"install", "runtime"}}),
		instance(CapabilitySSHAgent, map[string]any{"phase": "${{ kit.args.phase }}", "unrestricted": false, "sign": []any{"git"}}),
		instance(CapabilitySSHAgent, map[string]any{"phase": []any{"${{kit.args.phase}}", "install"}}),
		instance(CapabilityVolume, map[string]any{"path": "/data/cache"}),
		instance(CapabilityVolume, map[string]any{"path": "/data/./cache", "size": "1g"}),
		instance(CapabilityVolume, map[string]any{"path": "/data/cache/", "size": "${{ kit.args.size }}"}),
		instance(CapabilityVolume, map[string]any{"path": "/data/${{ kit.args.dir }}"}),
		// Cleans to /data/cache as written, which dir=a/b does not expand to.
		instance(CapabilityVolume, map[string]any{"path": "/data/cache/${{ kit.args.dir }}/.."}),
		instance(CapabilityVolume, map[string]any{"path": "/data/state"}),
		instance(CapabilityHostMount, map[string]any{"path": "/data/cache"}),
		instance(CapabilityHostMount, map[string]any{"path": "/data/shared"}),
		instance(CapabilityHostMount, map[string]any{"path": "/data/shared", "mode": "${{ kit.args.mode }}"}),
		instance(CapabilityAgentSkills, map[string]any{"path": "/home/agent/.claude/skills"}),
		instance(CapabilityAgentSkills, map[string]any{"path": "/home/agent/.claude/skills", "mode": SkillsReadWrite}),
		instance(CapabilityAgentSkills, map[string]any{"path": "/home/agent/.claude/skills", "mode": "${{ kit.args.mode }}"}),
		instance(CapabilityAgentSkills, map[string]any{"path": "/home/agent/.codex/skills"}),
		instance(CapabilityAgentSkills, map[string]any{"Path": "/home/agent/.codex/skills", "mode": SkillsReadWrite}),
		instance(CapabilityAgentSkills, map[string]any{"path": "/home/agent/${{ kit.args.agent }}/skills"}),
		instance(CapabilityAgentSkills, map[string]any{"path": "/home/agent/${{ kit.args.agent }}/skills", "mode": SkillsReadWrite}),
		instance(CapabilityAgentSkill, map[string]any{"path": "/opt/skills/review"}),
		instance(CapabilityAgentSkill, map[string]any{"path": "/opt/skills/review-v2", "name": "review"}),
		instance(CapabilityAgentSkill, map[string]any{"path": "/opt/skills/review", "name": "${{ kit.args.name }}"}),
		instance(CapabilityAgentSkill, map[string]any{"path": "/opt/skills/${{ kit.env.SKILL }}"}),
		instance(CapabilityAgentSkill, map[string]any{"path": "/opt/skills/lint"}),
		instance(CapabilityAgentSkill, map[string]any{"path": "/opt/skills/lint-v2", "Name": "lint"}),
		instance(CapabilityPort, map[string]any{"container": 8080}),
		instance(CapabilityPort, map[string]any{"container": 8080, "transport": "tcp", "name": "api"}),
		instance(CapabilityPort, map[string]any{"container": 8080, "name": "${{ kit.args.name }}"}),
		instance(CapabilityPort, map[string]any{"container": 8080, "transport": nil}),
		instance(CapabilityPort, map[string]any{"container": 8080, "transport": "udp"}),
		instance(CapabilityPort, map[string]any{"container": 8080, "Transport": "udp"}),
		instance(CapabilityPort, map[string]any{"container": 8080, "transport": "${{ kit.args.transport }}"}),
		instance(CapabilityPort, map[string]any{"container": 8080, "transport": "${{kit.args.transport}}", "name": "admin"}),
		instance(CapabilityPort, map[string]any{"container": 9090}),
		instance(CapabilityPort, map[string]any{"Container": 9090, "name": "metrics"}),
		// One @1 version only: @1 and @2 cannot be declared together
		// whatever their keys, and one body only, since a Kit contributes
		// one. Entries without a directory have no profile to key on and
		// are held to one, which their equal keys say too.
		instance(CapabilityAgentContext, map[string]any{"directory": "/home/agent/.claude", "filename": "CLAUDE.md"}),
		instance(CapabilityAgentContext, map[string]any{"directory": "/home/agent/.claude", "filename": "CLAUDE.md", "content": "Be brief."}),
		instance(CapabilityAgentContext, map[string]any{"directory": "/home/agent/.codex", "filename": "AGENTS.md"}),
		instance(CapabilityAgentContext, map[string]any{"directory": "/home/agent/${{ kit.args.agent }}", "filename": "AGENTS.md"}),
		instance(CapabilityAgentContext, map[string]any{"filename": "AGENTS.md"}),
		instance(CapabilityAgentContext, map[string]any{"filename": "CLAUDE.md"}),
		instance(CapabilityResources, map[string]any{"cpu": 2}),
		instance(CapabilityResources, map[string]any{"memory": "4g"}),
		instance(CapabilityUSBDevice, map[string]any{"vendorId": "1d6b", "productId": "0002"}),
		instance(CapabilityUSBDevice, map[string]any{"class": "03"}),
	}
	// The oracle expands references itself, each to a value naming it, and
	// splits phases the way composition does, so it shares nothing with
	// validation but the key. Two expansions stand in for every one; the
	// second puts a separator in each value, so a key cleaning a path
	// cannot fold the reference away under both.
	reference := regexp.MustCompile(`\$\{\{\s*kit\.(args|env)\.(\w+)\s*\}\}`)
	expansions := []string{"${1}-${2}", "${1}/${2}"}
	var expand func(value any, to string) any
	expand = func(value any, to string) any {
		switch v := value.(type) {
		case string:
			return reference.ReplaceAllString(v, to)
		case map[string]any:
			out := make(map[string]any, len(v))
			for field, child := range v {
				out[field] = expand(child, to)
			}
			return out
		case []any:
			out := make([]any, len(v))
			for i, child := range v {
				out[i] = expand(child, to)
			}
			return out
		}
		return value
	}
	keysUnder := func(c Capability, to string) []InstanceKey {
		config := expand(c.Config, to).(map[string]any)
		configs := []map[string]any{config}
		if phases, listed := config["phase"].([]any); listed {
			configs = nil
			for _, phase := range phases {
				one := maps.Clone(config)
				one["phase"] = phase
				configs = append(configs, one)
			}
		}
		var keys []InstanceKey
		for _, config := range configs {
			key, _, err := InstanceKeyOf(Capability{Type: c.Type, Config: config})
			require.NoError(t, err, "%s %v", c.Type, config)
			keys = append(keys, key)
		}
		return keys
	}
	shared := func(a, b []InstanceKey) bool {
		return slices.ContainsFunc(a, func(k InstanceKey) bool {
			return slices.ContainsFunc(b, func(o InstanceKey) bool { return reflect.DeepEqual(k, o) })
		})
	}
	// keys[e][i] is pool entry i's keys under expansion e.
	keys := make([][][]InstanceKey, len(expansions))
	for e := range expansions {
		keys[e] = make([][]InstanceKey, len(pool))
	}
	for i, c := range pool {
		require.NoError(t, validateCapabilityBlock(&Descriptor{Kind: KindWorkload, Capabilities: []Capability{c}}), "%s %v", c.Type, c.Config)
		if ArityOf(c.Type) != ArityInstance {
			continue
		}
		for e, to := range expansions {
			keys[e][i] = keysUnder(c, to)
		}
	}
	sameRequest := func(i, j int) bool {
		a, b := pool[i], pool[j]
		if a.Type == b.Type && reflect.DeepEqual(a.Config, b.Config) {
			return true
		}
		switch ArityOf(a.Type) {
		case ArityInstance:
			for e := range expansions {
				if !shared(keys[e][i], keys[e][j]) {
					return false
				}
			}
			return true
		case AritySingleton:
			return a.Type == b.Type
		}
		return false
	}
	for i, a := range pool {
		for j, b := range pool {
			err := validateCapabilityBlock(&Descriptor{Kind: KindWorkload, Capabilities: []Capability{a, b}})
			pair := fmt.Sprintf("%s %v and %s %v, keys %v and %v, then %v and %v", a.Type, a.Config, b.Type, b.Config, keys[0][i], keys[0][j], keys[1][i], keys[1][j])
			if !sameRequest(i, j) {
				require.NoError(t, err, pair)
				continue
			}
			// A collision between the two, not some unrelated failure.
			require.Error(t, err, pair)
			for _, e := range err.(ValidationErrors) {
				require.Contains(t, e.Error(), "capabilities[1]", pair)
				require.Contains(t, e.Error(), "capabilities[0]", pair)
			}
		}
	}
}
