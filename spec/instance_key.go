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
// re-exporting an arg merges entries before anything expands them.
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
	}).readingAsWritten("service", "phase"),
	// One socket per phase, whichever kits asked: the bounds are what that
	// socket signs, not which socket it is.
	CapabilitySSHAgent: keyOn([]string{"phase"}, func(a SSHAgent) ([]string, error) {
		phase, err := onlyPhase(a.Phase)
		return []string{phase}, err
	}).readingAsWritten("phase"),
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
	}).readingAsWritten("path"),
	CapabilityPort: keyOn([]string{"container", "transport"}, func(p Port) ([]string, error) {
		return []string{PortKey(p)}, nil
	}).defaulting("transport").readingAsWritten("transport"),
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
	// asWritten names the fields parts reads as written, each apart from
	// the others, so a reference in one stands in the key for whatever
	// value it expands to: entries spelling it alike share a key however
	// it expands. A field the key reworks cannot be keyed on its text.
	// path.Clean folds /data/${{ kit.args.d }}/.. to /data, which d=a/b
	// does not expand to, and a skill whose name expands empty is named
	// for its path.
	asWritten []string
	parts     func(Capability) ([]string, error)
}

// defaulting names the fields an entry may leave out or state null,
// parts reading their default.
func (k keyDefinition) defaulting(fields ...string) keyDefinition {
	k.defaulted = fields
	return k
}

// readingAsWritten names the fields parts reads as written, apart from
// the others.
func (k keyDefinition) readingAsWritten(fields ...string) keyDefinition {
	k.asWritten = fields
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

// literalInstanceKeys returns the keys an authored entry already settles,
// so validation can hold a declaration block to one entry per key before
// expansion or a merge separates or folds the entries. It reads only the
// fields its type's key reads, so another field that is invalid or still
// holds a reference cannot hide a repeated key. A reference expands to
// one value throughout a block, so a key field the key reads as written
// is keyed on its text when it holds one, respelled as expansion reads
// it. Any other key field still holding a reference gives no key, as
// does one the key cannot read, such as an undecodable value or an empty
// phase. So does leaving out a field that has no default, or stating it
// null; a field that has one reads its default either way, as decoding
// does. Neither a default nor a reference's text is the key's when
// another config key may yet be read as the field, so such an entry gets
// no key either. An entry naming several phases gives one key per phase,
// as composition splits it.
//
// Fields are read by their exact names. Decoding also reads a field
// spelled in another case, and such a spelling can override the exact
// one, so the key of an entry that decodes is the one InstanceKeyOf gives
// it.
func literalInstanceKeys(n Capability) []InstanceKey {
	definition, keyed := instanceKeys[n.Type]
	if !keyed || n.Group != nil {
		return nil
	}
	fields := make(map[string]any, len(definition.fields))
	for _, field := range definition.fields {
		value := n.Config[field]
		if value == nil && slices.Contains(definition.defaulted, field) {
			if mayBeReadAs(n.Config, field) {
				return nil
			}
			continue
		}
		if value == nil {
			return nil
		}
		if capabilityIsParameterized(Capability{Config: map[string]any{field: value}}) {
			if !slices.Contains(definition.asWritten, field) || mayBeReadAs(n.Config, field) {
				return nil
			}
			value = respellReferences(value)
		}
		fields[field] = value
	}
	configs := []map[string]any{fields}
	if _, phased := fields["phase"]; phased {
		phases, err := phasesOf(Capability{Type: n.Type, Config: fields})
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
		if key, _, err := keyAsWritten(Capability{Type: n.Type, Config: config}); err == nil {
			keys = append(keys, key)
		}
	}
	return keys
}

// respellReferences writes every reference in a string, or in a list's
// strings, the one way: ${{ kit.args.name }} or ${{ kit.env.NAME }}.
// Expansion reads only a reference's name, whatever whitespace its
// braces hold, so the spellings it merges here expand alike.
func respellReferences(value any) any {
	switch v := value.(type) {
	case string:
		v = argRef.ReplaceAllString(v, "$${{ kit.args.${1} }}")
		return envRef.ReplaceAllString(v, "$${{ kit.env.${1} }}")
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = respellReferences(item)
		}
		return out
	}
	return value
}

// mayBeReadAs reports whether a config key other than field itself may be
// read as field: decoding matches a key spelled in another case, as
// encoding/json does, and a key still holding a reference may expand to
// field's name.
func mayBeReadAs(config map[string]any, field string) bool {
	for key := range config {
		if key != field && (strings.EqualFold(key, field) || ContainsArgRef(key) || ContainsEnvRef(key)) {
			return true
		}
	}
	return false
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
