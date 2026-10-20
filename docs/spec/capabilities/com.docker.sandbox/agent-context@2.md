# `com.docker.sandbox/agent-context@2`

Inline instruction content the agent reads. This version materializes
combined guidance directly in each discovery profile; it does not use
`@1`'s progressive per-Kit index. No content reader or image filesystem
is needed to recover the guidance from an effective configuration.

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
    content: Use the bundled tools.
```

| Field | Type | Rules |
|---|---|---|
| `filename` | string | Discovery filename; without `directory`, workload Kits only. |
| `directory` | string | Absolute, canonical in-sandbox directory. Requires a single-component `filename`. Omit for workspace-sibling placement. |
| `content` | string | Inline instruction body. Optional for a profile-only declaration. |

An authored or published declaration **MUST** express any body through <!-- tck: agent-context@2/inline-only -->
`content`, reject `contentFile` and unknown config fields, and contain
at least a profile or a nonempty body. Tool mixins can contribute a body
without owning a profile.

Publication **MUST** retain inline bodies in the descriptor, without <!-- tck: agent-context@2/publication-inline -->
rewriting them into image paths or adding a build-only include form.

Content **MUST** follow the ordinary Kit expansion rules: build-phase <!-- tck: agent-context@2/expansion -->
arguments at build, remaining create-phase arguments and final `kit.env`
references at create. Ordinary `$VAR` and `${VAR}` remain literal.
Unresolved or invalid references fail under the existing validation rules.

## Composition and selection

Composition **MUST** retain every selected body in dependency order in <!-- tck: agent-context@2/complete-content -->
the effective `content` of every surviving profile. Bodies are joined
with two newlines, without trimming their text. A body-only composition
retains its content without inventing a discovery destination. Consumers
obtain all guidance from the effective configuration alone.

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

The existing serialized descriptor budgets **MUST** apply after <!-- tck: agent-context@2/size-budget -->
expansion and composition: warn above 64 KiB and refuse above 512 KiB.
Guidance is never truncated or silently moved into layers. Keep large
reference material in bundled files or skills and point to it briefly.

## Runtime surfacing

A runtime **MUST** materialize each effective profile with its own <!-- tck: agent-context@2/materialized -->
guidance and the combined inline `content` before agent launch, creating
its directory as needed. Without `directory`, place the profile beside
the workspace, outside the checkout. A body-only contribution uses the
profile selected by the composition.

A runtime **MUST** preserve existing user instructions outside its <!-- tck: agent-context@2/existing-content-preserved -->
managed sections when updating a profile.

A runtime **MUST NOT** interpret content as trusted runtime input. <!-- tck: agent-context@2/content-untrusted -->
Prose is for the agent; neutralize runtime-management delimiters in
Kit-supplied content before placing it in managed sections.

## Migration

This capability version follows the capability-versioning rule in
[RELEASES.md](../../../../RELEASES.md): removing `contentFile` and changing
surfacing changes the shipped contract. `schemaVersion` remains `3`.
`agent-context@1` and its schema remain supported and are not deprecated
by introducing this version.

To migrate, move a file's text into `content`, select `@2`, audit Kit
placeholders that previously remained literal in staged files, and
rebuild. Consumers advertise `@2` support before accepting required
requests. See [harness destinations](../../../agent-context-placement.md)
for discovery paths.
