package spec

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func skillEntry(source, name string) Capability {
	c := Capability{Type: CapabilityAgentSkill, Config: map[string]any{"path": source}}
	if name != "" {
		c.Config["name"] = name
	}
	return c
}

func TestAgentSkillValidation(t *testing.T) {
	for _, typ := range []string{CapabilityAgentSkill, CapabilityAgentSkills} {
		for _, value := range []any{"", "/", "relative", "/x/../y", "/x/./y", "/x//y", "/x/y/", nil, 1} {
			c := Capability{Type: typ, Config: map[string]any{"path": value}}
			_, err := Validate(&Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{c}})
			require.Error(t, err, "%s: %v", typ, value)
		}
		c := Capability{Type: typ, Config: map[string]any{"path": "/usr/share/skills/review"}}
		for _, kind := range []string{KindMixin, KindWorkload} {
			d := &Descriptor{SchemaVersion: SchemaVersion, Kind: kind, Capabilities: []Capability{c}}
			_, err := Validate(d)
			require.NoError(t, err)
			d.Capabilities = append(d.Capabilities, c)
			_, err = Validate(d)
			require.Error(t, err)
		}
		c.Config["typo"] = true
		_, err := Validate(&Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{c}})
		require.ErrorContains(t, err, "unknown field")
	}
	for _, value := range []any{"", nil, 1, ".", "..", "../review", "a/b", `a\b`, "a\x00b", "a\nb", ".hidden", strings.Repeat("a", 256)} {
		c := skillEntry("/skills/source", "")
		c.Config["name"] = value
		_, err := Validate(&Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{c}})
		require.Error(t, err, "name %v", value)
	}
	for _, source := range []string{"/skills/.hidden", "/skills/bad name"} {
		d := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{skillEntry(source, "")}}
		_, err := Validate(d)
		require.Error(t, err)
		d.Capabilities[0].Config["name"] = "review"
		_, err = Validate(d)
		require.NoError(t, err, "an override permits a differently named source")
	}
	d := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{skillEntry("/a/review", ""), skillEntry("/b/source", "review")}}
	_, err := Validate(d)
	require.ErrorContains(t, err, "already declared")
}

func TestAgentSkillComposition(t *testing.T) {
	first := skillEntry("/a/review", "")
	first.Name = "Display label"
	first.Optional = true
	second := skillEntry("/a/review", "review")
	destination := Capability{Type: CapabilityAgentSkills, Config: map[string]any{"path": "/home/agent/.example/skills"}}
	inputs := []Contribution{
		{Reference: "skill", Descriptor: &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{first}}},
		{Reference: "agent", Descriptor: &Descriptor{SchemaVersion: SchemaVersion, Kind: KindWorkload, Capabilities: []Capability{second, destination}}},
	}
	composed, err := Compose(inputs)
	require.NoError(t, err)
	require.Len(t, composed.Capabilities, 2)
	requests, err := AgentSkillRequestsOf(composed.Capabilities)
	require.NoError(t, err)
	require.Len(t, requests, 1)
	require.False(t, requests[0].Optional)
	require.Equal(t, "Display label", requests[0].DisplayName)
	require.Equal(t, "review", AgentSkillName(requests[0].AgentSkill))
	dirs, err := AgentSkillsOf(composed.Capabilities)
	require.NoError(t, err)
	require.Len(t, dirs, 1)
	require.Equal(t, destination.Config["path"], dirs[0].Path)
	require.Equal(t, SurfaceOf(&Descriptor{Capabilities: []Capability{destination}}), SurfaceOf(composed))
	published, err := Merge(inputs, MergeOptions{})
	require.NoError(t, err)
	raw, err := json.Marshal(published.Descriptor)
	require.NoError(t, err)
	decoded, err := Decode(raw)
	require.NoError(t, err)
	_, err = ValidatePublished(raw, decoded)
	require.NoError(t, err)
	republished, err := Merge([]Contribution{{Reference: "set", Descriptor: decoded}}, MergeOptions{})
	require.NoError(t, err)
	again, err := json.Marshal(republished.Descriptor)
	require.NoError(t, err)
	require.JSONEq(t, string(raw), string(again))
	// Same discovery name, different source: optionality and display labels do not resolve it.
	inputs[1].Descriptor.Capabilities[0] = skillEntry("/b/review", "")
	_, err = Compose(inputs)
	require.ErrorContains(t, err, "different things")
	_, err = Merge(inputs, MergeOptions{})
	require.ErrorContains(t, err, "different things")
}

