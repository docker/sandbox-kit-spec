package spec

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestPublishedScopesRetainDependencies(t *testing.T) {
	tool := contribute(t, "tool", `schemaVersion: '3'
kind: mixin
provides: [tool@1]
capabilities:
  - type: com.docker.sandbox/lifecycle@1
    after: [extra]
    config: {install: [{command: [install-tool]}]}
`)
	app := contribute(t, "app", `schemaVersion: '3'
kind: mixin
requires: [tool]
integrates: [optional-service]
capabilities:
  - type: com.docker.sandbox/lifecycle@1
    config: {install: [{command: [use-tool]}]}
`)
	extra := contribute(t, "extra", `schemaVersion: '3'
kind: mixin
provides: [extra@1]
capabilities:
  - type: com.docker.sandbox/lifecycle@1
    config: {install: [{command: [extra]}]}
`)
	// Moving tool after an unrelated Kit must also keep its requiring app
	// after tool. Publication removes the top-level requirement because the
	// set supplies it internally; only original scope metadata retains it.
	first, err := Merge([]Contribution{tool, app}, MergeOptions{})
	require.NoError(t, err)
	require.Empty(t, first.Descriptor.Requires)
	require.Equal(t, []string{"tool"}, first.Descriptor.CapabilityScopes[1].Requires)
	require.Equal(t, []string{"optional-service"}, first.Descriptor.CapabilityScopes[1].Integrates)
	nested, err := Merge([]Contribution{{Reference: "published", Descriptor: first.Descriptor}, extra}, MergeOptions{})
	require.NoError(t, err)
	for _, marshal := range []func(any) ([]byte, error){json.Marshal, yaml.Marshal} {
		raw, err := marshal(nested.Descriptor)
		require.NoError(t, err)
		descriptor, err := Decode(raw)
		require.NoError(t, err)
		_, err = Validate(descriptor)
		require.NoError(t, err)
		result, err := ComposeRuntime(t.Context(), []Contribution{{Reference: "nested", Descriptor: descriptor}}, RuntimeOptions{Select: Supported(CapabilityLifecycle)})
		require.NoError(t, err)
		var commands []string
		for _, action := range runtimeTestActions(result.Plan, PhaseInstall, StepExecute) {
			config, err := LifecycleOf([]Capability{*action.Capability})
			require.NoError(t, err)
			commands = append(commands, config.Install[0].Command[0])
		}
		require.Equal(t, []string{"extra", "install-tool", "use-tool"}, commands)
	}
}

func TestScopeDependenciesDecodeStrictly(t *testing.T) {
	for _, field := range []string{"requires", "integrates"} {
		for _, value := range []any{nil, 7, "tool", []any{7}} {
			doc := map[string]any{"schemaVersion": "3", "kind": "mixin", "capabilityScopes": []any{map[string]any{field: value}}}
			for _, marshal := range []func(any) ([]byte, error){json.Marshal, yaml.Marshal} {
				raw, err := marshal(doc)
				require.NoError(t, err)
				_, err = Decode(raw)
				require.Error(t, err, "%s=%v", field, value)
			}
		}
		for _, value := range []string{"tool >= 1", "tool > 1, < 0", "not a dependency"} {
			scope := CapabilityScope{}
			if field == "requires" {
				scope.Requires = []string{value}
			} else {
				scope.Integrates = []string{value}
			}
			_, err := Validate(&Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, CapabilityScopes: []CapabilityScope{scope}})
			if value == "tool >= 1" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		}
	}
}
