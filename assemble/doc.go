// Package assemble composes image metadata for a resolved Kit set.
// Assemble loads the platform manifests and configs through a caller-supplied
// ImageLoader, puts the workload's layer stack first, and merges the mixins.
// Merge performs the same arithmetic on inputs the caller already loaded.
//
// Image exposes typed Config and Layers. Manifest computes a config reference
// from their current values; WriteMetadata writes config and manifest blobs
// from one serialization snapshot. Layer transfer, file collision checks,
// image naming, and container creation belong to the runtime. Container
// environment overrides remain separate from the image defaults.
//
// The declaration half is spec.ComposeRuntime for runtime execution planning,
// spec.Compose for reconciliation alone, or spec.Merge for
// publishing. fetch.Client.Resolve handles registry-backed descriptor resolution;
// fetch.Client.LoadImage is the registry-backed image loader.
package assemble
