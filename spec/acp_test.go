package spec

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func acpEntry(agent string, command any) Capability {
	return Capability{Type: CapabilityACP, Config: map[string]any{"agent": agent, "command": command}}
}

func TestACPValidation(t *testing.T) {
	for _, kind := range []string{KindWorkload, KindMixin} {
		for _, command := range []any{"adapter --stdio", []string{"adapter", ""}} {
			d := &Descriptor{SchemaVersion: SchemaVersion, Kind: kind, Capabilities: []Capability{acpEntry("claude", command)}}
			_, err := Validate(d)
			require.NoError(t, err)
		}
	}
	for _, tc := range []struct {
		name, config, want string
	}{
		{"missing config", `null`, "agent is required"},
		{"missing agent", `{"command":["adapter"]}`, "agent is required"},
		{"missing command", `{"agent":"claude"}`, "command is required"},
		{"null agent", `{"agent":null,"command":["adapter"]}`, "must not be null"},
		{"versioned agent", `{"agent":"claude@1.0.0","command":["adapter"]}`, "unversioned provides name"},
		{"invalid agent", `{"agent":"Claude","command":["adapter"]}`, "unversioned provides name"},
		{"blank agent", `{"agent":" claude ","command":["adapter"]}`, "unversioned provides name"},
		{"blank command", `{"agent":"claude","command":"  "}`, "name a command"},
		{"empty argv", `{"agent":"claude","command":[]}`, "name a command"},
		{"blank executable", `{"agent":"claude","command":[" ","arg"]}`, "name a command"},
		{"null command", `{"agent":"claude","command":null}`, "must not be null"},
		{"null element", `{"agent":"claude","command":["adapter",null]}`, "must not be null"},
		{"numeric element", `{"agent":"claude","command":["adapter",3]}`, "command must be"},
		{"nul command", `{"agent":"claude","command":["adapter","\u0000"]}`, "contains NUL"},
		{"null env", `{"agent":"claude","command":["adapter"],"env":null}`, "must not be null"},
		{"null env value", `{"agent":"claude","command":["adapter"],"env":{"MODE":null}}`, "values must be strings"},
		{"numeric env value", `{"agent":"claude","command":["adapter"],"env":{"MODE":7}}`, "cannot unmarshal number"},
		{"invalid env name", `{"agent":"claude","command":["adapter"],"env":{"BAD-NAME":"x"}}`, "invalid environment variable"},
		{"nul env", `{"agent":"claude","command":["adapter"],"env":{"MODE":"\u0000"}}`, "contains NUL"},
		{"zero version", `{"agent":"claude","command":["adapter"],"protocolVersion":0}`, "positive"},
		{"negative version", `{"agent":"claude","command":["adapter"],"protocolVersion":-1}`, "positive"},
		{"fractional version", `{"agent":"claude","command":["adapter"],"protocolVersion":1.5}`, "cannot unmarshal number"},
		{"null version", `{"agent":"claude","command":["adapter"],"protocolVersion":null}`, "must not be null"},
		{"unknown field", `{"agent":"claude","command":["adapter"],"loadSession":true}`, "unknown field"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(`{"schemaVersion":"3","kind":"mixin","capabilities":[{"type":"` + CapabilityACP + `","config":` + tc.config + `}]}`)
			d, err := Decode(raw)
			require.NoError(t, err)
			_, err = ValidateRaw(raw, d)
			require.ErrorContains(t, err, tc.want)
		})
	}
	// A parameterized sibling must not hide malformed authored commands.
	d := mustDecode(t, `schemaVersion: "3"
kind: mixin
args:
  agent: {default: claude}
capabilities:
  - type: com.docker.sandbox/acp@1
    config: {agent: "${{ kit.args.agent }}", command: []}
`)
	_, err := Validate(d)
	require.ErrorContains(t, err, "name a command")
}

