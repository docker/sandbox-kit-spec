package spec

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestFUSEValidation(t *testing.T) {
	base := func(caps ...Capability) *Descriptor {
		return &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: caps}
	}
	for _, optional := range []bool{false, true} {
		_, err := Validate(base(Capability{Type: CapabilityFUSE, Optional: optional}))
		require.NoError(t, err)
	}

	_, err := Validate(base(
		Capability{Type: CapabilityFUSE},
		Capability{Type: CapabilityFUSE, Optional: true},
	))
	require.ErrorContains(t, err, "already declared")

	for _, spelling := range []string{"{}", "null", `{"allowOther": true}`} {
		t.Run(spelling, func(t *testing.T) {
			var fromYAML, fromJSON Capability
			require.NoError(t, yaml.Unmarshal([]byte("type: "+CapabilityFUSE+"\nconfig: "+spelling+"\n"), &fromYAML))
			require.NoError(t, json.Unmarshal([]byte(`{"type":"`+CapabilityFUSE+`","config":`+spelling+`}`), &fromJSON))
			for _, entry := range []Capability{fromYAML, fromJSON} {
				_, err := Validate(base(entry))
				require.ErrorContains(t, err, "takes no config")
			}
		})
	}
}

// FUSE has no config, so its presence is the grant and surfaces as the
// type in the services list, as kvm@1 does. Container elevation does not
// preapprove it: a privileged sandbox may still filter /dev/fuse.
func TestFUSESurfaceRequiresIndependentApproval(t *testing.T) {
	require.False(t, PrivilegedOf([]Capability{{Type: CapabilityFUSE}}), "FUSE does not request container elevation")
	for _, existing := range []string{"", CapabilityPrivileged, CapabilityDockerd, CapabilityKVM} {
		t.Run("existing="+existing, func(t *testing.T) {
			d := &Descriptor{}
			if existing != "" {
				d.Capabilities = []Capability{{Type: existing}}
			}
			granted := SurfaceOf(d)
			d.Capabilities = append(d.Capabilities, Capability{Type: CapabilityFUSE, Optional: true, Description: "Mounts a bucket"})
			candidate := SurfaceOf(d)
			require.Equal(t, []Widening{{Category: "services", Detail: CapabilityFUSE}}, DiffWidenings(granted, candidate))
			require.Empty(t, DiffWidenings(candidate, granted), "withdrawal is not a widening")
		})
	}
}
