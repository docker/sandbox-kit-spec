package spec

import (
	"encoding/json"
	"fmt"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestOrderingFieldsStrictDecode(t *testing.T) {
	for _, field := range []string{"id", "before", "after", "scope"} {
		values := []any{nil, true, map[string]any{}}
		switch field {
		case "id":
			values = append(values, 7, "", []any{"setup"})
		case "before", "after":
			values = append(values, 7, "#setup", []any{nil}, []any{7}, []any{true})
		case "scope":
			values = append(values, "0", -1, 1.5, uint64(1)<<32, []any{0})
		}
		for _, value := range values {
			t.Run(fmt.Sprintf("%s=%v", field, value), func(t *testing.T) {
				entry := map[string]any{"type": CapabilityDockerd, field: value}
				for _, marshal := range []func(any) ([]byte, error){json.Marshal, yaml.Marshal} {
					raw, err := marshal(map[string]any{"schemaVersion": "3", "kind": KindMixin, "capabilities": []any{entry}})
					require.NoError(t, err)
					_, err = Decode(raw)
					require.Error(t, err)
				}
				raw, err := json.Marshal(entry)
				require.NoError(t, err)
				var c Capability
				require.Error(t, json.Unmarshal(raw, &c))
			})
		}
	}
	for _, scope := range []any{nil, "scope", []any{nil}, []any{7}, []any{map[string]any{"unknown": true}}, []any{map[string]any{"provides": nil}}, []any{map[string]any{"provides": []any{7}}}} {
		doc := map[string]any{"schemaVersion": "3", "kind": KindMixin, "capabilityScopes": scope}
		for _, marshal := range []func(any) ([]byte, error){json.Marshal, yaml.Marshal} {
			raw, err := marshal(doc)
			require.NoError(t, err)
			_, err = Decode(raw)
			require.Error(t, err, "%v", scope)
		}
		raw, err := json.Marshal(doc)
		require.NoError(t, err)
		var d Descriptor
		require.Error(t, json.Unmarshal(raw, &d), "%v", scope)
	}
	for _, field := range []string{"id: setup", "before: []", "after: []", "scope: 0"} {
		_, err := Decode([]byte("schemaVersion: '3'\nkind: mixin\ncapabilities:\n  - " + field + "\n    group:\n      capabilities: [{type: com.example/feature@1}]\n"))
		require.ErrorContains(t, err, "group item must not state")
	}
}

func TestOrderingIDAliases(t *testing.T) {
	for _, layout := range []struct {
		name, entries string
	}{
		{"ordinary", "  - type: com.example/feature@1\n    id: *idValue\n"},
		{"group member", "  - group:\n      capabilities:\n        - type: com.example/feature@1\n          id: *idValue\n"},
	} {
		for _, tc := range []struct {
			name, value, decodeError, validationError string
		}{
			{"empty", `""`, "id must not be empty", ""},
			{"valid", `"setup"`, "", ""},
			{"invalid name", `"not an id"`, "", "id must be a literal unqualified capability name"},
			{"non-string", "7", "id must be a string", ""},
		} {
			t.Run(layout.name+"/"+tc.name, func(t *testing.T) {
				// The alias node carries its anchor's name, while the ID
				// contract applies to the string the author referenced.
				raw := []byte("schemaVersion: '3'\nkind: mixin\ndescription: &idValue " + tc.value + "\ncapabilities:\n" + layout.entries)
				d, err := Decode(raw)
				if tc.decodeError != "" {
					require.ErrorContains(t, err, tc.decodeError)
					return
				}
				require.NoError(t, err)
				_, err = Validate(d)
				if tc.validationError != "" {
					require.ErrorContains(t, err, tc.validationError)
					return
				}
				require.NoError(t, err)
				entries := DeclaredCapabilities(d.Capabilities)
				require.Len(t, entries, 1)
				require.Equal(t, "setup", entries[0].ID)
				published, err := json.Marshal(d)
				require.NoError(t, err)
				republished, err := Decode(published)
				require.NoError(t, err)
				require.Equal(t, "setup", DeclaredCapabilities(republished.Capabilities)[0].ID)
			})
		}
	}
}

func TestOrderingValidationAcrossGroups(t *testing.T) {
	for _, tc := range []struct {
		name, entries, want string
	}{
		{"local across groups", `- id: setup
  type: com.example/feature@1
- group:
    optional: true
    capabilities:
      - type: com.example/another@1
        after: ["#setup"]`, ""},
		{"duplicate across groups", `- id: setup
  type: com.example/feature@1
- group:
    optional: true
    capabilities:
      - id: setup
        type: com.example/another@1`, "already declared"},
		{"missing", `- type: com.example/feature@1
  after: ["#missing"]`, "not declared"},
		{"self", `- id: setup
  type: com.example/feature@1
  before: ["#setup"]`, "self-reference"},
		{"cycle through optional group", `- id: first
  type: com.example/first@1
  after: ["#last"]
- group:
    optional: true
    capabilities:
      - id: last
        type: com.example/last@1
        after: ["#first"]`, "cycle"},
		{"literal id", `- id: '${{ kit.args.name }}'
  type: com.example/feature@1`, "literal"},
		{"malformed target", `- type: com.example/feature@1
  after: ["com.example/agent@2#setup"]`, "invalid ordering target"},
		{"bad version", `- type: com.example/feature@1
  before: ["com.example/feature@0"]`, "invalid ordering target"},
		{"literal target", `- type: com.example/feature@1
  before: ['${{ kit.env.TARGET }}']`, "invalid ordering target"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := mustDecode(t, "schemaVersion: '3'\nkind: mixin\ncapabilities:\n"+tc.entries+"\n")
			_, err := Validate(d)
			if tc.want == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.want)
			}
		})
	}
}

