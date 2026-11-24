package spec

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func skillsMergeEntry(mode string, optional bool) Capability {
	entry := Capability{Type: CapabilityAgentSkills, Optional: optional, Config: map[string]any{"path": "/skills"}}
	if mode != "" {
		entry.Config["mode"] = mode
	}
	return entry
}

// A shared destination has one host mount, so both publication and runtime
// composition must preserve the widest request independently of Kit order.
func TestAgentSkillsMergeUsesWidestMode(t *testing.T) {
	modes := []string{"", SkillsOff, SkillsReadOnly, SkillsReadWrite}
	want := [][]string{
		{SkillsOff, SkillsOff, SkillsReadOnly, SkillsReadWrite},
		{SkillsOff, SkillsOff, SkillsReadOnly, SkillsReadWrite},
		{SkillsReadOnly, SkillsReadOnly, SkillsReadOnly, SkillsReadWrite},
		{SkillsReadWrite, SkillsReadWrite, SkillsReadWrite, SkillsReadWrite},
	}
	for i, firstMode := range modes {
		for j, secondMode := range modes {
			t.Run(fmt.Sprintf("%q+%q", firstMode, secondMode), func(t *testing.T) {
				first, second := skillsMergeEntry(firstMode, true), skillsMergeEntry(secondMode, false)
				first.Name, first.Description = "first destination", "first declaration"
				second.Name, second.Description = "second destination", "second declaration"
				other := skillsMergeEntry(SkillsOff, true)
				other.Config["path"] = "/other"
				inputs := []Contribution{
					{Reference: "first", Descriptor: &Descriptor{SchemaVersion: SchemaVersion, Kind: KindWorkload, Capabilities: []Capability{first}}},
					{Reference: "second", Descriptor: &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{second, other}}},
				}
				before, err := json.Marshal(inputs)
				require.NoError(t, err)
				composed, err := Compose(inputs)
				require.NoError(t, err)
				published, err := Merge(inputs, MergeOptions{})
				require.NoError(t, err)
				for _, result := range []*Descriptor{composed, published.Descriptor} {
					_, err = Validate(result)
					require.NoError(t, err)
					dirs, err := AgentSkillsOf(result.Capabilities)
					require.NoError(t, err)
					require.Len(t, dirs, 2)
					require.Equal(t, "/skills", dirs[0].Path)
					require.Equal(t, "/other", dirs[1].Path)
					require.Equal(t, want[i][j], SkillsMode(dirs[0].AgentSkills))
					require.Equal(t, SkillsOff, SkillsMode(dirs[1].AgentSkills))
					require.False(t, dirs[0].Optional, "a required contributor wins even when it asks for less access")
					require.True(t, dirs[1].Optional)
					require.Equal(t, first.Name, result.Capabilities[0].Name)
					require.Equal(t, first.Description, result.Capabilities[0].Description)
					surface := SurfaceOf(result)
					if want[i][j] == SkillsOff {
						require.Empty(t, surface.SkillsPaths)
					} else {
						require.Equal(t, []string{"/skills"}, surface.SkillsPaths)
					}
					if want[i][j] == SkillsReadWrite {
						require.Equal(t, []string{"/skills"}, surface.SkillsWritePaths)
					} else {
						require.Empty(t, surface.SkillsWritePaths)
					}
				}
				after, err := json.Marshal(inputs)
				require.NoError(t, err)
				require.JSONEq(t, string(before), string(after), "reconciliation must not rewrite the contributors")
			})
		}
	}
}

func TestAgentSkillsMergeOptionalityAndName(t *testing.T) {
	for _, firstOptional := range []bool{false, true} {
		for _, secondOptional := range []bool{false, true} {
			first, second := skillsMergeEntry(SkillsReadOnly, firstOptional), skillsMergeEntry(SkillsReadWrite, secondOptional)
			second.Name = "shared skills"
			result, err := Compose([]Contribution{
				{Reference: "first", Descriptor: &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{first}}},
				{Reference: "second", Descriptor: &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{second}}},
			})
			require.NoError(t, err)
			require.Len(t, result.Capabilities, 1)
			require.Equal(t, firstOptional && secondOptional, result.Capabilities[0].Optional)
			require.Equal(t, second.Name, result.Capabilities[0].Name)
		}
	}
}

// Selection can remove a wider optional request. The published declarations
// must retain that boundary instead of widening the required destination early.
func TestAgentSkillsGroupSelectionUsesWidestSelectedMode(t *testing.T) {
	descriptor := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindWorkload, Capabilities: []Capability{
		skillsMergeEntry("", false),
		{Group: &CapabilityGroup{Optional: true, Capabilities: []Capability{
			skillsMergeEntry(SkillsReadWrite, false), {Type: "example.com/sharing@1"},
		}}},
	}}
	_, err := Validate(descriptor)
	require.NoError(t, err)
	published, err := Merge([]Contribution{{Reference: "agent", Descriptor: descriptor}}, MergeOptions{})
	require.NoError(t, err)
	for _, acceptGroup := range []bool{false, true} {
		t.Run(fmt.Sprintf("accept-group=%t", acceptGroup), func(t *testing.T) {
			supported := []string{CapabilityAgentSkills}
			if acceptGroup {
				supported = append(supported, "example.com/sharing@1")
			}
			selection, err := SelectCapabilities(t.Context(), "published", published.Descriptor, Supported(supported...))
			require.NoError(t, err)
			selected := *published.Descriptor
			selected.Capabilities = selection.Capabilities
			result, err := Compose([]Contribution{{Reference: "selected", Descriptor: &selected}})
			require.NoError(t, err)
			dirs, err := AgentSkillsOf(result.Capabilities)
			require.NoError(t, err)
			require.Len(t, dirs, 1)
			require.False(t, dirs[0].Optional)
			if acceptGroup {
				require.Equal(t, SkillsReadWrite, SkillsMode(dirs[0].AgentSkills))
				require.Equal(t, []string{"/skills"}, SurfaceOf(result).SkillsWritePaths)
			} else {
				require.Equal(t, SkillsOff, SkillsMode(dirs[0].AgentSkills))
				require.Empty(t, SurfaceOf(result).SkillsPaths)
			}
		})
	}
}

func TestAgentSkillsDifferentModesWithinOneKitStillConflict(t *testing.T) {
	for _, modes := range [][2]string{{"", SkillsReadOnly}, {"", SkillsReadWrite}, {SkillsOff, SkillsReadOnly}, {SkillsOff, SkillsReadWrite}, {SkillsReadOnly, SkillsReadWrite}} {
		for _, pair := range [][2]string{modes, {modes[1], modes[0]}} {
			_, err := Validate(&Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{
				skillsMergeEntry(pair[0], false), skillsMergeEntry(pair[1], false),
			}})
			require.ErrorContains(t, err, "already declared")
		}
	}
}

// Publication may still carry a re-exported mode. It cannot compare that
// mode with a literal until create-time expansion makes the grant concrete.
func TestAgentSkillsMergeDoesNotDiscardParameterizedModes(t *testing.T) {
	for _, literal := range []string{SkillsOff, SkillsReadOnly, SkillsReadWrite} {
		for _, modes := range [][2]string{{"${{ kit.args.mode }}", literal}, {literal, "${{ kit.args.mode }}"}} {
			_, err := Merge([]Contribution{
				{Reference: "first", Descriptor: &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{skillsMergeEntry(modes[0], false)}}},
				{Reference: "second", Descriptor: &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{skillsMergeEntry(modes[1], false)}}},
			}, MergeOptions{})
			require.ErrorContains(t, err, "ask for different things")
		}
	}
}
