package spec

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// newConfig returns a pointer to a new config struct of a config-bearing
// well-known type, the target DecodeCapabilityConfig fills.
func newConfig(typ string) any { return reflect.New(configTypes[typ]).Interface() }

// TestCapabilitySchemasAgreeOnCaseVariants tries every field of every type;
// these cases pin the messages and the shapes it does not reach.
func TestDecodeCapabilityConfigExactFieldNames(t *testing.T) {
	caseVariant := func(key, field string) string {
		return fmt.Sprintf("unknown field %q: config field names are case-sensitive; did you mean %q?", key, field)
	}
	credential := func(extra map[string]any) map[string]any {
		config := map[string]any{"service": "github", "phase": "runtime"}
		maps.Copy(config, extra)
		return config
	}
	for _, tt := range []struct {
		name   string
		typ    string
		config map[string]any
		// want is the error after the "capability <type>: " prefix, or
		// empty when the config decodes.
		want string
	}{
		{"exact names", CapabilityVolume, map[string]any{"path": "/data/cache", "size": "1G"}, ""},
		{"beside the exact name", CapabilityVolume, map[string]any{"path": "/data/cache", "Path": "/data/other"}, caseVariant("Path", "path")},
		{"typo", CapabilityVolume, map[string]any{"path": "/data", "sizee": "1G"}, `unknown field "sizee"`},
		{"long s", CapabilityPort, map[string]any{"container": 8080, "tran\u017fport": "udp"}, caseVariant("tran\u017fport", "transport")},
		{"list element", CapabilityCredential, credential(map[string]any{"apiKey": map[string]any{"inject": []any{
			map[string]any{"domain": "a.example.com"},
			map[string]any{"Domain": "b.example.com"},
		}}}), caseVariant("apiKey.inject[1].Domain", "apiKey.inject[1].domain")},
		{"object entry beside a bare one", CapabilityNetworkPolicyV2, map[string]any{"install": map[string]any{"allow": []any{"example.com", map[string]any{"Hosts": []any{"api.example.com"}}}}}, caseVariant("install.allow[1].Hosts", "install.allow[1].hosts")},
		// Map keys are data, not field names.
		{"env names", CapabilityACP, map[string]any{"agent": "claude", "command": []any{"adapter"}, "env": map[string]any{"Path": "/bin"}}, ""},
		{"credential file structure", CapabilityCredential, credential(map[string]any{"oauth": map[string]any{"credentialFile": map[string]any{"path": "/t", "structure": map[string]any{"Path": "{{.AccessToken}}", "nested": map[string]any{"Token": "x"}}}}}), ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := DecodeCapabilityConfig(Capability{Type: tt.typ, Config: tt.config}, newConfig(tt.typ))
			if tt.want == "" {
				require.NoError(t, err)
				return
			}
			require.EqualError(t, err, "capability "+tt.typ+": "+tt.want)
		})
	}
}

// A value of the wrong shape is still the decoder's to report.
func TestDecodeCapabilityConfigLeavesShapeToDecoder(t *testing.T) {
	for _, tt := range []struct {
		name   string
		config map[string]any
		want   string
	}{
		{"scalar for object", map[string]any{"service": "github", "phase": "runtime", "apiKey": "GH_TOKEN"}, "cannot unmarshal"},
		{"object for list", map[string]any{"service": "github", "phase": "runtime", "apiKey": map[string]any{"inject": map[string]any{"Domain": "x"}}}, "cannot unmarshal"},
		{"case variant beside a bad value", map[string]any{"service": 1, "Phase": "runtime"}, `unknown field "Phase"`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := DecodeCapabilityConfig(Capability{Type: CapabilityCredential, Config: tt.config}, new(Credential))
			require.ErrorContains(t, err, tt.want)
		})
	}
	err := DecodeCapabilityConfig(Capability{Type: CapabilityVolume, Config: map[string]any{"Path": "/data"}}, nil)
	require.ErrorContains(t, err, "Unmarshal(nil)")
}

func TestConfigFieldsNamesFieldsAsEncodingJSONDoes(t *testing.T) {
	type inner struct {
		Deep string `json:"deep"`
	}
	type sample struct {
		Tagged   string `json:"tagged,omitempty"`
		Untagged string
		Hidden   string `json:"-"`
	}
	fields, err := configFields(reflect.TypeFor[sample]())
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"tagged", "Untagged"}, slices.Collect(maps.Keys(fields)))

	type embeds struct {
		inner
	}
	_, err = configFields(reflect.TypeFor[embeds]())
	require.ErrorContains(t, err, "embeds spec.inner")
}

