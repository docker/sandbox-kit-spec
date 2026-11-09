# `com.docker.sandbox/agent-skill@1`

One bundled agent skill, supplied by the Kit's image and exposed in the
composed agent's skill directories. A workload or a mixin can supply it.

- **Shape**: instance — keyed by effective skill name.
- **Permission surface**: no — image content on the entrypoint's trust
  plane. Host sharing is governed by the destination
  `agent-skills@1` declaration and host policy.

## Execution plan

The default `order` is 300. The scheduled actions are
`setup:materialize`.
See [execution order](../../SPEC-v3.md#712-execution-order) for stage
barriers, prerequisite validation, and effective-order reconciliation.

## Config

```yaml
- type: com.docker.sandbox/agent-skill@1
  name: Pull request review              # optional display label
  config:
    path: /usr/share/example-skills/pr-review
    name: review-pr                       # optional basename override
```

Here, `type` identifies the capability contract, the entry's `name`
labels the request in a UI, and `config.name` chooses the skill directory.
For a destination of `/home/agent/.claude/skills`, this skill appears at
`/home/agent/.claude/skills/review-pr`. Omitting `config.name` uses
`pr-review`, the source basename, even when the display label is present.
Only the effective skill directory name participates in skill identity
and collision checks; changing the display label has no such effect.

| Field | Type | Rules |
|---|---|---|
| `path` | string | REQUIRED. Absolute, canonical in-image directory path; not `/`, no trailing slash, repeated separator, or `.` or `..` segments. |
| `name` | string | optional. Directory name in the agent's skill store. Defaults to the last component of `path`. Distinct from the entry's display-only `name`. |

The published source `path` **MUST** be literal. <!-- tck: agent-skill@1/source-literal -->
Build-phase arguments may choose it while authoring; create-phase
arguments and `kit.env` references cannot, because artifact validation
checks the source before a sandbox exists. The destination name may still be chosen at create.

The effective name **MUST** match `[A-Za-z0-9][A-Za-z0-9._-]*` and be at most 255 bytes. <!-- tck: agent-skill@1/name-valid -->
This portable component cannot escape the discovery directory. An explicit
empty or null `name` is invalid; omit it to use the basename.

The Kit **MUST** ship a regular `SKILL.md` file at the root of `path`. <!-- tck: agent-skill@1/content-present -->
Supporting scripts and references travel in the same directory tree.
This capability does not rewrite skill content or frontmatter; authors
keep any frontmatter name aligned with the effective name their agent
expects. The source directory itself may have a different name.

## Runtime behavior

Agent-bearing Kits declare destinations with
[agent-skills@1](agent-skills@1.md). Selected paths
union across the composition, including agent mixins. A missing, empty,
or disabled host store does not prevent bundled content from being
exposed.

A conforming runtime:

- **MUST** expose the complete directory at `<destination>/<effective-name>` <!-- tck: agent-skill@1/exposed -->
  at every selected discovery destination, preserving file contents,
  executable permissions, and relative references within the tree.
- **MUST** make it readable by the agent before launching the workload. <!-- tck: agent-skill@1/before-launch -->
- **MUST** refuse an unsatisfiable required request, including one with <!-- tck: agent-skill@1/unavailable -->
  no selected destination; an unsatisfiable optional request is skipped
  and recorded under the ordinary capability selection rules.
- **MUST NOT** execute bundled scripts merely to register the skill. <!-- tck: agent-skill@1/no-execution -->
  Running a skill is the agent's decision; credentials and network
  access remain separate grants.
- **MUST NOT** overwrite an existing skill of the same effective name in <!-- tck: agent-skill@1/existing-conflict -->
  a destination. A conflicting existing entry makes the request
  unsatisfiable, whether it came from image content or a shared store.
  A runtime's own exposure from a previous start is not a new conflict.

Mounts, copies, links, and combining host-shared and bundled content are
runtime choices. The observable result above is the contract. Bundled
skills do not grant permission to modify the host's shared store.

## Composition

Within one declaration block, duplicate effective names are errors.
Across Kits, requests with the same source path and effective name
collapse; required wins over optional and the first nonempty display
label is retained. Different source paths under one effective name are
a composition error, even if either request is optional. This compares
published declarations, not the contents of their trees.

Source and destination paths are independent: a skill mixin need not know
which agents consume it. The image overlay still owns source-path
collisions under the ordinary layer composition rules; authors should
use a Kit-specific source prefix.
