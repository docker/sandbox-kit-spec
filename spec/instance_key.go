package spec

import (
	"fmt"
	"path"
	"strconv"
)

// InstanceKey returns the §7.1 identity of one instance-shaped capability
// entry. The identity is the key space together with the ordered parts;
// parts alone are not unique across types. The key space is c.Type except
// that host-mount@1 uses CapabilityVolume, sharing volume@1's path space.
//
// The parts have this stable encoding (type names omit com.docker.sandbox/):
//
//   - acp@1: the NormalizeCapabilityName form of config.agent.
//   - credential@1: config.service, then its single phase.
//   - ssh-agent@1: its single phase.
//   - volume@1 and host-mount@1: path.Clean(config.path), using POSIX paths.
//   - agent-skills@1: config.path, whose canonical form validation enforces.
//   - agent-skill@1: AgentSkillName, including its source-basename fallback.
//   - port@1: the container port in base ten, then PortTransport (default tcp).
//   - agent-context@1 and agent-context@2: the POSIX path.Join of directory
//     and filename for an explicit profile; one empty string when directory
//     is absent, identifying the undirected entry independently of its body
//     or legacy filename. The two versions have separate key spaces.
//
// ok is false, with an empty key space and nil parts, for singleton types,
// usb-device@1, and unknown types: none has an instance key beyond its type
// or whole request. An error also returns those empty values and ok false.
//
// Call this on a validated, expanded entry, normally from Compose. It
// strictly decodes configs for keyed types, but does not validate a
// descriptor, expand arguments, select groups, or split phases. credential@1
// and ssh-agent@1 require exactly one phase; zero or several is an error.
// Entry metadata, including Name, Optional, and Source, does not affect keys.
//
// Equal keys identify requests to reconcile; each capability's contract
// decides whether those requests merge or conflict. Callers may persist the
// key space and parts using their own unambiguous encoding. Changing a
// type's key space, part order, or normalization is a breaking capability
// contract change governed by RELEASES.md's capability versioning rules.
func InstanceKey(c Capability) (keySpace string, parts []string, ok bool, err error) {
	keySpace = c.Type
	switch c.Type {
	case CapabilityACP:
		var a ACP
		err = DecodeCapabilityConfig(c, &a)
		parts = []string{NormalizeCapabilityName(a.Agent)}
	case CapabilityCredential:
		var credential Credential
		err = DecodeCapabilityConfig(c, &credential)
		if err == nil {
			if len(credential.Phase) != 1 || credential.Phase[0] == "" {
				err = fmt.Errorf("credential needs exactly one phase after expansion")
			} else {
				parts = []string{credential.Service, credential.Phase[0]}
			}
		}
	case CapabilitySSHAgent:
		var a SSHAgent
		err = DecodeCapabilityConfig(c, &a)
		if err == nil {
			if len(a.Phase) != 1 || a.Phase[0] == "" {
				err = fmt.Errorf("ssh-agent needs exactly one phase after expansion")
			} else {
				parts = []string{a.Phase[0]}
			}
		}
	case CapabilityVolume:
		var v Volume
		err = DecodeCapabilityConfig(c, &v)
		parts = []string{path.Clean(v.Path)}
	case CapabilityHostMount:
		var mount HostMount
		err = DecodeCapabilityConfig(c, &mount)
		keySpace = CapabilityVolume
		parts = []string{path.Clean(mount.Path)}
	case CapabilityAgentSkill:
		var s AgentSkill
		err = DecodeCapabilityConfig(c, &s)
		parts = []string{AgentSkillName(s)}
	case CapabilityAgentSkills:
		var s AgentSkills
		err = DecodeCapabilityConfig(c, &s)
		parts = []string{s.Path}
	case CapabilityPort:
		var p Port
		err = DecodeCapabilityConfig(c, &p)
		parts = []string{strconv.Itoa(p.Container), PortTransport(p)}
	case CapabilityAgentContext, CapabilityAgentContextV2:
		var directory, filename string
		if c.Type == CapabilityAgentContext {
			var a AgentContext
			err = DecodeCapabilityConfig(c, &a)
			directory, filename = a.Directory, a.Filename
		} else {
			var a AgentContextV2
			err = DecodeCapabilityConfig(c, &a)
			directory, filename = a.Directory, a.Filename
		}
		destination := ""
		if directory != "" {
			destination = path.Join(directory, filename)
		}
		parts = []string{destination}
	default:
		return "", nil, false, nil
	}
	if err != nil {
		return "", nil, false, err
	}
	return keySpace, parts, true, nil
}
