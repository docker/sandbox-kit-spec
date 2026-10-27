package spec

import (
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"
)

// Arity is what identifies the entries of a capability type, per §7.1.
// It belongs to the type: every entry of one type has the same arity.
type Arity string

const (
	// ArityInstance types appear once per thing requested, and an entry's
	// InstanceKey identifies it.
	ArityInstance Arity = "instance"
	// AritySingleton types appear at most once, so the type alone
	// identifies the entry.
	AritySingleton Arity = "singleton"
	// ArityWholeEntry types are instance-shaped with no key beyond the
	// exact entry: two entries are one request only when they are
	// identical. usb-device@1 is one.
	ArityWholeEntry Arity = "whole-entry"
	// ArityUnknown is a type this library does not define. Its config
	// rides opaquely (§7.3); composition deduplicates its entries as
	// whole entries, but what identifies them is the type's own contract.
	ArityUnknown Arity = "unknown"
)

// ArityOf returns the arity of a capability type.
func ArityOf(typ string) Arity {
	if _, keyed := instanceKeys[typ]; keyed {
		return ArityInstance
	}
	switch {
	case singletonCapabilities[typ]:
		return AritySingleton
	case typ == CapabilityUSBDevice:
		return ArityWholeEntry
	}
	return ArityUnknown
}

// InstanceKey is the identity §7.1 gives one entry of an instance-shaped
// capability type. Entries with equal keys request the same thing however
// each was spelled, though not necessarily on the same terms: the type's
// composition contract decides whether they merge or conflict, and a
// decision about a request, such as a user's consent, belongs to the
// whole entry.
//
// Integrations may store keys, so what each type's key holds is part of
// that type's contract: changing it moves the type's version, as
// RELEASES.md says.
type InstanceKey struct {
	// Space is the key space Parts belong to: the entry's type, except
	// host-mount@1, which keys into volume@1's storage path space.
	// agent-context@1 and @2 keep separate spaces; they never compose
	// together.
	Space string

	// Parts is the key within Space, in order:
	//
	//   - acp@1: the agent name, normalized by NormalizeCapabilityName
	//   - credential@1: the service, then the phase
	//   - ssh-agent@1: the phase
	//   - volume@1, host-mount@1: the path, cleaned by path.Clean
	//   - agent-skills@1: the path, which validation holds to canonical form
	//   - agent-skill@1: the effective name, as AgentSkillName gives it
	//   - port@1: PortKey, the container port and transport, tcp when
	//     omitted (8080/tcp)
	//   - agent-context@1, agent-context@2: the profile destination,
	//     path.Join of directory and filename; none for an entry without
	//     a directory, of which a composed descriptor holds at most one
	//
	// They are parts rather than one string so each integration can join
	// them with a separator its own storage carries.
	Parts []string
}

// InstanceKeyOf returns the §7.1 key of one composed capability entry,
// the key composition reconciles entries on, with the arity of its type.
// The key is set only for ArityInstance; any other arity says what
// identifies the entry instead.
//
// The entry must be one composed request. credential@1 and ssh-agent@1
// key on one phase, so an entry naming several is an error; composition
// splits such an entry per phase. A group is an error too: select it
// first. So is a config still holding an arg or environment reference,
// whose key is not known until expansion replaces it. On error, key and
// arity are zero. The config is decoded strictly but not validated.
func InstanceKeyOf(c Capability) (key InstanceKey, arity Arity, err error) {
	if ArityOf(c.Type) == ArityInstance && capabilityIsParameterized(c) {
		return InstanceKey{}, "", fmt.Errorf("capability %s: config still holds a reference; expand it before keying", c.Type)
	}
	return keyAsWritten(c)
}

