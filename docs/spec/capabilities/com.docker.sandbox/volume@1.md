# `com.docker.sandbox/volume@1`

One sandbox-private storage destination. Block storage belongs to the
sandbox name within its owner scope and outlives sandbox deletion; tmpfs
is scratch storage.

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
| `size` | string | optional. Dedicated block capacity when enforced, additive to the root disk; a RAM limit for tmpfs. Byte-size (`512m`, `2g`, `1gib`); suffixes use powers of 1024, with or without `i` or `b`. Omission leaves capacity unspecified. |
| `tmpfs` | bool | optional. RAM-backed mount; contents do not survive a stop. |
| `mode` | string | optional. Initial octal permissions (`755`, `0755`, `1777`). Omission leaves initial permissions unspecified. |

## Runtime behavior

A conforming runtime:

- **MUST** mount storage at `path` before lifecycle hooks run, so install <!-- tck: volume@1/mounted-before-hooks -->
  hooks can populate it.
- **MUST** make a block (non-tmpfs) volume persistent across container <!-- tck: volume@1/persists-across-restart -->
  restarts.
- **MUST** preserve block-volume contents when re-creating a sandbox, <!-- tck: volume@1/persists-across-recreate -->
  including delete followed by create under the same owner scope and
  name. Re-create replaces the container and its writable layer and
  resolves the composition again, whether through an update operation
  or delete followed by create.
- **MUST** back a `tmpfs: true` entry with RAM; its contents are <!-- tck: volume@1/tmpfs-ram-backed -->
  scratch.
- **MUST** discard tmpfs contents on stop, restart, re-create, or delete. <!-- tck: volume@1/tmpfs-cleared -->
- **SHOULD** apply `size` as dedicated capacity and `mode` to the mount <!-- tck: volume@1/size-and-mode-applied -->
  root when allocating fresh storage. Reattachment preserves existing
  permissions. An enforced block-volume `size` reserves that many bytes
  independently of the root filesystem and other volumes: filling one
  does not consume the capacity available to another. Physical devices
  may be shared if reservations provide this guarantee. A runtime that
  cannot enforce `size` MAY treat it as advisory, but retains the declared
  request for compatibility checks. Tmpfs `size` remains a RAM-backed
  capacity limit; it does not request block capacity.
- **MUST** disclose advisory `size` in its capability advertisement or <!-- tck: volume@1/advisory-size-disclosed -->
  documentation, including when using unreserved root-filesystem space.
  An omitted `size` leaves capacity to the runtime.
- Ownership: the mount root **SHOULD** be writable by the agent user <!-- tck: volume@1/agent-writable-root -->
  (uid 1000); Kits that need different ownership fix it in a
  [lifecycle](lifecycle@1.md) startup hook (a fresh mount may come up
  root-owned).

## Identity and lifetime

A runtime **MUST** isolate block storage by (owner scope, sandbox name, <!-- tck: volume@1/name-and-path-identity -->
cleaned destination path). Owner scope is the runtime-owned namespace
used for sandbox names, such as the local host user or a hosted account
or organization. Reusing a name in that scope reuses its retained
storage. Different names or owner scopes have independent storage even
when composing identical Kits. Kit references, digests, display metadata,
and `source` attribution do not determine volume identity.

If sandbox rename is supported, a runtime **MUST** move all of that <!-- tck: volume@1/rename-follows-storage -->
name's storage and retained requests to the new name within the same
owner scope, including unmounted paths. The old name then has no storage.
A rename to a name with existing storage or a live sandbox refuses
without changing either name's state; it never merges their data.

A runtime **MUST** reuse existing block storage at a selected path when <!-- tck: volume@1/composition-independent -->
re-creating under the same owner scope and name, including Kit updates,
changes to mixins, or replacement of the workload through the normal
permission gate. The name's data remains available to its newly selected
composition. Packaging contributions into a published set does not
change this rule. New destinations receive fresh storage; renaming a
destination does not move its data.

A runtime **MUST** retain block storage for destinations no longer <!-- tck: volume@1/undeclared-retained -->
selected, without mounting them. Selecting the same destination again
reattaches its retained storage, subject to configuration compatibility.
Retaining data grants no access while its destination is undeclared;
reintroducing a path goes through the normal widening gate.

Deleting a sandbox **MUST NOT** delete its block-volume storage by <!-- tck: volume@1/delete-retains-storage -->
default, including retained, unmounted destinations. Delete unmounts it;
create under the same owner scope and name reattaches selected paths,
subject to configuration compatibility and the gate. Tmpfs contents are
discarded. A new name starts with fresh storage.

A runtime **MUST** let the operator list retained block storage by <!-- tck: volume@1/retained-storage-listed -->
sandbox name and cleaned path, including after the sandbox is deleted.

