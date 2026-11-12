# `com.docker.sandbox/sbx@1`

A declaration that the workload targets the sandbox agent platform: the
host launches the agent rather than letting the image entrypoint be PID 1,
and it honors the identity the image config states instead of assuming one.

- **Shape**: singleton, **config-less** — any `config` value is a
  validation error.
- **Permission surface**: **no** — it asks the host to run the workload a
  particular way and to read what the image already declares. Nothing
  crosses the boundary the gate guards.

Declaring it is a claim in both directions, which is what makes it
checkable from both sides. The kit asserts its filesystem can be operated
this way; the host owes the duties below. A runtime that cannot perform
them refuses the type by name rather than approximating it.

## Config

```yaml
- type: com.docker.sandbox/sbx@1
```

No `config`. The identity the host must honor is the image config's
`user`, which images already express; restating it here would create two
answers to one question, and one of them would go stale.

## What the kit provides

A workload declaring this type:

- **MUST** ship a POSIX shell at `/bin/sh` that the declared user can <!-- tck: sbx@1/posix-shell-present -->
  execute. The host runs hooks, install steps, and its own idle process
  through it. Occupying the path is not enough: a FIFO, socket, or device
  node, or bits that leave the declared user out, resolve here and then
  fail at the first hook. A link counts when what it resolves to is an
  ordinary file that user can execute.
- **MUST** ship a `bash` at `/bin/bash` the declared user can execute. <!-- tck: sbx@1/bash-present -->
  The agent is launched under bash specifically, because bash is what
  sources `BASH_ENV`; a POSIX shell would start the agent without its
  persistent environment.
- **MUST** declare a non-empty `user` in its image config. An image that <!-- tck: sbx@1/image-declares-user -->
  declares none leaves the host nothing to honor, and the host would be
  back to assuming.
- **MUST** resolve that user in `/etc/passwd` to a uid, a gid, and a home <!-- tck: sbx@1/user-resolves-in-passwd -->
  path. The host reads all four out of the image: the uid before the
  container exists, since it lands in the container's environment; the
  name to run commands as a login user; the gid to own what it writes;
  and the home as the working directory it writes from. A user the image
  names but does not resolve leaves the host guessing the rest.
  Resolution follows what a runtime does with the spelling: a numeric
  user is a uid rather than a login name — and stays one, so a numeric
  spelling a host cannot hold resolves to nothing rather than falling
  back to a login name that reads the same — a non-empty `user:group`
  suffix is the gid that applies instead of the passwd primary and
  resolves against `/etc/group` when it is named, and comments and
  malformed records are passed over as any account resolver passes over
  them. A row with no login name, a uid or gid a host cannot hold —
  anything at or above `4294967295`, whose top value is the reserved
  "leave this one alone" sentinel rather than an identity — or a home
  that is not absolute resolves nothing.
- **SHOULD** name the file by absolute path in `BASH_ENV` and ship it. <!-- tck: sbx@1/bash-env-names-a-shipped-file -->
  Without it the agent starts with whatever the image config carries and
  nothing the sandbox adds later, and a relative value resolves against
  whatever directory the agent happens to run from rather than against
  the image.

A kit artifact is judged without being run, so what the two shells are held to
before publication is presence, kind, and permission for the declared
user — an ordinary file at the path with an execute bit that identity
can use. Whether what is there behaves as `sh` or as `bash` is not
something image metadata can state, and an image that puts something
else at either path fails wherever the host uses it: hooks, install
steps, and the agent's own launch.

## Runtime behavior

A conforming runtime:

- **MUST NOT** let the image's entrypoint become PID 1. The entrypoint <!-- tck: sbx@1/entrypoint-not-pid-one -->
  names the agent's launch command, which the host reads and runs later;
  left in place it would prepend itself to whatever the host runs as PID 1.
- **MUST** run the agent, hooks, and file writes as the user the image <!-- tck: sbx@1/honors-image-user -->
  config declares — resolving its uid, gid, name, and home from the
  image — rather than as a fixed identity of the runtime's choosing.
- **MUST** launch the agent under `bash` so the file named by `BASH_ENV` <!-- tck: sbx@1/agent-launched-under-bash -->
  is sourced. The agent is not started from a login or interactive shell,
  so profile and rc files never run; this is the only thing that loads it.
