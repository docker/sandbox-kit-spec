# `com.docker.sandbox/agent-context@2`

Instruction content stored inline in the descriptor, with configurable
runtime delivery. Mixin guidance defaults to progressive surfacing: each
discovery profile indexes a per-Kit file that the agent reads on demand.
An explicit `delivery: inline` places that contribution directly in every
profile. Workload guidance always appears directly in the profiles.

- **Shape**: instance — one entry per explicit profile destination
  (`directory` plus `filename`), and at most one undirected entry per
  declaration block. Several profiles may each carry content.
- **Permission surface**: **no** — prose on the agent's trust plane.

## Config

```yaml
- type: com.docker.sandbox/agent-context@2
  config:
    filename: AGENTS.md
    directory: /home/agent/.example-agent
    content: |
      Use the Kit tools.
      Team: ${{ kit.args.team }}

# Tool mixins need no discovery profile.
- type: com.docker.sandbox/agent-context@2
  config:
    delivery: progressive # Default; inline loads this body in every profile.
    content: Use the bundled tools.
```

| Field | Type | Rules |
|---|---|---|
| `filename` | string | Discovery filename; without `directory`, workload Kits only. |
| `directory` | string | Absolute, canonical in-sandbox directory. Requires a single-component `filename`. Omit for workspace-sibling placement. |
| `content` | string | Instruction body stored inline in the descriptor. Optional for a profile-only declaration. |
| `delivery` | string | `progressive` (default when omitted) or `inline`; controls this declaration's mixin guidance. Explicit empty values are invalid. Accepts ordinary Kit argument and environment references before expansion. |

An authored or published declaration **MUST** express any body through <!-- tck: agent-context@2/inline-only -->
`content`, reject `contentFile` and unknown config fields, and contain
at least a profile or a nonempty body. Tool mixins can contribute a body
without owning a profile.

Publication **MUST** retain inline bodies in the descriptor, without <!-- tck: agent-context@2/publication-inline -->
rewriting them into image paths or adding a build-only include form.

Content and delivery **MUST** follow the ordinary Kit expansion rules: build-phase <!-- tck: agent-context@2/expansion -->
arguments at build, remaining create-phase arguments and final `kit.env`
references at create. Ordinary `$VAR` and `${VAR}` remain literal.
Unresolved or invalid references fail under the existing validation rules.

## Composition and selection

Composition **MUST** retain selected workload bodies and mixin bodies <!-- tck: agent-context@2/complete-content -->
whose delivery is `inline`, in dependency order, in the effective
`content` of every surviving profile. Bodies are joined with two
newlines, without trimming their text. Effective profile delivery is
`inline`: its content is already the root guidance. Progressive mixin
bodies remain in the selected input declarations, which consumers retain
alongside the effective configuration for runtime surfacing. A body-only
composition invents no discovery destination; when it has only
progressive mixin bodies, no effective profile entry is needed.

Delivery **MUST** default to `progressive` when omitted and accept only <!-- tck: agent-context@2/delivery -->
`progressive` or `inline` after expansion. For each selected progressive
mixin contribution, the runtime writes its body to a readable per-Kit
file and includes an attributed link to that file in every discovery
profile. For each selected inline mixin contribution, it includes the
body directly in every profile. Selecting inline for one contribution
does not change another contribution's delivery. Every contribution
applies to every profile, including a profile declared by another Kit;
delivery does not route guidance to a subset of profiles. If progressive
surfacing cannot be provided, the runtime refuses rather than silently
loading the body inline.

A consumed workload owns all its selected guidance, including bodies
retained when publishing a flattened workload set: these bodies appear
directly in every profile regardless of delivery. The consumed
`kind: workload` determines that role, rather than diagnostic source
metadata. A published mixin set retains each declaration's delivery;
flattening it does not turn its progressive contributions into inline
ones.

Publication **MUST** preserve original declaration and selection <!-- tck: agent-context@2/selection-boundaries -->
boundaries in flattened sets, including optional declarations and groups.
Selection precedes composition: skipped declarations contribute no body.
Skipping one optional profile does not discard selected guidance needed
by another profile. Publication retains declarations in groups rather
than publishing duplicated, already combined profile bodies.

A runtime **MUST** prefer explicit destinations over the legacy workload <!-- tck: agent-context@2/profile-precedence -->
filename, independently of order, and materialize every distinct explicit
destination. Identical destinations describe one profile. With no explicit
destination, at most one contribution owns the legacy filename. Each
profile is required when an ask naming that destination is required;
undirected asks count toward the first profile.

Selected `@1` and `@2` requests **MUST NOT** compose together. <!-- tck: agent-context@2/mixed-versions -->
The runtime refuses that composition explicitly, even when an `@1` body
is inline. Conditional published sets may carry both versions, provided
selection leaves only one. No file-backed `@1` request is upgraded.

Schema 3 does not express version alternatives. Two optional context
requests remain independent, including when both have the same content
or profile destination. A runtime accepting both exact types selects
both and refuses the composition above. A rollout using both optional
versions needs an explicit runtime policy accepting just one across the
composition; see [SPEC-v3 §7.1.2](../../SPEC-v3.md#712-capability-versions-and-selection).

The existing serialized descriptor budgets **MUST** apply after <!-- tck: agent-context@2/size-budget -->
expansion and composition: warn above 64 KiB and refuse above 512 KiB.
Guidance is never truncated or silently moved into layers. Keep large
reference material in bundled files or skills and point to it briefly.

## Runtime surfacing

A runtime **MUST** materialize each effective profile with its own <!-- tck: agent-context@2/materialized -->
guidance, the combined inline `content`, and the progressive Kit index
and files before agent launch, creating directories as needed. Without
`directory`, place the profile beside the workspace, outside the
checkout. A body-only contribution uses the profile selected by the
composition. Each progressive body retains its expanded text; materializing
it does not expand ordinary shell variables.

A runtime **MUST** preserve existing user instructions outside its <!-- tck: agent-context@2/existing-content-preserved -->
managed sections when updating a profile.

A runtime **MUST NOT** interpret content as trusted runtime input. <!-- tck: agent-context@2/content-untrusted -->
Prose is for the agent; neutralize runtime-management delimiters in
Kit-supplied content before placing it in managed sections.

## Migration

The configurable delivery contract uses the maintainer-approved
draft-stage exception in [RELEASES.md](../../../../RELEASES.md), recorded
in [issue #878](https://github.com/docker/sbx-releases/issues/878).
Strict readers built before this addition reject `delivery`; older
implementations that load all `@2` mixin bodies inline need updating for
the progressive default. `schemaVersion` remains `3`.
`agent-context@1` and its schema remain supported and are not deprecated
by introducing this version.

To migrate, move a file's text into `content`, select `@2`, audit Kit
placeholders that previously remained literal in staged files, and
rebuild. Consumers advertise `@2` support before accepting required
requests. See [harness destinations](../../../agent-context-placement.md)
for discovery paths.

Expose an installer's choice through an ordinary create-phase Kit argument,
without a delivery-specific CLI option:

```yaml
args:
  context_delivery:
    default: progressive
    enum: [progressive, inline]
    description: How the agent reads this Kit's guidance
capabilities:
  - type: com.docker.sandbox/agent-context@2
    config:
      delivery: ${{ kit.args.context_delivery }}
      content: Use the bundled tools.
```
