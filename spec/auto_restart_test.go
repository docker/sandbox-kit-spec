package spec

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestAutoRestartValidation(t *testing.T) {
	base := func(caps ...Capability) *Descriptor {
		return &Descriptor{SchemaVersion: SchemaVersion, Kind: KindWorkload, Capabilities: caps}
	}
	for _, optional := range []bool{false, true} {
		entry := Capability{Type: CapabilityAutoRestart, Optional: optional}
		_, err := Validate(base(entry))
		require.NoError(t, err)
		mixin := base(entry)
		mixin.Kind = KindMixin
		_, err = Validate(mixin)
		require.NoError(t, err)
	}

	_, err := Validate(base(
		Capability{Type: CapabilityAutoRestart},
		Capability{Type: CapabilityAutoRestart, Optional: true},
	))
	require.ErrorContains(t, err, "already declared")

	for _, spelling := range []string{"{}", "null", `{"keepAlive": true}`} {
		t.Run(spelling, func(t *testing.T) {
			var fromYAML, fromJSON Capability
			require.NoError(t, yaml.Unmarshal([]byte("type: "+CapabilityAutoRestart+"\nconfig: "+spelling+"\n"), &fromYAML))
			require.NoError(t, json.Unmarshal([]byte(`{"type":"`+CapabilityAutoRestart+`","config":`+spelling+`}`), &fromJSON))
			for _, entry := range []Capability{fromYAML, fromJSON} {
				_, err := Validate(base(entry))
				require.ErrorContains(t, err, "takes no config")
			}
		})
	}

	set := base(Capability{Type: CapabilityAutoRestart})
	set.Kind = KindSet
	set.Kits = []Kit{{Ref: "example.com/workload:1"}}
	_, err = Validate(set)
	require.NoError(t, err)
}

func TestAutoRestartMergeAndSurface(t *testing.T) {
	for _, kind := range []string{KindWorkload, KindMixin} {
		base := &Descriptor{SchemaVersion: SchemaVersion, Kind: kind}
		// A set's own declarations enter Merge without a kind; its
		// members supply the resolved kind that validation judges.
		own := &Descriptor{SchemaVersion: SchemaVersion, Capabilities: []Capability{{Type: CapabilityAutoRestart}}}
		base.Capabilities = []Capability{{Type: CapabilityAutoRestart, Optional: true}}
		merged := mergeOK(t, Contribution{Reference: "base", Descriptor: base}, Contribution{Reference: "set", Descriptor: own}).Descriptor
		_, err := Validate(merged)
		require.NoError(t, err)
		require.Len(t, merged.Capabilities, 1)
		require.False(t, merged.Capabilities[0].Optional, "a required declaration wins")
		require.Equal(t, SurfaceOf(&Descriptor{}), SurfaceOf(merged), "lifetime grants no permission surface")
	}
}

// The requesting kit's kind and contribution order cannot weaken a
// required declaration, and a mixin's request survives without any
// matching request on the workload.
func TestAutoRestartMixinComposition(t *testing.T) {
	required := []Capability{{Type: CapabilityAutoRestart}}
	optional := []Capability{{Type: CapabilityAutoRestart, Optional: true}}
	for _, tc := range []struct {
		name            string
		workload, mixin []Capability
		wantOptional    bool
	}{
		{"mixin alone requires", nil, required, false},
		{"mixin alone prefers", nil, optional, true},
		{"mixin strengthens workload", optional, required, false},
		{"workload strengthens mixin", required, optional, false},
		{"both optional", optional, optional, true},
		{"both required", required, required, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workload := Contribution{Reference: "workload", Descriptor: &Descriptor{
				SchemaVersion: SchemaVersion, Kind: KindWorkload, Capabilities: tc.workload,
			}}
			mixin := Contribution{Reference: "mixin", Descriptor: &Descriptor{
				SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: tc.mixin,
			}}
			for _, inputs := range [][]Contribution{{workload, mixin}, {mixin, workload}} {
				merged := mergeOK(t, inputs...).Descriptor
				_, err := Validate(merged)
				require.NoError(t, err)
				require.Equal(t, KindWorkload, merged.Kind)
				require.Equal(t, []Capability{{Type: CapabilityAutoRestart, Optional: tc.wantOptional}}, merged.Capabilities)
			}
		})
	}
}

func TestAutoRestartSelection(t *testing.T) {
	for _, optional := range []bool{false, true} {
		d := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin,
			Capabilities: []Capability{{Type: CapabilityAutoRestart, Optional: optional}}}
		selected, err := SelectCapabilities(t.Context(), "service", d, Supported(CapabilityAutoRestart))
		require.NoError(t, err)
		require.Len(t, selected.Capabilities, 1)
		require.Equal(t, CapabilityAutoRestart, selected.Capabilities[0].Type)
		require.Equal(t, optional, selected.Capabilities[0].Optional)

		// Knowing the session-lifetime capability cannot satisfy recovery.
		// These exact claims are independent even though both affect lifetime.
		selected, err = SelectCapabilities(t.Context(), "service", d, Supported(CapabilityLongRunning))
		if optional {
			require.NoError(t, err)
			require.Empty(t, selected.Capabilities)
			require.NotEmpty(t, selected.Skipped)
		} else {
			require.ErrorContains(t, err, CapabilityAutoRestart)
		}
	}
}

func TestAutoRestartAndLongRunningRemainIndependent(t *testing.T) {
	for _, caps := range [][]Capability{
		{{Type: CapabilityAutoRestart}},
		{{Type: CapabilityAutoRestart}, {Type: CapabilityLongRunning}},
	} {
		d := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindWorkload, Capabilities: caps}
		merged := mergeOK(t, Contribution{Reference: "service", Descriptor: d}).Descriptor
		_, err := Validate(merged)
		require.NoError(t, err)
		require.Equal(t, caps, merged.Capabilities, "composition does not invent or collapse lifetime requests")
		require.Equal(t, SurfaceOf(&Descriptor{}), SurfaceOf(merged))
	}
}
