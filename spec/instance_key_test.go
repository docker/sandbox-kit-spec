package spec_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/docker/sandbox-kit-spec/v3/spec"
	"github.com/stretchr/testify/require"
)

// Literal expectations pin the public encoding, including the types without
// keys. A newly registered type needs an explicit decision in this table.
func TestInstanceKey(t *testing.T) {
	tests := []struct {
		name  string
		typ   string
		cfg   any
		space string
		parts []string
	}{
		{"acp short name", spec.CapabilityACP, spec.ACP{Agent: "claude"}, spec.CapabilityACP, []string{"com.docker.kit/claude"}},
		{"acp qualified name", spec.CapabilityACP, spec.ACP{Agent: "com.docker.kit/claude"}, spec.CapabilityACP, []string{"com.docker.kit/claude"}},
		{"acp other namespace", spec.CapabilityACP, spec.ACP{Agent: "example.com/claude"}, spec.CapabilityACP, []string{"example.com/claude"}},
		{"credential", spec.CapabilityCredential, spec.Credential{Service: "github.com", Phase: []string{"runtime"}}, spec.CapabilityCredential, []string{"github.com", "runtime"}},
		{"ssh agent", spec.CapabilitySSHAgent, spec.SSHAgent{Phase: spec.SSHAgentPhases{"install"}}, spec.CapabilitySSHAgent, []string{"install"}},
		{"volume", spec.CapabilityVolume, spec.Volume{Path: "/data/cache"}, spec.CapabilityVolume, []string{"/data/cache"}},
		{"volume cleaned", spec.CapabilityVolume, spec.Volume{Path: "/data/./tmp/../cache/"}, spec.CapabilityVolume, []string{"/data/cache"}},
		{"host mount shared space", spec.CapabilityHostMount, spec.HostMount{Path: "/data/./cache/"}, spec.CapabilityVolume, []string{"/data/cache"}},
		{"skills destination", spec.CapabilityAgentSkills, spec.AgentSkills{Path: "/home/agent/skills"}, spec.CapabilityAgentSkills, []string{"/home/agent/skills"}},
		{"skill basename", spec.CapabilityAgentSkill, spec.AgentSkill{Path: "/skills/review"}, spec.CapabilityAgentSkill, []string{"review"}},
		{"skill explicit name", spec.CapabilityAgentSkill, spec.AgentSkill{Path: "/skills/source", Name: "review"}, spec.CapabilityAgentSkill, []string{"review"}},
		{"port default", spec.CapabilityPort, spec.Port{Container: 8080}, spec.CapabilityPort, []string{"8080", "tcp"}},
		{"port explicit tcp", spec.CapabilityPort, spec.Port{Container: 8080, Transport: "tcp", Family: "ipv4", Name: "web"}, spec.CapabilityPort, []string{"8080", "tcp"}},
		{"port udp", spec.CapabilityPort, spec.Port{Container: 53, Transport: "udp"}, spec.CapabilityPort, []string{"53", "udp"}},
		{"context v1", spec.CapabilityAgentContext, spec.AgentContext{Directory: "/home/agent/.codex", Filename: "AGENTS.md"}, spec.CapabilityAgentContext, []string{"/home/agent/.codex/AGENTS.md"}},
		{"context v2", spec.CapabilityAgentContextV2, spec.AgentContextV2{Directory: "/home/agent/.codex", Filename: "AGENTS.md", Content: "guidance"}, spec.CapabilityAgentContextV2, []string{"/home/agent/.codex/AGENTS.md"}},
		{"legacy context v1", spec.CapabilityAgentContext, spec.AgentContext{Filename: "AGENTS.md"}, spec.CapabilityAgentContext, []string{""}},
		{"legacy context v2", spec.CapabilityAgentContextV2, spec.AgentContextV2{Filename: "AGENTS.md"}, spec.CapabilityAgentContextV2, []string{""}},
		{"body only v1", spec.CapabilityAgentContext, spec.AgentContext{ContentFile: "/usr/share/sandbox/kit/tool/context.md"}, spec.CapabilityAgentContext, []string{""}},
		{"body only v2", spec.CapabilityAgentContextV2, spec.AgentContextV2{Content: "guidance"}, spec.CapabilityAgentContextV2, []string{""}},
		{"network v1", spec.CapabilityNetworkPolicy, spec.PhasedNetwork{}, "", nil},
		{"network v2", spec.CapabilityNetworkPolicyV2, spec.PhasedNetworkV2{}, "", nil},
		{"resources", spec.CapabilityResources, spec.Resources{CPU: 2}, "", nil},
		{"privileged", spec.CapabilityPrivileged, nil, "", nil},
		{"kit registry", spec.CapabilityKitRegistry, nil, "", nil},
		{"sessions", spec.CapabilityAgentSessions, spec.AgentSessions{}, "", nil},
		{"interactive sessions", spec.CapabilityAgentInteractiveSessions, spec.AgentInteractiveSessions{}, "", nil},
		{"lifecycle", spec.CapabilityLifecycle, spec.Lifecycle{}, "", nil},
		{"sbx", spec.CapabilitySbx, nil, "", nil},
		{"long running", spec.CapabilityLongRunning, nil, "", nil},
		{"git identity", spec.CapabilityGitIdentity, nil, "", nil},
		{"usb", spec.CapabilityUSBDevice, spec.USBDevice{VendorID: "1234"}, "", nil},
		{"unknown", "example.com/custom@1", map[string]any{"path": "/data"}, "", nil},
	}
	covered := map[string]bool{}
	for _, tt := range tests {
		covered[tt.typ] = true
		t.Run(tt.name, func(t *testing.T) {
			entry, err := spec.CapabilityWithConfig(spec.Capability{Type: tt.typ}, tt.cfg)
			require.NoError(t, err)
			before, err := json.Marshal(entry)
			require.NoError(t, err)
			space, parts, ok, err := spec.InstanceKey(*entry)
			require.NoError(t, err)
			require.Equal(t, tt.space, space)
			require.Equal(t, tt.parts, parts)
			require.Equal(t, tt.parts != nil, ok)
			after, err := json.Marshal(entry)
			require.NoError(t, err)
			require.Equal(t, before, after, "keying must not rewrite a declaration")

			entry.Name, entry.Description, entry.Optional = "display label", "explanation", true
			entry.Source = &spec.CapabilitySource{}
			space, parts, ok, err = spec.InstanceKey(*entry)
			require.NoError(t, err)
			require.Equal(t, tt.space, space)
			require.Equal(t, tt.parts, parts)
			require.Equal(t, tt.parts != nil, ok, "metadata does not identify a request")
		})
	}
	for _, typ := range spec.KnownCapabilities() {
		require.True(t, covered[typ], "missing public key contract for %s", typ)
	}
}

