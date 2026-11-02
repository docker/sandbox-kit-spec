# Claude host sessions

A declaration-only mixin that keeps Claude Code session state on the
host after sandbox removal. Compose it with the Claude workload or a
workload plus `claude-mixin`:

```sh
sbx run ./examples/claude --kit ./examples/claude-sessions .
```

Compose with a Claude kit that declares **no** private session volumes
(this repository's `examples/claude` after the sessions split). A
published `sbx/claude` tag that still mounts `~/.claude/projects` and
siblings will fail the install hook: those mounts occupy the paths the
mixin needs to replace with symlinks.

The required `host-mount@1` request mounts one directory at
`/home/agent/claude-history`. An install hook runs as `agent` after the
Claude Kit's hooks and links these directories under `~/.claude`:

- `projects`: conversation transcripts and project memory.
- `sessions`: per-session state.
- `todos`: task state.
- `shell-snapshots`: shell state snapshots.
- `statsig`: feature-flag cache.

The hook finishes before the agent starts, accepts its existing links,
and refuses to replace nonempty directories or unexpected symlinks.
Settings, credentials, onboarding flags, and skills stay under the
Claude Kit's control. The Claude workload and agent mixin declare no
private session volumes; compose this mixin to retain their state.

The runtime chooses the host directory; the Kit cannot name it. Storage
is shared across sandboxes composing this same Kit and survives tag
updates and sandbox removal. Concurrent sandboxes see each other's
state; this Kit adds no writer locking. Use the runtime's storage
interface to find, copy, or remove the host files.

Older Claude Kits mounted private volumes at these five destinations.
Before recreating an existing sandbox, copy any state to keep into the
runtime-owned host directory. Removing the volume declarations leaves
their data retained but unmounted until sandbox removal; the new mixin
does not migrate it automatically. A fresh sandbox needs no migration.

Build against the current frontend source:

```sh
task kit:dev KIT=claude-sessions
```
