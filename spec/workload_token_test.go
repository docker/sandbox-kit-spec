package spec

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const workloadTokenAllow = "  - type: com.docker.sandbox/network-policy@2\n    config: {runtime: {allow: [{hosts: [api.example.com, sts.amazonaws.com]}]}}\n"

func workloadTokenKit(t *testing.T, kind, entries string) *Descriptor {
	t.Helper()
	return mustDecode(t, "schemaVersion: \"3\"\nkind: "+kind+"\nversion: \"1.0.0\"\ncapabilities:\n"+workloadTokenAllow+entries)
}

func tokenEntry(config string) string {
	return "  - type: " + CapabilityWorkloadToken + "\n    config: " + config + "\n"
}

func TestWorkloadTokenValidation(t *testing.T) {
	valid := []string{
		`{name: backend, audience: "https://api.example.com", inject: [{domain: api.example.com}]}`,
		`{name: aws, audience: sts.amazonaws.com, inject: [{domain: sts.amazonaws.com, header: X-Token, format: "%s"}]}`,
		`{name: backend, audience: "${{ kit.args.aud }}", inject: [{domain: api.example.com}]}`,
		`{name: "${{ kit.args.n }}", audience: "https://api.example.com", inject: [{domain: api.example.com}]}`,
	}
	for _, config := range valid {
		for _, kind := range []string{KindWorkload, KindMixin} {
			_, err := Validate(workloadTokenKit(t, kind, tokenEntry(config)))
			require.NoError(t, err, "%s: %s", kind, config)
		}
	}

	invalid := []struct{ config, want string }{
		{`{audience: "https://api.example.com", inject: [{domain: api.example.com}]}`, "name is required"},
		{`{name: "-bad", audience: "https://api.example.com", inject: [{domain: api.example.com}]}`, "must match"},
		{`{name: backend, inject: [{domain: api.example.com}]}`, "audience is required"},
		{`{name: backend, audience: "has space", inject: [{domain: api.example.com}]}`, "without whitespace"},
		{`{name: backend, audience: "https://api.example.com"}`, "at least one inject rule"},
		{`{name: backend, audience: "https://api.example.com", inject: []}`, "at least one inject rule"},
		{`{name: backend, audience: "https://api.example.com", inject: [{header: X}]}`, "domain is required"},
		{`{name: backend, audience: "https://api.example.com", inject: [{domain: api.example.com, scheme: basic}]}`, "scheme and username do not apply"},
		{`{name: backend, audience: "https://api.example.com", inject: [{domain: api.example.com, format: "Bearer"}]}`, "exactly one %s"},
		{`{name: backend, audience: "https://api.example.com", inject: [{domain: other.example.com}]}`, "not in the network policy's runtime allow list"},
		{`{name: backend, audience: null, inject: [{domain: api.example.com}]}`, "is null"},
		{`{name: backend, audience: "https://api.example.com", inject: [{domain: api.example.com}], type: jwt}`, "unknown field"},
	}
	for _, tt := range invalid {
		_, err := Validate(workloadTokenKit(t, KindWorkload, tokenEntry(tt.config)))
		require.ErrorContains(t, err, tt.want, tt.config)
	}

	_, err := Validate(workloadTokenKit(t, KindWorkload,
		tokenEntry(`{name: backend, audience: "https://api.example.com", inject: [{domain: api.example.com}]}`)+
			tokenEntry(`{name: backend, audience: sts.amazonaws.com, inject: [{domain: sts.amazonaws.com}]}`)))
	require.ErrorContains(t, err, `workload token "backend" already declared`)

	_, err = Validate(workloadTokenKit(t, KindWorkload,
		tokenEntry(`{name: backend, audience: "https://api.example.com", inject: [{domain: api.example.com}]}`)+
			tokenEntry(`{name: aws, audience: sts.amazonaws.com, inject: [{domain: sts.amazonaws.com}]}`)))
	require.NoError(t, err, "two tokens with different names coexist")
}

func TestWorkloadTokensOfAndDefaults(t *testing.T) {
	d := workloadTokenKit(t, KindWorkload,
		tokenEntry(`{name: backend, audience: "https://api.example.com", inject: [{domain: api.example.com}]}`)+
			tokenEntry(`{name: aws, audience: sts.amazonaws.com, inject: [{domain: sts.amazonaws.com, header: X-Token, format: "%s"}]}`))
	tokens, err := WorkloadTokensOf(d.Capabilities)
	require.NoError(t, err)
	require.Len(t, tokens, 2)
	require.Equal(t, "backend", tokens[0].Name)
	require.Equal(t, WorkloadTokenDefaultHeader, tokens[0].InjectHeader(tokens[0].Inject[0]))
	require.Equal(t, WorkloadTokenDefaultFormat, tokens[0].InjectFormat(tokens[0].Inject[0]))
	require.Equal(t, "X-Token", tokens[1].InjectHeader(tokens[1].Inject[0]))
	require.Equal(t, "%s", tokens[1].InjectFormat(tokens[1].Inject[0]))
}

func TestWorkloadTokenMergeKeysOnName(t *testing.T) {
	backend := Capability{Type: CapabilityWorkloadToken, Config: map[string]any{"name": "backend", "audience": "https://api.example.com", "inject": []any{map[string]any{"domain": "api.example.com"}}}}
	same := Capability{Type: CapabilityWorkloadToken, Optional: true, Config: map[string]any{"name": "backend", "audience": "https://api.example.com", "inject": []any{map[string]any{"domain": "api.example.com"}}}}
	other := Capability{Type: CapabilityWorkloadToken, Config: map[string]any{"name": "backend", "audience": "sts.amazonaws.com", "inject": []any{map[string]any{"domain": "sts.amazonaws.com"}}}}
	aws := Capability{Type: CapabilityWorkloadToken, Config: map[string]any{"name": "aws", "audience": "sts.amazonaws.com", "inject": []any{map[string]any{"domain": "sts.amazonaws.com"}}}}

	contribute := func(entries ...Capability) []Contribution {
		out := make([]Contribution, 0, len(entries))
		for i, e := range entries {
			out = append(out, Contribution{Reference: string(rune('a' + i)), Descriptor: &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{e}}})
		}
		return out
	}
	out, _, err := mergeCapabilities(contribute(backend, same, aws))
	require.NoError(t, err)
	var names []string
	for _, c := range out {
		var w WorkloadToken
		require.NoError(t, DecodeCapabilityConfig(c, &w))
		names = append(names, w.Name)
		if w.Name == "backend" {
			require.False(t, c.Optional, "required wins over optional")
		}
	}
	require.Equal(t, []string{"backend", "aws"}, names)

	_, _, err = mergeCapabilities(contribute(backend, other))
	require.ErrorContains(t, err, "ask for different things")
}