func TestInstanceKeyRejectsUnsplitPhases(t *testing.T) {
	for _, typ := range []string{spec.CapabilityCredential, spec.CapabilitySSHAgent} {
		for _, phase := range []any{nil, "", []string{}, []string{"install", "runtime"}, []string{"runtime", "runtime"}} {
			t.Run(fmt.Sprint(typ, "/", phase), func(t *testing.T) {
				space, parts, ok, err := spec.InstanceKey(spec.Capability{Type: typ, Config: map[string]any{"phase": phase}})
				require.ErrorContains(t, err, "needs exactly one phase")
				require.Empty(t, space)
				require.Nil(t, parts)
				require.False(t, ok)
			})
		}
		_, _, ok, err := spec.InstanceKey(spec.Capability{Type: typ})
		require.ErrorContains(t, err, "needs exactly one phase")
		require.False(t, ok)
	}
}

func TestInstanceKeyOfComposedEntries(t *testing.T) {
	for _, typ := range []string{spec.CapabilityCredential, spec.CapabilitySSHAgent} {
		t.Run(typ, func(t *testing.T) {
			entry := spec.Capability{Type: typ, Config: map[string]any{"phase": []string{"install", "runtime"}}}
			if typ == spec.CapabilityCredential {
				entry.Config["service"] = "github.com"
			}
			_, _, _, err := spec.InstanceKey(entry)
			require.ErrorContains(t, err, "needs exactly one phase")
			composed, err := spec.Compose([]spec.Contribution{{Reference: "phases", Descriptor: &spec.Descriptor{
				SchemaVersion: spec.SchemaVersion, Kind: spec.KindMixin, Capabilities: []spec.Capability{entry},
			}}})
			require.NoError(t, err)
			require.Len(t, composed.Capabilities, 2)
			for i, phase := range []string{"install", "runtime"} {
				space, parts, ok, err := spec.InstanceKey(composed.Capabilities[i])
				require.NoError(t, err)
				require.True(t, ok)
				require.Equal(t, typ, space)
				want := []string{phase}
				if typ == spec.CapabilityCredential {
					want = []string{"github.com", phase}
				}
				require.Equal(t, want, parts)
			}
			recomposed, err := spec.Compose([]spec.Contribution{{Reference: "again", Descriptor: composed}})
			require.NoError(t, err)
			require.Len(t, recomposed.Capabilities, 2)
			for i, entry := range recomposed.Capabilities {
				space, parts, ok, err := spec.InstanceKey(entry)
				require.NoError(t, err)
				require.True(t, ok)
				previousSpace, previousParts, _, err := spec.InstanceKey(composed.Capabilities[i])
				require.NoError(t, err)
				require.Equal(t, previousSpace, space)
				require.Equal(t, previousParts, parts)
			}
		})
	}
}

