# `com.docker.sandbox/lifecycle@1`

The Kit's setup and launch behavior, executed by the engine across the
sandbox's life: install hooks once at create, startup hooks every boot,
files written at start, and the interactive argv tail for TTY sessions. A
capability rather than grammar fields so a host that cannot execute them
refuses the Kit by name at preflight instead of silently never running its
setup.

- **Shape**: singleton — at most one entry per descriptor.
- **Permission surface**: **no** — hooks and files run inside the sandbox
  on the entrypoint's trust plane; nothing crosses the boundary the gate
  guards. What hooks can *reach* is bounded by the other capabilities
  ([network](network-policy@1.md), [credentials](credential@1.md),
  [SSH agent](ssh-agent@1.md)).

Background hooks detach from boot; they do not by themselves prevent
session-based sandbox auto-stop. A Kit that needs to survive its
last client disconnecting declares [long-running@1](long-running@1.md).

## Config

```yaml
- type: com.docker.sandbox/lifecycle@1
  config:
    install:                       # once per sandbox, at create
      - command:                   # string (run via `sh -c`) or argv list
          - sh
          - -c
          - printf '%s' "$WORKSPACE_DIR" > /home/agent/.claude.json
        user: "0"                  # optional execution user
        env: [WORKSPACE_DIR]       # env vars this hook may see — deny by default
        description: Seed trust flags
    startup:                       # every boot — MUST tolerate re-running
      - command: [sh, -c, "chown -R agent:agent /home/agent/.claude"]
        user: "0"
        background: false          # true detaches
        description: Re-own the volume mount root
    files:                         # written at start
      - path: /home/agent/.config/settings.json    # REQUIRED, absolute
        content: '{"timeout": ${{ kit.args.timeout }}}'
        mode: "0644"               # optional octal
        overwrite: false           # default true; false skips when the file exists
        description: Default settings
    interactive: []                # argv tail for an interactive (TTY) session
```

