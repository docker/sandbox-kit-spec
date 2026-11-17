package spec

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func mcpEntry(config string) string {
	return "  - type: " + CapabilityMCPRegistration + "\n    config: " + config + "\n"
}

func TestMCPRegistrationValidation(t *testing.T) {
	valid := []string{
		`{name: todos, url: "https://api.example.com/mcp"}`,
		`{name: todos, url: "https://api.example.com"}`,
		`{name: "${{ kit.args.name }}", url: "${{ kit.args.url }}", access: "${{ kit.args.access }}"}`,
	}
	for _, config := range valid {
		for _, kind := range []string{KindWorkload, KindMixin} {
			_, err := Validate(crossAppAccessKit(t, kind, mcpEntry(config)))
			require.NoError(t, err, "%s: %s", kind, config)
		}
	}

	invalid := []struct{ config, want string }{
		{`{url: "https://api.example.com/mcp"}`, "name is required"},
		{`{name: "-bad", url: "https://api.example.com/mcp"}`, "must match"},
		{`{name: todos}`, "url is required"},
		{`{name: todos, url: "http://api.example.com/mcp"}`, "url must be an https URL"},
		{`{name: todos, url: "https://api.example.com/mcp?key=a"}`, "url must be an https URL"},
		{`{name: todos, url: "https://api.example.com/mcp#x"}`, "url must be an https URL"},
		{`{name: todos, url: "https://user@api.example.com/mcp"}`, "url must be an https URL"},
		{`{name: todos, url: "api.example.com/mcp"}`, "url must be an https URL"},
		{`{name: todos, url: "https://api.example.com/mcp", access: "two words"}`, "access \"two words\" must match"},
		{`{name: todos, url: "https://other.example.com/mcp"}`, "not in the network policy's runtime allow list"},
		{`{name: todos, url: null}`, "is null"},
		{`{name: todos, url: "https://api.example.com/mcp", clientId: abc}`, "unknown field"},
		{`{name: todos, url: "https://api.example.com/mcp", access: todos}`, "names no " + CapabilityCrossAppAccess + " entry"},
	}
	for _, tt := range invalid {
		_, err := Validate(crossAppAccessKit(t, KindWorkload, mcpEntry(tt.config)))
		require.ErrorContains(t, err, tt.want, tt.config)
	}
}

func TestMCPRegistrationAccessServesURL(t *testing.T) {
	server := mcpEntry(`{name: todos, url: "https://api.example.com/mcp", access: todos}`)
	served := []string{
		`{name: todos, issuer: "https://auth.example.com", inject: [{domain: api.example.com}]}`,
		`{name: todos, issuer: "https://auth.example.com", inject: [{domain: "api.example.com:443", header: authorization, format: "Bearer %s"}]}`,
		`{name: todos, issuer: "https://auth.example.com", resource: "https://api.example.com/mcp", inject: [{domain: api.example.com}]}`,
		`{name: todos, issuer: "https://auth.example.com", resource: "https://API.example.com:443/mcp/", inject: [{domain: api.example.com}]}`,
	}
	for _, grant := range served {
		_, err := Validate(crossAppAccessKit(t, KindMixin, crossAppEntry(grant)+server))
		require.NoError(t, err, grant)
	}

	refused := []string{
		`{name: todos, issuer: "https://auth.example.com", inject: [{domain: files.example.com}]}`,
		`{name: todos, issuer: "https://auth.example.com", inject: [{domain: api.example.com, header: X-Token}]}`,
		`{name: todos, issuer: "https://auth.example.com", inject: [{domain: api.example.com, format: "%s"}]}`,
		`{name: todos, issuer: "https://auth.example.com", resource: "https://api.example.com", inject: [{domain: api.example.com}]}`,
		`{name: todos, issuer: "https://auth.example.com", resource: "https://files.example.com/mcp", inject: [{domain: api.example.com}]}`,
		`{name: todos, issuer: "https://auth.example.com", resource: "urn:example:todos", inject: [{domain: api.example.com}]}`,
	}
	for _, grant := range refused {
		_, err := Validate(crossAppAccessKit(t, KindMixin, crossAppEntry(grant)+server))
		require.ErrorContains(t, err, `cross-app access "todos" does not serve https://api.example.com/mcp`, grant)
	}

	_, err := Validate(crossAppAccessKit(t, KindMixin,
		crossAppEntry(`{name: todos, issuer: "https://auth.example.com", inject: [{domain: api.example.com}]}`)+
			mcpEntry(`{name: todos, url: "https://api.example.com:8443/mcp", access: todos}`)))
	require.ErrorContains(t, err, "does not serve", "a token is presented on the default port only")

	_, err = Validate(crossAppAccessKit(t, KindMixin,
		crossAppEntry(`{name: todos, issuer: "auth.example.com", inject: [{domain: api.example.com}]}`)+server))
	require.ErrorContains(t, err, "issuer must be an https URL")
	require.NotContains(t, err.Error(), "does not serve", "a refused grant reports its own errors once")
	require.NotContains(t, err.Error(), "names no", "a refused grant is still the one named")
}

