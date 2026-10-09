# `com.docker.sandbox/volume@1`

One sandbox-private storage destination. Block storage belongs to the
sandbox instance and outlives its container; tmpfs is scratch storage.

- **Shape**: instance — one entry per cleaned path in a declaration block;
  duplicates rejected. Matching requests across Kits merge.
- **Permission surface**: yes — the path.

## Config

```yaml
- type: com.docker.sandbox/volume@1
  config:
    path: /home/agent/.claude/projects   # REQUIRED, absolute
    size: 2g                             # optional byte-size string
    tmpfs: false                         # optional; RAM-backed instead of block
    mode: "0755"                         # optional octal permissions
```

| Field | Type | Rules |
|---|---|---|
| `path` | string | REQUIRED. Absolute in-container path. |
| `size` | string | optional. Byte-size (`512m`, `2g`, `1gib`); suffixes use powers of 1024, with or without `i` or `b`. Omission leaves capacity unspecified. |
| `tmpfs` | bool | optional. RAM-backed mount; contents do not survive a stop. |
| `mode` | string | optional. Initial octal permissions (`755`, `0755`, `1777`). Omission leaves initial permissions unspecified. |

## Runtime behavior

A conforming runtime:

- **MUST** mount storage at `path` before lifecycle hooks run, so install <!-- tck: volume@1/mounted-before-hooks -->
  hooks can populate it.
- **MUST** make a block (non-tmpfs) volume persistent across container <!-- tck: volume@1/persists-across-restart -->
  restarts.
- **MUST** preserve block-volume contents across sandbox recreation. <!-- tck: volume@1/persists-across-recreate -->
  Recreation replaces the container and its writable layer within the
  same sandbox instance.
- **MUST** back a `tmpfs: true` entry with RAM; its contents are <!-- tck: volume@1/tmpfs-ram-backed -->
  scratch.
- **MUST** discard tmpfs contents on stop or recreation. <!-- tck: volume@1/tmpfs-cleared -->
- **SHOULD** apply `size` as a capacity limit and `mode` to the mount <!-- tck: volume@1/size-and-mode-applied -->
  root when allocating fresh storage. Reattachment preserves existing
  permissions. A runtime that cannot enforce `size` MAY treat it as
  advisory, but retains the declared request for compatibility checks.
- Ownership: the mount root **SHOULD** be writable by the agent user <!-- tck: volume@1/agent-writable-root -->
  (uid 1000); Kits that need different ownership fix it in a
  [lifecycle](lifecycle@1.md) startup hook (a fresh mount may come up
  root-owned).

## Identity and lifetime

A runtime **MUST** isolate storage by sandbox instance and cleaned <!-- tck: volume@1/instance-and-path-identity -->
destination path. Instance identity is runtime-owned, stable across
restart and recreation, and distinct for every new sandbox. A name is
an alias, not storage identity. Kit references, digests, display metadata,
and `source` attribution do not determine volume identity. Two sandboxes
composing identical Kits have independent storage.

A runtime **MUST** reuse existing block storage at a selected path when <!-- tck: volume@1/composition-independent -->
recreating the same instance, including after Kit updates, changes to
mixins, or replacement of the workload through the normal permission
gate. The instance's data remains available to its newly selected
composition. Packaging contributions into a published set does not
change this rule. New destinations receive fresh storage; renaming a
destination does not move its data.

A runtime **MUST** retain block storage for destinations no longer <!-- tck: volume@1/undeclared-retained -->
selected, without mounting them. Selecting the same destination again
reattaches its retained storage, subject to configuration compatibility.
Retaining data grants no access while its destination is undeclared;
reintroducing a path goes through the normal widening gate.

Removing a sandbox instance **MUST** delete all its volume storage, <!-- tck: volume@1/removal-deletes-storage -->
including retained, unmounted destinations. Creating another sandbox
with the same name or Kits allocates a new instance with fresh storage.
There is no automatic cross-instance reattachment. This does not require
secure erasure of the underlying medium.

Before recreating a sandbox, a runtime **MUST** compare each selected <!-- tck: volume@1/recreate-config-compatible -->
request with any retained request at its destination using the same
configuration equivalence as composition. This capability permits only
equivalent retained requests; it does not define a directional transition
such as accepting a larger minimum size. A difference in `size`, `mode`,
or `tmpfs` refuses recreation before replacing the container or changing
storage. It leaves the previous composition and data intact. This applies
even if a size limit was advisory or permissions were changed inside the
sandbox. No implicit resize, permission reset, or block/tmpfs conversion
is performed. Explicit operator-managed data migration or storage reset
is outside this capability's contract; Kits cannot request it.

A runtime **MUST** preserve existing contents and permissions when <!-- tck: volume@1/reattach-preserves-state -->
reattaching storage. The initial `mode` request is not reapplied.

A runtime **MUST** allocate new storage with empty contents before <!-- tck: volume@1/initially-empty -->
lifecycle hooks run; it does not copy files hidden by the mount from the
image. Hooks may initialize it. Reattached storage retains its contents.

## Composition

Paths union across the set. Requests at one cleaned destination **MUST** <!-- tck: volume@1/matching-requests-merge -->
merge when their concrete storage configurations match, at publication
and at runtime composition. Publication defers requests with re-exported
text inputs as described below. Compare typed configurations after
argument and environment expansion: clean the path, compare sizes by
byte value and modes by octal value, and treat omitted `tmpfs` as `false`.
Omitted or empty `size` and `mode` remain unspecified and differ from
explicit values. Equivalent spellings such as `1g`, `1024m`, and `1gib`,
or `755` and `0755`, match. Display metadata and provenance do not affect
compatibility; a required request wins over an optional one.

When publication retains a re-exported text argument, it preserves the
requests separately until create expands their configurations and
selection completes, using the groups described in
[SPEC-v3 §9.5](../../SPEC-v3.md#95-merging-a-set). A placeholder size is
not a different byte value. The merge and conflict rules apply to the
concrete selected requests after expansion.

Two different storage configurations at one cleaned destination **MUST** <!-- tck: volume@1/no-silent-merge -->
fail composition. A runtime does not choose one request or combine their
settings. Matching declarations within one declaration block are still
duplicates and rejected.

The cleaned path also conflicts with a
[host-mount@1](host-mount@1.md) declaration at that destination.

## Gate

The path is permission surface. A new path widens; `size`/`mode` changes do
not (they constrain, not grant).
