package fetch

import (
	"maps"
	"slices"

	"github.com/docker/sandbox-kit-spec/v3/spec"
)

// Struct copies retain spec's private presence flags. Serialization cannot:
// omitempty drops explicit empty configs, and null presence lives only in flags.
func cloneExpandedDescriptor(d *spec.Descriptor) *spec.Descriptor {
	out := *d
	out.Licenses = slices.Clone(d.Licenses)
	out.Provides = slices.Clone(d.Provides)
	out.Requires = slices.Clone(d.Requires)
	out.Integrates = slices.Clone(d.Integrates)
	out.Conflicts = slices.Clone(d.Conflicts)
	out.Capabilities = cloneExpandedCapabilities(d.Capabilities)
	// Returned Kit descriptors remain mutable, but their scope bindings must
	// not rewrite the retained pre-selection evidence. Isolate both the
	// scope table and each scope's provided names from those descriptors.
	out.CapabilityScopes = slices.Clone(d.CapabilityScopes)
	for i := range out.CapabilityScopes {
		out.CapabilityScopes[i].Provides = slices.Clone(d.CapabilityScopes[i].Provides)
	}
	out.Args = maps.Clone(d.Args)
	for name, arg := range out.Args {
		if arg.Default != nil {
			value := *arg.Default
			arg.Default = &value
		}
		arg.Enum = slices.Clone(arg.Enum)
		out.Args[name] = arg
	}
	out.Kits = slices.Clone(d.Kits)
	for i := range out.Kits {
		out.Kits[i].Args = maps.Clone(d.Kits[i].Args)
	}
	return &out
}

func cloneExpandedCapabilities(entries []spec.Capability) []spec.Capability {
	out := slices.Clone(entries)
	for i, entry := range out {
		entry.DeclarationBlocks = slices.Clone(entry.DeclarationBlocks)
		if entry.Source != nil {
			source := *entry.Source
			entry.Source = &source
		}
		if entry.Group != nil {
			group := *entry.Group
			group.Capabilities = cloneExpandedCapabilities(group.Capabilities)
			entry.Group = &group
		}
		entry.Config = maps.Clone(entry.Config)
		for key, value := range entry.Config {
			entry.Config[key] = cloneExpandedValue(value)
		}
		out[i] = entry
	}
	return out
}

// Expanded configs have passed through spec.Decode: their mutable values are
// maps and sequences, while scalars can be shared without changing evidence.
func cloneExpandedValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		out := maps.Clone(value)
		for key, child := range out {
			out[key] = cloneExpandedValue(child)
		}
		return out
	case []any:
		out := slices.Clone(value)
		for i, child := range out {
			out[i] = cloneExpandedValue(child)
		}
		return out
	default:
		return value
	}
}