func TestCapabilityScopeValidation(t *testing.T) {
	zero, one := uint32(0), uint32(1)
	for _, tc := range []struct {
		name   string
		scopes []CapabilityScope
		scope  *uint32
		want   string
	}{
		{"implicit", nil, nil, ""},
		{"explicit", []CapabilityScope{{Provides: []string{"com.example/agent"}}}, &zero, ""},
		{"empty provider", []CapabilityScope{{}}, &zero, ""},
		{"empty table", []CapabilityScope{}, nil, "must not be empty"},
		{"missing table", nil, &zero, "requires capabilityScopes"},
		{"missing scope", []CapabilityScope{{}}, nil, "valid capabilityScopes index"},
		{"outside table", []CapabilityScope{{}}, &one, "valid capabilityScopes index"},
		{"not normalized", []CapabilityScope{{Provides: []string{"agent"}}}, &zero, "normalized unversioned"},
		{"versioned", []CapabilityScope{{Provides: []string{"com.example/agent@1"}}}, &zero, "normalized unversioned"},
		{"unprovided", []CapabilityScope{{Provides: []string{"com.example/other"}}}, &zero, "absent from descriptor provides"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Provides: []string{"com.example/agent@1"}, CapabilityScopes: tc.scopes,
				Capabilities: []Capability{{Type: CapabilityDockerd, ID: "setup", Scope: tc.scope}}}
			_, err := Validate(d)
			if tc.want == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.want)
			}
		})
	}
}

func TestPublicationRetainsRepeatedNestedOrigins(t *testing.T) {
	agent := contribute(t, "same-reference", `schemaVersion: '3'
kind: mixin
provides: [com.example/agent@1]
capabilities:
  - id: setup
    type: com.docker.sandbox/lifecycle@1
    config: {startup: [{command: [prepare]}]}
  - group:
      optional: true
      capabilities:
        - id: launch
          type: com.docker.sandbox/lifecycle@1
          after: ['#setup']
          config: {startup: [{command: [launch]}]}
`)
	empty := contribute(t, "empty-provider", "schemaVersion: '3'\nkind: mixin\nprovides: [com.example/empty@1]\n")
	own := contribute(t, "wrapper", "schemaVersion: '3'\nkind: mixin\nprovides: [com.example/wrapper@1]\ncapabilities:\n  - id: setup\n    type: com.docker.sandbox/lifecycle@1\n    config: {startup: [{command: [wrapper]}]}\n")
	before, err := json.Marshal(agent.Descriptor)
	require.NoError(t, err)
	inner := publishOK(t, agent, agent, empty).Descriptor
	inner.Provides = append(inner.Provides, "com.example/aggregate@1")
	raw, err := json.Marshal(inner)
	require.NoError(t, err)
	decoded, err := Decode(raw)
	require.NoError(t, err)
	outer := publishOK(t, Contribution{Reference: "same-set", Descriptor: decoded}, Contribution{Reference: "same-set", Descriptor: decoded}, own).Descriptor
	raw, err = json.Marshal(outer)
	require.NoError(t, err)
	republished, err := Decode(raw)
	require.NoError(t, err)
	_, err = ValidatePublished(raw, republished)
	require.NoError(t, err)
	require.Len(t, republished.CapabilityScopes, 7)
	require.Equal(t, []string{"com.example/empty", "com.example/aggregate"}, republished.CapabilityScopes[2].Provides)
	require.Equal(t, []string{"com.example/wrapper"}, republished.CapabilityScopes[6].Provides)
	entries := DeclaredCapabilities(republished.Capabilities)
	require.Len(t, entries, 9)
	var scopes []uint32
	for _, entry := range entries {
		require.NotNil(t, entry.Scope)
		scopes = append(scopes, *entry.Scope)
	}
	require.Equal(t, []uint32{0, 0, 1, 1, 3, 3, 4, 4, 6}, scopes)
	for _, i := range []int{1, 3, 5, 7} {
		require.Equal(t, []string{"#setup"}, entries[i].After)
	}
	effective := selectPublished(t, republished)
	lifecycles, err := LifecyclesOf(effective.Capabilities)
	require.NoError(t, err)
	require.Len(t, lifecycles, 9)
	_, err = LifecycleOf(effective.Capabilities)
	require.ErrorContains(t, err, "LifecyclesOf")
	require.Nil(t, effective.CapabilityScopes)
	for _, entry := range effective.Capabilities {
		require.Empty(t, entry.ID)
		require.Nil(t, entry.Before)
		require.Nil(t, entry.After)
		require.Nil(t, entry.Scope)
	}
	after, err := json.Marshal(agent.Descriptor)
	require.NoError(t, err)
	require.JSONEq(t, string(before), string(after))
}

