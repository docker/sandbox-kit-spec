# sbxenv.yaml field reference

`sbxenv.yaml` is the sbx CLI's declarative description of one sandbox
environment. `sbx env plan|create|run|exec|rm` read it. Its `schemaVersion`
belongs to sbx environments and is unrelated to the Kit descriptor's
`schemaVersion: "3"`.

Decoding is strict: an unknown key anywhere is an error. Keys are
lowerCamelCase.

## Files and merging

| File | Role |
|---|---|
| `<project>/sbxenv.yaml` | The project file. It is the only name `sbx env` reads from a directory. |
| `~/.sbxenv.yaml` | Your personal base layer, merged beneath every project file. Keep personal defaults here, such as your own bindings or `sandboxOptions`. |
| any other path | Read only when passed explicitly: `sbx env run path/to/file.yaml`. |

- Running `sbx env run` without a path merges `~/.sbxenv.yaml` beneath
  `./sbxenv.yaml`. Running `sbx env run .` (an explicit path) skips the user
  base.
- Several paths can be given. They merge in order, and later files win.
  Lists under `lifecycle` concatenate, so base-layer commands run first.
- A project `.sbxenv.yaml` (with the leading dot) is **not** read from the
  project directory. Offer to rename it to `sbxenv.yaml`.
- Relative paths (`workspace`, local kit sources, `additionalWorkspaces`)
  resolve against the directory of the file that declares them.

## Top-level keys

