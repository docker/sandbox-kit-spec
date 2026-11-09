---
name: sbxenv
description: >-
  Creates or updates an sbxenv.yaml, the Docker Sandboxes (sbx) environment
  file read by `sbx env plan|create|run|rm`. Inspects the working directory
  and suggests v3 kits from the live Docker Hub catalog (sbx and docker
  orgs), sets kit args and environment args, sets up credential bindings,
  secrets and registry credentials, and refreshes kit references to the
  highest version tags (and digests when pinned). Use when asked to create,
  write, scaffold, update, upgrade, bump, pin or review an sbxenv.yaml or
  ~/.sbxenv.yaml, or to set up a project for `sbx env`.
---

# Create or update an sbxenv.yaml

`sbxenv.yaml` declares one sandbox environment: the agent, the kits composed
onto it, mounts, env, args, secrets and credential bindings. Field-by-field
rules are in [REFERENCE.md](REFERENCE.md). Read it before writing a field
you are not sure about, because decoding is strict.

## Ground rules

- **Look up the catalog live, every time.** Kits, tags and digests change
  daily. Get every kit name, tag, digest, arg and credential from
  `scripts/kits.py` in this run. Never take them from memory, from an
  earlier conversation, or from the examples in these files. If Hub is
  unreachable, say so and stop suggesting kits. Do not guess.
- **Pick the highest version tag, not the one pushed last.** A republish
  of an older release, or a moving tag such as `latest`, can be newer on
  Hub's timeline than the highest version. Resolve the release with
  `kits.py update <repo>` (or `update` on the current ref) and take
  `newestRelease` / `suggested` — that helper ranks dotted version tags,
  not push time. Do not take the first row of `kits.py tags`: that list
  is ordered by Hub's `last_updated`. Do not write a bare untagged ref or
  `:latest` when a release tag exists.
- **Suggest v3 kits.** Search results are v3-only by default. If the file
  uses a v2 kit (often a `-kit` suffix) and a v3 sibling exists, offer the
  move.
- **Never write secret values.** Use `ref:` or `command:`. A binding
  approves a credential; it does not contain one.
- **Guide, don't interrogate.** At each step, recommend a concrete choice
  with a one-line reason, and ask the user to confirm or adjust. Batch
  related questions. Skip questions the workspace already answers.
- **When updating, edit in place.** Keep the user's comments, ordering and
  formatting. Change only what the user agreed to.

## The helper

`scripts/kits.py` is in this skill's directory. The commands below write
it as `<skill-dir>/scripts/kits.py`; substitute the path this `SKILL.md`
was loaded from. It uses only the standard library, writes JSON, and
needs only anonymous access to public repositories. Run it with
`python3 -I`. A nonzero exit means at least one reference failed; its
entry carries `error` and the other entries are still valid. Kits
published before descriptors switched to JSON need PyYAML to describe.

