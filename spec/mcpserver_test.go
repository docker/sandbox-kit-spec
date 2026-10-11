package spec

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func mcpServerEntry(config map[string]any) Capability {
	return Capability{Type: CapabilityMCPServer, Config: config}
}

func stdioServer() Capability {
	return mcpServerEntry(map[string]any{"name": "docs", "transport": "stdio"})
}

func TestMCPServerValidation(t *testing.T) {
	for _, config := range []map[string]any{
		{"name": "docs", "transport": "stdio"},
		{"name": "a", "transport": "stdio"},
		{"name": "docs-2", "transport": "streamable-http", "port": 8080},
		{"name": "docs", "transport": "streamable-http", "port": 1, "path": "/"},
		{"name": "docs", "transport": "streamable-http", "port": 65535, "path": "/v1/mcp;x=1"},
		{"name": "docs", "transport": "streamable-http", "port": 8080, "path": "/m%20cp/%e2%9c%93"},
		{"name": "docs", "transport": "stdio", "arguments": []any{"--read-only", ""}},
		{"name": "docs", "transport": "streamable-http", "port": 8080, "arguments": []any{"serve", "--port", "8080"}},
	} {
		d := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindWorkload, Capabilities: []Capability{mcpServerEntry(config)}}
		_, err := Validate(d)
		require.NoError(t, err, "%v", config)
	}
	for _, tc := range []struct {
		name, config, want string
	}{
		{"missing config", `null`, "name is required"},
		{"missing name", `{"transport":"stdio"}`, "name is required"},
		{"missing transport", `{"name":"docs"}`, "transport is required"},
		{"null name", `{"name":null,"transport":"stdio"}`, "must not be null"},
		{"uppercase name", `{"name":"Docs","transport":"stdio"}`, "DNS label"},
		{"dotted name", `{"name":"docs.example","transport":"stdio"}`, "DNS label"},
		{"edge hyphen", `{"name":"docs-","transport":"stdio"}`, "DNS label"},
		{"long name", `{"name":"` + strings.Repeat("a", 64) + `","transport":"stdio"}`, "DNS label"},
		{"empty name", `{"name":"","transport":"stdio"}`, "DNS label"},
		{"sse transport", `{"name":"docs","transport":"sse"}`, "transport must be"},
		{"stdio port", `{"name":"docs","transport":"stdio","port":8080}`, "applies only to streamable-http"},
		{"stdio path", `{"name":"docs","transport":"stdio","path":"/mcp"}`, "applies only to streamable-http"},
		{"http without port", `{"name":"docs","transport":"streamable-http"}`, "port is required"},
		{"null port", `{"name":"docs","transport":"streamable-http","port":null}`, "must not be null"},
		{"zero port", `{"name":"docs","transport":"streamable-http","port":0}`, "1-65535"},
		{"high port", `{"name":"docs","transport":"streamable-http","port":65536}`, "1-65535"},
		{"string port", `{"name":"docs","transport":"streamable-http","port":"8080"}`, "cannot unmarshal"},
		{"relative path", `{"name":"docs","transport":"streamable-http","port":8080,"path":"mcp"}`, "absolute URL path"},
		{"query path", `{"name":"docs","transport":"streamable-http","port":8080,"path":"/mcp?x=1"}`, "absolute URL path"},
		{"fragment path", `{"name":"docs","transport":"streamable-http","port":8080,"path":"/mcp#x"}`, "absolute URL path"},
		{"space path", `{"name":"docs","transport":"streamable-http","port":8080,"path":"/m cp"}`, "absolute URL path"},
		{"empty path", `{"name":"docs","transport":"streamable-http","port":8080,"path":""}`, "absolute URL path"},
		{"bad escape", `{"name":"docs","transport":"streamable-http","port":8080,"path":"/mcp%ZZ"}`, "absolute URL path"},
		{"short escape", `{"name":"docs","transport":"streamable-http","port":8080,"path":"/mcp%4"}`, "absolute URL path"},
		{"arg name", `{"name":"${{ kit.args.name }}","transport":"stdio"}`, "must be literal"},
		{"arg port", `{"name":"docs","transport":"streamable-http","port":"${{ kit.args.port }}"}`, "must be literal"},
		{"env path", `{"name":"docs","transport":"streamable-http","port":8080,"path":"${{ kit.env.MCP_PATH }}"}`, "must be literal"},
		{"arg name beside arguments", `{"name":"${{ kit.args.name }}","transport":"stdio","arguments":["--x"]}`, "must be literal"},
		{"env argument", `{"name":"docs","transport":"stdio","arguments":["--home=${{ kit.env.HOME }}"]}`, "not kit.env"},
		{"empty arguments", `{"name":"docs","transport":"stdio","arguments":[]}`, "must not be empty"},
		{"null arguments", `{"name":"docs","transport":"stdio","arguments":null}`, "must not be null"},
		{"null argument", `{"name":"docs","transport":"stdio","arguments":["--x",null]}`, "must not be null"},
		{"object argument", `{"name":"docs","transport":"stdio","arguments":[{"x":1}]}`, "must be a string"},
		{"string arguments", `{"name":"docs","transport":"stdio","arguments":"--x"}`, "list of strings"},
		{"nul argument", `{"name":"docs","transport":"stdio","arguments":["--x\u0000y"]}`, "NUL"},
		{"unknown field", `{"name":"docs","transport":"stdio","command":["server"]}`, "unknown field"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(`{"schemaVersion":"3","kind":"workload","args":{"name":{"default":"docs"},"port":{"default":"8080"}},"capabilities":[{"type":"` + CapabilityMCPServer + `","config":` + tc.config + `}]}`)
			d, err := Decode(raw)
			require.NoError(t, err)
			_, err = ValidateRaw(raw, d)
			require.ErrorContains(t, err, tc.want)
		})
	}
}

