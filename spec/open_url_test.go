package spec

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestOpenURLValidation(t *testing.T) {
	for _, kind := range []string{KindWorkload, KindMixin, KindSet} {
		for _, optional := range []bool{false, true} {
			d := &Descriptor{SchemaVersion: SchemaVersion, Kind: kind, Capabilities: []Capability{{Type: CapabilityOpenURL, Optional: optional}}}
			if kind == KindSet {
				d.Kits = []Kit{{Ref: "example.com/workload:1"}}
			}
			_, err := Validate(d)
			require.NoError(t, err)
		}
	}
	for _, value := range []string{"{}", "null", `{"browser":"firefox"}`} {
		var fromYAML, fromJSON Capability
		require.NoError(t, yaml.Unmarshal([]byte("type: "+CapabilityOpenURL+"\nconfig: "+value+"\n"), &fromYAML))
		require.NoError(t, json.Unmarshal([]byte(`{"type":"`+CapabilityOpenURL+`","config":`+value+`}`), &fromJSON))
		for _, entry := range []Capability{fromYAML, fromJSON} {
			_, err := Validate(&Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{entry}})
			require.ErrorContains(t, err, "takes no config")
		}
	}
	d := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{
		{Type: CapabilityOpenURL}, {Type: CapabilityOpenURL, Optional: true},
	}}
	_, err := Validate(d)
	require.ErrorContains(t, err, "already declared")
}

func TestOpenURLMergeAndSurface(t *testing.T) {
	for _, optional := range []bool{false, true} {
		workload := Contribution{Reference: "workload", Descriptor: &Descriptor{
			SchemaVersion: SchemaVersion, Kind: KindWorkload,
			Capabilities: []Capability{{Type: CapabilityOpenURL, Optional: true}},
		}}
		mixin := Contribution{Reference: "mixin", Descriptor: &Descriptor{
			SchemaVersion: SchemaVersion, Kind: KindMixin,
			Capabilities: []Capability{{Type: CapabilityOpenURL, Optional: optional}},
		}}
		for _, inputs := range [][]Contribution{{workload, mixin}, {mixin, workload}} {
			merged := composeOK(t, inputs...).Descriptor
			_, err := Validate(merged)
			require.NoError(t, err)
			require.Equal(t, []Capability{{Type: CapabilityOpenURL, Optional: optional}}, merged.Capabilities)
			surface := SurfaceOf(merged)
			require.Equal(t, Surface{Services: []string{CapabilityOpenURL}}, surface, "no implied egress or port grants")
			require.NotEmpty(t, DiffWidenings(SurfaceOf(&Descriptor{}), surface))
			require.Empty(t, DiffWidenings(surface, SurfaceOf(&Descriptor{})))
			merged.Capabilities[0].Optional = !optional
			merged.Capabilities[0].Name = "Host browser"
			merged.Capabilities[0].Description = "Open documentation"
			require.Equal(t, surface, SurfaceOf(merged))
		}
	}
}

func TestOpenURLSelection(t *testing.T) {
	for _, optional := range []bool{false, true} {
		d := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{{Type: CapabilityOpenURL, Optional: optional}}}
		selected, err := SelectCapabilities(t.Context(), "browser", d, Supported(CapabilityOpenURL))
		require.NoError(t, err)
		require.Len(t, selected.Capabilities, 1)
		require.Equal(t, CapabilityOpenURL, selected.Capabilities[0].Type)
		require.Equal(t, optional, selected.Capabilities[0].Optional)
		selected, err = SelectCapabilities(t.Context(), "browser", d, Supported())
		if optional {
			require.NoError(t, err)
			require.Empty(t, selected.Capabilities)
			require.NotEmpty(t, selected.Skipped)
		} else {
			require.ErrorContains(t, err, CapabilityOpenURL)
		}
	}
}
