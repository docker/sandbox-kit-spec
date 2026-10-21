package spec

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func hostPortDescriptor(config map[string]any) *Descriptor {
	return &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{{Type: CapabilityHostPort, Config: config}}}
}

func TestHostPortGrammar(t *testing.T) {
	for _, config := range []map[string]any{
		{"host": 1}, {"host": 65535}, {"host": 5432, "container": 0, "transport": ""},
		{"host": 5432, "container": 15432, "transport": "tcp"},
		{"host": 5432, "transport": "udp"},
	} {
		_, err := Validate(hostPortDescriptor(config))
		require.NoError(t, err)
	}
	for _, config := range []map[string]any{
		nil, {}, {"host": 0}, {"host": -1}, {"host": 65536}, {"host": "5432"}, {"host": 1.5},
		{"host": 5432, "container": -1}, {"host": 5432, "container": 65536},
		{"host": 5432, "container": "5432"}, {"host": 5432, "transport": "quic"},
		{"host": 5432, "address": "127.0.0.1"},
		{"host": nil}, {"host": 5432, "container": nil}, {"host": 5432, "transport": nil},
	} {
		_, err := Validate(hostPortDescriptor(config))
		require.Error(t, err, "%#v", config)
	}
	d := hostPortDescriptor(map[string]any{"host": 5432})
	d.Capabilities = append(d.Capabilities, Capability{Type: CapabilityHostPort, Config: map[string]any{"host": 5432, "container": 5432, "transport": "tcp"}})
	_, err := Validate(d)
	require.ErrorContains(t, err, "already declared")
}

func TestHostPortCompositionAndSurface(t *testing.T) {
	first := hostPortDescriptor(map[string]any{"host": 5432})
	explicit := hostPortDescriptor(map[string]any{"host": 5432, "container": 5432, "transport": "tcp"})
	zero := hostPortDescriptor(map[string]any{"host": 5432, "container": 0, "transport": ""})
	for _, equivalent := range []*Descriptor{explicit, zero} {
		contributions := []Contribution{{Reference: "one", Descriptor: first}, {Reference: "two", Descriptor: equivalent}}
		merged, err := Merge(contributions, MergeOptions{})
		require.NoError(t, err)
		require.Len(t, merged.Descriptor.Capabilities, 1)
		composed, err := Compose(contributions)
		require.NoError(t, err)
		require.Len(t, composed.Capabilities, 1)
		require.Equal(t, SurfaceOf(first), SurfaceOf(equivalent))
	}
	for _, config := range []map[string]any{{"host": 6379}, {"host": 5432, "container": 15432}} {
		other := hostPortDescriptor(config)
		composed, err := Compose([]Contribution{{Reference: "one", Descriptor: first}, {Reference: "two", Descriptor: other}})
		require.NoError(t, err)
		require.Len(t, composed.Capabilities, 2)
		require.NotEmpty(t, DiffWidenings(SurfaceOf(first), SurfaceOf(other)))
	}
	conflict := hostPortDescriptor(map[string]any{"host": 6379, "container": 5432})
	for _, parts := range [][]Contribution{
		{{Reference: "one", Descriptor: first}, {Reference: "two", Descriptor: conflict}},
		{{Reference: "two", Descriptor: conflict}, {Reference: "one", Descriptor: first}},
	} {
		_, err := Compose(parts)
		require.Error(t, err)
		_, err = Merge(parts, MergeOptions{})
		require.Error(t, err)
	}
	surface := SurfaceOf(first)
	require.Equal(t, []string{"5432->5432/tcp"}, surface.HostPorts)
	require.Empty(t, surface.Ports)
	require.Empty(t, surface.Services)
	_, err := SelectCapabilities(t.Context(), "test", first, Supported(CapabilityPort))
	require.Error(t, err)
	first.Capabilities[0].Optional = true
	selected, err := SelectCapabilities(t.Context(), "test", first, Supported(CapabilityPort))
	require.NoError(t, err)
	require.Empty(t, selected.Capabilities)
	require.Len(t, selected.Skipped, 1)
}

func TestHostPortSchema(t *testing.T) {
	schema := loadJSON(t, perTypeSchemaPath(CapabilityHostPort))
	require.Equal(t, false, schema["additionalProperties"])
	require.Equal(t, []any{"host"}, schema["required"])
	for _, field := range []string{"host", "container"} {
		property := at(t, schema, "properties", field)
		assertAcceptsKitArg(t, property, "whole")
		literal := property["anyOf"].([]any)[0].(map[string]any)
		minimum := float64(1)
		if field == "container" {
			minimum = 0
		}
		require.Equal(t, minimum, literal["minimum"])
		require.Equal(t, float64(65535), literal["maximum"])
	}
	transport := at(t, schema, "properties", "transport")
	assertAcceptsKitArg(t, transport, "bearing")
	require.Equal(t, []any{"", "tcp", "udp"}, transport["anyOf"].([]any)[0].(map[string]any)["enum"])
}