// TestMCPServerArguments follows a parameterized argv from the published
// descriptor through create-phase expansion to the argv a host launches.
func TestMCPServerArguments(t *testing.T) {
	raw := []byte(`schemaVersion: "3"
kind: workload
args:
  root: {default: /data}
  value: {required: true}
capabilities:
  - type: ` + CapabilityMCPServer + `
    config:
      name: files
      transport: stdio
      arguments: ["--root=${{ kit.args.root }}", "${{ kit.args.value }}"]
`)
	d, err := Decode(raw)
	require.NoError(t, err)
	_, err = ValidateRaw(raw, d)
	require.NoError(t, err)

	// A whole-value reference adopts a number or boolean type at create;
	// the argv entry must still be exactly what the installer supplied.
	for _, value := range []string{"plain", "", "8080", "-1", "1.5", "0.0000001", "9007199254740993", "true", "1.0", "007", "1e5"} {
		effective, err := ExpandCreateArgs(raw, d.Args, map[string]string{"root": "/srv", "value": value})
		require.NoError(t, err, value)
		expanded, err := Decode(effective)
		require.NoError(t, err, value)
		_, err = ValidateEffective(effective, expanded)
		require.NoError(t, err, value)
		m, err := MCPServerOf(expanded.Capabilities)
		require.NoError(t, err, value)
		require.Equal(t, MCPArguments{"--root=/srv", value}, m.Arguments, value)
	}
}

