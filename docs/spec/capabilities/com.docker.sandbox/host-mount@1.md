# `com.docker.sandbox/host-mount@1`

One host directory shared with the host and across sandboxes composing
the same Kit. The runtime chooses its location and mount mechanism;
Linux bind mounts and VM filesystem sharing can satisfy the same grant.
Use [volume@1](volume@1.md) for sandbox storage without this sharing
contract.

- **Shape**: instance — one entry per path; duplicates rejected.
- **Permission surface**: yes — the path, separately marked host-shared.

## Config

```yaml
- type: com.docker.sandbox/host-mount@1
  config:
    path: /home/agent/.cache/pip   # REQUIRED, absolute and canonical
    mode: "0755"                 # optional initial directory permissions
```

| Field | Type | Rules |
|---|---|---|
| `path` | string | REQUIRED. Absolute, canonical in-container path; no `.`, `..`, doubled separators, trailing slash, NUL, or root `/`. |
| `mode` | string | optional. Octal (`755`, `0755`, `1777`), applied when the runtime creates the directory. |

There is no host-path field. The Kit requests where storage appears
inside the sandbox, not which host files it can access.

## Runtime behavior

A conforming runtime:

- **MUST NOT** let the Kit choose the host directory's location. <!-- tck: host-mount@1/runtime-owned-location -->
  The runtime owns it; a user MAY explicitly choose a replacement.
- **MUST** key the directory on the declaring Kit's identity and the <!-- tck: host-mount@1/isolated-by-kit -->
  cleaned `path`, so sandboxes composing the same Kit share storage,
  while another Kit declaring that path does not inherit it.
- **MUST** keep distinct destinations of the same Kit in separate <!-- tck: host-mount@1/isolated-by-path -->
  directories. A Kit's identity alone is not the storage key.
- **MUST** use a Kit identity another Kit cannot claim. A published <!-- tck: host-mount@1/identity-not-self-declared -->
  repository can supply that identity; descriptor display metadata and
  `source` attribution cannot. Identity for unpublished Kits is
  runtime-owned.
- **MUST** reuse the directory across tag or digest updates within one <!-- tck: host-mount@1/survives-kit-update -->
  Kit identity. Updating a Kit does not allocate fresh storage.
- **MUST** mount the directory at `path` before lifecycle hooks run. <!-- tck: host-mount@1/mounted-before-hooks -->
- **MUST** allow concurrent sandboxes of the same Kit to read and write <!-- tck: host-mount@1/shared-concurrently -->
  the directory. Kits coordinate access; the grant promises no locking
  or transactional behavior.
- **MUST** keep the directory's contents independently of any sandbox, <!-- tck: host-mount@1/survives-sandbox-removal -->
  including after its last sandbox is removed.
- **MUST** let the user find and remove the directory and access its <!-- tck: host-mount@1/listed-and-removable -->
  contents from the host. Sandbox writes reach this directory.
- **SHOULD** make the mount root writable by the agent user (uid 1000). <!-- tck: host-mount@1/agent-writable-root -->
- **SHOULD** apply `mode` when creating the directory; reopening it <!-- tck: host-mount@1/initial-mode-applied -->
  preserves existing permissions and contents.

Filesystem semantics MAY be weaker than those of `volume@1`: ownership
may be mapped from the host and overlayfs upper layers or xattrs may be
unavailable. Kits requiring those semantics use `volume@1`.

A runtime without host-directory sharing does not advertise this type.
An unclaimed required entry **MUST** fail closed; an optional one <!-- tck: host-mount@1/unadvertised-is-unmet -->
**MUST** be skipped and recorded without a host-sharing grant.

## Composition

Paths union across the set. `host-mount@1` and `volume@1` share the
cleaned in-container path as their storage identity key.

Two Kits declaring the same path, whether both use `host-mount@1` or <!-- tck: host-mount@1/no-silent-merge -->
one uses `volume@1`, conflict. A runtime **MUST NOT** silently merge
them, even when their configs are identical: shared storage has one
declaring Kit identity.

## Gate

The permission surface **MUST** list host-shared paths separately from <!-- tck: host-mount@1/separate-permission-surface -->
`volume@1` storage paths. Data written here reaches the host and other
sandboxes of the same Kit. A new path widens the grant; moving a path
from `volume@1` to `host-mount@1` also widens it. `mode` changes do not.