| Key | Required | Meaning |
|---|---|---|
| `schemaVersion` | yes | `"1"` (quoted string). |
| `name` | no | Sandbox name, and the scope for secrets provisioned by this file. Defaults to `<agent>-<workspace-basename>`. `--name` overrides it. |
| `args` | no | Environment arguments. See [Arguments](#arguments). |
| `agent` | yes | A built-in agent (`claude`, `codex`, …) or the **handle** of the agent workload kit listed under `kits`. |
| `entrypoint` | no | argv list that replaces the agent's entrypoint. `[]` clears it. No shell parsing. |
| `cmd` | no | argv list that replaces the agent's default command. `[]` clears it. Extra args after `--` on `sbx env run` are passed through. |
| `kits` | no | Kits to compose. See [Kits](#kits). |
| `workspace` | no | Primary read/write mount: a path string, or `{path, clone}`. Omit the key to mount nothing. A blank path is an error. |
| `additionalWorkspaces` | no | List of `{path, target?, readOnly?}`. Requires `workspace`. `target` must be an absolute container path without `:`. |
| `mounts` | no | Map of absolute sandbox path → host directory. |
| `env` | no | Map of environment variables set inside the sandbox. Values are strings. `$VAR` is passed through literally. |
| `sandboxOptions` | no | Create-time knobs. See [sandboxOptions](#sandboxoptions). |
| `secrets` | no | Secrets provisioned at this sandbox's scope. See [Secrets](#secrets). |
| `bindings` | no | Per-service credential approvals. See [Bindings](#bindings). |
| `registries` | no | Registry pull credentials, keyed by hostname. See [Registries](#registries). |
| `mcp` | no | `servers:` list of `{name, url}` or `{name, command, args}`. Exactly one of `url`/`command` per entry. Registrations are host-global, and `sbx env rm` leaves them. |
| `ports` | no | List of `{sandbox, host?, protocol?, hostIP?}`. `sandbox` is required (1–65535). `host` 0 or omitted means ephemeral. `protocol` is one of `tcp`, `tcp4` (default), `tcp6`, `udp`, `udp4`, `udp6`. An empty `hostIP` means loopback. |
| `lifecycle` | no | Host-side commands. See [Lifecycle](#lifecycle). |

## Kits

Each entry is a bare reference string, or a mapping with `source` and `args`:

```yaml
kits:
  - sbx/github-ssh:1.1.0                     # OCI, tag
  - sbx/gh:2.103.2@sha256:…                  # OCI, tag + digest (pinned)
  - source: docker/sbx-kit-codex:0.166.0     # OCI with kit args
    args:
      model: gpt-6.1-sol
  - ./kits/dev-tools                         # local directory (built from source on create)
  - ./dist/tool.zip                          # local ZIP
  - git+https://github.com/org/repo.git#dir=kits/tool   # git
```

- **Handle.** A v3 kit's handle is the last path segment of its repository:
  `sbx/claude` → `claude`, `docker/sbx-kit-codex` → `sbx-kit-codex`. For git
  sources it is the `dir=` basename, and for a directory it is the directory
  name. `agent:` names the workload kit by this handle.
- **One workload.** A composition has exactly one `kind: workload` kit (the
  agent) and zero or more `kind: mixin` kits.
- **Relative sources.** Only `./…`, `../…`, `.`, `..`, or `*.zip` count as
  file paths. A bare `kits/tool` is read as a registry reference.
- **Kit args.** `args` keys are the bare names the kit declares (no `kit.`
  prefix). An arg the kit does not declare is an error. A value must satisfy
  the kit's `enum`/`pattern`.
- **Published kits.** A published (OCI) kit accepts only its **create-phase**
  args. Args with `buildArg` were baked in when the kit was published. To
  change one of those (typically `version`), pick a different tag.
  Local-directory and git sources build on create, so their build args apply.
- `--kit-arg name=value` (all kits) or `--kit-arg kit.name=value` (one kit
  handle) overrides `args` for a single invocation. `--kit-args-file` reads
  `name=value` lines. Both flags are experimental.

## Arguments

Environment arguments let one file serve several invocations without editing
it. They are declared with the same shape a kit uses:

```yaml
args:
  model:
    description: Codex model
    default: gpt-6.1-sol          # or `required: true`; mutually exclusive
    pattern: '^[A-Za-z0-9._-]+$'  # or enum: [...]; mutually exclusive
```

- Reference them anywhere else in the file as `${{ env.args.<name> }}`,
  including inside a kit entry's `args`. This is how one value fans out to
  kits.
- `${{ env.projectDir }}` is the project directory, and
  `${{ env.fileDir }}` is the directory of the file containing the
  reference.
- Values come only from `--env-arg name=value` or `--env-args-file FILE`.
  Both are experimental. The host environment is never read.
- An undeclared reference, a missing required value, a supplied name that
  isn't declared, and a value failing `enum`/`pattern` are all errors.
- Substitution is structural: a value always stays inside one scalar and
  cannot add keys.
- The `args:` block itself is never expanded.

Use an env arg for a value that callers vary (model, a target repository, a
branch). Pin a value directly in the kit entry's `args` when it is a project
decision.

## sandboxOptions

| Key | Meaning |
|---|---|
| `template` | Override the container image. |
| `memory` | Memory limit, e.g. `"16g"`. |
| `cpus` | CPU count (integer). `0` means auto. |
| `rootSize`, `dockerSize`, `clonedWorkspaceSize` | Disk sizes, e.g. `"40g"`. |
| `pullPolicy` | `always` \| `missing` \| `never`. Use `always` to re-resolve tags on each create. |
| `skills` | Shared skills store: `off` \| `readonly` \| `readwrite`. |
| `display` | `true` exposes a Wayland display socket. |
| `gpu` | `true` passes the host GPU through (Linux x86_64, NVIDIA). |
| `fuse` | `false` disables FUSE for this sandbox. |
| `nested` | Nested virtualization (macOS Apple Silicon M3+). |
| `usb` | List of USB device selectors. |
| `writableEnvFiles` | Let the sandbox edit its own env files. By default they are mounted read-only, because they decide what runs on the host. |

## Secrets

`secrets:` provisions values into the credential store **scoped to this
sandbox**. `sbx env rm` removes them. Keys are service names (the
`credential@1` `service` of a kit).

Each entry needs exactly one of:

| Source | Use |
|---|---|
| `ref:` | A vault reference resolved at use time, e.g. `op://Vault/item/field` (1Password) or a cloud secret ARN. **Preferred.** |
| `command:` | A host shell command whose stdout is the secret, e.g. `gh auth token`. |
| `value:` | A literal. Never commit a real secret this way. |

Options:

- `noVerify: true` skips the one-shot resolve check when provisioning.
- `refresh:` sets the re-resolution policy for `ref`/`command`
  (`on-demand`, `55m`, …).
- `backend:` selects the resolver (`sdk` or `cli`).
- `snapshot: true` stores the result taken at create time as a literal.
  It needs `ref` or `command`, and cannot be combined with `refresh` or
  `noVerify`.

A credential that already exists in the global store (`sbx secret ls`) needs
no `secrets:` entry. A binding alone approves its use.

## Bindings

A binding approves a credential **mechanism** for a service, and the domains
the proxy may use it on. The block's presence is your consent. Bindings merge
into the user's global sbx `credentials.yaml`. They outlive `sbx env rm`
unless you pass `--prune-bindings`.

```yaml
bindings:
  github:
    apiKey:
      domains: [api.github.com, github.com]
  openai:
    oauth:
      domains: [auth.openai.com, chatgpt.com]
```

- `apiKey` injects the stored secret for that service into requests to
  `domains`. The kit's `credential@1` `apiKey.inject[].domain` list is the
  ceiling, and the effective set is the intersection.
- `oauth` approves OAuth for the service. `domains` is the token endpoint
  host plus the resource hosts. Login is a manual step on the host, so no
  `secrets:` entry is needed.
- A **required** `credential@1` entry with no binding fails create. An
  **optional** one is skipped.
- Bind only the mechanism the user will actually use. Don't add both by
  default.

`kits.py describe` prints a `suggestedBinding` per credential, derived from
the live descriptor.

## Registries

Pull credentials for private kit or image registries, scoped to this sandbox
and resolved once at create:

```yaml
registries:
  ghcr.io:
    secret:            # required: password or token (value/ref/command)
      command: gh auth token
    # username: {...}  # optional; omitted means token-only auth
```

`docker.io` also covers `dhi.io`.

## Lifecycle

Commands that run **on the host**, as the user, outside the sandbox. They are
part of the plan the user approves.

```yaml
lifecycle:
  initialize:     # before anything is resolved; runs on create AND attach, so it must be idempotent
    - name: Fetch fixtures
      command: ./scripts/fetch-fixtures.sh
      workdir: .          # default: project directory
      timeout: 5m         # default: unbounded
  postCreate: []  # after the sandbox exists, before attach
  preRemove: []   # before `sbx env rm` deletes it; `sbx env exec` still works
```

- Commands run under `sh -c` (`cmd /c` on Windows).
- Each command sees `SBX_LIFECYCLE_PHASE`, `SBX_ENV_FILE`, `SBX_ENV_FILES`,
  `SBX_ENV_DIR`, `SBX_SANDBOX_NAME`, `SBX_AGENT`, and `SBX_WORKSPACE`.
- They do **not** see the file's `env:` or any secrets.
- Kits own what runs inside the sandbox. Use `lifecycle` only for host-side
  preparation and teardown.

## Commands

| Command | Does |
|---|---|
| `sbx env plan` | Preview the plan. Changes nothing. |
| `sbx env create` | Provision secrets and bindings, then create the sandbox. |
| `sbx env run [-- ARG…]` | Create if needed, then attach. `-d` creates without attaching. |
| `sbx env exec -- CMD…` | Run a command in the environment's sandbox. |
| `sbx env rm [--prune-bindings]` | Remove the sandbox and its scoped secrets. |

Common flags: `--name`, `--clone` (overrides `workspace.clone`), `--env-arg`,
`--kit-arg`.