func TestMCPServerRole(t *testing.T) {
	workload := func(capabilities ...Capability) *Descriptor {
		return &Descriptor{SchemaVersion: SchemaVersion, Kind: KindWorkload, Capabilities: capabilities}
	}
	// The types that bound the server's own container, plus third-party
	// types the grammar cannot judge, travel beside the declaration.
	_, err := Validate(workload(
		stdioServer(),
		Capability{Type: CapabilityNetworkPolicy, Config: map[string]any{"runtime": map[string]any{"allow": []any{"api.example.com:443"}}}},
		Capability{Type: CapabilityCredential, Optional: true, Config: map[string]any{"service": "example", "phase": "runtime",
			"apiKey": map[string]any{"name": "EXAMPLE_TOKEN", "proxyManaged": true, "inject": []any{map[string]any{"domain": "api.example.com", "header": "Authorization", "format": "Bearer %s"}}}}},
		Capability{Type: CapabilityResources, Config: map[string]any{"memory": "512m"}},
		Capability{Type: "com.example/telemetry@1", Optional: true},
	))
	require.NoError(t, err)

	mixin := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{stdioServer()}}
	_, err = Validate(mixin)
	require.ErrorContains(t, err, "workload-only")

	optional := stdioServer()
	optional.Optional = true
	_, err = Validate(workload(optional))
	require.ErrorContains(t, err, "must not be optional")

	_, err = Validate(workload(Capability{Group: &CapabilityGroup{Optional: true, Capabilities: []Capability{stdioServer()}}}))
	require.ErrorContains(t, err, "must not be a group member")

	_, err = Validate(workload(stdioServer(), stdioServer()))
	require.ErrorContains(t, err, "already declared")

	for _, sandboxOnly := range []Capability{
		{Type: CapabilitySbx},
		{Type: CapabilityLongRunning},
		{Type: CapabilityPrivileged},
		{Type: CapabilityLifecycle, Config: map[string]any{"startup": []any{map[string]any{"command": "true"}}}},
		{Type: CapabilityAgentSkills, Config: map[string]any{"path": "/home/agent/.skills"}},
		{Type: CapabilityACP, Config: map[string]any{"agent": "docs", "command": []any{"docs"}}},
		{Type: CapabilityPort, Config: map[string]any{"container": 8080}},
	} {
		_, err := Validate(workload(stdioServer(), sandboxOnly))
		require.ErrorContains(t, err, "does not apply to a Kit declaring", sandboxOnly.Type)
		// The rule follows the declaration into groups, where a host
		// selecting the member would otherwise build what it forbids.
		_, err = Validate(workload(stdioServer(), Capability{Group: &CapabilityGroup{Optional: true, Capabilities: []Capability{sandboxOnly}}}))
		require.ErrorContains(t, err, "does not apply to a Kit declaring", sandboxOnly.Type)
	}
	// Without the declaration none of this applies.
	_, err = Validate(workload(Capability{Type: CapabilitySbx}))
	require.NoError(t, err)
}

func TestMCPServerAccessor(t *testing.T) {
	m, err := MCPServerOf([]Capability{{Type: CapabilityResources, Config: map[string]any{"memory": "1g"}}})
	require.NoError(t, err)
	require.Nil(t, m)

	m, err = MCPServerOf([]Capability{mcpServerEntry(map[string]any{"name": "docs", "transport": "streamable-http", "port": 8080})})
	require.NoError(t, err)
	require.Equal(t, "docs", m.Name)
	require.Equal(t, 8080, *m.Port)
	require.Equal(t, DefaultMCPPath, MCPServerPath(*m))
	require.Nil(t, m.Arguments)

	m, err = MCPServerOf([]Capability{mcpServerEntry(map[string]any{"name": "docs", "transport": "stdio", "arguments": []any{"serve", "--stdio"}})})
	require.NoError(t, err)
	require.Equal(t, MCPArguments{"serve", "--stdio"}, m.Arguments)

	_, err = MCPServerOf([]Capability{{Group: &CapabilityGroup{Capabilities: []Capability{stdioServer()}}}})
	require.ErrorContains(t, err, "select groups first")
}

func TestMCPServerComposition(t *testing.T) {
	server := Contribution{Reference: "server", Descriptor: &Descriptor{
		SchemaVersion: SchemaVersion, Kind: KindWorkload, Capabilities: []Capability{stdioServer()},
	}}
	content := Contribution{Reference: "content", Descriptor: &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin}}
	published := mergeOK(t, server, content).Descriptor
	require.Equal(t, KindWorkload, published.Kind)
	_, err := Validate(published)
	require.NoError(t, err)
	m, err := MCPServerOf(published.Capabilities)
	require.NoError(t, err)
	require.Equal(t, "docs", m.Name)

	// A restatement that omits the HTTP path asks for the endpoint an
	// explicit /mcp names, so the two collapse; a different path or a
	// different launch is a second author.
	http := func(config map[string]any) Contribution {
		return Contribution{Reference: fmt.Sprint(config), Descriptor: &Descriptor{
			SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{mcpServerEntry(config)},
		}}
	}
	base := map[string]any{"name": "docs", "transport": "streamable-http", "port": 8080}
	explicit := map[string]any{"name": "docs", "transport": "streamable-http", "port": 8080, "path": "/mcp"}
	_, err = Merge([]Contribution{http(base), http(explicit)}, MergeOptions{})
	require.NoError(t, err)
	for _, other := range []map[string]any{
		{"name": "docs", "transport": "streamable-http", "port": 8080, "path": "/v2"},
		{"name": "docs", "transport": "streamable-http", "port": 8080, "arguments": []any{"--x"}},
	} {
		_, err = Merge([]Contribution{http(base), http(other)}, MergeOptions{})
		require.ErrorContains(t, err, "both declare mcp-server", other)
	}

	// A set cannot carry a server onto an agent: the agent is a second
	// workload, and the merged declarations would be refused besides.
	agent := Contribution{Reference: "agent", Descriptor: &Descriptor{
		SchemaVersion: SchemaVersion, Kind: KindWorkload, Capabilities: []Capability{{Type: CapabilitySbx}},
	}}
	_, err = Merge([]Contribution{server, agent}, MergeOptions{})
	require.Error(t, err)

	// A mixin that brings a sandbox-only type is refused on the merged
	// descriptor, where it would otherwise become the server's.
	sandboxMixin := Contribution{Reference: "sandbox-mixin", Descriptor: &Descriptor{
		SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{{Type: CapabilityLongRunning}},
	}}
	merged, err := Merge([]Contribution{server, sandboxMixin}, MergeOptions{})
	if err == nil {
		_, err = Validate(merged.Descriptor)
	}
	require.ErrorContains(t, err, "does not apply to a Kit declaring")
}

