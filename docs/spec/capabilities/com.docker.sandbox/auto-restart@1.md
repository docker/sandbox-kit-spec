# `com.docker.sandbox/auto-restart@1`

Resume a previously running sandbox after the host or runtime recovers.
Service Kits can request this behavior so that other sandboxes can reach
them again after a host reboot or a runtime upgrade.

- **Shape**: singleton, **config-less**.
- **Permission surface**: **no**. Recovery changes workload lifetime
  without granting access across the sandbox boundary.

## Config

```yaml
capabilities:
  - type: com.docker.sandbox/auto-restart@1
```

The entry is required by default. Use `optional: true` only when the
workload can tolerate manual recovery. Services that also need to outlive
client sessions declare [long-running@1](long-running@1.md) separately:

```yaml
capabilities:
  - type: com.docker.sandbox/long-running@1
  - type: com.docker.sandbox/auto-restart@1
```

- The entry **MUST NOT** carry `config`, including an empty or null <!-- tck: auto-restart@1/no-config -->
  value.

## Runtime behavior

A conforming runtime:

- **MUST** start a sandbox that was running when the host or runtime <!-- tck: auto-restart@1/restarts-after-runtime-restart -->
  process stopped, once that host or runtime has recovered, without a
  new client session or an explicit client start. Recovery includes
  re-running ordinary startup hooks. Leaving that sandbox stopped solely
  because the host or runtime recovered does not satisfy this contract.
- **MUST** honor explicit stop across recovery. A sandbox the operator <!-- tck: auto-restart@1/explicit-stop-honored -->
  stopped before recovery remains stopped rather than being started by
  this capability.
- **MUST NOT** treat this capability as a substitute for <!-- tck: auto-restart@1/without-long-running-still-session-autostops -->
  `long-running@1`. On a runtime with session-based auto-stop, a sandbox
  that declares only `auto-restart@1` is still subject to that policy
  after its last client session disconnects.
- **MUST** refuse a required entry it cannot provide during capability <!-- tck: auto-restart@1/required-unsatisfiable-refused -->
  preflight, by type name, before starting the workload. An optional
  entry it cannot provide is skipped and recorded.

This capability does not request restart after an in-guest process crash,
promise uninterrupted availability, or change delete and recreate
semantics. A runtime's ordinary failure handling can still apply.

## Composition

A workload, mixin, or enclosing set can request this capability. A request
from any Kit applies to the whole sandbox. Identical declarations
collapse, and a required declaration wins over an optional declaration.
A composition is optional only when every requester can tolerate manual
recovery. This request does not imply a `long-running@1` declaration.

## Gate

Not permission surface. The host can refuse to supply the behavior, just
as it can refuse other engine-executed capabilities.
