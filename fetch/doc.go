// Package fetch reads published Kit descriptors and image metadata from OCI
// registries. Credentials, transport, and platform selection belong to Client.
//
// Assemble is the single-call runtime API: Kit requests and Options produce
// selected declarations, image metadata, and final container settings. It
// defaults to Docker credentials and known capabilities; supply Options.Loader
// for another store or Options.CapabilitySelector for the runtime's actual
// claims and policy. Options.OnProgress reports stages. Client.LoadKit supplies
// a loader with custom credentials and platform.
// Source-form references (directories, git URLs) use a custom loader that builds
// the Kit and supplies its runtime image reference in LoadedKit.Image. The
// caller's reference remains the identity for resolution, diagnostics and locks.
//
// Layer validation is opt-in: set Options.LayerValidator to DefaultLayerValidator for
// the built-in integrity, safe-extraction, inventory-limit, and cross-Kit file
// collision checks. A custom LayerValidator can use a runtime's verified store.
// Nil enables metadata-only assembly for consent-time resolution or images
// already checked by the caller. It never calls LayerLoader and preserves
// metadata and descriptor validation and the same Result. The caller remains
// responsible for layer integrity, safe extraction, resource limits, and
// cross-Kit collisions before using the image.
//
// Resolve fetches a closed set of requests, resolves create arguments, validates
// every input, orders the dependency graph, selects capability groups,
// reconciles the selected descriptors with
// spec.Compose, and validates the final descriptor. ResolvePartial performs the
// same checks while allowing a mixin-only set. There is no validation bypass.
//
// WithCapabilitySelector supplies runtime decisions over expanded entries.
// Callbacks receive the operation context, the owning Kit's expanded descriptor
// (including DisplayName), and the capability, both passed by value.
// They return a spec.CapabilityDecision; messages survive in selection records
// and required-rejection diagnostics.
// The default selector accepts this library's known types; runtimes should
// supply their actual claims with spec.Supported or a stricter callback.
// Resolved.Selections retains published descriptors, their original bytes,
// and create-time decisions.
// Resolved.Kits retains only selected contributions, pinned image identities,
// resolved arguments, and each Kit's Env exports in dependency order.
// Resolved.ContainerEnv contains their conflict-checked union for container
// creation; these override image defaults at runtime.
// Resolved.Warnings contains advisory findings from final validation.
//
// LoadImage implements assemble.ImageLoader using the same credentials and
// platform as descriptor resolution. Pass it to assemble.Assemble together
// with Resolved.Kits to load the pinned images and compose their metadata.
// Resolve reads manifests only; LoadImage also reads config blobs. Neither
// downloads filesystem layers or performs filesystem collision checks.
//
// A complete runnable consumer lives in fetch/example. Publishing a flattened
// Kit remains spec.Merge's job, with staging owned by the BuildKit frontend.
package fetch