// Validation refuses the case variants its raw-key duplicate checks
// cannot see, and attributes the refusal to the key, where Positions
// finds it.
func TestValidateRefusesCaseVariantConfigKeys(t *testing.T) {
	for _, tt := range []struct {
		name  string
		needs string
		path  string
	}{
		{"volume beside its exact name", `{type: com.docker.sandbox/volume@1, config: {path: /data/cache}}, {type: com.docker.sandbox/volume@1, config: {Path: /data/cache}}`, "capabilities[1].config.Path"},
		{"credential beside its exact name", `{type: com.docker.sandbox/credential@1, config: {service: github, phase: runtime, apiKey: {name: A}}}, {type: com.docker.sandbox/credential@1, config: {Service: github, phase: runtime, apiKey: {name: B}}}`, "capabilities[1].config.Service"},
		{"list element", `{type: com.docker.sandbox/lifecycle@1, config: {files: [{path: /etc/a, content: a}, {Path: /etc/b, content: b}]}}`, "capabilities[0].config.files[1].Path"},
		{"agent-context@2", `{type: com.docker.sandbox/agent-context@2, config: {Directory: /home/agent/.agent, filename: AGENTS.md}}`, "capabilities[0].config.Directory"},
		{"agent skill", `{type: com.docker.sandbox/agent-skill@1, config: {Path: /skills/review}}`, "capabilities[0].config.Path"},
		{"typo", `{type: com.docker.sandbox/volume@1, config: {path: /data, sizee: 1G}}`, "capabilities[0].config.sizee"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw := []byte("schemaVersion: \"3\"\nkind: workload\ncapabilities: [" + tt.needs + "]\n")
			d, err := Decode(raw)
			require.NoError(t, err, "the descriptor decodes; only the config is refused")
			_, err = Validate(d)
			var fieldErr *FieldError
			require.ErrorAs(t, err, &fieldErr)
			require.ErrorContains(t, fieldErr, "unknown field")
			require.Equal(t, tt.path, fieldErr.Path)
			require.Contains(t, Positions(raw), fieldErr.Path)
		})
	}
}

// referenceKeyRefused is how publication and create refuse a reference in
// a key outside a free-form map.
const referenceKeyRefused = "this key holds a reference, which may stand in a map key only in a free-form map, such as a credential file's structure"

// requireKeyRefused requires err to hold exactly one field error naming
// want, attributed to path, which Positions finds in raw. Other checks
// may refuse the entry too, so it looks for the refusal of the key.
func requireKeyRefused(t *testing.T, raw []byte, err error, want, path string) {
	t.Helper()
	require.ErrorContains(t, err, want)
	var all ValidationErrors
	require.ErrorAs(t, err, &all)
	var refused []*FieldError
	for _, e := range all {
		if fieldErr, ok := errors.AsType[*FieldError](e); ok && strings.Contains(fieldErr.Error(), want) {
			refused = append(refused, fieldErr)
		}
	}
	require.Len(t, refused, 1, "one field error names %s", want)
	require.Equal(t, path, refused[0].Path)
	require.Contains(t, Positions(raw), path)
}

