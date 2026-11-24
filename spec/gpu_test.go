package spec

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func gpuEntry(config map[string]any) Capability {
	return Capability{Type: CapabilityGPU, Config: config}
}

func gpuMixin(caps ...Capability) *Descriptor {
	return &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: caps}
}

func TestGPUValidation(t *testing.T) {
	for _, config := range []map[string]any{
		{"driver": GPUDriverNVIDIA},
		{"driver": GPUDriverVenus, "devices": "all"},
		{"driver": GPUDriverNVIDIA, "devices": "1"},
		{"driver": GPUDriverNVIDIA, "devices": "16"},
		{"driver": "docker.com/asahi"},
		{"driver": "gpu.example.co.uk/a.b_c-d"},
		{"driver": "local/gpu"},
	} {
		_, err := Validate(gpuMixin(gpuEntry(config)))
		require.NoError(t, err, "%v", config)
	}

	for _, tt := range []struct {
		config map[string]any
		path   string
	}{
		{nil, "capabilities[0].config.driver"},
		{map[string]any{"devices": "1"}, "capabilities[0].config.driver"},
		{map[string]any{"driver": "nvidia"}, "capabilities[0].config.driver"},
		{map[string]any{"driver": "NVIDIA.com/gpu"}, "capabilities[0].config.driver"},
		{map[string]any{"driver": "nvidia.com/gpu/all"}, "capabilities[0].config.driver"},
		{map[string]any{"driver": "-nvidia.com/gpu"}, "capabilities[0].config.driver"},
		{map[string]any{"driver": "nvidia.com/"}, "capabilities[0].config.driver"},
		{map[string]any{"driver": strings.Repeat("a", 254) + "/gpu"}, "capabilities[0].config.driver"},
		{map[string]any{"driver": "nvidia.com/" + strings.Repeat("g", 64)}, "capabilities[0].config.driver"},
		{map[string]any{"driver": GPUDriverNVIDIA, "devices": "0"}, "capabilities[0].config.devices"},
		{map[string]any{"driver": GPUDriverNVIDIA, "devices": "01"}, "capabilities[0].config.devices"},
		{map[string]any{"driver": GPUDriverNVIDIA, "devices": "two"}, "capabilities[0].config.devices"},
		{map[string]any{"driver": GPUDriverNVIDIA, "devices": "ALL"}, "capabilities[0].config.devices"},
		{map[string]any{"driver": GPUDriverNVIDIA, "devices": "device=0"}, "capabilities[0].config.devices"},
	} {
		_, err := Validate(gpuMixin(gpuEntry(tt.config)))
		var fieldErr *FieldError
		require.ErrorAs(t, err, &fieldErr, "%v", tt.config)
		require.Equal(t, tt.path, fieldErr.Path, "%v", tt.config)
	}

	_, err := Validate(gpuMixin(gpuEntry(map[string]any{"driver": GPUDriverNVIDIA}), gpuEntry(map[string]any{"driver": GPUDriverVenus})))
	require.ErrorContains(t, err, "already declared")
}

func TestGPUMerge(t *testing.T) {
	contribution := func(ref string, n Capability) Contribution {
		kind := KindMixin
		if ref == "workload" {
			kind = KindWorkload
		}
		return Contribution{Reference: ref, Descriptor: &Descriptor{SchemaVersion: SchemaVersion, Kind: kind, Capabilities: []Capability{n}}}
	}

	// Omitted devices and "all" are one request, and a required asker
	// decides for an optional one.
	omitted := gpuEntry(map[string]any{"driver": GPUDriverNVIDIA})
	omitted.Optional = true
	spelled := gpuEntry(map[string]any{"driver": GPUDriverNVIDIA, "devices": "all"})
	merged := mergeOK(t, contribution("workload", omitted), contribution("mixin", spelled)).Descriptor
	g, err := GPUOf(merged.Capabilities)
	require.NoError(t, err)
	require.Equal(t, &GPU{Driver: GPUDriverNVIDIA}, g)
	require.False(t, merged.Capabilities[0].Optional)

	for _, other := range []map[string]any{
		{"driver": GPUDriverNVIDIA, "devices": "2"},
		{"driver": GPUDriverVenus},
	} {
		_, err := Merge([]Contribution{
			contribution("workload", gpuEntry(map[string]any{"driver": GPUDriverNVIDIA, "devices": "1"})),
			contribution("mixin", gpuEntry(other)),
		}, MergeOptions{})
		require.ErrorContains(t, err, "mixin capabilities[0] both declare gpu", "%v", other)
	}
}

func TestGPUSurface(t *testing.T) {
	surface := func(config map[string]any) Surface {
		return SurfaceOf(gpuMixin(gpuEntry(config)))
	}
	nvidia := func(devices string) Surface {
		config := map[string]any{"driver": GPUDriverNVIDIA}
		if devices != "" {
			config["devices"] = devices
		}
		return surface(config)
	}

	require.Equal(t, Surface{GPU: []string{"nvidia.com/gpu all"}}, nvidia(""))
	require.Equal(t, nvidia(""), nvidia("all"), "the default and its spelling are one grant")

	require.Equal(t, []Widening{{Category: "gpu", Detail: "nvidia.com/gpu 1"}}, DiffWidenings(Surface{}, nvidia("1")))
	for _, tt := range []struct {
		granted, candidate Surface
		widens             bool
	}{
		{nvidia("all"), nvidia("2"), false},
		{nvidia("2"), nvidia("1"), false},
		{nvidia("2"), nvidia("2"), false},
		{nvidia("2"), nvidia("all"), true},
		{nvidia("1"), nvidia("2"), true},
		{nvidia("all"), surface(map[string]any{"driver": GPUDriverVenus}), true},
		{nvidia("all"), Surface{}, false},
	} {
		got := DiffWidenings(tt.granted, tt.candidate)
		require.Equal(t, tt.widens, len(got) > 0, "%v -> %v: %v", tt.granted.GPU, tt.candidate.GPU, got)
	}

	// A config that does not decode still lands on the surface.
	require.Equal(t, []string{capabilitySurfaceEntry(gpuEntry(map[string]any{"driver": 1}))},
		surface(map[string]any{"driver": 1}).Services)
}

// The schema's literal patterns are the expressions validation compiles.
func TestGPUSchemaMatchesValidation(t *testing.T) {
	schema := loadJSON(t, perTypeSchemaPath(CapabilityGPU))
	require.Equal(t, false, schema["additionalProperties"])
	require.Equal(t, []any{"driver"}, schema["required"])
	driver := at(t, schema, "properties", "driver")
	devices := at(t, schema, "properties", "devices")
	require.Equal(t, gpuDriver.String(), literalPattern(t, driver))
	require.Equal(t, gpuDevices.String(), literalPattern(t, devices))
	assertAcceptsKitArg(t, driver, "bearing")
	assertAcceptsKitArg(t, devices, "bearing")
}