| Command | Use |
|---|---|
| `kits.py search [TERM …]` | v3 kits under `sbx/` and `docker/`, ranked by pulls. Each term is a separate Hub search, merged. No terms lists the whole catalog for those orgs. `--orgs a,b` changes the orgs. `--include-v2` adds v2 kits. |
| `kits.py describe REF … [--arg [HANDLE.]NAME=VALUE …] [--platform OS/ARCH]` | From the live descriptor: `kind` (workload or mixin), `agent` (launchable agent), `handle` (what `agent:` uses), `version`, the create-phase `args` an sbxenv may set, `buildArgs` (fixed by the tag; the defaults shown are declarations, not what was built), `buildSecrets` (build input declarations with no runtime value), `credentials` with a `suggestedBinding` each, and `hostGrants` (ssh-agent, ports, privileged, …). Requests inside capability groups are included and take the group's optionality; `group` names the group. Kit-arg references in a credential resolve from arg defaults and `--arg` (a `HANDLE.` prefix scopes it to one kit); any left over are listed in `unresolved`, and that credential gets no `suggestedBinding`. When the descriptor is only on the platform manifests, the one for `--platform` is read (default `linux/<this machine's arch>`); `platform` says which was used. Pass the architecture of the machine that runs `sbx`, if it differs from the one you are on. |
| `kits.py update REF … [--pin]` | `suggested` is the reference to move to: the highest version release tag (numeric dotted order, not Hub push time), or the same moving tag. It carries the new digest when REF was pinned or `--pin` is given; a digest-only REF moves to the highest release's digest and stays digest-only. Also reports `newestRelease` and notes (major bumps, moving tags). Local and git sources are skipped. |
| `kits.py pin REF …` | The digest the reference's tag points to now, as `suggested` (`TAG@DIGEST`). It keeps the release exactly as written and never upgrades. |
| `kits.py tags REF` | All tags with digests, ordered by Hub's last-updated time. Use this to inspect what exists; do not pick a release from the top of the list — use `update` for that. |

## Step 1: Decide what you're doing

Look for the target file:

- `./sbxenv.yaml` exists → **update** (step U).
- `./.sbxenv.yaml` exists → it is not read from a project directory. Offer
  to rename it to `sbxenv.yaml`, then update.
- The user names `~/.sbxenv.yaml` → this is their personal base layer.
  Usually it holds only personal defaults (bindings, `sandboxOptions`),
  not project kits.
- Nothing exists → **create** (step C).

## Step C: Create

### C1. Read the workspace

Collect signals, then turn them into search terms. Read only what you need:
the file listing, manifest files and the git remote.

| Signal | Search terms |
|---|---|
| `AGENTS.md`, `CLAUDE.md`, `.claude/`, `.codex/`, `GEMINI.md`, `.cursor/` | the agent it implies (`claude`, `codex`, `gemini`, `copilot`, …). Otherwise ask which agent. |
| git remote on github.com | `github`, `gh`. Add `ssh` if the remote is SSH or commits are signed. |
| `go.mod` | `go`, `golang` |
| `package.json`, `pnpm-lock.yaml`, `bun.lockb` | `node`, `pnpm`, `bun` |
| `pyproject.toml`, `requirements*.txt`, `uv.lock` | `python`, `uv` |
| `Cargo.toml` | `rust` |
| `pom.xml`, `build.gradle*` | `java`, `maven`, `gradle` |
| `Dockerfile`, `compose*.yaml` | `docker`, `buildx`, `compose` |
| `Taskfile.yaml`, `Makefile`, `justfile` | `task`, `make`, `just` |
| `*.tf`, `helm/`, `k8s/`, `kustomization.yaml` | `terraform`, `helm`, `kubectl` |
| docs (`*.md` heavy, `.vale.ini`) | `vale`, `markdown` |
| local kit directories (`*.yaml` with `schemaVersion: "3"`, often under `kits/` or `.sbx/`) | none. Offer them as `./path` sources. |

Then run:

```bash
python3 -I <skill-dir>/scripts/kits.py search <terms…>
python3 -I <skill-dir>/scripts/kits.py search    # whole catalog, to pick the agent
```

Run `describe` on the candidates. You need `kind` and `agent` to tell
agent workloads from mixins.

### C2. Propose a composition

A composition has **exactly one workload** (the agent) and any number of
mixins.

Check capability versions separately from Kit release tags. A runtime
accepts exact versioned types; support for `@2` does not satisfy a
required `@1`. Optional requests and groups do not express alternatives.
For Kits offering optional agent-context versions, confirm the runtime
policy accepts one exact version across the composition or skips both.
Selecting both versions fails composition. An sbxenv does not add a
capability-version negotiation field.

- Pick the agent workload (`kind: workload`, `agent: true`). Set
  `agent:` to its `handle` and list the kit under `kits:`.
- Pick mixins for the signals you found. A tool the workload already
  provides needs no mixin, so check its `provides`.
- When both `X` and `X-mixin` exist: the plain kit is the workload, and the
  `-mixin` layers that agent onto another workload. Use the mixin only when
  the user wants a different base.

For each OCI kit you keep, run `kits.py update <org>/<name>` (no tag) and
use `newestRelease` as the tag in the file. That is the highest version,
not the tag Hub lists first. Skip this for local `./path` and `git+`
sources.

Present the proposal as a short table (kit, kind, tag, why) and ask the
user to confirm, add or remove kits.

### C3. Kit args and environment args

For every chosen kit, take `args` from `describe`:

- `required: true` → must be set. Ask for the value.
- Optional args → set one only when the user wants a non-default value.
  Show the `description`, plus the `enum` or `pattern` the value must match.
- `buildArgs` (usually `version`) cannot be set on a published kit. Choose
  a different tag instead. Read the installed release from `version`, not
  from a build arg's default.

Put the value directly in the kit entry's `args:` when it is a project
decision. Declare an **environment arg** and reference it as
`${{ env.args.<name> }}` when:

- callers will vary it per run (model, target repo, branch), or
- one value feeds several kits, or
- a required kit arg has no sensible project-wide value (declare the env
  arg with `required: true` so `sbx env run` asks for `--env-arg`).

`--env-arg` and `--kit-arg` are experimental flags. Mention this when you
rely on them.

`buildSecrets` are consumed while building source kits. Published images
need no binding for them, and an sbxenv cannot set them as create args.
Do not treat them as runtime credentials. Source builds need the separate
caller integration described by the kit's build instructions.

### C4. Credentials, secrets and bindings

For each entry in `credentials` from `describe`:

1. **Mechanism.** If `mechanisms` has both `apiKey` and `oauth`, ask which
   one the user will use. Subscription logins (Claude, ChatGPT) usually mean
   `oauth`, and tokens mean `apiKey`. Write a `bindings.<service>` block
   with only that mechanism, copying its domains from `suggestedBinding`.
   An `apiKey` with no inject rules (only `apiKeyEnv`) yields an empty
   domain list: the kit reads the key from an environment variable, and
   the binding approves that with nothing injected outbound.
   A credential whose service or domains reference a kit arg is resolved
   with the arg defaults. When the file sets that arg, rerun
   `describe REF --arg NAME=VALUE` with the same value. If `unresolved`
   is not empty, `suggestedBinding` is null: supply the missing arg, or
   for a `${{ kit.env.NAME }}` reference ask the user what the variable
   will hold in the sandbox. Never copy a `${{ … }}` into the file.
2. **Secret source (apiKey only).** Ask the user to run `sbx secret ls` on
   the host.
   - If the service is already in the global store, the binding is enough.
   - Otherwise add `secrets.<service>` with `ref:` (for example 1Password
     `op://Vault/item/field`) or `command:` (for example
     `gh auth token --hostname github.com` for `github`).
   - If neither fits, the user stores the secret on the host with
     `sbx secret set` (see `sbx secret --help`) and the binding alone
     approves it. Do not write a `value:` entry, even on request: the
     file is meant to be committed, and a literal in it is a leaked
     secret.