func TestAgentSkillSelectionAndReaders(t *testing.T) {
	c := skillEntry("/skills/source", "review")
	c.Optional = true
	c.Name = "Label"
	got, err := AgentSkillRequestsOf([]Capability{c})
	require.NoError(t, err)
	require.Equal(t, "review", got[0].Name)
	require.Equal(t, "Label", got[0].DisplayName)
	require.Equal(t, "review", AgentSkillName(got[0].AgentSkill))
	require.True(t, got[0].Optional)
	delete(c.Config, "name")
	got, err = AgentSkillRequestsOf([]Capability{c})
	require.NoError(t, err)
	require.Empty(t, got[0].Name)
	require.Equal(t, "Label", got[0].DisplayName)
	require.Equal(t, "source", AgentSkillName(got[0].AgentSkill))
	for _, typ := range []string{CapabilityAgentSkill, CapabilityAgentSkills} {
		invalid := []Capability{{Type: typ, Config: map[string]any{"path": 7}}}
		if typ == CapabilityAgentSkill {
			_, err = AgentSkillRequestsOf(invalid)
		} else {
			_, err = AgentSkillsOf(invalid)
		}
		require.Error(t, err)
	}
	group := []Capability{{Group: &CapabilityGroup{Optional: true, Capabilities: []Capability{skillEntry("/a/review", "")}}}}
	_, err = AgentSkillRequestsOf(group)
	require.ErrorContains(t, err, "select groups first")
	_, err = AgentSkillsOf(group)
	require.ErrorContains(t, err, "select groups first")
}

func TestAgentSkillSchemas(t *testing.T) {
	s := loadJSON(t, perTypeSchemaPath(CapabilityAgentSkill))
	name := at(t, s, "properties", "name")
	literal := name["anyOf"].([]any)[0].(map[string]any)
	require.Equal(t, agentSkillName.String(), literal["pattern"])
	require.Equal(t, float64(255), literal["maxLength"])
	assertAcceptsKitArg(t, name, "bearing")
	for _, typ := range []string{CapabilityAgentSkill, CapabilityAgentSkills} {
		s = loadJSON(t, perTypeSchemaPath(typ))
		require.Equal(t, []any{"path"}, s["required"])
		assertAcceptsKitArg(t, at(t, s, "properties", "path"), "bearing")
	}
}

func TestAgentSkillDirectoriesCompose(t *testing.T) {
	destination := func(p string, optional bool) Capability {
		return Capability{Type: CapabilityAgentSkills, Optional: optional, Config: map[string]any{"path": p}}
	}
	inputs := []Contribution{
		{Reference: "agent-a", Descriptor: &Descriptor{SchemaVersion: SchemaVersion, Kind: KindWorkload, Capabilities: []Capability{destination("/skills/a", true)}}},
		{Reference: "agent-b", Descriptor: &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{destination("/skills/a", false), destination("/skills/b", true)}}},
	}
	got, err := Compose(inputs)
	require.NoError(t, err)
	dirs, err := AgentSkillsOf(got.Capabilities)
	require.NoError(t, err)
	require.Len(t, dirs, 2)
	require.False(t, dirs[0].Optional)
	require.True(t, dirs[1].Optional)
	require.Equal(t, "/skills/a", dirs[0].Path)
	require.Equal(t, "/skills/b", dirs[1].Path)
}

func TestAgentSkillGroupsAndExpansion(t *testing.T) {
	raw := []byte(`schemaVersion: "3"
kind: mixin
args:
  skill:
    default: review
capabilities:
  - group:
      optional: true
      capabilities:
        - type: com.docker.sandbox/agent-skill@1
          config:
            path: /usr/share/content
            name: "${{ kit.args.skill }}"
        - type: com.docker.sandbox/agent-skills@1
          config:
            path: /home/agent/.example/skills
`)
	d, err := Decode(raw)
	require.NoError(t, err)
	_, err = ValidateRaw(raw, d)
	require.NoError(t, err)
	for _, value := range []string{"review", "../escape"} {
		values, err := ResolveArgs(d.Args, map[string]string{"skill": value})
		require.NoError(t, err)
		expanded, err := ExpandCreateArgs(raw, d.Args, values)
		require.NoError(t, err)
		parsed, err := Decode(expanded)
		require.NoError(t, err)
		_, err = ValidatePublished(expanded, parsed)
		if value != "review" {
			require.Error(t, err)
			continue
		}
		require.NoError(t, err)
		selected, err := SelectCapabilities(t.Context(), parsed, Supported(CapabilityAgentSkill))
		require.NoError(t, err)
		require.Empty(t, selected.Capabilities)
		require.Len(t, selected.Skipped, 1)
		selected, err = SelectCapabilities(t.Context(), parsed, Supported(CapabilityAgentSkill, CapabilityAgentSkills))
		require.NoError(t, err)
		require.Len(t, selected.Capabilities, 2)
		skills, err := AgentSkillRequestsOf(selected.Capabilities)
		require.NoError(t, err)
		require.Equal(t, "review", AgentSkillName(skills[0].AgentSkill))
	}
}

