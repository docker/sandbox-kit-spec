# `com.docker.sandbox/privileged@1`

A request for elevated privilege. Present or absent — there is nothing to
configure, and the host may refuse.

- **Shape**: singleton, **config-less** — any `config` value is a
  validation error.
- **Permission surface**: yes — the strongest single widening.

## Execution plan

The default `order` is 100. The scheduled actions are
`configure:configure`.
See [execution order](../../SPEC-v3.md#712-execution-order) for stage
barriers, prerequisite validation, and effective-order reconciliation.

## Config

```yaml
- type: com.docker.sandbox/privileged@1
  description: Runs nested containers via the inner engine
```

No `config`. `description` SHOULD say why, because a human answers this <!-- tck: privileged@1/description-says-why -->
request.

## Runtime behavior

A conforming runtime:

- **MUST** run the sandbox with its platform's elevated-privilege mode when <!-- tck: privileged@1/elevation-granted -->
  granted (e.g. a privileged container).
- **MAY** refuse. Refusal fails resolution for a required entry; an
  optional entry is skipped and recorded, and the Kit runs unprivileged.
- **SHOULD** require explicit, non-default consent to grant — this is the <!-- tck: privileged@1/explicit-consent -->
  one request that dissolves most of the boundary the sandbox exists for.

## Composition

One Kit requesting it makes the composition privileged. There is no
narrower scope: privilege is per-sandbox.

## Gate

Boolean surface. Absent → present is a widening that MUST stop for <!-- tck: privileged@1/widening-gates -->
approval.