3. **OAuth.** No `secrets:` entry. The user signs in once on the host.
4. **Required vs optional.** A credential that is not optional and has no
   binding makes create fail. An optional one degrades silently. Say which
   it is.

For `hostGrants`:

- `ssh-agent@1` (not optional) → add a prerequisite comment: an SSH agent
  must be running with a key loaded (`ssh-add -l`).
- `host-port@1`, `host-mount@1`, `privileged@1`, `dockerd@1` → explain what
  the user will be asked to approve.
- A kit that publishes ports (`port@1`) → offer a `ports:` entry to pin the
  host port.

Private kits or images on a non-public registry need a `registries:` entry
(see REFERENCE.md).

### C5. Workspace and options

- Write `workspace: .` explicitly. Leaving the key out mounts nothing,
  so the agent would start without the project checkout. Offer
  `{path: ., clone: true}` when the user wants the agent on a private
  clone (the directory must be a git repository, not a worktree).
- Add `env:` only for values the sandbox needs that no kit sets.
- `sandboxOptions`: suggest `pullPolicy: always` when kits use tags without
  digests. Add `memory`/`cpus` only if the user asks or the project is
  heavy.
- Add `lifecycle` only for host-side steps (generating or fetching
  something the mount needs). Note that it runs on the host with the user's
  privileges.

