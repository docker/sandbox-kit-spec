package spec

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestCapabilityNameRoundTrip(t *testing.T) {
	for _, name := range []string{"", "GitHub access", "42", "認証 access"} {
		t.Run(name, func(t *testing.T) {
			d := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{
				{Type: CapabilityVolume, Name: name, Config: map[string]any{"path": "/cache"}},
				{Type: CapabilityVolume, Name: name, Config: map[string]any{"path": "/data"}},
			}}
			_, err := Validate(d)
			require.NoError(t, err, "labels need not be unique")
			y, err := yaml.Marshal(d)
			require.NoError(t, err)
			decoded, err := Decode(y)
			require.NoError(t, err)
			for i, c := range decoded.Capabilities {
				require.Equal(t, name, c.Name)
				require.Equal(t, d.Capabilities[i].Config, c.Config)
			}
			j, err := json.Marshal(decoded)
			require.NoError(t, err)
			var published Descriptor
			require.NoError(t, json.Unmarshal(j, &published))
			require.Equal(t, decoded, &published)
		})
	}
	// YAML aliases are still strings when their targets are strings.
	d := mustDecode(t, "schemaVersion: \"3\"\nkind: mixin\ndescription: &label GitHub access\ncapabilities:\n  - type: example.com/access@1\n    name: *label\n")
	require.Equal(t, "GitHub access", d.Capabilities[0].Name)
}

func TestCapabilityNameRejectsNonStrings(t *testing.T) {
	for _, value := range []string{"42", "2.5", "true", "null", "[]", "{}"} {
		t.Run(value, func(t *testing.T) {
			_, err := Decode([]byte("schemaVersion: \"3\"\nkind: mixin\ncapabilities:\n  - type: example.com/access@1\n    name: " + value + "\n"))
			require.Error(t, err)
			var c Capability
			require.Error(t, json.Unmarshal([]byte(`{"type":"example.com/access@1","name":`+value+`}`), &c))
		})
	}
	for _, field := range []string{"nam", "displayName"} {
		_, err := Decode([]byte("schemaVersion: \"3\"\ncapabilities:\n  - type: example.com/access@1\n    " + field + ": label\n"))
		require.Error(t, err, "custom decoding must remain strict")
		var c Capability
		require.Error(t, json.Unmarshal([]byte(`{"type":"example.com/access@1","`+field+`":"label"}`), &c))
	}
}

func TestCapabilityNamesReachRuntimeHelpers(t *testing.T) {
	d := mustDecode(t, `schemaVersion: "3"
kind: mixin
capabilities:
  - type: com.docker.sandbox/credential@1
    name: GitHub access
    description: Authenticate GitHub requests
    config: {service: github, phase: runtime, apiKey: {name: GH_TOKEN}}
  - type: com.docker.sandbox/agent-skills@1
    name: Shared skills
    config: {path: /skills}
  - type: com.docker.sandbox/usb-device@1
    name: Security key
    config: {vendorId: "1050", productId: "0407"}
`)
	_, err := Validate(d)
	require.NoError(t, err)
	credentials, err := CredentialsOfPhase(d.Capabilities, "runtime")
	require.NoError(t, err)
	require.Len(t, credentials, 1)
	require.Equal(t, "GitHub access", credentials[0].Name)
	require.Equal(t, "Authenticate GitHub requests", credentials[0].Description)
	require.Equal(t, "GH_TOKEN", credentials[0].APIKey.Name)
	skills, err := AgentSkillsOf(d.Capabilities)
	require.NoError(t, err)
	require.Len(t, skills, 1)
	require.Equal(t, "Shared skills", skills[0].Name)
	devices, err := USBDevicesOf(d.Capabilities)
	require.NoError(t, err)
	require.Len(t, devices, 1)
	require.Equal(t, "Security key", devices[0].Name)
}

func TestCapabilityNameDoesNotChangeIdentityOrSurface(t *testing.T) {
	for _, entry := range []string{
		"type: com.docker.sandbox/privileged@1",
		"type: com.docker.sandbox/volume@1\n    config: {path: /cache}",
		"type: com.docker.sandbox/credential@1\n    config: {service: github, phase: runtime, apiKey: {name: GH_TOKEN}}",
		"type: example.com/service@1\n    config: {mode: read}",
	} {
		d := mustDecode(t, "schemaVersion: \"3\"\nkind: mixin\ncapabilities:\n  - "+entry+"\n")
		_, err := Validate(d)
		require.NoError(t, err)
		unnamed := SurfaceOf(d)
		identity := capabilityIdentity(d.Capabilities[0])
		d.Capabilities[0].Name = "First label"
		require.Equal(t, identity, capabilityIdentity(d.Capabilities[0]))
		require.Equal(t, unnamed, SurfaceOf(d))
		d.Capabilities[0].Name = "Renamed label"
		require.Empty(t, DiffWidenings(unnamed, SurfaceOf(d)))
		require.Empty(t, DiffWidenings(SurfaceOf(d), unnamed))

		duplicate := d.Capabilities[0]
		duplicate.Name = "Different label"
		d.Capabilities = append(d.Capabilities, duplicate)
		_, namedErr := Validate(d)
		require.Error(t, namedErr, "different labels cannot bypass arity or duplicate checks")
		for i := range d.Capabilities {
			d.Capabilities[i].Name = ""
		}
		_, unnamedErr := Validate(d)
		require.EqualError(t, namedErr, unnamedErr.Error())
	}
}

