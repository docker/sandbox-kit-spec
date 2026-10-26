package fetch

import (
	"context"

	"github.com/docker/sandbox-kit-spec/v3/spec"
)

var knownCapabilities = spec.Supported(spec.KnownCapabilities()...)

// DefaultCapabilitySelector accepts library-known types and chooses one context
// version per Kit. It prefers @2 when offered, unless the Kit requires @1.
// Groups retain their optionality. Incompatible versions required by different
// Kits still fail composition; runtimes should supply their own claims and policy.
func DefaultCapabilitySelector(ctx context.Context, target spec.SelectionTarget, kit spec.Descriptor, capability spec.Capability) spec.CapabilityDecision {
	if capability.Type != spec.CapabilityAgentContext && capability.Type != spec.CapabilityAgentContextV2 {
		return knownCapabilities(ctx, target, kit, capability)
	}
	var modern, legacyRequired bool
	for _, item := range kit.Capabilities {
		members, optional := []spec.Capability{item}, item.Optional
		if item.Group != nil {
			members, optional = item.Group.Capabilities, item.Group.Optional
		}
		for _, member := range members {
			modern = modern || member.Type == spec.CapabilityAgentContextV2
			legacyRequired = legacyRequired || (member.Type == spec.CapabilityAgentContext && !optional)
		}
	}
	preferred := spec.CapabilityAgentContext
	if modern && !legacyRequired {
		preferred = spec.CapabilityAgentContextV2
	}
	if capability.Type != preferred {
		return spec.CapabilityDecision{Message: "default policy selected " + preferred}
	}
	return spec.CapabilityDecision{Accepted: true}
}