| Field | Type | Rules |
|---|---|---|
| `install[].command` | string \| list | REQUIRED. A string runs via `sh -c`; a list is exec argv. |
| `install[].user` | string | optional execution user (uid or name). |
| `install[].env` | list\<string\> | Env var names the hook consumes. **Deny by default**: a hook sees only what it declares, which is hygiene first and also makes its inputs enumerable. |
| `install[].description` | string | optional. |
| `startup[].command` | string \| list | REQUIRED. Same string-or-argv rule. |
| `startup[].user` | string | optional. |
| `startup[].background` | bool | Detach instead of blocking boot. |
| `startup[].env` | list\<string\> | Same deny-by-default rule. |
| `files[].path` | string | REQUIRED. Absolute after create-time expansion, including `${{ kit.env.HOME }}` from the final container environment ([§6.1](../../SPEC-v3.md#61-final-container-environment)). |
| `files[].content` | string | File body. `${{ kit.args.* }}` and `${{ kit.env.* }}` expand at create; `$VAR` remains literal in the file. |
| `files[].mode` | string | optional octal. |
| `files[].overwrite` | bool | Default **true**. `false` skips the write when the file exists (e.g. state on a persistent volume). |
| `interactive` | list\<string\> | Argv tail appended to the workload's launch command (image `Entrypoint` + `Cmd` is the headless mode) for an interactive (TTY) session. The image config's `Cmd` is single-valued, so the interactive variant has no native slot; it rides here because the engine consumes it like the hooks — behavior, not a grant. Meaningful on workload kits: the launch command it modifies is the workload's. |

A lifecycle entry declaring no hooks, no files, and no interactive tail is
invalid — drop the entry instead.

`interactive` names the same launch as the `newSession` verb of
[agent-interactive-sessions@1](agent-interactive-sessions@1.md); a Kit
declaring both gives them the same argv (an explicit `[]` included), and
validation rejects a mismatch. `agent-interactive-sessions@1` `newSession` defaults to this
tail when omitted.

## Runtime behavior

A conforming runtime:

- **MUST** run `install` hooks exactly once per sandbox, at create. <!-- tck: lifecycle@1/install-once -->
- **MUST** finish `install` hooks before the workload's entrypoint first <!-- tck: lifecycle@1/install-before-entrypoint -->
  runs.
- **MUST** run one composition's hooks in **dependency order** (a <!-- tck: lifecycle@1/hooks-dependency-order -->
  provider's hooks before its requirers').
- **MUST** hold the [network policy's](network-policy@1.md) install phase <!-- tck: lifecycle@1/install-network-scope-held -->
  open while install hooks run and close it before the entrypoint starts.
- **MUST** end install-phase [credentials](credential@1.md) with the <!-- tck: lifecycle@1/install-credentials-end-with-phase -->
  install phase, exactly as the network scope ends.
- **MUST** restrict a hook's environment to its declared `env` names plus <!-- tck: lifecycle@1/hook-env-restricted -->
  the **platform baseline**: `PATH`, `HOME`, `HOSTNAME`, `TERM`, `PWD`,
  `OLDPWD`, `SHLVL`, and `_`. Hooks run through a shell, which needs the
  first two to run anything and introduces the rest itself; pretending
  they can be absent would make every conforming runtime non-conforming.
  Baseline values **MUST** derive from the image and the sandbox, never <!-- tck: lifecycle@1/baseline-values-sandbox-derived -->
  from the host's environment — the baseline names are a shape, not a
  tunnel.
- **MUST** run `startup` hooks on every boot (create, stop/start, daemon <!-- tck: lifecycle@1/startup-every-boot -->
  restart, host reboot).
- Hook authors MUST make `startup` hooks idempotent: every boot means <!-- tck: lifecycle@1/startup-idempotent-authors -->
  every boot.
- **MUST** write `files` at start, each one belonging to the agent, and <!-- tck: lifecycle@1/files-written -->
  writable by it where the entry declares no `mode`: a file entry
  carries no `user:`, and this capability grants no permission surface
  precisely because its writes stay on the entrypoint's trust plane. A
  declared `mode` is applied as the kit asked, to a file the agent owns
  — a read-only file its owner can still change is on that plane, one
  the agent does not own is not. Which user the runtime writes as stays
  `default-users` below, and a runtime writing as root satisfies this by
  leaving the result the agent's. A path only root can own is an install
  hook's job — what `install[].user` is for.
- **MUST** honor each file's `overwrite` declaration: an existing file <!-- tck: lifecycle@1/files-overwrite-honored -->
  stays unless the entry says otherwise.
- **MUST** write `files` before the entrypoint runs, so the agent never <!-- tck: lifecycle@1/files-before-entrypoint -->
  observes the sandbox without them.
- **MUST** expand `${{ kit.args.* }}` in commands and file contents with <!-- tck: lifecycle@1/args-expanded -->
  the create-phase arg values, leaving `$VAR` untouched for the shell.
- **SHOULD** default execution users to root for install and the agent <!-- tck: lifecycle@1/default-users -->
  user (uid 1000) for startup and files when `user:` is unset, matching
  the platform floor's write-surface expectations.
- **MUST** launch interactive (TTY) sessions with the workload's launch <!-- tck: lifecycle@1/interactive-launch -->
  argv plus the `interactive` tail, and headless runs with the image
  config's `Entrypoint` + `Cmd` as-is.
- **MUST** refuse a Kit whose **required** lifecycle entry it cannot <!-- tck: lifecycle@1/required-unsatisfiable-refused -->
  execute (a host with no hook execution), rather than composing the Kit
  and silently skipping its setup.
- **SHOULD** attribute a failing hook to its Kit in errors (which Kit, <!-- tck: lifecycle@1/failing-hook-attributed -->
  which hook index).

## Composition

Install and startup lists concatenate in dependency order across the set;
files likewise. Composition MUST reject duplicate file paths and multiple <!-- tck: lifecycle@1/shared-paths-avoided -->
`interactive` declarations, including contributions from selected groups.
Repeated hook commands remain separate hooks. Singleton arity applies per
declaration block; selected contributions produce one effective entry.