// A parameterized entry's unknown key is refused at publication, as it is
// at create, and a key holding a reference names no field (SPEC-v3 §6).
// Each config-bearing type renames one key of an entry that is otherwise
// valid and holds a reference elsewhere.
func TestValidateRefusesUnknownKeysInParameterizedEntries(t *testing.T) {
	const ref, refKey = "${{ kit.args.s }}", "${{ kit.args.k }}"
	types := []struct {
		typ string
		// s is the default of the arg config references.
		s      string
		config map[string]any
		// field is the key renamed: to a case variant, a typo, or a key
		// holding a reference.
		field string
	}{
		{typ: CapabilityNetworkPolicy, s: "api", config: map[string]any{"runtime": map[string]any{"allow": []any{ref + ".example.com"}}}, field: "runtime"},
		{typ: CapabilityNetworkPolicyV2, s: "api", config: map[string]any{"runtime": map[string]any{"allow": []any{map[string]any{"hosts": []any{ref + ".example.com"}, "methods": []any{"GET"}}}}}, field: "runtime"},
		{typ: CapabilityCredential, s: "GH_TOKEN", config: map[string]any{"service": "github", "phase": "runtime", "apiKey": map[string]any{"name": ref}}, field: "service"},
		{typ: CapabilitySSHAgent, s: "git", config: map[string]any{"phase": "runtime", "unrestricted": false, "sign": []any{ref}}, field: "sign"},
		{typ: CapabilityVolume, s: "1G", config: map[string]any{"path": "/data", "size": ref}, field: "path"},
		{typ: CapabilityHostMount, s: "0755", config: map[string]any{"path": "/data", "mode": ref}, field: "path"},
		{typ: CapabilityPort, s: "8080", config: map[string]any{"container": ref, "transport": "tcp"}, field: "transport"},
		{typ: CapabilityAgentSkills, s: "claude", config: map[string]any{"path": "/home/agent/." + ref + "/skills", "mode": SkillsReadOnly}, field: "mode"},
		{typ: CapabilityAgentSkill, s: "review", config: map[string]any{"path": "/skills/review", "name": ref}, field: "name"},
		{typ: CapabilityUSBDevice, s: "1d6b", config: map[string]any{"vendorId": ref, "productId": "0002"}, field: "productId"},
		{typ: CapabilityResources, s: "2", config: map[string]any{"cpu": ref, "memory": "1G"}, field: "memory"},
		{typ: CapabilityLifecycle, s: "ready", config: map[string]any{"startup": []any{map[string]any{"command": "echo " + ref}}}, field: "startup"},
		{typ: CapabilityAgentContext, s: "tabs", config: map[string]any{"content": "Indent with " + ref + "."}, field: "content"},
		{typ: CapabilityAgentContextV2, s: "tabs", config: map[string]any{"content": "Indent with " + ref + "."}, field: "content"},
		{typ: CapabilityACP, s: "debug", config: map[string]any{"agent": "claude", "command": []any{"claude-acp"}, "env": map[string]any{"LOG": ref}}, field: "env"},
		{typ: CapabilityAgentSessions, s: "opus", config: map[string]any{"prompt": []any{"--model", ref, "-p", SessionPromptPlaceholder}}, field: "prompt"},
		{typ: CapabilityAgentInteractiveSessions, s: "opus", config: map[string]any{"newSession": []any{"--model", ref}}, field: "newSession"},
	}
	var covered []string
	for _, tt := range types {
		covered = append(covered, tt.typ)
	}
	require.ElementsMatch(t, slices.Collect(maps.Keys(configTypes)), covered, "every config-bearing type")

	for _, tt := range types {
		variant := strings.ToUpper(tt.field[:1]) + tt.field[1:]
		typo := tt.field + tt.field[len(tt.field)-1:]
		// kit renders a workload holding the entry with field renamed to
		// key.
		kit := func(key string) []byte {
			config := maps.Clone(tt.config)
			config[key] = config[tt.field]
			if key != tt.field {
				delete(config, tt.field)
			}
			return parameterizedKit(t, tt.typ, config, tt.s)
		}
		at := func(key string) string { return "capabilities[0].config." + key }
		t.Run(tt.typ, func(t *testing.T) {
			t.Run("exact", func(t *testing.T) {
				raw := kit(tt.field)
				require.NoError(t, validatePublished(t, raw))
				require.NoError(t, validateCreated(t, raw))
			})
			for _, refused := range []struct{ name, key, want string }{
				{"case variant", variant, fmt.Sprintf("capabilities[0]: capability %s: unknown field %q: config field names are case-sensitive; did you mean %q?", tt.typ, variant, tt.field)},
				{"typo", typo, fmt.Sprintf("capabilities[0]: capability %s: unknown field %q", tt.typ, typo)},
				{"key holding a reference", refKey, referenceKeyRefused},
			} {
				t.Run(refused.name, func(t *testing.T) {
					raw := kit(refused.key)
					requireKeyRefused(t, raw, validatePublished(t, raw), refused.want, at(refused.key))
				})
			}
		})
	}
}

