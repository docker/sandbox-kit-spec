package spec

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestDisplayCapabilityValidation(t *testing.T) {
	for _, config := range []string{"{}", "null", `{"socket":"/tmp/other"}`} {
		t.Run(config, func(t *testing.T) {
			var fromYAML, fromJSON Capability
			require.NoError(t, yaml.Unmarshal([]byte("type: "+CapabilityDisplay+"\nconfig: "+config), &fromYAML))
			require.NoError(t, json.Unmarshal([]byte(`{"type":"`+CapabilityDisplay+`","config":`+config+`}`), &fromJSON))
			for _, entry := range []Capability{fromYAML, fromJSON} {
				_, err := Validate(&Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{entry}})
				require.ErrorContains(t, err, "takes no config")
			}
		})
	}

	_, err := Validate(&Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{
		{Type: CapabilityDisplay}, {Type: CapabilityDisplay, Optional: true},
	}})
	require.ErrorContains(t, err, "already declared")
}

func TestDisplayCapabilityComposition(t *testing.T) {
	workload := Contribution{Reference: "workload", Descriptor: &Descriptor{
		SchemaVersion: SchemaVersion, Kind: KindWorkload,
		Capabilities: []Capability{{Type: CapabilityDisplay, Optional: true}},
	}}
	for _, optional := range []bool{true, false} {
		mixin := Contribution{Reference: "mixin", Descriptor: &Descriptor{
			SchemaVersion: SchemaVersion, Kind: KindMixin,
			Capabilities: []Capability{{Type: CapabilityDisplay, Optional: optional}},
		}}
		for _, inputs := range [][]Contribution{{workload, mixin}, {mixin, workload}} {
			merged := mergeOK(t, inputs...).Descriptor
			require.Equal(t, []Capability{{Type: CapabilityDisplay, Optional: optional}}, merged.Capabilities)
			require.Equal(t, Surface{Services: []string{CapabilityDisplay}}, SurfaceOf(merged))
		}
	}
}

func TestDisplayCapabilityPermission(t *testing.T) {
	d := &Descriptor{Capabilities: []Capability{{Type: CapabilityClipboard}, {Type: CapabilityOpenURL}}}
	granted := SurfaceOf(d)
	d.Capabilities = append(d.Capabilities, Capability{Type: CapabilityDisplay, Optional: true})
	candidate := SurfaceOf(d)
	require.Equal(t, []Widening{{Category: "services", Detail: CapabilityDisplay}}, DiffWidenings(granted, candidate))
	require.Empty(t, DiffWidenings(candidate, granted))
}

func TestDisplayCapabilitySelection(t *testing.T) {
	d := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{{Type: CapabilityDisplay}}}
	selected, err := SelectCapabilities(t.Context(), "gui", d, Supported(CapabilityDisplay))
	require.NoError(t, err)
	require.Len(t, selected.Capabilities, 1)
	require.Equal(t, CapabilityDisplay, selected.Capabilities[0].Type)

	_, err = SelectCapabilities(t.Context(), "gui", d, Supported(CapabilityClipboard))
	require.ErrorContains(t, err, CapabilityDisplay)

	d.Capabilities[0].Optional = true
	selected, err = SelectCapabilities(t.Context(), "gui", d, Supported(CapabilityClipboard))
	require.NoError(t, err)
	require.Empty(t, selected.Capabilities)
	require.Len(t, selected.Skipped, 1)
	require.Equal(t, []string{"capabilities[0]"}, selected.Skipped[0].Rejected)
}