func TestLifecycleSequenceKeepsIdenticalCommandsAndRejectsSharedFiles(t *testing.T) {
	entry := Capability{Type: CapabilityLifecycle, Config: map[string]any{"startup": []any{map[string]any{"command": []string{"run"}}}}}
	d := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{entry, entry}}
	_, err := Validate(d)
	require.NoError(t, err)
	composed, err := Compose([]Contribution{{Reference: "agent", Descriptor: d}})
	require.NoError(t, err)
	lifecycles, err := LifecyclesOf(composed.Capabilities)
	require.NoError(t, err)
	require.Len(t, lifecycles, 2)
	require.Equal(t, CommandLine{"run"}, lifecycles[0].Startup[0].Command)
	require.Equal(t, CommandLine{"run"}, lifecycles[1].Startup[0].Command)
	for _, config := range []map[string]any{
		{"files": []any{map[string]any{"path": "/same", "content": "body"}}},
		{"startup": []any{map[string]any{"command": []string{"run"}}}, "interactive": []string{}},
	} {
		d.Capabilities = []Capability{{Type: CapabilityLifecycle, Config: config}, {Type: CapabilityLifecycle, Config: config}}
		_, err := Validate(d)
		require.Error(t, err)
		_, err = Compose([]Contribution{{Reference: "agent", Descriptor: d}})
		require.Error(t, err)
	}
}

func TestOrderingSchemaPins(t *testing.T) {
	schema := loadSchema(t)
	id := at(t, schema, "definitions", "capability", "properties", "id")
	require.Equal(t, capabilityName.String(), id["pattern"])
	targets := regexp.MustCompile(at(t, schema, "definitions", "orderingTargets", "items")["pattern"].(string))
	for _, raw := range []string{"#setup", "com.example/agent#launch", "agent", "com.docker.sandbox/dockerd@1", "", "#", "a##b", "com.example/agent@1#launch", "${{ kit.args.name }}", "com.example/feature@0"} {
		_, err := parseOrderingTarget(raw)
		require.Equal(t, err == nil, targets.MatchString(raw), raw)
	}
}

func TestOrderingFieldsCannotExpand(t *testing.T) {
	for _, field := range []string{"id: '${{ kit.args.name }}'", "before: ['${{ kit.args.name }}']", "after: ['${{ kit.env.NAME }}']", "scope: '${{ kit.args.name }}'"} {
		raw := []byte("schemaVersion: '3'\nkind: mixin\ncapabilities:\n  - type: com.example/feature@1\n    " + field + "\n")
		_, err := ExpandBuildArgs(raw, map[string]Arg{"name": {BuildArg: "NAME"}}, map[string]string{"name": "setup"})
		require.ErrorContains(t, err, "must be literal")
		_, err = ExpandCreateArgs(raw, map[string]Arg{"name": {}}, map[string]string{"name": "setup"})
		require.ErrorContains(t, err, "must be literal")
	}
}