func TestACPAccessor(t *testing.T) {
	d := mustDecode(t, `schemaVersion: "3"
kind: mixin
capabilities:
  - type: com.docker.sandbox/acp@1
    config:
      agent: claude
      command: adapter --stdio
      env: {MODE: bypassPermissions, EMPTY: ""}
  - type: com.docker.sandbox/acp@1
    config: {agent: codex, command: [codex-acp], protocolVersion: 2}
`)
	_, err := Validate(d)
	require.NoError(t, err)
	endpoints, err := ACPsOf(d.Capabilities)
	require.NoError(t, err)
	require.Len(t, endpoints, 2)
	require.Equal(t, CommandLine{"sh", "-c", "adapter --stdio"}, endpoints[0].Command)
	require.Equal(t, map[string]string{"MODE": "bypassPermissions", "EMPTY": ""}, endpoints[0].Env)
	require.Equal(t, 1, ACPProtocolVersion(endpoints[0].ACP))
	require.Equal(t, 2, ACPProtocolVersion(endpoints[1].ACP))
	raw, err := json.Marshal(d)
	require.NoError(t, err)
	roundTrip, err := Decode(raw)
	require.NoError(t, err)
	again, err := ACPsOf(roundTrip.Capabilities)
	require.NoError(t, err)
	require.Equal(t, endpoints, again)
	_, err = ACPsOf([]Capability{acpEntry("claude", 7)})
	require.ErrorContains(t, err, "command must be")
	_, err = ACPsOf([]Capability{{Group: &CapabilityGroup{Capabilities: d.Capabilities}}})
	require.ErrorContains(t, err, "select groups first")
}

func TestACPAgentUniqueness(t *testing.T) {
	for _, agent := range []string{"claude", "com.docker.kit/claude"} {
		d := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{
			acpEntry("claude", []string{"first"}), acpEntry(agent, []string{"second"}),
		}}
		_, err := Validate(d)
		require.ErrorContains(t, err, "already declared")
	}
	raw := []byte(`schemaVersion: "3"
kind: mixin
args:
  agent: {default: claude}
capabilities:
  - type: com.docker.sandbox/acp@1
    config: {agent: claude, command: [first]}
  - type: com.docker.sandbox/acp@1
    config: {agent: "${{ kit.args.agent }}", command: [second]}
`)
	d, err := Decode(raw)
	require.NoError(t, err)
	_, err = ValidateRaw(raw, d)
	require.NoError(t, err)
	values, err := ResolveArgs(d.Args, nil)
	require.NoError(t, err)
	expanded, err := ExpandCreateArgs(raw, d.Args, values)
	require.NoError(t, err)
	effective, err := Decode(expanded)
	require.NoError(t, err)
	_, err = ValidateEffective(expanded, effective)
	require.ErrorContains(t, err, "already declared")
}

func TestACPComposition(t *testing.T) {
	base := Contribution{Reference: "workload", Descriptor: &Descriptor{
		SchemaVersion: SchemaVersion, Kind: KindWorkload,
		Capabilities: []Capability{acpEntry("claude", "adapter --stdio")},
	}}
	base.Descriptor.Capabilities[0].Optional = true
	mixin := Contribution{Reference: "mixin", Descriptor: &Descriptor{
		SchemaVersion: SchemaVersion, Kind: KindMixin,
		Capabilities: []Capability{acpEntry("com.docker.kit/claude", []string{"sh", "-c", "adapter --stdio"}), acpEntry("codex", []string{"codex-acp"})},
	}}
	mixin.Descriptor.Capabilities[0].Config["protocolVersion"] = 1
	mixin.Descriptor.Capabilities[0].Config["env"] = map[string]string{}
	mixin.Descriptor.Capabilities[0].Name = "Claude ACP"
	for _, inputs := range [][]Contribution{{base, mixin}, {mixin, base}} {
		runtime, err := Compose(inputs)
		require.NoError(t, err)
		_, err = Validate(runtime)
		require.NoError(t, err)
		require.Len(t, runtime.Capabilities, 2)
		require.False(t, runtime.Capabilities[0].Optional)
		require.Equal(t, "Claude ACP", runtime.Capabilities[0].Name)
		published := mergeOK(t, inputs...).Descriptor
		// Republish a set as a single contribution, then compose again.
		republished := mergeOK(t, Contribution{Reference: "set", Descriptor: published}).Descriptor
		endpoints, err := ACPsOf(republished.Capabilities)
		require.NoError(t, err)
		require.Len(t, endpoints, 2)
	}
	for _, change := range []map[string]any{
		{"command": []string{"different"}},
		{"env": map[string]string{"MODE": "strict"}},
		{"protocolVersion": 2},
	} {
		entry := acpEntry("claude", "adapter --stdio")
		for key, value := range change {
			entry.Config[key] = value
		}
		other := Contribution{Reference: "conflict", Descriptor: &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{entry}}}
		for _, inputs := range [][]Contribution{{base, other}, {other, base}, {mixin, other}} {
			_, err := Compose(inputs)
			require.ErrorContains(t, err, "different things")
			_, err = Merge(inputs, MergeOptions{})
			require.ErrorContains(t, err, "different things")
		}
	}
	// Selected groups still reconcile on the agent, never their labels.
	grouped := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{
		base.Descriptor.Capabilities[0],
		{Group: &CapabilityGroup{Optional: true, Capabilities: []Capability{acpEntry("claude", []string{"conflicting"})}}},
	}}
	selected, err := SelectCapabilities(t.Context(), grouped, Supported(CapabilityACP))
	require.NoError(t, err)
	grouped.Capabilities = selected.Capabilities
	_, err = Compose([]Contribution{{Reference: "selected", Descriptor: grouped}})
	require.ErrorContains(t, err, "different things")
}

