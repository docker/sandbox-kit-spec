package spec

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const crossAppAccessAllow = "  - type: com.docker.sandbox/network-policy@2\n    config: {runtime: {allow: [{hosts: [api.example.com, files.example.com]}]}}\n"

func crossAppAccessKit(t *testing.T, kind, entries string) *Descriptor {
	t.Helper()
	return mustDecode(t, "schemaVersion: \"3\"\nkind: "+kind+"\nversion: \"1.0.0\"\ncapabilities:\n"+crossAppAccessAllow+entries)
}

func crossAppEntry(config string) string {
	return "  - type: " + CapabilityCrossAppAccess + "\n    config: " + config + "\n"
}

func TestCrossAppAccessValidation(t *testing.T) {
	valid := []string{
		`{name: todos, issuer: "https://auth.example.com", inject: [{domain: api.example.com}]}`,
		`{name: todos, issuer: "https://auth.example.com/tenant/a", resource: "https://api.example.com", scopes: [todos.read, "todos:write"], inject: [{domain: api.example.com, header: X-Token, format: "%s"}]}`,
		`{name: todos, issuer: "${{ kit.args.issuer }}", resource: "${{ kit.args.resource }}", scopes: ["${{ kit.args.scope }}"], inject: [{domain: api.example.com}]}`,
		`{name: todos, issuer: "https://auth.example.com", resource: "urn:example:todos", inject: [{domain: api.example.com}]}`,
	}
	for _, config := range valid {
		for _, kind := range []string{KindWorkload, KindMixin} {
			_, err := Validate(crossAppAccessKit(t, kind, crossAppEntry(config)))
			require.NoError(t, err, "%s: %s", kind, config)
		}
	}

	invalid := []struct{ config, want string }{
		{`{issuer: "https://auth.example.com", inject: [{domain: api.example.com}]}`, "name is required"},
		{`{name: "-bad", issuer: "https://auth.example.com", inject: [{domain: api.example.com}]}`, "must match"},
		{`{name: todos, inject: [{domain: api.example.com}]}`, "issuer is required"},
		{`{name: todos, issuer: "http://auth.example.com", inject: [{domain: api.example.com}]}`, "issuer must be an https URL"},
		{`{name: todos, issuer: "https://auth.example.com?tenant=a", inject: [{domain: api.example.com}]}`, "issuer must be an https URL"},
		{`{name: todos, issuer: "https://auth.example.com#x", inject: [{domain: api.example.com}]}`, "issuer must be an https URL"},
		{`{name: todos, issuer: "https://user@auth.example.com", inject: [{domain: api.example.com}]}`, "issuer must be an https URL"},
		{`{name: todos, issuer: "auth.example.com", inject: [{domain: api.example.com}]}`, "issuer must be an https URL"},
		{`{name: todos, issuer: "https://auth.example.com", resource: "api.example.com", inject: [{domain: api.example.com}]}`, "resource must be an absolute URI"},
		{`{name: todos, issuer: "https://auth.example.com", resource: "https://api.example.com#part", inject: [{domain: api.example.com}]}`, "resource must be an absolute URI"},
		{`{name: todos, issuer: "https://auth.example.com", scopes: ["two words"], inject: [{domain: api.example.com}]}`, "one scope token"},
		{`{name: todos, issuer: "https://auth.example.com", scopes: [read, read], inject: [{domain: api.example.com}]}`, "listed twice"},
		{`{name: todos, issuer: "https://auth.example.com"}`, "at least one inject rule"},
		{`{name: todos, issuer: "https://auth.example.com", inject: [{header: X}]}`, "domain is required"},
		{`{name: todos, issuer: "https://auth.example.com", inject: [{domain: api.example.com, scheme: basic}]}`, "scheme and username do not apply"},
		{`{name: todos, issuer: "https://auth.example.com", inject: [{domain: api.example.com, format: "Bearer"}]}`, "exactly one %s"},
		{`{name: todos, issuer: "https://auth.example.com", inject: [{domain: other.example.com}]}`, "not in the network policy's runtime allow list"},
		{`{name: todos, issuer: null, inject: [{domain: api.example.com}]}`, "is null"},
		{`{name: todos, issuer: "https://auth.example.com", inject: [{domain: api.example.com}], client: abc}`, "unknown field"},
	}
	for _, tt := range invalid {
		_, err := Validate(crossAppAccessKit(t, KindWorkload, crossAppEntry(tt.config)))
		require.ErrorContains(t, err, tt.want, tt.config)
	}
}

