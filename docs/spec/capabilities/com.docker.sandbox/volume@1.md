# `com.docker.sandbox/volume@1`

One persistent (or tmpfs) path the workload needs backed by storage that
outlives the container.

- **Shape**: instance — one entry per path; duplicates rejected.
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
| `size` | string | optional. Byte-size (`512m`, `2g`, `1gib`). |
| `tmpfs` | bool | optional. RAM-backed mount; contents do not survive a stop. |
| `mode` | string | optional. Octal (`755`, `0755`, `1777`). |

## Runtime behavior

A conforming runtime:

- **MUST** mount storage at `path` before lifecycle hooks run, so install <!-- tck: volume@1/mounted-before-hooks -->
  hooks can populate it.
- **MUST** make a block (non-tmpfs) volume persistent across container <!-- tck: volume@1/persists-across-restart -->
  restarts, and **SHOULD** make it survive sandbox recreate, so agent state <!-- tck: volume@1/should-survive-recreate -->
  (sessions, caches) outlives the container image. How volume identity is
  keyed (sandbox, Kit, path) is runtime-owned; a recreate under the same
  identity reattaches the same volume.
- **MUST** back a `tmpfs: true` entry with RAM; its contents are <!-- tck: volume@1/tmpfs-ram-backed -->
  scratch and vanish on stop.
- **SHOULD** apply `size` as a capacity limit and `mode` to the mount <!-- tck: volume@1/size-and-mode-applied -->
  root. A runtime that cannot enforce `size` MAY treat it as advisory.
- Ownership: the mount root **SHOULD** be writable by the agent user <!-- tck: volume@1/agent-writable-root -->
  (uid 1000); Kits that need different ownership fix it in a
  [lifecycle](lifecycle@1.md) startup hook (a fresh mount may come up
  root-owned).

Whether removing a sandbox and later creating one with the same name
counts as a recreate is runtime-owned. Removal may delete its storage
or retain it for a later sandbox.

A runtime reattaching storage across sandbox removal **MUST** check <!-- tck: volume@1/reattach-checks-kit-identity -->
the Kit identity before attaching it, so a different Kit cannot inherit
the data. Descriptor display metadata and `source` attribution are not
sufficient identity evidence.

## Composition

Paths union across the set. Two Kits declaring the same path is a
composition conflict — a runtime MUST NOT silently merge them. <!-- tck: volume@1/no-silent-merge -->

The cleaned path also conflicts with a
[host-mount@1](host-mount@1.md) declaration at that destination.

## Gate

The path is permission surface. A new path widens; `size`/`mode` changes do
not (they constrain, not grant).
