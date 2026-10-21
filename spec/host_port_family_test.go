package spec

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHostPortFamilies(t *testing.T) {
	for _, family := range []string{"", PortFamilyIPv4, PortFamilyIPv6, PortFamilyDual} {
		t.Run(family, func(t *testing.T) {
			d := hostPortDescriptor(map[string]any{"host": 5432, "family": family})
			_, err := Validate(d)
			require.NoError(t, err)
			effective := family
			if effective == "" {
				effective = PortFamilyIPv4
			}
			require.Equal(t, []string{"5432->5432/tcp/" + effective}, SurfaceOf(d).HostPorts)
			key, arity, err := InstanceKeyOf(d.Capabilities[0])
			require.NoError(t, err)
			require.Equal(t, ArityInstance, arity)
			require.Equal(t, InstanceKey{Space: CapabilityHostPort, Parts: []string{"5432/tcp"}}, key)
		})
	}
	for _, family := range []any{nil, "IPv6", "any", 6} {
		_, err := Validate(hostPortDescriptor(map[string]any{"host": 5432, "family": family}))
		require.Error(t, err, "family=%v", family)
	}
	property := at(t, loadJSON(t, perTypeSchemaPath(CapabilityHostPort)), "properties", "family")
	assertAcceptsKitArg(t, property, "bearing")
	require.Equal(t, []any{"", PortFamilyIPv4, PortFamilyIPv6, PortFamilyDual}, property["anyOf"].([]any)[0].(map[string]any)["enum"])
}

func TestHostPortFamilyComposition(t *testing.T) {
	for _, first := range []string{"", PortFamilyIPv4, PortFamilyIPv6, PortFamilyDual} {
		for _, second := range []string{"", PortFamilyIPv4, PortFamilyIPv6, PortFamilyDual} {
			t.Run(first+"/"+second, func(t *testing.T) {
				a := hostPortDescriptor(map[string]any{"host": 5432, "family": first})
				b := hostPortDescriptor(map[string]any{"host": 5432, "family": second})
				parts := []Contribution{{Reference: "first", Descriptor: a}, {Reference: "second", Descriptor: b}}
				merged, mergeErr := Merge(parts, MergeOptions{})
				composed, composeErr := Compose(parts)
				if (HostPort{Family: first}).EffectiveFamily() != (HostPort{Family: second}).EffectiveFamily() {
					require.Error(t, mergeErr)
					require.Error(t, composeErr)
					require.NotEmpty(t, DiffWidenings(SurfaceOf(a), SurfaceOf(b)))
				} else {
					require.NoError(t, mergeErr)
					require.NoError(t, composeErr)
					require.Len(t, merged.Descriptor.Capabilities, 1)
					require.Len(t, composed.Capabilities, 1)
				}
			})
		}
	}
}

func TestHostPortPublishedPortConflicts(t *testing.T) {
	for _, transport := range []string{"", "tcp", "udp"} {
		for _, family := range []string{"", PortFamilyIPv4, PortFamilyIPv6, PortFamilyDual} {
			for _, container := range []int{0, 5432} {
				for _, reverse := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/%d/%t", transport, family, container, reverse), func(t *testing.T) {
						a := hostPortDescriptor(map[string]any{"host": 5432, "container": container, "transport": transport, "family": family})
						b := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{{Type: CapabilityPort, Config: map[string]any{"container": 5432, "transport": transport, "family": PortFamilyIPv6}}}}
						if reverse {
							a, b = b, a
						}
						parts := []Contribution{{Reference: "first", Descriptor: a}, {Reference: "second", Descriptor: b}}
						_, err := Merge(parts, MergeOptions{})
						require.ErrorContains(t, err, "conflicts")
						_, err = Compose(parts)
						require.ErrorContains(t, err, "conflicts")
						a.Capabilities = append(a.Capabilities, b.Capabilities...)
						_, err = Validate(a)
						require.ErrorContains(t, err, "conflicts")
					})
				}
			}
		}
	}
	t.Run("different transports", func(t *testing.T) {
		a := hostPortDescriptor(map[string]any{"host": 5432})
		b := Capability{Type: CapabilityPort, Config: map[string]any{"container": 5432, "transport": "udp"}}
		a.Capabilities = append(a.Capabilities, b)
		_, err := Validate(a)
		require.NoError(t, err)
		_, err = Compose([]Contribution{{Reference: "both", Descriptor: a}})
		require.NoError(t, err)
	})
}

func TestHostPortPublicationPreservesFamilyArgument(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, family := range []string{PortFamilyIPv4, PortFamilyIPv6, PortFamilyDual, "invalid"} {
			t.Run(fmt.Sprintf("%t/%s", reverse, family), func(t *testing.T) {
				literal := hostPortDescriptor(map[string]any{"host": 5432})
				parameterized := hostPortDescriptor(map[string]any{"host": 5432, "family": "${{ kit.args.family }}"})
				parameterized.Args = map[string]Arg{"family": {Required: true}}
				parts := []Contribution{{Reference: "literal", Descriptor: literal}, {Reference: "parameterized", Descriptor: parameterized}}
				if reverse {
					parts[0], parts[1] = parts[1], parts[0]
				}
				published, err := Merge(parts, MergeOptions{})
				require.NoError(t, err)
				published.Descriptor.Args = parameterized.Args
				for range 2 {
					published, err = Merge([]Contribution{{Reference: "re-export", Descriptor: published.Descriptor}}, MergeOptions{})
					require.NoError(t, err)
					published.Descriptor.Args = parameterized.Args
				}
				raw, err := json.Marshal(published.Descriptor)
				require.NoError(t, err)
				expanded, err := ExpandCreateArgs(raw, published.Descriptor.Args, map[string]string{"family": family})
				require.NoError(t, err)
				d, err := Decode(expanded)
				require.NoError(t, err)
				_, err = Validate(d)
				if family == "invalid" {
					require.Error(t, err)
					return
				}
				require.NoError(t, err)
				selection, err := SelectCapabilities(t.Context(), "published", d, Supported(CapabilityHostPort))
				require.NoError(t, err)
				d.Capabilities = selection.Capabilities
				_, err = Compose([]Contribution{{Reference: "expanded", Descriptor: d}})
				if family == PortFamilyIPv4 {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
				}
			})
		}
	}
}