- **MUST** place the workspace at the image config's working directory <!-- tck: sbx@1/workspace-at-workdir -->
  when a workspace is assigned and the image declares an absolute one,
  so the kit's paths and the host's agree. Observable wherever the host derives a path from the
  workspace, such as the `agent-context@1` profile it writes beside it.
- **MAY** refuse the type. Refusal fails resolution for a required entry;
  an optional entry is skipped and recorded, and the kit runs however the
  host runs an ordinary image.

### Workspace and sandbox environment

A runtime accepting this capability **MUST** supply the following <!-- tck: sbx@1/environment-provided -->
values in the workload's initial environment. The image need not declare
them.

| Variable | Value |
|---|---|
| `WORKSPACE_DIR` | The assigned workspace's non-empty absolute path inside the sandbox; unset when no workspace is assigned. |
| `SANDBOX_NAME` | The sandbox's non-empty runtime-assigned name. |
| `SANDBOX_ID` | The sandbox's non-empty runtime-assigned identifier. |
| `IS_SANDBOX` | The literal string `1`. |

These names are runtime-owned. The runtime **MUST** derive their values <!-- tck: sbx@1/environment-authoritative -->
from the sandbox it is creating, never inherit them from the host, and
apply them after image defaults, Kit argument exports, and explicit
runtime or runtime-launched process environment overrides. Conflicting inputs, including
empty overrides, are ignored for these names. With no assigned workspace,
this removes `WORKSPACE_DIR` even when another input supplied it.
This governs runtime launch inputs; a process can still change its own
environment and that of children it launches.
Overrides for other names retain the precedence in
[SPEC-v3 §6.1](../../SPEC-v3.md#61-final-container-environment).

A workspace may be host-mounted, cloned, or assigned within the sandbox's
own filesystem. No host bind mount does not mean no workspace. An image's
working directory alone does not designate a workspace; the runtime
assigns one. When an absolute image `WORKDIR` determines its placement
under the rule above, `WORKSPACE_DIR` identifies that location. A
process-specific cwd does not change the assigned workspace. Presence
identifies a path; it is not itself a readiness signal. The runtime
finishes initial cloning or other preparation before lifecycle install
commands, under [§12](../../SPEC-v3.md#12-runtime-environment).

`SANDBOX_NAME` and `SANDBOX_ID` **MUST** identify the same sandbox. <!-- tck: sbx@1/identity-consistent -->
Names can be reused; identifiers distinguish sandbox instances.
The ID **MUST** remain stable across restart and replacement of the <!-- tck: sbx@1/identity-stable -->
backing container within that sandbox.
A new sandbox created after deletion **MUST** receive a new ID, even <!-- tck: sbx@1/identity-new-after-deletion -->
when it reuses the name.
If rename is supported, the ID **MUST** remain stable and newly launched <!-- tck: sbx@1/identity-rename -->
processes receive the current name. A previously expanded descriptor
continues to hold its create-time values.
Kits **MUST NOT** parse the ID or infer a VM or container identifier <!-- tck: sbx@1/identity-opaque -->
from it.

The runtime **MUST** make these create-time values available to <!-- tck: sbx@1/environment-expanded -->
`${{ kit.env.NAME }}` expansion under SPEC-v3 §6.1. With no assigned
workspace, a reference to `${{ kit.env.WORKSPACE_DIR }}` is a missing-name
error. Restart uses the persisted expanded descriptor; it does not
re-expand after a later context change.

Install and startup hooks **MUST** receive these values only when <!-- tck: sbx@1/environment-hooks -->
explicitly listed in the hook's `env`, agreeing with the agent's assigned
workspace and sandbox identity at their respective launch times. These
names do not join lifecycle's automatic platform baseline. A requested
`WORKSPACE_DIR` remains unset when no workspace is assigned, rather than
becoming an empty string.

## Composition

Singleton, and a workload concern: the kit whose layers are the root
filesystem is the one whose image config carries the identity. A mixin
declaring it says nothing a host can act on, because a mixin's image
config does not become the composed image's.

## Gate

Not permission surface, so declaring it is not a widening. What it changes
is how the host runs the workload, not what the workload may reach.