func TestMergeCapabilityNames(t *testing.T) {
	for _, tc := range []struct {
		name, entry, later string
	}{
		{"network v1", "type: com.docker.sandbox/network-policy@1\n    config: {runtime: {allow: [example.com]}}", ""},
		{"network v2", "type: com.docker.sandbox/network-policy@2\n    config: {runtime: {allow: [example.com]}}", ""},
		{"mixed network versions", "type: com.docker.sandbox/network-policy@1\n    config: {runtime: {allow: [example.com]}}", "type: com.docker.sandbox/network-policy@2\n    config: {runtime: {allow: [other.example.com]}}"},
		{"lifecycle", "type: com.docker.sandbox/lifecycle@1\n    config: {startup: [{command: echo ready}]}", ""},
		{"context", "type: com.docker.sandbox/agent-context@1\n    config: {content: Instructions}", ""},
		{"resources", "type: com.docker.sandbox/resources@1\n    config: {cpu: 2}", ""},
		{"sessions", "type: com.docker.sandbox/agent-sessions@1\n    config: {continue: [--continue]}", ""},
		{"interactive sessions", "type: com.docker.sandbox/agent-interactive-sessions@1\n    config: {newSession: []}", ""},
		{"port", "type: com.docker.sandbox/port@1\n    config: {container: 8080}", "type: com.docker.sandbox/port@1\n    config: {container: 8080, transport: tcp}"},
		{"skills", "type: com.docker.sandbox/agent-skills@1\n    config: {path: /skills}", ""},
		{"ssh agent", "type: com.docker.sandbox/ssh-agent@1\n    config: {phase: runtime}", ""},
		{"bounded ssh agent", "type: com.docker.sandbox/ssh-agent@1\n    config: {phase: runtime, unrestricted: false, sign: [git]}", "type: com.docker.sandbox/ssh-agent@1\n    config: {phase: runtime, unrestricted: false, sign: [file]}"},
		{"configless", "type: com.docker.sandbox/privileged@1", ""},
		{"unknown", "type: example.com/service@1\n    config: {mode: read}", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, names := range [][]string{{"", "", ""}, {"First label", "Second label", "Third label"}, {"", "Second label", "Third label"}, {"", "", "Third label"}} {
				var inputs []Contribution
				wantName := ""
				for i, name := range names {
					entry := tc.entry
					if i > 0 && tc.later != "" {
						entry = tc.later
					}
					inputs = append(inputs, contribute(t, fmt.Sprintf("kit-%d", i), "schemaVersion: \"3\"\nkind: mixin\ncapabilities:\n  - "+entry+"\n"))
					_, err := Validate(inputs[i].Descriptor)
					require.NoError(t, err)
					if wantName == "" {
						wantName = name
					}
				}
				unnamed := mergeOK(t, inputs...)
				for i, name := range names {
					inputs[i].Descriptor.Capabilities[0].Name = name
				}
				named := mergeOK(t, inputs...)
				require.Len(t, named.Descriptor.Capabilities, 1)
				require.Equal(t, wantName, named.Descriptor.Capabilities[0].Name)
				named.Descriptor.Capabilities[0].Name = ""
				require.Equal(t, unnamed, named, "labels change no other merge result")
				for i, name := range names {
					require.Equal(t, name, inputs[i].Descriptor.Capabilities[0].Name, "merge must not mutate its inputs")
				}
			}
		})
	}
}

func TestCapabilityNamesDoNotHideMergeConflicts(t *testing.T) {
	for _, entries := range [][2]string{
		{"type: com.docker.sandbox/credential@1\n    config: {service: github, phase: runtime, apiKey: {name: GH_TOKEN}}", "type: com.docker.sandbox/credential@1\n    config: {service: github, phase: runtime, apiKey: {name: GH_TOKEN}}"},
		{"type: com.docker.sandbox/resources@1\n    config: {cpu: 1}", "type: com.docker.sandbox/resources@1\n    config: {cpu: 2}"},
		{"type: com.docker.sandbox/volume@1\n    config: {path: /cache}", "type: com.docker.sandbox/volume@1\n    config: {path: /./cache}"},
		{"type: com.docker.sandbox/volume@1\n    config: {path: /data, size: 1GiB}", "type: com.docker.sandbox/volume@1\n    config: {path: /data, size: 2GiB}"},
	} {
		var inputs []Contribution
		for i, entry := range entries {
			inputs = append(inputs, contribute(t, fmt.Sprintf("kit-%d", i), "schemaVersion: \"3\"\nkind: mixin\ncapabilities:\n  - "+entry+"\n"))
		}
		_, baselineErr := Merge(inputs, MergeOptions{})
		require.Error(t, baselineErr)
		for _, names := range [][2]string{{"Same label", "Same label"}, {"First label", "Second label"}} {
			for i, name := range names {
				inputs[i].Descriptor.Capabilities[0].Name = name
			}
			_, err := Merge(inputs, MergeOptions{})
			require.EqualError(t, err, baselineErr.Error())
		}
	}
}