func TestCrossAppAccessNamesAndDomainsAreExclusive(t *testing.T) {
	todos := crossAppEntry(`{name: todos, issuer: "https://auth.example.com", inject: [{domain: api.example.com}]}`)

	_, err := Validate(crossAppAccessKit(t, KindWorkload, todos+
		crossAppEntry(`{name: todos, issuer: "https://other.example.com", inject: [{domain: files.example.com}]}`)))
	require.ErrorContains(t, err, `cross-app access "todos" already declared`)

	_, err = Validate(crossAppAccessKit(t, KindWorkload, todos+
		crossAppEntry(`{name: files, issuer: "https://auth.example.com", inject: [{domain: files.example.com}]}`)))
	require.NoError(t, err, "two grants with different names and domains coexist")

	_, err = Validate(crossAppAccessKit(t, KindWorkload, todos+
		crossAppEntry(`{name: files, issuer: "https://auth.example.com", inject: [{domain: API.example.com}]}`)))
	require.ErrorContains(t, err, "already receives cross-app access todos")

	_, err = Validate(crossAppAccessKit(t, KindWorkload, todos+
		tokenEntry(`{name: backend, audience: "https://api.example.com", inject: [{domain: api.example.com}]}`)))
	require.ErrorContains(t, err, "already receives workload token backend")
}

func TestCrossAppAccessesOfAndDefaults(t *testing.T) {
	d := crossAppAccessKit(t, KindMixin, crossAppEntry(`{name: todos, issuer: "https://auth.example.com", scopes: [todos.read], inject: [{domain: api.example.com}]}`))
	_, err := Validate(d)
	require.NoError(t, err)
	grants, err := CrossAppAccessesOf(d.Capabilities)
	require.NoError(t, err)
	require.Len(t, grants, 1)
	require.Equal(t, "https://auth.example.com", grants[0].Issuer)
	require.Equal(t, []string{"todos.read"}, grants[0].Scopes)
	require.Equal(t, WorkloadTokenDefaultHeader, grants[0].InjectHeader(grants[0].Inject[0]))
	require.Equal(t, WorkloadTokenDefaultFormat, grants[0].InjectFormat(grants[0].Inject[0]))
	require.Equal(t, "X-Token", grants[0].InjectHeader(Inject{Header: "X-Token"}))
	require.Equal(t, "%s", grants[0].InjectFormat(Inject{Format: "%s"}))
}

func TestCrossAppAccessMergeKeysOnName(t *testing.T) {
	entry := func(name, issuer string, optional bool) Capability {
		return Capability{Type: CapabilityCrossAppAccess, Optional: optional, Config: map[string]any{
			"name": name, "issuer": issuer, "inject": []any{map[string]any{"domain": "api.example.com"}},
		}}
	}
	contribute := func(entries ...Capability) []Contribution {
		out := make([]Contribution, 0, len(entries))
		for i, e := range entries {
			out = append(out, Contribution{Reference: string(rune('a' + i)), Descriptor: &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{e}}})
		}
		return out
	}

	out, _, err := mergeCapabilities(contribute(
		entry("todos", "https://auth.example.com", false),
		entry("todos", "https://auth.example.com", true),
		entry("files", "https://auth.example.com", false),
	))
	require.NoError(t, err)
	var names []string
	for _, c := range out {
		var x CrossAppAccess
		require.NoError(t, DecodeCapabilityConfig(c, &x))
		names = append(names, x.Name)
		if x.Name == "todos" {
			require.False(t, c.Optional, "required wins over optional")
		}
	}
	require.Equal(t, []string{"todos", "files"}, names)

	_, _, err = mergeCapabilities(contribute(
		entry("todos", "https://auth.example.com", false),
		entry("todos", "https://other.example.com", false),
	))
	require.ErrorContains(t, err, "ask for different things")
}
