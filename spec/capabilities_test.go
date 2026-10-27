package spec

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

// configTargets is the struct each config-bearing well-known type decodes
// into. TestCapabilitySchemasAgreeOnCaseVariants requires it to cover every
// such type, so a new one cannot skip the agreement check.
var configTargets = map[string]func() any{
	CapabilityNetworkPolicy:            func() any { return new(PhasedNetwork) },
	CapabilityNetworkPolicyV2:          func() any { return new(PhasedNetworkV2) },
	CapabilityCredential:               func() any { return new(Credential) },
	CapabilitySSHAgent:                 func() any { return new(SSHAgent) },
	CapabilityVolume:                   func() any { return new(Volume) },
	CapabilityHostMount:                func() any { return new(HostMount) },
	CapabilityPort:                     func() any { return new(Port) },
	CapabilityAgentSkills:              func() any { return new(AgentSkills) },
	CapabilityAgentSkill:               func() any { return new(AgentSkill) },
	CapabilityUSBDevice:                func() any { return new(USBDevice) },
	CapabilityResources:                func() any { return new(Resources) },
	CapabilityLifecycle:                func() any { return new(Lifecycle) },
	CapabilityAgentContext:             func() any { return new(AgentContext) },
	CapabilityAgentContextV2:           func() any { return new(AgentContextV2) },
	CapabilityACP:                      func() any { return new(ACP) },
	CapabilityAgentSessions:            func() any { return new(AgentSessions) },
	CapabilityAgentInteractiveSessions: func() any { return new(AgentInteractiveSessions) },
}

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
			err := DecodeCapabilityConfig(Capability{Type: tt.typ, Config: tt.config}, configTargets[tt.typ]())
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
