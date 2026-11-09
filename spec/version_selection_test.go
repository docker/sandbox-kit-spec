package spec

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSupportedMatchesExactVersionsAndEveryInstance(t *testing.T) {
	const family = "com.example/feature"
	items := []Capability{
		{Type: family + "@1", Optional: true, Config: map[string]any{"instance": "first"}},
		{Type: family + "@2", Optional: true, Config: map[string]any{"instance": "second"}},
		{Type: family + "@10", Optional: true, Config: map[string]any{"instance": "third"}},
		{Type: family + "@2", Optional: true, Config: map[string]any{"instance": "fourth"}},
	}
	for _, tc := range []struct {
		name    string
		claimed []string
		indices []int
	}{
		{name: "none"},
		{name: "@1", claimed: []string{family + "@1"}, indices: []int{0}},
		{name: "@2 includes every instance", claimed: []string{family + "@2"}, indices: []int{1, 3}},
		{name: "@10", claimed: []string{family + "@10"}, indices: []int{2}},
		{name: "all remain independent", claimed: []string{family + "@1", family + "@2", family + "@10"}, indices: []int{0, 1, 2, 3}},
		{name: "unversioned claim grants nothing", claimed: []string{family}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selection, err := SelectCapabilities(t.Context(), "source", &Descriptor{Kind: KindMixin, Capabilities: items}, Supported(tc.claimed...))
			require.NoError(t, err)
			require.Len(t, selection.Selected, len(tc.indices))
			require.Len(t, selection.Skipped, len(items)-len(tc.indices))
			for i, index := range tc.indices {
				require.Equal(t, items[index].Type, selection.Capabilities[i].Type)
				require.Equal(t, items[index].Config, selection.Capabilities[i].Config)
			}
			require.Nil(t, items[0].Source, "selection does not rewrite declarations")
		})
	}
}

func TestVersionSelectionKeepsCrossedGroupsAtomic(t *testing.T) {
	const a1, a2 = "com.example/a@1", "com.example/a@2"
	const b1, b2 = "com.example/b@1", "com.example/b@2"
	d := &Descriptor{Kind: KindMixin, Capabilities: []Capability{
		{Group: &CapabilityGroup{Name: "X", Optional: true, Capabilities: []Capability{{Type: a2}, {Type: b1}}}},
		{Group: &CapabilityGroup{Name: "Y", Optional: true, Capabilities: []Capability{{Type: a1}, {Type: b2}}}},
	}}
	for _, tc := range []struct {
		name    string
		claimed []string
		groups  []string
		types   []string
	}{
		{name: "higher version of each cannot form a group", claimed: []string{a2, b2}},
		{name: "lower version of each cannot form a group", claimed: []string{a1, b1}},
		{name: "X", claimed: []string{a2, b1}, groups: []string{"X"}, types: []string{a2, b1}},
		{name: "Y", claimed: []string{a1, b2}, groups: []string{"Y"}, types: []string{a1, b2}},
		{name: "both independent groups", claimed: []string{a1, a2, b1, b2}, groups: []string{"X", "Y"}, types: []string{a2, b1, a1, b2}},
		{name: "neither"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selection, err := SelectCapabilities(t.Context(), "source", d, Supported(tc.claimed...))
			require.NoError(t, err)
			var names, types []string
			for _, record := range selection.Selected {
				names = append(names, record.Name)
				require.Len(t, record.Members, 2)
			}
			for _, capability := range selection.Capabilities {
				types = append(types, capability.Type)
			}
			require.Equal(t, tc.groups, names)
			require.Equal(t, tc.types, types)
			for _, record := range selection.Skipped {
				require.Len(t, record.Members, 2)
				require.Len(t, record.Decisions, 2)
				require.NotEmpty(t, record.Rejected)
			}
		})
	}
}

func TestVersionSelectionValidatesUnclaimedKnownConfig(t *testing.T) {
	for _, typ := range []string{CapabilityAgentContext, CapabilityAgentContextV2} {
		t.Run(typ, func(t *testing.T) {
			d := &Descriptor{Kind: KindWorkload, Capabilities: []Capability{{Group: &CapabilityGroup{Optional: true, Capabilities: []Capability{
				{Type: typ, Config: map[string]any{"content": "guidance", "unknown": true}},
			}}}}}
			_, err := SelectCapabilities(t.Context(), "source", d, func(context.Context, SelectionTarget, Descriptor, Capability) CapabilityDecision {
				t.Fatal("invalid unclaimed configuration reached runtime policy")
				return CapabilityDecision{}
			})
			require.ErrorContains(t, err, "capabilities[0].group.capabilities[0].config")
			require.ErrorContains(t, err, "unknown")
		})
	}
}

