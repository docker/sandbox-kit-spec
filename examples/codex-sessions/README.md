# Codex host sessions

A declaration-only mixin that keeps Codex transcripts on the host after
sandbox removal. Compose it with the Codex workload or a workload plus
`codex-mixin`:

```sh
sbx run ./examples/codex --kit ./examples/codex-sessions .
```

The required `host-mount@1` request mounts one directory at
`/home/agent/codex-history`. An install hook runs as `agent` after the
Codex Kit's hooks and links `~/.codex/sessions` and
`~/.codex/archived_sessions` to subdirectories of that mount. Keeping both
on one filesystem lets Codex archive sessions by renaming their files.
The hook finishes before the agent starts, accepts its existing links,
and refuses to replace nonempty directories or unexpected symlinks.

Config, credentials, skills, and local indexes remain under the Codex
Kit's control. This persists transcripts, rather than all of `~/.codex`.

The runtime chooses the host directory; the Kit cannot name it. Storage
is shared across sandboxes composing this same Kit and survives tag
updates and sandbox removal. Concurrent sandboxes see each other's
transcripts; this Kit adds no writer locking. Use the runtime's storage
interface to find, copy, or remove the host files.

`sbxenv.yaml` composes this mixin instead of running host-side
`postCreate` commands. The previous setup stored transcripts under
`~/.sandbox-state/<sandbox-name>/codex` on the host. It is not migrated
automatically: before launching a replacement sandbox, copy its
`sessions` and `archived_sessions` directories into the runtime-owned
host directory. Keep the originals until the sessions are visible.

Build against the current frontend source:

```sh
task kit:dev KIT=codex-sessions
```
