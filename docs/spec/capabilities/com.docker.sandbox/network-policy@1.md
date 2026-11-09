# `com.docker.sandbox/network-policy@1`

Phase-scoped egress policy: what the sandbox may reach while the Kit's
install hooks run, and what the agent may reach in steady state.

- **Shape**: singleton — at most one entry per descriptor, and exclusive
  with [`network-policy@2`](network-policy@2.md).
- **Permission surface**: yes — all four lists, direction-aware
  ([SPEC-v3 §7.4](../../SPEC-v3.md#74-permission-surface-and-the-gate)).

This version remains valid and is what a Kit that gates egress by host
alone should state. [`@2`](network-policy@2.md) adds HTTP rules that
narrow method and path over the connections these lists permit.

## Execution plan

The default `order` is 100. The scheduled actions are
`install:policy`, `start:policy`.
See [execution order](../../SPEC-v3.md#712-execution-order) for stage
barriers, prerequisite validation, and effective-order reconciliation.

## Config

```yaml
- type: com.docker.sandbox/network-policy@1
  config:
    install:                      # open only while install hooks run
      allow: [registry.npmjs.org]
    runtime:                      # the agent's steady state
      allow:
        - api.anthropic.com:443
        - "*.github.com"
      deny:
        - telemetry.example.com
```

| Field | Type | Rules |
|---|---|---|
| `install` | object | optional. `allow`/`deny` lists for the install phase. |
| `runtime` | object | optional. `allow`/`deny` lists for the runtime phase. |
| `*.allow` | list\<string\> | Egress patterns to permit. |
| `*.deny` | list\<string\> | Egress patterns to refuse. Deny wins. |

Entry patterns:

| Pattern | Example | Meaning |
|---|---|---|
| exact host | `api.example.com` | the host, any port unless one is stated |
| host + port | `api.example.com:443` | the host on that port |
| single-label wildcard | `*.example.com` | exactly one subdomain label |
| everything | `*` or `**` | all egress (typical only for install phases of build-heavy Kits) |

The precise wildcard matcher is runtime-owned; the patterns above are the
portable core. Port suffixes are ignored for allow-list membership checks
(`host:443` and `host` name the same host).

## Validation

- Strict config decode; unknown keys are errors.
- Cross-entry invariant: every
  [`credential@1`](credential@1.md) inject domain MUST appear in the <!-- tck: network-policy@1/inject-domain-in-allow -->
  **matching phase's** allow list (a bare `*`/`**` entry covers every
  domain). Injection sets the header; egress is gated separately — an
  inject domain outside the allow list would be a credential mapped onto a
  connection that can never occur.

## Runtime behavior

A conforming runtime:

- **MUST** enforce deny-by-default: egress not matched by an allow entry is <!-- tck: network-policy@1/deny-by-default -->
  refused. An absent phase block grants nothing for that phase.
- **MUST** apply deny precedence: a host matching both lists is refused. <!-- tck: network-policy@1/deny-precedence -->
- **MUST** scope the install lists to the install phase only — open while <!-- tck: network-policy@1/install-phase-scoped -->
  the composition's [lifecycle install hooks](lifecycle@1.md) run, and
  **closed before the workload's entrypoint starts**. Install-phase grants
  are unreachable from the running agent.
- **MUST** enforce at a boundary the sandbox cannot bypass (typically a <!-- tck: network-policy@1/unbypassable-boundary -->
  host-side proxy the container's egress is forced through), not by
  in-container configuration the workload could rewrite.
- **SHOULD** surface refused connections observably (logs, events) so a <!-- tck: network-policy@1/refusals-observable -->
  missing allow entry is diagnosable.

## Composition

Across the resolved set, allow lists union per phase and deny lists union
per phase; deny precedence applies to the merged result. One Kit's deny is
not defeated by another Kit's allow.

## Gate

All four lists are permission surface. A new allow entry widens; a
**removed deny entry also widens** — the deny was part of what made the
grant acceptable.
