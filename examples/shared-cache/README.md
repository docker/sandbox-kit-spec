# Shared pip cache

A declaration-only mixin using `com.docker.sandbox/host-mount@1` to
share pip downloads across sandboxes composing this Kit. The runtime
chooses the host directory and how it appears inside the sandbox.
No image content or host path is supplied by the Kit.

The optional group couples the mount with pip's configuration file.
A runtime without host sharing skips both requests; pip then uses its
ordinary cache. The workload must already provide pip.

The cache is keyed by this Kit's runtime-resolved identity and the
in-container path. It survives removal of the last sandbox and remains
accessible to the host user, who can find and remove it through the
runtime's storage interface. Concurrent sandboxes share read/write
access; pip is responsible for coordinating cache writes.

This grants host sharing as a separate permission from sandbox-private
storage. For a cache without that sharing contract, see
[optional-cache](../optional-cache/README.md).

Build against the current frontend source:

```sh
task kit:dev KIT=shared-cache
```
