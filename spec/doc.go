// Package spec defines the kit v3 descriptor: the declaration half of a kit,
// published as a manifest annotation on an ordinary OCI image whose layers
// are the kit's content.
//
// The descriptor deliberately carries no top-level identity name, image
// reference, or runtime config (entrypoint, env, workdir, user, default
// command): identity is the reference a kit is consumed by, capability
// names are display labels, and runtime config lives in the image config,
// set by the companion Dockerfile.
//
// Decode and Validate read a descriptor. Merge folds an ordered set of
// them into the one descriptor a composition publishes. Compose reconciles
// selected runtime declarations, and InstanceKeyOf returns the §7.1 key it
// reconciles each instance-shaped entry on. SelectCapabilities evaluates
// atomic groups using the runtime's callback; its callers retain selected
// inputs for handlers that need per-Kit provenance. Ordering the set is
// resolve; reading the descriptors out of a registry is fetch; folding the
// images is assemble.
package spec