func TestHostPortRejectsExplicitNullDefaults(t *testing.T) {
	for _, field := range []string{"container", "transport"} {
		t.Run(field, func(t *testing.T) {
			raw := []byte(`{"schemaVersion":"3","kind":"mixin","capabilities":[{"type":"com.docker.sandbox/host-port@1","config":{"host":5432,"` + field + `":null}}]}`)
			d, err := Decode(raw)
			require.NoError(t, err)
			_, err = ValidateRaw(raw, d)
			require.ErrorContains(t, err, field+" must not be null")
		})
	}
}

func TestHostPortPublicationPreservesTransportArgument(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprintf("reverse=%t", reverse), func(t *testing.T) {
			concrete := hostPortDescriptor(map[string]any{"host": 5432})
			parameterized := hostPortDescriptor(map[string]any{"host": 5432, "transport": "${{ kit.args.transport }}"})
			parameterized.Args = map[string]Arg{"transport": {Required: true}}
			parts := []Contribution{{Reference: "concrete", Descriptor: concrete}, {Reference: "parameterized", Descriptor: parameterized}}
			if reverse {
				parts[0], parts[1] = parts[1], parts[0]
			}
			published, err := Merge(parts, MergeOptions{})
			require.NoError(t, err)
			// The publisher attaches the enclosing set's exported inputs.
			published.Descriptor.Args = parameterized.Args
			for _, reexport := range []bool{false, true} {
				t.Run(fmt.Sprintf("reexport=%t", reexport), func(t *testing.T) {
					d := published.Descriptor
					if reexport {
						outer, err := Merge([]Contribution{{Reference: "published", Descriptor: d}}, MergeOptions{})
						require.NoError(t, err)
						d = outer.Descriptor
						d.Args = parameterized.Args
					}
					raw, err := json.Marshal(d)
					require.NoError(t, err)
					decoded, err := Decode(raw)
					require.NoError(t, err)
					_, err = ValidatePublished(raw, decoded)
					require.NoError(t, err)
					require.Contains(t, string(raw), "${{ kit.args.transport }}")
					for _, transport := range []string{"", "tcp", "udp", "quic"} {
						t.Run(fmt.Sprintf("transport=%q", transport), func(t *testing.T) {
							values, err := ResolveArgs(decoded.Args, map[string]string{"transport": transport})
							require.NoError(t, err)
							expanded, err := ExpandCreateArgs(raw, decoded.Args, values)
							require.NoError(t, err)
							effective, err := Decode(expanded)
							require.NoError(t, err)
							selection, err := SelectCapabilities(t.Context(), "published", effective, Supported(CapabilityHostPort))
							if transport == "quic" {
								require.ErrorContains(t, err, "transport must be tcp or udp")
								return
							}
							require.NoError(t, err)
							effective.Capabilities = selection.Capabilities
							composed, err := Compose([]Contribution{{Reference: "published", Descriptor: effective}})
							require.NoError(t, err)
							if transport == "udp" {
								require.Len(t, composed.Capabilities, 2)
								require.Equal(t, []string{"5432->5432/tcp", "5432->5432/udp"}, SurfaceOf(composed).HostPorts)
							} else {
								require.Len(t, composed.Capabilities, 1)
								require.Equal(t, []string{"5432->5432/tcp"}, SurfaceOf(composed).HostPorts)
							}
						})
					}
				})
			}
		})
	}
}

func TestHostPortRequestTransportComparison(t *testing.T) {
	base := hostPortDescriptor(map[string]any{"host": 5432}).Capabilities[0]
	for _, transport := range []string{"", "tcp", "udp", "${{ kit.args.transport }}"} {
		other := hostPortDescriptor(map[string]any{"host": 5432, "transport": transport}).Capabilities[0]
		want := transport == "" || transport == "tcp"
		require.Equal(t, want, sameRequest(base, other), "transport %q", transport)
		require.Equal(t, want, sameRequest(other, base), "transport %q in reverse", transport)
	}
}

func TestHostPortUDPComposition(t *testing.T) {
	udp := hostPortDescriptor(map[string]any{"host": 5353, "transport": "udp"})
	duplicate := hostPortDescriptor(map[string]any{"host": 5353, "container": 5353, "transport": "udp"})
	conflict := hostPortDescriptor(map[string]any{"host": 9999, "container": 5353, "transport": "udp"})
	tcp := hostPortDescriptor(map[string]any{"host": 9999, "container": 5353})
	for _, second := range []*Descriptor{duplicate, conflict, tcp} {
		for _, reverse := range []bool{false, true} {
			parts := []Contribution{{Reference: "udp", Descriptor: udp}, {Reference: "second", Descriptor: second}}
			if reverse {
				parts[0], parts[1] = parts[1], parts[0]
			}
			merged, err := Merge(parts, MergeOptions{})
			composed, composeErr := Compose(parts)
			if second == conflict {
				require.Error(t, err)
				require.Error(t, composeErr)
				continue
			}
			require.NoError(t, err)
			require.NoError(t, composeErr)
			_, err = Validate(merged.Descriptor)
			require.NoError(t, err)
			if second == duplicate {
				require.Len(t, merged.Descriptor.Capabilities, 1)
				require.Len(t, composed.Capabilities, 1)
			} else {
				require.Len(t, merged.Descriptor.Capabilities, 2)
				require.Len(t, composed.Capabilities, 2)
			}
		}
	}
	require.NotEmpty(t, DiffWidenings(SurfaceOf(udp), SurfaceOf(tcp)))
}
