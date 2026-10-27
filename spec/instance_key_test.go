package spec

import (
	"encoding/json"
	"fmt"
	"maps"
	"math/rand"
	"path"
	"reflect"
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

			_, err = ValidateRaw(raw, d)
			require.NoError(t, err, "declared create references may remain before expansion")
			_, err = ValidatePublished(raw, d)
			require.NoError(t, err)

			delete(d.Args, "port")
			undeclared := []byte(strings.ReplaceAll(string(raw), "args:\n  port:\n    default: \"8080\"\n", ""))
			_, err = ValidateRaw(undeclared, d)
			require.ErrorContains(t, err, `declares no arg "port"`)
			path := "capabilities[0].config.container"
			if name == "group member" {
				path = "capabilities[0].group.capabilities[0].config.container"
			}
			require.Contains(t, err.Error(), path)

			d.Args["port"] = Arg{BuildArg: "PORT"}
			published := []byte(strings.ReplaceAll(string(raw), `default: "8080"`, "buildArg: PORT"))
			_, err = ValidateRaw(published, d)
			require.NoError(t, err, "declared build references may remain in authored configs")
			_, err = ValidatePublished(published, d)
			require.ErrorContains(t, err, "which resolves at build; expansion did not reach it")
			require.Contains(t, err.Error(), path)

			jsonRaw, err := json.Marshal(d)
			require.NoError(t, err)
			jsonDescriptor, err := Decode(jsonRaw)
			require.NoError(t, err)
			_, err = ValidatePublished(jsonRaw, jsonDescriptor)
			require.ErrorContains(t, err, "which resolves at build; expansion did not reach it")
			require.Contains(t, err.Error(), path)
			delete(jsonDescriptor.Args, "port")
			jsonRaw, err = json.Marshal(jsonDescriptor)
			require.NoError(t, err)
			_, err = ValidateRaw(jsonRaw, jsonDescriptor)
			require.ErrorContains(t, err, `declares no arg "port"`)
			require.Contains(t, err.Error(), path)

			_, err = ValidateEffective(raw, d)
			require.ErrorContains(t, err, "still references kit args [port]")

			_, err = SelectCapabilities(t.Context(), "kit", d, Supported(CapabilityPort))
			require.ErrorContains(t, err, "still contains unresolved arguments")
		})
	}
}