A runtime **MUST** let the operator explicitly remove block storage per <!-- tck: volume@1/explicit-removal-deletes-storage -->
path or for a whole sandbox name, including unmounted destinations and
their retained requests. A later create at a removed destination starts
empty and can use a different configuration. A runtime MAY offer this as
an opt-in part of sandbox deletion; it is never the default. Removal of
mounted storage may require stopping or deleting its sandbox first.
Secure erasure of the underlying medium is not required. Kits cannot
request storage removal or migration.

A runtime **MUST NOT** silently expire retained storage. A retention or <!-- tck: volume@1/no-silent-expiry -->
quota policy is allowed only when documented and its removals are visible
to the user. Such removal ends the affected storage lifetime just as
explicit removal does.

Before re-creating or creating a sandbox, a runtime **MUST** compare each <!-- tck: volume@1/recreate-config-compatible -->
selected request with any retained request at its destination using the
same configuration equivalence as composition. This capability permits
only equivalent retained requests; it does not define a directional
transition such as accepting a larger minimum size. A difference in
`size`, `mode`, or `tmpfs` refuses the operation before allocating storage
or replacing the container, leaving prior composition and data intact.
The error names the path and differing setting. This applies even if a
size limit was advisory or permissions were changed inside the sandbox.
No implicit resize, permission reset, or block/tmpfs conversion is
performed. The operator resolves a conflict by restoring the earlier
configuration or explicitly removing retained storage. A discarded tmpfs
has no retained storage or request after deletion; a retained block
request still refuses conversion to tmpfs on a later create.

A runtime **MUST** preserve existing contents and permissions when <!-- tck: volume@1/reattach-preserves-state -->
reattaching storage. The initial `mode` request is not reapplied.

A runtime **MUST** allocate new storage with empty contents before <!-- tck: volume@1/initially-empty -->
lifecycle hooks run; it does not copy files hidden by the mount from the
image. Hooks may initialize it. Reattached storage retains its contents.

## User-facing lifecycle and durability

The following table applies within one owner scope and sandbox name.
"Preserved" retains contents and permissions; "fresh" starts empty before
hooks. Reattachment is subject to configuration compatibility and the
gate. Unsupported operations need not be offered by a runtime.

| Operation | Block volume | Tmpfs volume |
|---|---|---|
| Stop / start, restart | Preserved | Discarded |
| Hibernate / resume (suspend) | Preserved | MAY be preserved when memory is restored; otherwise discarded |
| Re-create: Kit update, mixin change, workload replacement | Preserved | Discarded |
| Delete sandbox | Retained, unmounted | Discarded |
| Create with retained storage under the same name | Selected paths reattached | Fresh |
| Create with a new name | Fresh | Fresh |
| Explicit volume removal | Deleted | Not applicable |

A runtime **MUST** preserve block storage during supported hibernate / <!-- tck: volume@1/preserves-across-suspend-and-move -->
resume and any host or node move it performs as a normal lifecycle
operation. This does not require replication or survival of arbitrary
infrastructure failure.

After infrastructure failure, a runtime **MUST NOT** silently roll back <!-- tck: volume@1/no-silent-rollback -->
volume contents. Restoring an earlier point in time reports the restore
and the approximate age of the restored data to the user. Unrecoverable
data loss fails the sandbox visibly; it does not start with empty storage
as though newly allocated. Explicit removal or a disclosed retention
policy is distinct from infrastructure loss.

A runtime **MUST** document its block-volume durability guarantee, <!-- tck: volume@1/durability-documented -->
including host or disk loss and any recovery-point loss bound. For
example, it may guarantee survival of single-host loss or disclose that
host loss can destroy all local data. This capability has no Kit-visible
replication or durability selector.

A runtime **MUST NOT** attach a name's storage to a different sandbox <!-- tck: volume@1/no-cross-name-reattachment -->
name, except through the explicit rename above. Copying or migrating data
is an operator action outside this capability. For host access and
concurrent sharing across sandboxes composing the same Kit, use
[host-mount@1](host-mount@1.md): it keys storage on Kit identity and path,
while `volume@1` keys private storage on owner scope, sandbox name, and
path. Separately managed volumes attachable to several sandboxes need a
separate capability contract.

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

A runtime **MUST** identify reattached paths as existing data on the <!-- tck: volume@1/retained-data-surfaced -->
create permission surface. Reattaching an already granted path is not a
new grant; reintroducing a path removed from the composition still goes
through the normal widening gate. Retaining bytes alone grants no access.
Reusing a name for an unrelated composition can therefore expose that
name's earlier data within its owner scope, which this surface makes
visible before create.
