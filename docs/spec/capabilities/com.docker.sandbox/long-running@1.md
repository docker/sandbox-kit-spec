# `com.docker.sandbox/long-running@1`

The workload runs independently of client sessions. A background startup
hook or a published port alone does not request this behavior: on a
runtime with session-based auto-stop, the sandbox can otherwise stop when
its last client disconnects.

- **Shape**: singleton, **config-less**.
- **Permission surface**: **no** — it changes when the host stops the
  workload, without granting access across the sandbox boundary.

## Execution plan

The default `order` is 0. This contract has no independently scheduled
action; its launch, platform, or lifetime behavior remains in force.
See [execution order](../../SPEC-v3.md#712-execution-order) for stage
barriers, prerequisite validation, and effective-order reconciliation.

## Config

```yaml
capabilities:
  - type: com.docker.sandbox/long-running@1
```

The entry is required by default. Use `optional: true` only when the
workload can tolerate ordinary session-based auto-stop.

- The entry **MUST NOT** carry `config`, including an empty or null <!-- tck: long-running@1/no-config -->
  value.

## Runtime behavior

A conforming runtime:

- **MUST NOT** automatically stop a sandbox granted this capability <!-- tck: long-running@1/survives-session-disconnect -->
  solely because no interactive, agent, exec, or other client session
  remains attached. Background workload processes keep running across
  that disconnection, including beyond the normal auto-stop grace period.
- **MUST** honor explicit stop operations for that sandbox. <!-- tck: long-running@1/explicit-stop-honored -->
  The capability also leaves runtime shutdown and recovery, failure
  handling, and explicit removal subject to the runtime's normal policy.
- **MUST** refuse a required entry it cannot provide during capability <!-- tck: long-running@1/required-unsatisfiable-refused -->
  preflight, by type name, before starting the workload. An optional
  entry it cannot provide is skipped and recorded; ordinary session
  auto-stop may then apply.

A runtime without session-based auto-stop already supplies the behavior,
but still advertises the type before accepting it as required. Detached
mode, a durable lease, or a service manager can implement the contract;
the capability prescribes none of them. It does not request automatic
restart after a failure or promise uninterrupted availability.

## Composition

A workload, mixin, or enclosing set can request this capability. One
Kit requesting it applies the behavior to the whole sandbox: a service
supplied by a mixin may need to outlive client sessions just as the
workload does. Identical declarations collapse; a required declaration
wins over an optional one. A composition is optional only when every
requester can tolerate session-based auto-stop.

## Gate

Not permission surface. The host can refuse to supply the behavior, just
as it can refuse other engine-executed capabilities.