func TestACPSurface(t *testing.T) {
	for _, optional := range []bool{false, true} {
		entry := acpEntry("claude", []string{"adapter"})
		entry.Optional = optional
		d := &Descriptor{Capabilities: []Capability{entry}}
		require.Equal(t, SurfaceOf(&Descriptor{}), SurfaceOf(d))
		entry.Config["env"] = map[string]string{"MODE": "strict"}
		entry.Config["command"] = []string{"different"}
		d.Capabilities[0] = entry
		require.Equal(t, SurfaceOf(&Descriptor{}), SurfaceOf(d))
	}
}

func TestACPExamples(t *testing.T) {
	raw, err := os.ReadFile("../examples/claude-acp/claude-acp.yaml")
	require.NoError(t, err)
	d, err := Decode(raw)
	require.NoError(t, err)
	endpoints, err := ACPsOf(d.Capabilities)
	require.NoError(t, err)
	require.Len(t, endpoints, 1)
	require.Equal(t, "claude", endpoints[0].Agent)
	require.Equal(t, 1, ACPProtocolVersion(endpoints[0].ACP))
	for _, c := range d.Capabilities {
		if c.Type == CapabilityACP {
			require.True(t, c.Optional)
		}
	}
	selected, err := SelectCapabilities(t.Context(), d, Supported(CapabilityAgentContext))
	require.NoError(t, err, "a host without ACP keeps the kit usable")
	require.False(t, HasCapability(selected.Capabilities, CapabilityACP))
}

func TestACPSchema(t *testing.T) {
	schema := loadJSON(t, perTypeSchemaPath(CapabilityACP))
	require.Equal(t, false, schema["additionalProperties"])
	require.ElementsMatch(t, []any{"agent", "command"}, schema["required"])
	props := at(t, schema, "properties")
	command := at(t, props, "command")["oneOf"].([]any)
	for _, branch := range command {
		shape := branch.(map[string]any)
		if shape["type"] == "string" {
			require.Equal(t, `\S`, shape["pattern"])
		} else {
			require.Equal(t, float64(1), shape["minItems"])
			executable := shape["items"].([]any)[0].(map[string]any)
			require.Equal(t, `\S`, executable["pattern"])
			require.Equal(t, "string", at(t, shape, "additionalItems")["type"])
		}
	}
	require.Equal(t, envVarName.String(), at(t, props, "env", "propertyNames")["pattern"])
	version := at(t, props, "protocolVersion")
	require.Equal(t, float64(1), version["default"])
	integer := version["anyOf"].([]any)[0].(map[string]any)
	require.Equal(t, "integer", integer["type"])
	require.Equal(t, float64(1), integer["minimum"])
	pattern := regexp.MustCompile(literalPattern(t, at(t, props, "agent")))
	literalAgent := literalOrArgRefBranches(t, at(t, props, "agent"))[0].(map[string]any)
	namespaceOverflow := regexp.MustCompile(at(t, literalAgent, "not")["pattern"].(string))
	for _, agent := range []string{"claude", "codex", "com.docker.kit/claude", "com.example/agent", "deb/agent", "apk/agent", "Claude", "", " claude ", "claude@1.0.0", "com..example/agent", "example/agent", strings.Repeat("a", 65)} {
		require.Equal(t, validCapabilityName(agent), pattern.MatchString(agent) && !namespaceOverflow.MatchString(agent), "agent %q", agent)
	}
	// Valid label lengths alone admit a 255-character namespace. Exercise
	// the total-length boundary with both short and longest agent names.
	for _, length := range []int{253, 254, 255} {
		namespace := strings.Repeat(strings.Repeat("a", 63)+".", 3) + strings.Repeat("b", length-192)
		require.Len(t, namespace, length)
		for _, name := range []string{"a", strings.Repeat("a", 64)} {
			agent := namespace + "/" + name
			want := length <= maxNamespaceLength
			require.Equal(t, want, pattern.MatchString(agent) && !namespaceOverflow.MatchString(agent), "schema agent %q", agent)
			d := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{acpEntry(agent, []string{"adapter"})}}
			_, err := Validate(d)
			if want {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "no domain is longer than 253")
			}
		}
	}
}