func TestVersionSelectionPublicationPreservesProvenanceAndNetworkUnion(t *testing.T) {
	const lower, higher = "com.example/feature@1", "com.example/feature@2"
	policy := func(host string) Capability {
		return Capability{Type: CapabilityNetworkPolicy, Config: map[string]any{"runtime": map[string]any{"allow": []any{host}}}}
	}
	inputs := []Contribution{
		{Reference: "a", Descriptor: &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{
			policy("base.example"),
			{Group: &CapabilityGroup{Optional: true, Capabilities: []Capability{{Type: lower}, policy("lower.example")}}},
			{Group: &CapabilityGroup{Optional: true, Capabilities: []Capability{{Type: higher}, policy("higher.example")}}},
		}}},
		{Reference: "b", Descriptor: &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{policy("extra.example")}}},
	}
	for _, input := range inputs {
		for i := range input.Descriptor.Capabilities {
			item := &input.Descriptor.Capabilities[i]
			path := fmt.Sprintf("capabilities[%d]", i)
			item.Source = &CapabilitySource{Kit: input.Reference, Path: path}
			if item.Group != nil {
				for j := range item.Group.Capabilities {
					memberPath := fmt.Sprintf("%s.group.capabilities[%d]", path, j)
					item.Group.Capabilities[j].Source = &CapabilitySource{Kit: input.Reference, Path: memberPath}
				}
			}
		}
	}
	published, err := Merge(inputs, MergeOptions{})
	require.NoError(t, err)
	raw, err := json.Marshal(published.Descriptor)
	require.NoError(t, err)
	decoded, err := Decode(raw)
	require.NoError(t, err)
	_, err = ValidatePublished(raw, decoded)
	require.NoError(t, err)
	for _, tc := range []struct {
		name    string
		claimed []string
		hosts   []string
	}{
		{name: "lower", claimed: []string{lower}, hosts: []string{"base.example", "lower.example", "extra.example"}},
		{name: "higher", claimed: []string{higher}, hosts: []string{"base.example", "higher.example", "extra.example"}},
		{name: "both", claimed: []string{lower, higher}, hosts: []string{"base.example", "lower.example", "higher.example", "extra.example"}},
		{name: "neither", hosts: []string{"base.example", "extra.example"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selector := Supported(append([]string{CapabilityNetworkPolicy}, tc.claimed...)...)
			var direct []Contribution
			var directSources []CapabilitySource
			for _, input := range inputs {
				selected, err := SelectCapabilities(t.Context(), input.Reference, input.Descriptor, selector)
				require.NoError(t, err)
				d := *input.Descriptor
				d.Capabilities = selected.Capabilities
				direct = append(direct, Contribution{Reference: input.Reference, Descriptor: &d})
				for _, c := range selected.Capabilities {
					directSources = append(directSources, *c.Source)
				}
			}
			want, err := Compose(direct)
			require.NoError(t, err)
			selected, err := SelectCapabilities(t.Context(), "published", decoded, selector)
			require.NoError(t, err)
			var publishedSources []CapabilitySource
			for _, record := range selected.Selected {
				publishedSources = append(publishedSources, record.MemberSources...)
			}
			require.Equal(t, directSources, publishedSources)
			effective := *decoded
			effective.Capabilities = selected.Capabilities
			got, err := Compose([]Contribution{{Reference: "published", Descriptor: &effective}})
			require.NoError(t, err)
			require.Equal(t, want, got)
			network, err := NetworkPolicyOf(got.Capabilities)
			require.NoError(t, err)
			require.ElementsMatch(t, tc.hosts, network.Runtime.Allow)
		})
	}
}

func TestExactVersionSelectionAfterPublishedNetworkUnion(t *testing.T) {
	for _, grouped := range []bool{false, true} {
		t.Run(fmt.Sprintf("grouped=%t", grouped), func(t *testing.T) {
			var inputs []Contribution
			for i, typ := range []string{CapabilityNetworkPolicy, CapabilityNetworkPolicyV2} {
				capability := Capability{Type: typ, Optional: true, Config: map[string]any{"runtime": map[string]any{"allow": []any{fmt.Sprintf("v%d.example", i+1)}}}}
				if grouped {
					capability.Optional = false
					capability = Capability{Group: &CapabilityGroup{Optional: true, Capabilities: []Capability{capability}}}
				}
				inputs = append(inputs, Contribution{Reference: fmt.Sprintf("kit%d", i), Descriptor: &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{capability}}})
			}
			published, err := Merge(inputs, MergeOptions{})
			require.NoError(t, err)
			raw, err := json.Marshal(published.Descriptor)
			require.NoError(t, err)
			decoded, err := Decode(raw)
			require.NoError(t, err)
			_, err = ValidatePublished(raw, decoded)
			require.NoError(t, err)
			for _, typ := range []string{CapabilityNetworkPolicy, CapabilityNetworkPolicyV2} {
				selected, err := SelectCapabilities(t.Context(), "published", decoded, Supported(typ))
				require.NoError(t, err)
				effective := *decoded
				effective.Capabilities = selected.Capabilities
				composed, err := Compose([]Contribution{{Reference: "published", Descriptor: &effective}})
				require.NoError(t, err)
				if !grouped && typ == CapabilityNetworkPolicy {
					require.Empty(t, composed.Capabilities, "concrete publication emits @2, which @1 support cannot select")
					continue
				}
				network, err := NetworkPolicyV2Of(composed.Capabilities)
				require.NoError(t, err)
				want := []string{"v1.example", "v2.example"}
				if grouped {
					want = []string{"v1.example"}
					if typ == CapabilityNetworkPolicyV2 {
						want = []string{"v2.example"}
					}
				}
				var hosts []string
				for _, grant := range network.Runtime.Allow {
					hosts = append(hosts, grant.Hosts...)
				}
				require.ElementsMatch(t, want, hosts)
			}
		})
	}
}
