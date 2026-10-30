package spec

import (
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

func commandServer(name string) Capability {
	return mcpServerEntry(map[string]any{"name": name, "transport": "stdio", "command": []any{name + "-mcp"}})
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
		{"name": "docs", "transport": "stdio", "command": []any{"docs-mcp", "--stdio"}},
		{"name": "docs", "transport": "stdio", "command": "exec docs-mcp --stdio"},
		{"name": "docs", "transport": "streamable-http", "port": 8080, "command": []any{"docs-mcp", "serve"}},
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
		{"arg name beside command", `{"name":"${{ kit.args.name }}","transport":"stdio","command":["docs-mcp"]}`, "must be literal"},
		{"empty arguments", `{"name":"docs","transport":"stdio","arguments":[]}`, "must not be empty"},
		{"null arguments", `{"name":"docs","transport":"stdio","arguments":null}`, "must not be null"},
		{"null argument", `{"name":"docs","transport":"stdio","arguments":["--x",null]}`, "must not be null"},
		{"object argument", `{"name":"docs","transport":"stdio","arguments":[{"x":1}]}`, "must be a string"},
		{"string arguments", `{"name":"docs","transport":"stdio","arguments":"--x"}`, "list of strings"},
		{"nul argument", `{"name":"docs","transport":"stdio","arguments":["--x\u0000y"]}`, "NUL"},
		{"empty command", `{"name":"docs","transport":"stdio","command":[]}`, "must name a command"},
		{"blank command", `{"name":"docs","transport":"stdio","command":"  "}`, "must name a command"},
		{"null command", `{"name":"docs","transport":"stdio","command":null}`, "must not be null"},
		{"nul command", `{"name":"docs","transport":"stdio","command":["docs\u0000mcp"]}`, "NUL"},
		{"command and arguments", `{"name":"docs","transport":"stdio","command":["docs-mcp"],"arguments":["--x"]}`, "state the whole argv in command"},
		{"unknown field", `{"name":"docs","transport":"stdio","image":"docs"}`, "unknown field"},
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
      arguments: ["--root=${{ kit.args.root }}", "${{ kit.args.value }}", "--home=${{ kit.env.HOME }}"]
  - type: ` + CapabilityMCPServer + `
    config:
      name: notes
      transport: stdio
      command: ["notes-mcp", "--root=${{ kit.args.root }}"]
`)
	d, err := Decode(raw)
	require.NoError(t, err)
	_, err = ValidateRaw(raw, d)
	require.NoError(t, err)

	// A whole-value reference adopts a number or boolean type at create;
	// the argv entry must still be exactly what the installer supplied.
	// kit.env waits for the final container environment (§6.1).
	for _, value := range []string{"plain", "", "8080", "-1", "1.5", "0.0000001", "9007199254740993", "true", "1.0", "007", "1e5"} {
		effective, err := ExpandCreateArgs(raw, d.Args, map[string]string{"root": "/srv", "value": value})
		require.NoError(t, err, value)
		created, err := Decode(effective)
		require.NoError(t, err, value)
		servers, err := MCPServersOf(created.Capabilities)
		require.NoError(t, err, value)
		require.Equal(t, MCPArguments{"--root=/srv", value, "--home=${{ kit.env.HOME }}"}, servers[0].Server.Arguments, value)

		final, err := ExpandEnvironment(created, map[string]string{"HOME": "/home/agent"})
		require.NoError(t, err, value)
		servers, err = MCPServersOf(final.Capabilities)
		require.NoError(t, err, value)
		require.Len(t, servers, 2, value)
		require.Equal(t, MCPArguments{"--root=/srv", value, "--home=/home/agent"}, servers[0].Server.Arguments, value)
		require.Equal(t, CommandLine{"notes-mcp", "--root=/srv"}, servers[1].Server.Command, value)
	}
}

func TestMCPServerRole(t *testing.T) {
	workload := func(capabilities ...Capability) *Descriptor {
		return &Descriptor{SchemaVersion: SchemaVersion, Kind: KindWorkload, Capabilities: capabilities}
	}
	mixin := func(capabilities ...Capability) *Descriptor {
		return &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: capabilities}
	}

	// A server is a sandbox like any other: everything that bounds or
	// extends one travels beside it, and so do servers of its own.
	_, err := Validate(workload(
		stdioServer(),
		commandServer("notes"),
		Capability{Type: CapabilityNetworkPolicy, Config: map[string]any{"runtime": map[string]any{"allow": []any{"api.example.com:443"}}}},
		Capability{Type: CapabilityCredential, Optional: true, Config: map[string]any{"service": "example", "phase": "runtime",
			"apiKey": map[string]any{"name": "EXAMPLE_TOKEN", "proxyManaged": true, "inject": []any{map[string]any{"domain": "api.example.com", "header": "Authorization", "format": "Bearer %s"}}}}},
		Capability{Type: CapabilityResources, Config: map[string]any{"memory": "512m"}},
		Capability{Type: CapabilityLongRunning},
		Capability{Type: CapabilityPort, Config: map[string]any{"container": 8080}},
		Capability{Type: "com.example/telemetry@1", Optional: true},
	))
	require.NoError(t, err)

	// An agent Kit can carry servers of its own, optional and grouped
	// ones included, as long as each says how it starts.
	optional := commandServer("search")
	optional.Optional = true
	_, err = Validate(workload(
		Capability{Type: CapabilitySbx},
		commandServer("notes"),
		optional,
		Capability{Group: &CapabilityGroup{Optional: true, Capabilities: []Capability{commandServer("files")}}},
	))
	require.NoError(t, err)
	_, err = Validate(workload(Capability{Type: CapabilitySbx}, stdioServer()))
	require.ErrorContains(t, err, "that launch is the agent")

	_, err = Validate(mixin(commandServer("notes")))
	require.NoError(t, err)
	_, err = Validate(mixin(stdioServer()))
	require.ErrorContains(t, err, "a mixin has none")

	launch := func(name string) Capability {
		return mcpServerEntry(map[string]any{"name": name, "transport": "stdio"})
	}
	_, err = Validate(workload(launch("docs"), launch("other")))
	require.ErrorContains(t, err, "a sandbox has one launch")
	// Group members count: selecting both would leave two servers
	// claiming the one launch.
	_, err = Validate(workload(launch("docs"), Capability{Group: &CapabilityGroup{Optional: true, Capabilities: []Capability{launch("other")}}}))
	require.ErrorContains(t, err, "a sandbox has one launch")

	_, err = Validate(workload(stdioServer(), commandServer("docs")))
	require.ErrorContains(t, err, "already declared")
	_, err = Validate(workload(commandServer("docs"), Capability{Group: &CapabilityGroup{Optional: true, Capabilities: []Capability{commandServer("docs")}}}))
	require.ErrorContains(t, err, "already declared")
}

func TestMCPServerAccessor(t *testing.T) {
	servers, err := MCPServersOf([]Capability{{Type: CapabilityResources, Config: map[string]any{"memory": "1g"}}})
	require.NoError(t, err)
	require.Empty(t, servers)

	optional := commandServer("notes")
	optional.Optional = true
	optional.Name = "Notes"
	servers, err = MCPServersOf([]Capability{
		mcpServerEntry(map[string]any{"name": "docs", "transport": "streamable-http", "port": 8080}),
		optional,
	})
	require.NoError(t, err)
	require.Len(t, servers, 2)
	docs := servers[0].Server
	require.Equal(t, "docs", docs.Name)
	require.Equal(t, 8080, *docs.Port)
	require.Equal(t, DefaultMCPPath, MCPServerPath(docs))
	require.True(t, MCPServerRunsLaunch(docs))
	require.Nil(t, docs.Arguments)
	require.False(t, servers[0].Optional)
	require.Equal(t, "notes", servers[1].Server.Name)
	require.Equal(t, "Notes", servers[1].Name)
	require.True(t, servers[1].Optional)
	require.False(t, MCPServerRunsLaunch(servers[1].Server))

	servers, err = MCPServersOf([]Capability{
		mcpServerEntry(map[string]any{"name": "docs", "transport": "stdio", "arguments": []any{"serve", "--stdio"}}),
		mcpServerEntry(map[string]any{"name": "sh", "transport": "stdio", "command": "exec docs-mcp"}),
	})
	require.NoError(t, err)
	require.Equal(t, MCPArguments{"serve", "--stdio"}, servers[0].Server.Arguments)
	require.Equal(t, CommandLine{"sh", "-c", "exec docs-mcp"}, servers[1].Server.Command)

	_, err = MCPServersOf([]Capability{{Group: &CapabilityGroup{Capabilities: []Capability{stdioServer()}}}})
	require.ErrorContains(t, err, "select groups first")
}

func TestMCPServerComposition(t *testing.T) {
	contribution := func(reference, kind string, capabilities ...Capability) Contribution {
		return Contribution{Reference: reference, Descriptor: &Descriptor{SchemaVersion: SchemaVersion, Kind: kind, Capabilities: capabilities}}
	}

	// A mixin brings a server into whatever workload it joins, an agent
	// included; the workload's own launch entry stays its own.
	server := contribution("server", KindWorkload, stdioServer())
	notes := contribution("notes", KindMixin, commandServer("notes"))
	published := mergeOK(t, server, notes).Descriptor
	_, err := Validate(published)
	require.NoError(t, err)
	servers, err := MCPServersOf(published.Capabilities)
	require.NoError(t, err)
	require.Len(t, servers, 2)
	require.True(t, MCPServerRunsLaunch(servers[0].Server))
	require.Equal(t, "notes", servers[1].Server.Name)

	agent := contribution("agent", KindWorkload, Capability{Type: CapabilitySbx})
	published = mergeOK(t, agent, notes).Descriptor
	_, err = Validate(published)
	require.NoError(t, err)

	// Entries union on name. A restatement that omits the HTTP path asks
	// for the endpoint an explicit /mcp names, so the two collapse; a
	// different path or a different start is a second author.
	http := func(reference string, config map[string]any) Contribution {
		return contribution(reference, KindMixin, mcpServerEntry(config))
	}
	base := map[string]any{"name": "docs", "transport": "streamable-http", "port": 8080, "command": []any{"docs-mcp"}}
	explicit := map[string]any{"name": "docs", "transport": "streamable-http", "port": 8080, "path": "/mcp", "command": []any{"docs-mcp"}}
	merged := mergeOK(t, http("base", base), http("explicit", explicit)).Descriptor
	servers, err = MCPServersOf(merged.Capabilities)
	require.NoError(t, err)
	require.Len(t, servers, 1)
	for _, other := range []map[string]any{
		{"name": "docs", "transport": "streamable-http", "port": 8080, "path": "/v2", "command": []any{"docs-mcp"}},
		{"name": "docs", "transport": "streamable-http", "port": 8080, "command": []any{"docs-mcp", "--x"}},
		{"name": "docs", "transport": "stdio", "command": []any{"docs-mcp"}},
	} {
		_, err = Merge([]Contribution{http("base", base), http("other", other)}, MergeOptions{})
		require.ErrorContains(t, err, "ask for different things", other)
	}
}

func TestMCPServerSurface(t *testing.T) {
	d := &Descriptor{Capabilities: []Capability{stdioServer()}}
	require.Equal(t, SurfaceOf(&Descriptor{}), SurfaceOf(d))
	d.Capabilities[0] = mcpServerEntry(map[string]any{"name": "other", "transport": "streamable-http", "port": 9000, "command": []any{"other-mcp"}})
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
	// command is the same command line acp@1 states.
	acp := loadJSON(t, perTypeSchemaPath(CapabilityACP))
	require.Equal(t, at(t, acp, "properties", "command")["oneOf"], at(t, props, "command")["oneOf"])

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

	// Presence rules live in allOf, mirroring validateMCPServerAuthored
	// and validateMCPServer.
	rules := schema["allOf"].([]any)
	require.Len(t, rules, 3)
	stdio := rules[0].(map[string]any)
	require.Equal(t, MCPTransportStdio, at(t, stdio, "if", "properties", "transport")["const"])
	http := rules[1].(map[string]any)
	require.Equal(t, MCPTransportStreamableHTTP, at(t, http, "if", "properties", "transport")["const"])
	require.Equal(t, []any{"port"}, at(t, http, "then")["required"])
	command := rules[2].(map[string]any)
	require.Equal(t, []any{"command"}, at(t, command, "if")["required"])
	require.Equal(t, []any{"arguments"}, at(t, command, "then", "not")["required"])
}