// An unknown key beside a reference is refused wherever it sits: nested,
// in a list, in a group member, or beside an entry its exact spelling
// would duplicate. Only a free-form map's keys may hold a reference.
func TestValidatePublishedRefusesUnknownKeysBesideReferences(t *testing.T) {
	for _, tt := range []struct {
		name  string
		needs string
		// want is the refusal and path the field it names; empty when the
		// descriptor validates.
		want string
		path string
	}{
		{"volume", `{type: com.docker.sandbox/volume@1, config: {Path: /data, size: "${{ kit.args.s }}"}}`, `unknown field "Path": config field names are case-sensitive; did you mean "path"?`, "capabilities[0].config.Path"},
		{"credential beside its exact spelling", `{type: com.docker.sandbox/credential@1, config: {service: github, phase: runtime, apiKey: {name: GH_TOKEN}}}, {type: com.docker.sandbox/credential@1, config: {Service: github, phase: runtime, apiKey: {name: "${{ kit.args.s }}"}}}`, `capabilities[1]: capability com.docker.sandbox/credential@1: unknown field "Service"`, "capabilities[1].config.Service"},
		{"credential nested typo", `{type: com.docker.sandbox/credential@1, config: {service: github, phase: runtime, apiKey: {name: "${{ kit.args.s }}", injet: [{domain: api.github.com}]}}}`, `unknown field "apiKey.injet"`, "capabilities[0].config.apiKey.injet"},
		{"network-policy@2 entry", `{type: com.docker.sandbox/network-policy@2, config: {runtime: {allow: [{hosts: ["${{ kit.args.s }}.example.com"], Methods: [GET]}]}}}`, `unknown field "runtime.allow[0].Methods": config field names are case-sensitive; did you mean "runtime.allow[0].methods"?`, "capabilities[0].config.runtime.allow[0].Methods"},
		{"group member", `{group: {name: storage, capabilities: [{type: com.docker.sandbox/volume@1, config: {Path: /data, size: "${{ kit.args.s }}"}}]}}`, `capabilities[0].group.capabilities[0]: capability com.docker.sandbox/volume@1: unknown field "Path"`, "capabilities[0].group.capabilities[0].config.Path"},
		{"nested key holding a reference", `{type: com.docker.sandbox/network-policy@2, config: {runtime: {allow: [{hosts: [api.example.com], "${{ kit.args.k }}": [GET]}]}}}`, referenceKeyRefused, "capabilities[0].config.runtime.allow[0].${{ kit.args.k }}"},
		{"environment reference in a key", `{type: com.docker.sandbox/volume@1, config: {path: /data, size: "${{ kit.env.SIZE }}", "${{ kit.env.KEY }}": x}}`, `unknown field "${{ kit.env.KEY }}"`, "capabilities[0].config.${{ kit.env.KEY }}"},
		// acp@1 env is a map, but its keys are variable names.
		{"acp env name holding a reference", `{type: com.docker.sandbox/acp@1, config: {agent: claude, command: [claude-acp], env: {"${{ kit.args.k }}": x}}}`, referenceKeyRefused, "capabilities[0].config.env.${{ kit.args.k }}"},
		{"free-form map key holding a reference", `{type: com.docker.sandbox/credential@1, config: {service: example, phase: runtime, oauth: {tokenEndpoint: {host: auth.example.com, path: /token}, credentialFile: {path: ~/.example/token.json, structure: {"${{ kit.args.k }}": "{{.AccessToken}}", nested: [{"${{ kit.args.k }}": x}]}}}}}`, "", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw := []byte("schemaVersion: \"3\"\nkind: workload\nargs:\n  s: {default: GH_TOKEN}\n  k: {default: methods}\ncapabilities: [" + tt.needs + "]\n")
			err := validatePublished(t, raw)
			if tt.want == "" {
				require.NoError(t, err)
				require.NoError(t, validateCreated(t, raw))
				return
			}
			requireKeyRefused(t, raw, err, tt.want, tt.path)
		})
	}
}

// An entry refused for a reference key stays out of the cross-entry checks,
// which would otherwise decode it again and refuse it a second time.
func TestReferenceKeyKeepsEntryOutOfCrossChecks(t *testing.T) {
	raw := []byte(`schemaVersion: "3"
kind: workload
args: {k: {default: header}}
capabilities:
  - {type: com.docker.sandbox/network-policy@2, config: {runtime: {allow: [api.example.com]}}}
  - {type: com.docker.sandbox/credential@1, config: {service: example, phase: runtime, apiKey: {name: A, inject: [{domain: api.example.com, "${{ kit.args.k }}": x}]}}}
`)
	d, err := Decode(raw)
	require.NoError(t, err)
	_, err = Validate(d)
	var all ValidationErrors
	require.ErrorAs(t, err, &all)
	require.Len(t, all, 1)
	require.ErrorContains(t, all[0], referenceKeyRefused)
}

// parameterizedKit renders a workload whose one entry is typ with config,
// declaring create-phase arg s with that default and k for a key.
func parameterizedKit(t *testing.T, typ string, config map[string]any, s string) []byte {
	t.Helper()
	body, err := json.Marshal(config)
	require.NoError(t, err)
	sDefault, err := json.Marshal(s)
	require.NoError(t, err)
	return fmt.Appendf(nil, "schemaVersion: \"3\"\nkind: workload\nprovides: [\"claude@2.1.0\"]\nargs:\n  s: {default: %s}\n  k: {default: k}\ncapabilities:\n  - type: %s\n    config: %s\n", sDefault, typ, body)
}

func validatePublished(t *testing.T, raw []byte) error {
	t.Helper()
	d, err := Decode(raw)
	require.NoError(t, err)
	_, err = ValidatePublished(raw, d)
	return err
}

// validateCreated expands raw's create-phase args to their defaults and
// validates the effective descriptor.
func validateCreated(t *testing.T, raw []byte) error {
	t.Helper()
	d, err := Decode(raw)
	require.NoError(t, err)
	values, err := ResolveArgs(d.Args, nil)
	require.NoError(t, err)
	effective, err := ExpandCreateArgs(raw, d.Args, values)
	require.NoError(t, err)
	de, err := Decode(effective)
	require.NoError(t, err)
	_, err = ValidateEffective(effective, de)
	return err
}
