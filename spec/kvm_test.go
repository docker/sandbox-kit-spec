package spec

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestKVMValidation(t *testing.T) {
	for _, kind := range []string{KindWorkload, KindMixin, KindSet} {
		for _, optional := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/optional=%t", kind, optional), func(t *testing.T) {
				d := &Descriptor{SchemaVersion: SchemaVersion, Kind: kind, Capabilities: []Capability{{Type: CapabilityKVM, Optional: optional}}}
				if kind == KindSet {
					d.Kits = []Kit{{Ref: "example.com/workload:1"}}
				}
				_, err := Validate(d)
				require.NoError(t, err)
			})
		}
	}

	for _, value := range []string{"{}", "null", `{"devices":["/dev/kvm"]}`} {
		t.Run("config="+value, func(t *testing.T) {
			var fromYAML, fromJSON Capability
			require.NoError(t, yaml.Unmarshal([]byte("type: "+CapabilityKVM+"\nconfig: "+value+"\n"), &fromYAML))
			require.NoError(t, json.Unmarshal([]byte(`{"type":"`+CapabilityKVM+`","config":`+value+`}`), &fromJSON))
			for _, entry := range []Capability{fromYAML, fromJSON} {
				_, err := Validate(&Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{entry}})
				require.ErrorContains(t, err, "takes no config")
			}
		})
	}

	for _, entries := range [][]Capability{
		{{Type: CapabilityKVM}, {Type: CapabilityKVM, Optional: true}},
		{{Group: &CapabilityGroup{Capabilities: []Capability{{Type: CapabilityKVM}, {Type: CapabilityKVM}}}}},
	} {
		_, err := Validate(&Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: entries})
		require.ErrorContains(t, err, "already declared")
	}
}

func TestKVMMergeAndSurface(t *testing.T) {
	for _, optional := range []bool{false, true} {
		t.Run(fmt.Sprintf("optional=%t", optional), func(t *testing.T) {
			workload := Contribution{Reference: "workload", Descriptor: &Descriptor{
				SchemaVersion: SchemaVersion, Kind: KindWorkload,
				Capabilities: []Capability{{Type: CapabilityKVM, Optional: true}},
			}}
			mixin := Contribution{Reference: "mixin", Descriptor: &Descriptor{
				SchemaVersion: SchemaVersion, Kind: KindMixin,
				Capabilities: []Capability{{Type: CapabilityKVM, Optional: optional}},
			}}
			for _, inputs := range [][]Contribution{{workload, mixin}, {mixin, workload}} {
				merged := mergeOK(t, inputs...).Descriptor
				_, err := Validate(merged)
				require.NoError(t, err)
				require.Equal(t, []Capability{{Type: CapabilityKVM, Optional: optional}}, merged.Capabilities)
				require.False(t, PrivilegedOf(merged.Capabilities), "KVM does not request container elevation")
				surface := SurfaceOf(merged)
				require.Equal(t, Surface{Services: []string{CapabilityKVM}}, surface,
					"KVM grants neither elevation nor storage, credentials, or network access")
				require.Empty(t, DiffWidenings(surface, SurfaceOf(&Descriptor{})), "withdrawal is not a widening")
				merged.Capabilities[0].Optional = !optional
				merged.Capabilities[0].Name = "Inner virtual machines"
				merged.Capabilities[0].Description = "Runs hardware-accelerated sandboxes"
				require.Equal(t, surface, SurfaceOf(merged), "display metadata and optionality do not change permission")
			}
		})
	}
}

// A tool mixin must request hardware access without requiring the workload to
// repeat that request. Set-owned requests use the same composition contract.
func TestKVMMixinAndSetRequests(t *testing.T) {
	for _, contribution := range []Contribution{
		{Reference: "mixin", Descriptor: &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{{Type: CapabilityKVM}}}},
		{Reference: "set", Descriptor: &Descriptor{SchemaVersion: SchemaVersion, Capabilities: []Capability{{Type: CapabilityKVM}}}},
	} {
		t.Run(contribution.Reference, func(t *testing.T) {
			workload := Contribution{Reference: "workload", Descriptor: &Descriptor{SchemaVersion: SchemaVersion, Kind: KindWorkload}}
			for _, inputs := range [][]Contribution{{workload, contribution}, {contribution, workload}} {
				merged := mergeOK(t, inputs...).Descriptor
				_, err := Validate(merged)
				require.NoError(t, err)
				require.Equal(t, []Capability{{Type: CapabilityKVM}}, merged.Capabilities)
			}
		})
	}
}

func TestKVMSurfaceRequiresIndependentApproval(t *testing.T) {
	for _, existing := range []string{"", CapabilityPrivileged, CapabilityDockerd} {
		t.Run("existing="+existing, func(t *testing.T) {
			d := &Descriptor{}
			if existing != "" {
				d.Capabilities = []Capability{{Type: existing}}
			}
			granted := SurfaceOf(d)
			d.Capabilities = append(d.Capabilities, Capability{Type: CapabilityKVM})
			widenings := DiffWidenings(granted, SurfaceOf(d))
			require.Len(t, widenings, 1, "container elevation does not preapprove hardware virtualization")
			require.Equal(t, Widening{Category: "services", Detail: CapabilityKVM}, widenings[0])
		})
	}
}

func TestKVMSelection(t *testing.T) {
	for _, optional := range []bool{false, true} {
		d := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{{Type: CapabilityKVM, Optional: optional}}}
		selected, err := SelectCapabilities(t.Context(), "inner-vm", d, Supported(CapabilityKVM))
		require.NoError(t, err)
		require.Len(t, selected.Capabilities, 1)
		require.Equal(t, CapabilityKVM, selected.Capabilities[0].Type)
		require.Equal(t, optional, selected.Capabilities[0].Optional)
		selected, err = SelectCapabilities(t.Context(), "inner-vm", d, Supported())
		if optional {
			require.NoError(t, err)
			require.Empty(t, selected.Capabilities)
			require.Len(t, selected.Skipped, 1)
			require.Equal(t, []string{"capabilities[0]"}, selected.Skipped[0].Rejected)
			require.Contains(t, selected.Skipped[0].Decisions[0].Message, CapabilityKVM)
			require.Empty(t, SurfaceOf(&Descriptor{Capabilities: selected.Capabilities}).Services)
		} else {
			require.ErrorContains(t, err, CapabilityKVM)
		}
	}
}

func TestKVMOptionalGroupSelectsAtomically(t *testing.T) {
	// Coupled setup must not run after its hardware prerequisite was refused.
	d := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{
		{Group: &CapabilityGroup{Optional: true, Capabilities: []Capability{
			{Type: CapabilityKVM}, groupHook("prepare-inner-vm"),
		}}},
	}}
	_, err := Validate(d)
	require.NoError(t, err)
	for _, supported := range [][]string{{CapabilityKVM, CapabilityLifecycle}, {CapabilityLifecycle}} {
		selected, err := SelectCapabilities(t.Context(), "inner-vm", d, Supported(supported...))
		require.NoError(t, err)
		if len(supported) == 2 {
			require.Len(t, selected.Capabilities, 2)
			require.Equal(t, []string{CapabilityKVM}, SurfaceOf(&Descriptor{Capabilities: selected.Capabilities}).Services)
		} else {
			require.Empty(t, selected.Capabilities)
			require.Len(t, selected.Skipped, 1)
			require.Equal(t, []string{"capabilities[0].group.capabilities[0]"}, selected.Skipped[0].Rejected)
			require.Empty(t, SurfaceOf(&Descriptor{Capabilities: selected.Capabilities}).Services)
		}
	}
}