func TestMCPRegistrationNamesAreExclusive(t *testing.T) {
	todos := mcpEntry(`{name: todos, url: "https://api.example.com/mcp"}`)

	_, err := Validate(crossAppAccessKit(t, KindWorkload, todos+mcpEntry(`{name: todos, url: "https://files.example.com/mcp"}`)))
	require.ErrorContains(t, err, `MCP registration "todos" already declared`)

	_, err = Validate(crossAppAccessKit(t, KindWorkload, todos+mcpEntry(`{name: files, url: "https://files.example.com/mcp"}`)))
	require.NoError(t, err, "two servers with different names coexist")
}

func TestMCPRegistrationsOf(t *testing.T) {
	d := crossAppAccessKit(t, KindMixin,
		crossAppEntry(`{name: todos, issuer: "https://auth.example.com", inject: [{domain: api.example.com}]}`)+
			mcpEntry(`{name: todos, url: "https://api.example.com/mcp", access: todos}`)+
			mcpEntry(`{name: files, url: "https://files.example.com/mcp"}`))
	_, err := Validate(d)
	require.NoError(t, err)
	servers, err := MCPRegistrationsOf(d.Capabilities)
	require.NoError(t, err)
	require.Equal(t, []MCPRegistration{
		{Name: "todos", URL: "https://api.example.com/mcp", Access: "todos"},
		{Name: "files", URL: "https://files.example.com/mcp"},
	}, servers)
}

func TestMCPRegistrationMergeKeysOnName(t *testing.T) {
	entry := func(name, endpoint string, optional bool) Capability {
		return Capability{Type: CapabilityMCPRegistration, Optional: optional, Config: map[string]any{"name": name, "url": endpoint}}
	}
	contribute := func(entries ...Capability) []Contribution {
		out := make([]Contribution, 0, len(entries))
		for i, e := range entries {
			out = append(out, Contribution{Reference: string(rune('a' + i)), Descriptor: &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{e}}})
		}
		return out
	}

	out, _, err := mergeCapabilities(contribute(
		entry("todos", "https://api.example.com/mcp", false),
		entry("todos", "https://api.example.com/mcp", true),
		entry("files", "https://files.example.com/mcp", false),
	))
	require.NoError(t, err)
	var names []string
	for _, c := range out {
		var m MCPRegistration
		require.NoError(t, DecodeCapabilityConfig(c, &m))
		names = append(names, m.Name)
		if m.Name == "todos" {
			require.False(t, c.Optional, "required wins over optional")
		}
	}
	require.Equal(t, []string{"todos", "files"}, names)

	_, _, err = mergeCapabilities(contribute(
		entry("todos", "https://api.example.com/mcp", false),
		entry("todos", "https://files.example.com/mcp", false),
	))
	require.ErrorContains(t, err, "ask for different things")
}
