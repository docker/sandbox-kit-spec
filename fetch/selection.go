package fetch

import (
	"context"

	"github.com/docker/sandbox-kit-spec/v3/spec"
)

var knownCapabilities = spec.Supported(spec.KnownCapabilities()...)

// DefaultCapabilitySelector accepts each library-known exact type independently.
// Understanding a schema does not claim runtime support. Runtime consumers
// should supply their own supported types and policy through a selector.
func DefaultCapabilitySelector(ctx context.Context, target spec.SelectionTarget, kit spec.Descriptor, capability spec.Capability) spec.CapabilityDecision {
	return knownCapabilities(ctx, target, kit, capability)
}