// keyAsWritten is InstanceKeyOf for an entry that may still hold
// references, keying them on their text. Publication needs it: a set
// re-exporting an arg merges entries before anything expands them, though
// it reconciles only those whose key is literal: publication keeps any
// whose key still holds a reference apart until create
// (keyHoldsReference).
func keyAsWritten(c Capability) (InstanceKey, Arity, error) {
	if c.Group != nil {
		return InstanceKey{}, "", errors.New("capability group has no instance key; select it first")
	}
	key, keyed := instanceKeys[c.Type]
	if !keyed {
		return InstanceKey{}, ArityOf(c.Type), nil
	}
	parts, err := key.parts(c)
	if err != nil {
		return InstanceKey{}, "", err
	}
	space := c.Type
	if c.Type == CapabilityHostMount {
		space = CapabilityVolume
	}
	return InstanceKey{Space: space, Parts: parts}, ArityInstance, nil
}

// instanceKeys holds each instance-shaped type's key. ArityOf reads its
// keys as the instance-shaped types, and validation keys a declaration
// block's entries through it, so they agree on what identifies an entry.
var instanceKeys = map[string]keyDefinition{
	CapabilityACP: keyOn([]string{"agent"}, func(a ACP) ([]string, error) {
		return []string{NormalizeCapabilityName(a.Agent)}, nil
	}),
	CapabilityCredential: keyOn([]string{"service", "phase"}, func(c Credential) ([]string, error) {
		phase, err := onlyPhase(c.Phase)
		return []string{c.Service, phase}, err
	}),
	// One socket per phase, whichever kits asked: the bounds are what that
	// socket signs, not which socket it is.
	CapabilitySSHAgent: keyOn([]string{"phase"}, func(a SSHAgent) ([]string, error) {
		phase, err := onlyPhase(a.Phase)
		return []string{phase}, err
	}),
	// The destination, not the spelling: validation asks only that a
	// volume path be absolute, so /data/cache and /data/./cache are one
	// mount written two ways, and keying on the text would emit both and
	// leave a runtime to reconcile sizes nobody agreed on.
	CapabilityVolume: keyOn([]string{"path"}, func(v Volume) ([]string, error) {
		return []string{path.Clean(v.Path)}, nil
	}),
	CapabilityHostMount: keyOn([]string{"path"}, func(m HostMount) ([]string, error) {
		return []string{path.Clean(m.Path)}, nil
	}),
	CapabilityAgentSkill: keyOn([]string{"path", "name"}, func(s AgentSkill) ([]string, error) {
		return []string{AgentSkillName(s)}, nil
	}).defaulting("name"),
	CapabilityAgentSkills: keyOn([]string{"path"}, func(s AgentSkills) ([]string, error) {
		return []string{s.Path}, nil
	}),
	CapabilityPort: keyOn([]string{"container", "transport"}, func(p Port) ([]string, error) {
		return []string{PortKey(p)}, nil
	}).defaulting("transport"),
	CapabilityAgentContext: keyOn([]string{"directory", "filename"}, func(a AgentContext) ([]string, error) {
		return contextProfile(a.Directory, a.Filename), nil
	}),
	CapabilityAgentContextV2: keyOn([]string{"directory", "filename"}, func(a AgentContextV2) ([]string, error) {
		return contextProfile(a.Directory, a.Filename), nil
	}),
}

// keyDefinition is one instance-shaped type's key.
type keyDefinition struct {
	// fields names every config field parts reads, which is what lets
	// validation key an authored entry on those fields alone.
	fields []string
	// defaulted names the fields an entry may leave out or state null and
	// still be keyed, parts reading their default. Leaving out any other
	// field in fields, or stating it null, leaves an entry without a key.
	defaulted []string
	parts     func(Capability) ([]string, error)
}

// defaulting names the fields an entry may leave out or state null,
// parts reading their default.
func (k keyDefinition) defaulting(fields ...string) keyDefinition {
	k.defaulted = fields
	return k
}