func TestAgentSkillSourceIsKnownAtPublish(t *testing.T) {
	for _, grouped := range []bool{false, true} {
		c := skillEntry("/skills/${{ kit.args.source }}", "review")
		if grouped {
			c = Capability{Group: &CapabilityGroup{Optional: true, Capabilities: []Capability{c}}}
		}
		d := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Args: map[string]Arg{"source": {}}, Capabilities: []Capability{c}}
		raw, err := json.Marshal(d)
		require.NoError(t, err)
		_, err = ValidateRaw(raw, d)
		require.NoError(t, err)
		_, err = ValidatePublished(raw, d)
		require.ErrorContains(t, err, "published skill source path must be literal")
		if grouped {
			require.ErrorContains(t, err, "capabilities[0].group.capabilities[0].config.path")
		}
	}
}

func TestAgentSkillEnvironmentExpansion(t *testing.T) {
	d := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Capabilities: []Capability{
		skillEntry("/usr/share/source", "${{ kit.env.SKILL_NAME }}"),
		{Type: CapabilityAgentSkills, Config: map[string]any{"path": "${{ kit.env.HOME }}/.example/skills"}},
	}}
	expanded, err := ExpandEnvironment(d, map[string]string{"SKILL_NAME": "review", "HOME": "/home/agent"})
	require.NoError(t, err)
	_, err = Validate(expanded)
	require.NoError(t, err)
	skills, err := AgentSkillRequestsOf(expanded.Capabilities)
	require.NoError(t, err)
	require.Equal(t, "review", AgentSkillName(skills[0].AgentSkill))
	directories, err := AgentSkillsOf(expanded.Capabilities)
	require.NoError(t, err)
	require.Equal(t, "/home/agent/.example/skills", directories[0].Path)
	d.Capabilities[0].Config["path"] = "${{ kit.env.HOME }}/source"
	raw, err := json.Marshal(d)
	require.NoError(t, err)
	_, err = ValidatePublished(raw, d)
	require.ErrorContains(t, err, "published skill source path must be literal")
}

func TestAgentSkillLiteralSourceWithParameterizedName(t *testing.T) {
	for _, name := range []string{"${{ kit.args.name }}", "${{ kit.env.SKILL_NAME }}"} {
		for _, grouped := range []bool{false, true} {
			for _, tc := range []struct {
				label string
				path  any
			}{
				{"missing", nil}, {"null", nil}, {"number", 1}, {"boolean", true},
				{"array", []any{"/skills/review"}}, {"empty", ""}, {"relative", "skills/review"},
				{"root", "/"}, {"dot", "/skills/./review"}, {"parent", "/skills/../review"},
				{"separator", "/skills//review"}, {"trailing", "/skills/review/"},
				{"valid", "/skills/review"},
			} {
				t.Run(name+"/"+tc.label+"/"+map[bool]string{false: "ordinary", true: "group"}[grouped], func(t *testing.T) {
					c := Capability{Type: CapabilityAgentSkill, Config: map[string]any{"name": name}}
					if tc.label != "missing" {
						c.Config["path"] = tc.path
					}
					field := "capabilities[0].config.path"
					if grouped {
						c = Capability{Group: &CapabilityGroup{Optional: true, Capabilities: []Capability{c}}}
						field = "capabilities[0].group.capabilities[0].config.path"
					}
					d := &Descriptor{SchemaVersion: SchemaVersion, Kind: KindMixin, Args: map[string]Arg{"name": {}}, Capabilities: []Capability{c}}
					raw, err := json.Marshal(d)
					require.NoError(t, err)
					decoded, err := Decode(raw)
					require.NoError(t, err)
					_, authoredErr := ValidateRaw(raw, decoded)
					_, publishedErr := ValidatePublished(raw, decoded)
					if tc.label == "valid" {
						require.NoError(t, authoredErr)
						require.NoError(t, publishedErr)
					} else {
						require.ErrorContains(t, authoredErr, field)
						require.ErrorContains(t, publishedErr, field)
					}
				})
			}
		}
	}
}