func TestInstanceKeyMatchesComposition(t *testing.T) {
	tests := []struct {
		typ string
		a   map[string]any
		b   map[string]any
	}{
		{spec.CapabilityACP, map[string]any{"agent": "claude"}, map[string]any{"agent": "com.docker.kit/claude"}},
		{spec.CapabilityVolume, map[string]any{"path": "/data/cache", "size": "1Gi"}, map[string]any{"path": "/data/./cache", "size": "1Gi"}},
		{spec.CapabilityPort, map[string]any{"container": 8080}, map[string]any{"container": 8080, "transport": "tcp"}},
		{spec.CapabilityAgentContext, map[string]any{"directory": "/profiles/claude", "filename": "CLAUDE.md"}, map[string]any{"directory": "/profiles/claude", "filename": "CLAUDE.md"}},
		{spec.CapabilityAgentContextV2, map[string]any{"directory": "/profiles/claude", "filename": "CLAUDE.md", "content": "one"}, map[string]any{"directory": "/profiles/claude", "filename": "CLAUDE.md", "content": "two"}},
	}
	for _, tt := range tests {
		t.Run(tt.typ, func(t *testing.T) {
			a := spec.Capability{Type: tt.typ, Config: tt.a}
			b := spec.Capability{Type: tt.typ, Config: tt.b}
			space, parts, ok, err := spec.InstanceKey(a)
			require.NoError(t, err)
			require.True(t, ok)
			otherSpace, otherParts, _, err := spec.InstanceKey(b)
			require.NoError(t, err)
			require.Equal(t, space, otherSpace)
			require.Equal(t, parts, otherParts)
			out, err := spec.Compose([]spec.Contribution{
				{Reference: "a", Descriptor: &spec.Descriptor{Kind: spec.KindMixin, Capabilities: []spec.Capability{a}}},
				{Reference: "b", Descriptor: &spec.Descriptor{Kind: spec.KindMixin, Capabilities: []spec.Capability{b}}},
			})
			require.NoError(t, err)
			require.Len(t, out.Capabilities, 1)
			composedSpace, composedParts, ok, err := spec.InstanceKey(out.Capabilities[0])
			require.NoError(t, err)
			require.True(t, ok)
			require.Equal(t, space, composedSpace)
			require.Equal(t, parts, composedParts)
		})
	}
	// Sharing a key space does not grant permission to merge requests.
	_, err := spec.Compose([]spec.Contribution{
		{Reference: "volume", Descriptor: &spec.Descriptor{Kind: spec.KindMixin, Capabilities: []spec.Capability{
			{Type: spec.CapabilityVolume, Config: map[string]any{"path": "/data/cache"}},
		}}},
		{Reference: "mount", Descriptor: &spec.Descriptor{Kind: spec.KindMixin, Capabilities: []spec.Capability{
			{Type: spec.CapabilityHostMount, Config: map[string]any{"path": "/data/./cache"}},
		}}},
	})
	require.ErrorContains(t, err, "host mount has one owner")
}

func TestInstanceKeyStrictDecoding(t *testing.T) {
	for _, typ := range []string{spec.CapabilityACP, spec.CapabilityCredential, spec.CapabilitySSHAgent,
		spec.CapabilityVolume, spec.CapabilityHostMount, spec.CapabilityAgentSkill,
		spec.CapabilityAgentSkills, spec.CapabilityPort, spec.CapabilityAgentContext, spec.CapabilityAgentContextV2} {
		t.Run(typ, func(t *testing.T) {
			for _, cfg := range []map[string]any{{"typo": true}, {"unencodable": make(chan int)}} {
				space, parts, ok, err := spec.InstanceKey(spec.Capability{Type: typ, Config: cfg})
				require.Error(t, err)
				require.Contains(t, err.Error(), typ)
				require.Empty(t, space)
				require.Nil(t, parts)
				require.False(t, ok)
			}
		})
	}
	space, parts, ok, err := spec.InstanceKey(spec.Capability{
		Type: spec.CapabilityPort, Config: map[string]any{"container": "8080"},
	})
	require.ErrorContains(t, err, "cannot unmarshal string")
	require.Empty(t, space)
	require.Nil(t, parts)
	require.False(t, ok)
}

func ExampleInstanceKey() {
	space, parts, ok, err := spec.InstanceKey(spec.Capability{
		Type:   spec.CapabilityHostMount,
		Config: map[string]any{"path": "/data/./cache"},
	})
	if err != nil || !ok {
		return
	}
	// Store the space and parts together, with an encoding of your choice.
	fmt.Println(space, parts)
	// Output: com.docker.sandbox/volume@1 [/data/cache]
}