// keyOn keys an entry on fields, decoding its config strictly as T first.
func keyOn[T any](fields []string, key func(T) ([]string, error)) keyDefinition {
	return keyDefinition{fields: fields, parts: func(c Capability) ([]string, error) {
		var config T
		if err := DecodeCapabilityConfig(c, &config); err != nil {
			return nil, err
		}
		parts, err := key(config)
		if err != nil {
			return nil, fmt.Errorf("capability %s: %w", c.Type, err)
		}
		return parts, nil
	}}
}

// literalInstanceKeys returns the keys an authored entry's literal fields
// already settle, so validation can hold a declaration block to one entry
// per key before expansion or a merge separates or folds the entries. It
// reads only the fields its type's key reads, so another field that is
// invalid or still holds a reference cannot hide a repeated key. A key
// field that still holds a reference gives no key, as does one the key
// cannot read, such as an undecodable value or an empty phase. So does
// leaving out a field that has no default, or stating it null; a field
// that has one reads its default either way, as decoding does. An entry
// naming several phases gives one key per literal phase, as composition
// splits it.
func literalInstanceKeys(n Capability) []InstanceKey {
	definition, keyed := instanceKeys[n.Type]
	if !keyed || n.Group != nil {
		return nil
	}
	fields := make(map[string]any, len(definition.fields))
	for _, field := range definition.fields {
		value := n.Config[field]
		if value == nil && slices.Contains(definition.defaulted, field) {
			continue
		}
		if value == nil {
			return nil
		}
		fields[field] = value
	}
	configs := []map[string]any{fields}
	if _, phased := fields["phase"]; phased {
		phases, err := phasesOf(n)
		if err != nil {
			return nil
		}
		configs = configs[:0]
		for _, phase := range phases {
			one := maps.Clone(fields)
			one["phase"] = phase
			configs = append(configs, one)
		}
	}
	var keys []InstanceKey
	for _, config := range configs {
		if key, _, err := InstanceKeyOf(Capability{Type: n.Type, Config: config}); err == nil {
			keys = append(keys, key)
		}
	}
	return keys
}

// keyHoldsReference reports whether a field an instance-shaped entry's key
// reads still holds an arg or environment reference, so the key is not
// known until create expands it. Publication keeps such an entry apart
// until then rather than reconcile it on its text: different references
// may expand to one key, and folding two entries that spell one reference
// alike would erase a repeat their own declaration block has to refuse.
func keyHoldsReference(n Capability) bool {
	if _, keyed := instanceKeys[n.Type]; !keyed || n.Group != nil {
		return false
	}
	return capabilityIsParameterized(Capability{Type: n.Type, Config: keyFieldsOf(n)})
}

// keyFieldsOf projects an instance-shaped entry's config onto the fields
// its type's key reads, as stated.
func keyFieldsOf(n Capability) map[string]any {
	definition := instanceKeys[n.Type]
	fields := make(map[string]any, len(definition.fields))
	for _, field := range definition.fields {
		if value, stated := n.Config[field]; stated {
			fields[field] = value
		}
	}
	return fields
}

// identity is the key as one string, for keying a map.
func (k InstanceKey) identity() string {
	return k.Space + "\x00" + strings.Join(k.Parts, "\x00")
}

// contextProfile keys an agent-context entry on its explicit profile. An
// entry without a directory has none: it is the legacy profile or a body
// that every profile receives, and composition leaves at most one.
func contextProfile(directory, filename string) []string {
	if directory == "" {
		return nil
	}
	return []string{contextDestination(directory, filename)}
}

// contextDestination is the file an agent-context profile writes, which
// composition folds profiles on and InstanceKeyOf keys them by.
func contextDestination(directory, filename string) string {
	return path.Join(directory, filename)
}

// onlyPhase is the phase of an entry that names exactly one, as a
// composed entry does. An empty name is no phase: phase: null decodes to
// one.
func onlyPhase(phases Phases) (string, error) {
	if len(phases) != 1 || phases[0] == "" {
		return "", fmt.Errorf("an entry keys on exactly one named phase, got %q", []string(phases))
	}
	return phases[0], nil
}
