package fetch

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/docker/sandbox-kit-spec/v3/spec"
	"github.com/stretchr/testify/require"
)

func TestFetchReconcilesRegisteredExecutionDefaults(t *testing.T) {
	const custom = "com.example/service@1"
	for _, method := range []string{"Resolve", "ResolvePartial", "Assemble"} {
		for _, explicit := range []int32{42, 43} {
			t.Run(fmt.Sprintf("%s/order=%d", method, explicit), func(t *testing.T) {
				reg := newRegistry(t)
				for i := range 2 {
					kind := spec.KindMixin
					if i == 0 && method != "ResolvePartial" {
						kind = spec.KindWorkload
					}
					input, _ := assemblyFixture(t, kind, fmt.Sprintf("kit%d", i), fmt.Sprintf("kit%d", i))
					entry := spec.Capability{Type: custom, Config: map[string]any{"service": "shared"}}
					if i == 1 {
						entry.Order = &explicit
					}
					entries := []spec.Capability{entry}
					if i == 0 {
						entries = []spec.Capability{{Group: &spec.CapabilityGroup{Capabilities: entries}}}
					}
					input.Manifest.Annotations[spec.AnnotationDescriptor] = string(kitJSON(t, &spec.Descriptor{
						SchemaVersion: spec.SchemaVersion, Kind: kind, Capabilities: entries,
					}))
					manifest, err := json.Marshal(input.Manifest)
					require.NoError(t, err)
					config, err := json.Marshal(input.Config)
					require.NoError(t, err)
					repo := fmt.Sprintf("kit%d", i)
					reg.blobs[repo+"/blobs/"+input.Manifest.Config.Digest.String()] = config
					reg.tag(repo, "1.0.0", manifest)
				}
				client, err := New()
				require.NoError(t, err)
				requests := reqs(reg.ref("kit0", "1.0.0"), reg.ref("kit1", "1.0.0"))
				selector := spec.Supported(custom)
				actions := 0
				validations := 0
				contracts := map[string]spec.ExecutionContract{custom: {
					DefaultOrder: 42,
					Actions: func(spec.Capability) ([]spec.PlannedAction, error) {
						actions++
						return []spec.PlannedAction{{Stage: spec.StageStart, Action: "ready"}}, nil
					},
					ValidatePlan: func(spec.ExecutionPlan) error { validations++; return nil },
				}}
				var result *Resolved
				switch method {
				case "Resolve":
					result, err = client.Resolve(t.Context(), requests, WithCapabilitySelector(selector), WithExpandedDescriptors(), WithExecutionContracts(contracts))
				case "ResolvePartial":
					result, err = client.ResolvePartial(t.Context(), requests, WithCapabilitySelector(selector), WithExpandedDescriptors(), WithExecutionContracts(contracts))
				case "Assemble":
					var assembled *Result
					assembled, err = Assemble(t.Context(), requests, Options{Loader: client.LoadKit, CapabilitySelector: selector, RetainExpandedDescriptors: true, ExecutionContracts: contracts})
					if assembled != nil {
						result = assembled.Resolved
					}
				}
				require.Zero(t, actions, "fetch must not lower actions")
				require.Zero(t, validations, "fetch must not validate a plan")
				if explicit != 42 {
					require.Nil(t, result)
					require.ErrorContains(t, err, "conflicting effective orders")
					require.ErrorContains(t, err, requests[0].Reference)
					require.ErrorContains(t, err, requests[1].Reference)
					return
				}
				require.NoError(t, err)
				require.Len(t, result.Descriptor.Capabilities, 1)
				require.Equal(t, int32(42), *result.Descriptor.Capabilities[0].Order)
				require.Nil(t, spec.DeclaredCapabilities(result.Selections[0].ExpandedDescriptor.Capabilities)[0].Order)
				require.Nil(t, result.Selections[0].Selection.Capabilities[0].Order)
				require.Equal(t, explicit, *result.Selections[1].ExpandedDescriptor.Capabilities[0].Order)
				inputs := make([]spec.Contribution, len(result.Selections))
				for i, selection := range result.Selections {
					inputs[i] = spec.Contribution{Reference: selection.Reference, Descriptor: selection.ExpandedDescriptor}
				}
				planned, err := spec.ComposeRuntime(t.Context(), inputs, spec.RuntimeOptions{Select: selector, Contracts: contracts})
				require.NoError(t, err)
				require.Len(t, planned.Plan.Stages[3].Steps, 1)
				require.Len(t, planned.Plan.Stages[3].Steps[0].Origins, 2)
			})
		}
	}
}