func TestMCPServerSurface(t *testing.T) {
	d := &Descriptor{Capabilities: []Capability{stdioServer()}}
	require.Equal(t, SurfaceOf(&Descriptor{}), SurfaceOf(d))
	d.Capabilities[0] = mcpServerEntry(map[string]any{"name": "other", "transport": "streamable-http", "port": 9000})
	require.Equal(t, SurfaceOf(&Descriptor{}), SurfaceOf(d))
}

func TestMCPServerSchema(t *testing.T) {
	schema := loadJSON(t, perTypeSchemaPath(CapabilityMCPServer))
	require.Equal(t, false, schema["additionalProperties"])
	require.ElementsMatch(t, []any{"name", "transport"}, schema["required"])
	props := at(t, schema, "properties")
	require.Equal(t, mcpServerName.String(), at(t, props, "name")["pattern"])
	require.Equal(t, mcpServerPath.String(), at(t, props, "path")["pattern"])
	require.Equal(t, DefaultMCPPath, at(t, props, "path")["default"])
	transports := []string{}
	for _, v := range at(t, props, "transport")["enum"].([]any) {
		transports = append(transports, v.(string))
	}
	require.Equal(t, MCPTransports(), transports)
	port := at(t, props, "port")
	require.Equal(t, "integer", port["type"])
	require.Equal(t, float64(1), port["minimum"])
	require.Equal(t, float64(65535), port["maximum"])
	arguments := at(t, props, "arguments")
	require.Equal(t, "array", arguments["type"])
	require.Equal(t, float64(1), arguments["minItems"])
	require.Equal(t, "string", at(t, arguments, "items")["type"])

	// The schema patterns must judge exactly what validation does; Go's
	// RE2 and the schema's ECMA dialect agree on these character classes.
	name := regexp.MustCompile(at(t, props, "name")["pattern"].(string))
	for _, v := range []string{"docs", "a", "docs-2", "Docs", "docs.x", "-docs", "docs-", ""} {
		require.Equal(t, mcpServerName.MatchString(v), name.MatchString(v), v)
	}
	path := regexp.MustCompile(at(t, props, "path")["pattern"].(string))
	for _, v := range []string{"/", "/mcp", "/v1/mcp;x=1", "/m%20cp", "/%e2%9c%93", "/mcp%ZZ", "/mcp%4", "/mcp%", "mcp", "/mcp?x", "/m cp"} {
		require.Equal(t, mcpServerPath.MatchString(v), path.MatchString(v), v)
	}

	// Transport-dependent presence rules live in allOf, mirroring
	// validateMCPServerAuthored and validateMCPServer.
	rules := schema["allOf"].([]any)
	require.Len(t, rules, 2)
	stdio := rules[0].(map[string]any)
	require.Equal(t, MCPTransportStdio, at(t, stdio, "if", "properties", "transport")["const"])
	http := rules[1].(map[string]any)
	require.Equal(t, MCPTransportStreamableHTTP, at(t, http, "if", "properties", "transport")["const"])
	require.Equal(t, []any{"port"}, at(t, http, "then")["required"])
}
