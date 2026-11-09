# `com.docker.sandbox/resources@1`

The workload's compute: CPU, memory, GPU. A constraint on the Kit, not a
grant to it.

- **Shape**: singleton — at most one entry per descriptor.
- **Permission surface**: **no** — limits constrain the Kit rather than
  grant it anything, so changing them never stops for approval.

## Execution plan

The default `order` is 100. The scheduled actions are
`configure:configure`.
See [execution order](../../SPEC-v3.md#712-execution-order) for stage
barriers, prerequisite validation, and effective-order reconciliation.

## Config

```yaml
- type: com.docker.sandbox/resources@1
  config:
    cpu: 4.0           # cores; MUST be >= 0
    memory: 8g         # byte-size string
    gpu: "1"           # runtime-defined selector ("1", "all", …)
```

| Field | Type | Rules |
|---|---|---|
| `cpu` | float | optional. Cores; non-negative. |
| `memory` | string | optional. Byte-size (`4096m`, `8g`, `2gib`). |
| `gpu` | string | optional. Runtime-defined selector. |

Any unset field means "no constraint from this Kit".

## Runtime behavior

A conforming runtime:

- **SHOULD** apply the declared values as the sandbox's resource limits. <!-- tck: resources@1/limit-applied -->
  Enforcement precision (cgroup limits, VM sizing) is runtime-owned.
- **MAY** let the operator override declared values (for example, a builder
  Kit's defaults raised via runtime configuration); the descriptor states
  what the Kit wants, the host owns what it gets.
- **MUST NOT** treat an unsatisfiable request as silent truncation when the <!-- tck: resources@1/no-silent-truncation -->
  entry is required: refuse, or degrade observably.
- GPU semantics (which devices, which driver stack) are runtime-defined;
  the field is a request selector, not a hardware contract.

## Composition

The workload Kit SHOULD own the composition's resource declaration. When <!-- tck: resources@1/workload-owns-declaration -->
mixins also declare, reconciliation is runtime-owned; a runtime SHOULD <!-- tck: resources@1/max-of-declarations -->
satisfy the maximum of the stated needs and MUST NOT undercut the workload's <!-- tck: resources@1/never-undercut-workload -->
declaration silently.
