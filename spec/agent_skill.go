package spec

import (
	"fmt"
	"path"
	"regexp"
)

// AgentSkill identifies a complete skill directory in the published image.
// Name overrides its basename at discovery destinations, not in the source tree.
type AgentSkill struct {
	Path string `yaml:"path" json:"path"`
	Name string `yaml:"name,omitempty" json:"name,omitempty"`
}

// AgentSkillCapability exposes config.Name as Name and the entry label as
// DisplayName. Use AgentSkillName(request.AgentSkill) for the effective directory
// name, including the source-basename fallback when config.Name is omitted.
type AgentSkillCapability struct {
	AgentSkill
	Optional    bool
	DisplayName string
	Description string
}

var agentSkillName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// AgentSkillName returns the directory component used at every destination.
func AgentSkillName(s AgentSkill) string {
	if s.Name != "" {
		return s.Name
	}
	return path.Base(s.Path)
}

// A name chosen at create does not defer the source's literal grammar:
// publishers and artifact checks need a usable image path already.
func validateParameterizedSkillSource(field string, n Capability) error {
	source, ok := n.Config["path"].(string)
	if !ok {
		return fieldErrorf(field+".config.path", "skill source path must be a string")
	}
	if ContainsArgRef(source) || ContainsEnvRef(source) {
		// Authored sources may still contain build arguments. Publication
		// separately rejects any references that remain after that expansion.
		return nil
	}
	if source == "/" || !canonicalAbsPath(source) {
		return fieldErrorf(field+".config.path", "skill path %q must be absolute and canonical, not the root", source)
	}
	return nil
}

func validateBundledSkill(field string, n Capability) (string, error) {
	var s AgentSkill
	if err := DecodeCapabilityConfig(n, &s); err != nil {
		return "", fieldErrorf(configErrorPath(field, err), "%v", err)
	}
	source, key := s.Path, AgentSkillName(s)
	if _, present := n.Config["name"]; present && s.Name == "" {
		return "", fieldErrorf(field+".config.name", "omit name to use the source basename; an explicit name must not be empty or null")
	}
	if !agentSkillName.MatchString(key) || len(key) > 255 {
		return "", fieldErrorf(field+".config.name", "effective skill name %q must match %s and be at most 255 bytes", key, agentSkillName)
	}
	if source == "/" || !canonicalAbsPath(source) {
		return "", fieldErrorf(field+".config.path", "skill path %q must be absolute and canonical, not the root", source)
	}
	return key, nil
}

// AgentSkillRequestsOf reads selected, flat declarations in contribution order.
// It preserves optionality so a runtime can refuse or record unavailable skills.
func AgentSkillRequestsOf(capabilities []Capability) ([]AgentSkillCapability, error) {
	var out []AgentSkillCapability
	for _, c := range capabilities {
		if c.Group != nil {
			return nil, fmt.Errorf("agent skills: select groups first")
		}
		if c.Type != CapabilityAgentSkill {
			continue
		}
		var s AgentSkill
		if err := DecodeCapabilityConfig(c, &s); err != nil {
			return nil, err
		}
		out = append(out, AgentSkillCapability{AgentSkill: s, Optional: c.Optional, DisplayName: c.Name, Description: c.Description})
	}
	return out, nil
}

// A source is image inventory, so its location must be known when the
// artifact is checked, even if its discovery name is chosen at create.
func validatePublishedSkillPaths(items []Capability, prefix string) error {
	var errs ValidationErrors
	for i, c := range items {
		field := fmt.Sprintf("%s[%d]", prefix, i)
		if c.Group != nil {
			errs.add(validatePublishedSkillPaths(c.Group.Capabilities, field+".group.capabilities"))
		} else if c.Type == CapabilityAgentSkill {
			if source, ok := c.Config["path"].(string); ok && (ContainsArgRef(source) || ContainsEnvRef(source)) {
				errs.add(fieldErrorf(field+".config.path", "published skill source path must be literal; use a build-phase argument for image content"))
			}
		}
	}
	return errs.err()
}