### C6. Pin or not

Ask: **tags only**, or **tag + digest**? Tag + digest is reproducible, and
this skill can refresh it later. To pin, run `kits.py pin <each OCI ref>`
and use `suggested` as each entry's reference. `pin` keeps the tag chosen
in C2 and C3, whose args and credentials you already inspected.

Do not use `update` here: it moves a tag to the highest version release,
which can undo a version chosen on purpose (for example to satisfy
another kit's `requires`). If `update` shows a higher version, offer it
separately, and `describe` the new reference again before adopting it.

### C7. Write, then check

Write `sbxenv.yaml` in this style:

- A header comment with prerequisites (sign in, secrets that must exist,
  SSH agent) and the commands (`sbx env plan`, `sbx env run`,
  `sbx env rm`).
- A short comment above each kit, arg and binding saying why it is there.
- Then verify (see [Checks](#checks)) and tell the user to run
  `sbx env plan` on the host to preview.

## Step U: Update

1. **Read** the file. List every `kits:` source (bare strings and
   `source:` values) and `agent:`.
2. **Refresh** the OCI references in one call:
   `kits.py update <refs…>`. Pinned refs (`@sha256:`) come back with new
   digests automatically.
3. **Present** a table: current → suggested, plus notes. Flag major
   version jumps. Trust `suggested` / `newestRelease` over any
   last-pushed ordering you see elsewhere. For moving tags (`latest`,
   `edge`), show `newestRelease` and offer to switch to that fixed
   release. For unpinned refs, offer `--pin`.
4. **Diff what changes.** For each kit the user accepts, `describe` the old
   and new refs and compare:
   - **args**: an arg the file sets that the new version no longer declares
     must be removed. A new `required` arg must be set. A tightened
     `enum`/`pattern` must still accept the file's value.
   - **credentials**: a new service, mechanism or domain needs a binding
     change. A removed one can be pruned.
   - **hostGrants** and `kind`: a new required grant (such as
     `ssh-agent@1`) needs a prerequisite note. A changed `handle` or kind
     may require changing `agent:`.
5. **Review the rest.** Check for credentials without bindings, v2 kits
   with v3 siblings (`search <name>`), a project `.sbxenv.yaml`, and
   missing `schemaVersion: "1"`. Offer any new kit the workspace signals
   now suggest.
6. **Apply** only what the user accepted, using in-place edits. Update the
   comments that mention versions. Then run [Checks](#checks).

## Checks

Before you finish, confirm each of these:

- `schemaVersion: "1"` is present, and `agent:` is set.
- `agent:` is a built-in or the `handle` of exactly one workload under
  `kits:`.
- Every `kits` entry is a string or `{source, args}`, and every arg name is
  declared by that kit as a create-phase arg.
- Every `${{ env.args.x }}` has a matching `args.x`. Required env args are
  documented in the header with an `--env-arg` example.
- Each `secrets.*` entry has exactly one of `value`/`ref`/`command`, and no
  literal secret is committed.
- Every non-optional credential has a binding, and each binding names only
  the mechanisms the user chose.
- `additionalWorkspaces` and `workspace.clone` both require `workspace`.
- The YAML parses. Use whatever parser is available (`yq`,
  `python3 -c 'import yaml'`, `ruby -ryaml`). If there is none, say so
  rather than claiming the file is valid.
- If `sbx` is on PATH, run `sbx env plan`. Otherwise ask the user to run it
  on the host and share any errors.

After writing, report what changed and what the user still has to do on
the host (create secrets, `sbx login`, start an SSH agent, OAuth sign-in).
