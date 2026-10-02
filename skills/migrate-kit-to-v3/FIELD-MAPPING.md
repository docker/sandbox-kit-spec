# v2 → v3 field mapping

Reference for [SKILL.md](SKILL.md) step 2. The normative grammar is
[SPEC-v3.md](https://github.com/docker/sandbox-kit-spec/blob/main/docs/spec/SPEC-v3.md);
each capability's config schema and runtime behavior is normative on its own
[capability page](https://github.com/docker/sandbox-kit-spec/tree/main/docs/spec/capabilities/com.docker.sandbox).
Both are also on disk under `docs/spec/` when working inside the spec repo, and
the worked kits referenced below are under
[examples](https://github.com/docker/sandbox-kit-spec/tree/main/examples).

## Top-level fields

```text
schemaVersion: "2"            →  schemaVersion: "3"
name: claude                  →  (dropped — identity is the consumption reference)
kind: sandbox                 →  kind: workload      # never write "sandbox"
kind: mixin                   →  kind: mixin
displayName / description     →  unchanged
version: "1.0.0"              →  unchanged (fallback version for unversioned provides)
licenses: [...]               →  unchanged
sourceURL: ...                →  sourceUrl: ...
requires: {agent: claude}     →  requires: ["claude"]
args: {...}                   →  same shape, plus `env:` or `buildArg:` (see below)
sandbox.image: IMG            →  the recipe's `FROM IMG`
sandbox.entrypoint: [...]     →  `ENTRYPOINT [...]` in the recipe
sandbox.command.default       →  `CMD [...]` in the recipe
sandbox.command.interactive   →  capability lifecycle@1 `interactive:`
environment.variables         →  `ENV` in the recipe (mixins too — env merges)
permissions.network           →  capability network-policy@1
credentials[]                 →  capability credential@1, one per service
volumes[]                     →  capability volume@1, one per path
ports[]                       →  capability port@1, one per port
setup.install/startup/files   →  capability lifecycle@1
agentInstructions             →  capability agent-context@1
```

Every key in v3 is `lowerCamelCase` with acronyms title-cased: `sourceUrl`,
`apiKey`, `iconUrl`. Decoding is strict — an unrecognized key anywhere is an
error, which is why a v2 top-level `name:` left in place fails rather than
being ignored. An optional `name` on a capability entry is a display label,
not the removed Kit identity field.

### Fields the v2 kit may never have written

The table maps what a v2 kit *said*, and the mapping of an absence is an
absence. Three optional fields decide what the published artifact says about
itself, and a v2 kit that omitted them migrates faithfully into a v3 kit that
omits them too — the descriptor validates, `kit-tck` passes, and nothing
anywhere reports the loss:

| Field | Annotation it produces |
|---|---|
| `version:` | `org.opencontainers.image.version` |
| `sourceUrl:` | `org.opencontainers.image.source` |
| `licenses:` | `org.opencontainers.image.licenses` |

Add all three whether or not v2 had them. Publishing is the only place their
absence shows, and by then the artifact is already in a registry.

`version:` is the one that gets missed, because [Provides](#provides) discusses
it as the fallback for an unversioned provide — so a kit with **no** `provides:`
reads that, correctly concludes the fallback cannot apply to it, and leaves the
field out. It still names the artifact's own version. A kit with neither
`version:` nor a version-shaped publish tag carries no version at all, and
someone holding it cannot tell what they have or whether it changed.

The image config owns the runtime contract in v3. Entrypoint, cmd, env, user and
workdir move out of the descriptor and into the recipe; the descriptor
duplicates none of it. The one exception is the interactive argv tail, which has
no image-config slot and lives in `lifecycle@1.interactive`.

Because the entrypoint moves into the image config, **a v2 `Dockerfile`'s own
`CMD` usually has to go**. v2 recipes commonly carried a `CMD` mirroring the
kit's entrypoint so a plain `docker run` behaved like the sandbox; left in place
beside the migrated `ENTRYPOINT`, that `CMD` becomes a stray trailing argument
in the headless argv (`Entrypoint` + `Cmd`). Keep a `CMD` only where it carries
default *arguments* to the entrypoint's binary — which is exactly what
`sandbox.command.default` was.

### Kits with no v2 `Dockerfile`

Several v2 sandbox kits point `sandbox.image` at a published template and ship
no recipe. A v3 workload MUST have content, so write the minimal recipe that
says the same thing: `FROM <that image>`, plus the `ENV` from
`environment.variables` and the `ENTRYPOINT` from `sandbox.entrypoint`.

### Args

Same shape as v2 (`default`, `required`, `description`, `enum`, `pattern`), with
two additions that decide when the value resolves:

- `buildArg: FOO_VERSION` — resolves at **build**, is handed to the recipe as
  `--build-arg FOO_VERSION=<value>`, and every reference to it is expanded into
  the published descriptor.
- `env: FOO_TIMEOUT` — resolves at **create** and is exported to the container
  under that name.

They are mutually exclusive: an arg resolves in one phase. Args are private by
default — an arg reaches the container only through `env:` and the build only
through `buildArg:`.

Arg references keep v2's spelling: `${{ kit.args.argname }}` is what v2 wrote
too, so leave them alone rather than hunting for something to rewrite. Shell
variables (`$VAR`, `${VAR}`) pass through untouched, which is the whole point
of the `${{` opener — it is not valid shell, so the two vocabularies cannot
collide. Every `${{ kit.args.x }}` must name a declared arg. The one `${…}`
form v2 did define is `${WORKDIR}`, and v3 has no equivalent substitution —
see `files[].content` under lifecycle@1 for what replaces it.

**That applies inside comments too.** References are found by scanning the
descriptor's raw text, so a placeholder written in YAML prose is a real
reference and fails validation when the arg does not exist. Name the arg
(“the `version` arg”) rather than spelling the placeholder when documenting.

A whole-value reference adopts its type, so a numeric arg can reach a numeric
field (`container: ${{ kit.args.port }}`). A reference embedded in a larger
string is always text.

### Provides

**The rule everything below follows from:** every provide must carry a version
at publish — its own `@version`, or the descriptor's `version:` as a fallback —
and `RequireVersionedProvides` fails the build when neither exists. So an
unversioned provide is never a resting place. Either **pin** the version and
reference it, or **drop the provide**; §9.2 publishes a kit with no `provides`
perfectly happily, and offering nothing matchable is the honest answer when the
kit cannot know what it installed.

The one exception is a kit whose version genuinely *is* its content's, such as
one shipping its own documentation. There the `version:` fallback states a fact
rather than borrowing a number.

- **Pin through a build arg and reference it**: `provides: ["<tool>@${{ kit.args.version }}"]`.
  State it once — point the top-level field at the same arg,
  `version: "${{ kit.args.version }}"`, which §4 allows and publishing expands.
  One input then drives the descriptor's `version`, its `provides` entry, the
  `org.opencontainers.image.version` annotation and the publish tag, with no
  second place to drift.
- **Know what an unversioned provide resolves to**, because it explains why the
  fallback is not a fix. The resolver takes, in order: an explicit `@version`
  on the provide; the version a *version-shaped consumption reference* carries;
  then the descriptor's `version:`. A `:latest` tag is not version-shaped, so
  it contributes nothing. A kit shipping Claude Code 2.1.267 under
  `version: "1.0.0"` therefore offers `claude@1.0.0`, and a mixin asking for
  `claude >= 2.1` refuses to resolve against the very kit that satisfies it.
- **A version is `[0-9]+(\.[0-9A-Za-z-]+)*`**: first segment numeric, later
  segments alphanumeric, no `v` prefix. **A commit SHA is therefore not a
  version**, so a kit pinned to a git ref cannot reference that pin into its
  provide. Use the upstream version the recipe records if there is one;
  otherwise drop it.
- **A pinned provide is a claim about content, so make the build enforce it.**
  Wire the arg through to the installer and add a step that re-reads the
  installed version and fails on mismatch. Pinning the provide without pinning
  the install is worse than floating: it asserts a version the content may not
  have. Where the tool arrives inside a base image rather than from an install
  the kit performs, the honest form is an *assertion* — read the version out of
  the content and fail the build when it differs from the declared default.
- **Some installers genuinely cannot be pinned**, and those are the drop cases.
  Real examples: an installer whose whole option surface is `--help` and
  `--channel` and which fetches a literal `latest/` path; a vendor manifest
  whose asset URL carries an opaque build id beside the version; a tool that
  self-updates at run time; content that is someone else's mutable image tag.
- The name must match what v2 mixins asked for via `requires: {agent: X}`, so
  those requires keep resolving.

### `requires` deserves more than the v2 agent affinity

v2 could express exactly one dependency — `requires: {agent: <name>}` — so a v2
kit whose hook runs `apt-get`, calls `jq`, or drives the in-sandbox Docker
daemon says nothing about needing them, and simply fails at create on a base
that lacks them. Migrating only the agent affinity carries that silence
forward. Audit what each kit assumes **beyond the platform floor** (§12
guarantees only `bash`, `sh`, `curl`, `git`, a CA store, and the `agent` user at
uid 1000) and declare the rest.

What makes this expressible is **derived provides** (§9.6): publishing a
`kind: workload` kit reads `/var/lib/dpkg/status` (or apk's database) out of the
built content and states one provide per installed package under `deb/` or
`apk/`, at the upstream version with epochs and Debian revisions stripped. So a
workload on `dhi.io/sbx-templates:*` offers `deb/apt`, `deb/jq`,
`deb/docker-ce`, `deb/git` and hundreds more with nobody authoring them, and a
mixin may require those names.

**On a multi-platform workload the set is an intersection, not a union.** §9.6
states an entry only where every published platform agrees on the normalized
name *and* version, so an architecture-specific package, or one at different
versions across arches, is emitted by none of them. Check with the platform in
hand rather than whatever your laptop is:

```sh
docker run --rm --platform linux/amd64 <base> dpkg-query -W -f='${Version} ${Status}\n' <pkg>
docker run --rm --platform linux/arm64 <base> dpkg-query -W -f='${Version} ${Status}\n' <pkg>
```

A requirement on a package only one arch publishes refuses to compose on
**every** architecture, not just the one missing it. §9.5 makes the merged
descriptor identical across all published platforms, so the intersection in
§9.6 drops a disagreeing package from the single descriptor they share —
there is no per-platform requires list for it to survive in. The failure is
therefore the same as inventing a name, rather than a partial-coverage
problem you can ship around.

```yaml
requires: ["claude", "deb/apt", "deb/openssl >= 3.5"]
```

Three things to keep straight:

- **Requiring a `deb/` name is fine; authoring one as a provide is refused.**
  `provides: ["deb/apt"]` fails the build — §5.1 reserves the namespace for
  publishing to fill — and that check is authored-form only, so
  `spec.ValidateRaw` does not catch it and only the build tells you.
- **Verify the package is really in the database.** Two different things stop a
  name existing, and node happens to hit both. §9.6 drops a package whose
  upstream version contains a tilde, so a tilde-versioned entry is in dpkg and
  still never published. Separately, these templates install node into
  `/usr/local` via `n`, so it is not in dpkg at all. Either way `deb/nodejs` is
  not available to require. The single check that settles it is
  `docker run --rm <template> dpkg-query -W -f='${Version} ${Status}\n' <pkg>` —
  no output means no package, a tilde in the output means no provide.
- **An unprovidable name is worse than silence.** `requires` is a closed-set
  check (§5.3): a name nothing in the set provides makes the kit refuse to
  compose anywhere, rather than only on the bases that actually lack it. That is
  why an invented `docker-engine` is wrong and `deb/docker-ce` is right.
- **Do not transcribe a hook's command list into `requires`.** It is the most
  tempting wrong move in a migration, because v2 hooks are right there in front
  of you. Two rules settle nearly every case: the §12 floor — `bash`, `sh`,
  `curl`, `git`, a CA store, the `agent` user — is assumable, so a hook using
  `curl` or `git` declares nothing; and a dependency a hook reaches for only
  *conditionally* cannot be stated at all, because `requires` has no either/or
  and would refuse the bases where the condition never fires. A hook that
  apt-installs a tool **when the base lacks it** requires no `deb/apt`. Ask what
  must be present on every base for the kit to work, not what the hooks invoke.

## Capabilities

Entries may carry an optional human-readable `name` alongside their
`description`. Spaces and duplicate labels are allowed. Names do not
change identity or permissions; merging retains the first nonempty
label in contribution order. This field is distinct from names inside
config, such as a credential's `apiKey.name` environment variable.

Each entry is a typed request the host answers. `optional: true` means the kit
degrades gracefully without it; the default is required, which fails resolution
closed. Policy-shaped types are singletons; instance-shaped types appear once
per thing requested (`credential@1` per (service, phase), `volume@1` and
`host-mount@1` on a shared storage path key,
`port@1` per (container, transport), `agent-skills@1` per path).

Use `volume@1` for private persistent or tmpfs storage. For a cache,
dataset, or artifact directory that must be shared with the host and
across sandboxes of one Kit, use `host-mount@1`. It has only a canonical
in-container `path` and optional octal `mode`; the runtime owns its host
location. A v2 user-supplied host path is not copied into the descriptor.
Make the request optional, or group it with its setup, when host sharing
is not available on every runtime. See `examples/shared-cache`.

### network-policy@1

v2 had one flat list. v3 is phase-scoped — `install` and `runtime` — and an
absent phase grants nothing. Split the v2 list by who reaches the host:

- reached by `setup.install` hooks → `install.allow`
- reached by the agent in steady state, or by `setup.startup` hooks (they run at
  boot, in the runtime phase) → `runtime.allow`
- reached by both → list in both

The v2 comments usually say which hook needs which host. The union of the two
phases must not lose a host. Deny wins over allow, and a removed deny is a
widening. `network-policy@2` adds HTTP method and path rules over the same
grants and is exclusive with `@1`; a kit that gates by host alone stays on `@1`.

Write **package-mirror** entries portless (`archive.ubuntu.com`, not
`archive.ubuntu.com:80`). A portless pattern matches any port, which is what
apt hosts need — pinning `:80` breaks the moment a mirror answers over HTTPS,
and `apt-get update` then fails wholesale rather than skipping one source.
Elsewhere a pinned `:443` is fine and common; both `claude` examples pin it
throughout. For a v2 kit that did pin a mirror's port, dropping it is a
deliberate widening, so note it in the migration's review notes rather than
letting it pass as a transcription detail.

### credential@1

One entry per v2 `credentials[]` element, with `service:` and `phase:`
(`runtime` unless the credential exists only so install hooks can download
something). `apiKey`, `inject`, `proxyManaged`, `oauth.tokenEndpoint`,
`sentinels`, `resourceHosts`, `responseFields` and `passthrough` carry over
unchanged. `phase` also accepts a non-empty list of distinct phases:
`[install, runtime]` shares the configuration across both. Overlapping
(service, phase) pairs are errors, whether scalar or list-shaped.

- **Add `optional: true` to preserve v2 behavior.** v2 credentials default to
  not-required; v3 entries are required unless they opt out.
- `oauth.skipIfEnv` is **dropped**, and strict decoding rejects it if left in.
  Do not oversell the change in the note: the field was v1-era and already a
  no-op under v2, so removing it costs nothing that was working.
- `credentialFile.template` (a Go template) becomes the declarative
  `credentialFile.structure` map, encoded after substitution so the output is
  well-formed whatever the values contain. The placeholder vocabulary is
  `{{.AccessToken}}`, `{{.RefreshToken}}`, `{{.ExpiresAt}}` (a number),
  `{{.Scopes}}` (an array) and `{{.PrimaryApiKey}}` (whose enclosing key is
  omitted when no key is captured). `format: toml` is available for agents that
  read TOML.
- Validation enforces that every `inject[].domain` appears in **every
  listed phase's** allow list. An inject domain outside it would be a
  credential mapped onto a connection that can never occur — which is how the claude migration
  found two dead inject rules.
- That check is an **exact host match**, escaped only by a bare `*` or `**`. A
  single-label wildcard like `*.example.com` does not satisfy an inject domain
  of `api.example.com`, even though it covers the host at run time, because the
  matcher does not expand globs. Add the literal host beside the wildcard; it
  widens nothing.

### lifecycle@1

One entry holding `install:`, `startup:`, `files:` and `interactive:`. Commands,
`user:` and `description:` carry over verbatim. `interactive` names the same
launch as `agent-interactive-sessions@1`'s `newSession`, which defaults to it.

**First ask whether the hook should exist at all.** In v2 a mixin had no content
mechanism, so an install hook was the only way for one to install anything. v3
lifts that — a mixin may carry an overlay — so many install hooks encode a v2
limitation rather than a v3 requirement, and a faithful migration carries them
across intact when they should become layers. Moving a pure-content install
into the recipe buys four things: no per-sandbox-create latency, digest-pinned
and scannable content, a failure that surfaces at publish instead of in a
user's sandbox, and — the one that shows up in the permission surface — the
install-phase network grant disappears, because it existed only to let that
hook reach the network.

Move it when it is a pinned artifact download or a language-package install
that needs nothing from create time. Keep it a hook when:

- it runs `apt-get` **in a mixin** — apt needs the composed base's real dpkg
  database and keyring, and an overlay cannot carry a package's shared-library
  closure;
- it reads a create-time value (`WORKSPACE_DIR`, `SBX_CRED_*_MODE`,
  `MCP_GATEWAY_URL`, or an arg that resolves at create);
- it needs the in-sandbox Docker daemon, which does not exist at build;
- its target path is a declared `volume@1`, or an `agent-skills@1` path the
  host mounts over — baking there ships content the mount then hides;
- it merges into a file the composed base also writes. A layer *replaces* a
  file rather than merging it, so a shared registry or a `~/.bashrc` line has
  to be appended at create.

- **Hook environments are deny-by-default.** Every `$VAR` a hook body reads must
  be listed in that hook's `env: [...]`, except the platform baseline (`PATH`,
  `HOME`, `HOSTNAME`, `TERM`, `PWD`, `OLDPWD`, `SHLVL`, `_`). This is the single
  most common migration bug — audit every hook body.
- **A hook's children need their variables declared too.** The restriction is on
  the hook's whole environment, not on the names its script spells out, so a
  hook that never mentions `$HTTPS_PROXY` but shells out to `curl`, `pip`, `npm`
  or `docker` still has to declare what those read — `HTTP_PROXY`/`HTTPS_PROXY`
  for anything fetching through the sandbox's forced proxy, `DOCKER_HOST` where
  a base points the CLI at a non-default socket. v2's unrestricted hook
  environment handed these over silently, so a faithful migration declares them
  and says why. `examples/builder/builder.yaml` declares the proxy pair for
  exactly this reason.
- `files[].onlyIfMissing: true` → `overwrite: false`.
- `files[].content` expands `${{ kit.args.* }}` and `${{ kit.env.* }}` at
  create. Environment references use the final container environment:
  composed image defaults, then argument exports, then runtime overrides.
  This is string substitution, not shell evaluation; `$VAR`, `${VAR}`,
  and `~/` remain literal in the file. For v2 `${WORKDIR}` substitution,
  use `${{ kit.env.WORKSPACE_DIR }}` when the runtime supplies that
  variable in the final environment. Missing variables are errors.
  Otherwise, keep `$WORKSPACE_DIR` inside a script for the shell to
  expand when it runs, or write the file from an install hook declaring
  `env: [WORKSPACE_DIR]`. The v3 hook variable is `WORKSPACE_DIR`.
- A v2 `startup[].background` carries over unchanged. The field is
  **startup-only** in v3 — `InstallHook` has no `background`, so strict
  decoding rejects it on an install hook rather than ignoring it.
- Install hooks run once at create, before the entrypoint, with the install-phase
  network and credentials open. Startup hooks run on every boot and must be
  idempotent.

### agent-context@1

Move the v2 `agentInstructions.content` body verbatim into `<kit>-context.md`
and reference it with `contentFile: ./<kit>-context.md`; the frontend stages the
body into the image and rewrites the field to the staged path.

**Unless the body interpolates an arg — then use inline `content:`.** A
`contentFile` body is read and staged into a layer at publish, while
create-phase args expand into the *descriptor* at create, so a
`${{ kit.args.host }}` inside a staged body is never expanded: it is staged
byte-for-byte and reaches the agent as literal placeholder text, with no
warning from the build. Inline `content:` is a descriptor string and does
expand. This is the one case where a kit's instructions name the instance, the
clone directory, or anything else an installer chooses.

Audit for the mistake with:

```sh
rg -l '\$\{\{ *kit\.args\.' --glob '*-context.md' .
```

A workload's `filename:` alone keeps the legacy workspace-sibling profile.
An agent workload or mixin declares `filename:` together with `directory:`
at the agent's discovery location. The directory is absolute and canonical;
Codex uses `/home/agent/.codex`. An explicit destination overrides the
legacy fallback, and differing explicit destinations conflict. Tool mixins
carry `contentFile` or `content` alone. Staged bodies keep their paths.

### long-running@1

For a workload or mixin that must keep serving after the last client disconnects,
add a required `com.docker.sandbox/long-running@1` entry with no `config`.
Background startup hooks and published ports alone do not request this
lifetime. The capability leaves explicit stop and failure handling intact;
it does not express a Compose restart policy. Keep it on mixin variants
when their service needs that lifetime too: a request from any Kit
applies to the whole sandbox, and a required declaration wins over an
optional one.

### git-identity@1

For a Kit whose commit workflow needs the user's runtime-provided Git
identity, declare `com.docker.sandbox/git-identity@1`. It has no `config`,
including empty or null. The runtime selects `user.name` and `user.email`
and exposes them as the workload user's global defaults before that
user's hooks, workload and exec sessions run. Repository-local identity still
takes precedence. Authentication and signing are outside this grant.

Use `optional: true` only when the Kit can operate without that identity.
A required entry refuses creation if the runtime withholds the pair or
either value is unavailable; an optional entry is skipped and recorded.
Adding either form widens permission surface: the grant discloses the
selected name and email to sandbox processes and commit recipients.

Audit v2 hooks, environment variables and mounts that supplied Git
identity. When migrating runtime-provided attribution to this capability,
remove the replaced identity plumbing and explain the change with a
`# MIGRATION NOTE:`. Do not bake the user's pair into image content or
descriptor args, mount a host gitconfig, or copy its unrelated settings
to implement this request. Source selection belongs to the runtime.
Keep the request on a `-mixin` variant if its workflow needs it too; a
request from either shape applies to the sandbox, and required wins over
optional when requests merge.

Do not add this capability merely because Git is installed, the Kit
clones a repository, or an agent could create commits. Preserve an
intentional bot identity or repository-local attribution unless the
migration explicitly changes that behavior; neither requires disclosing
the user's runtime identity.

### sbx@1

Config-less. Add it to every migrated **workload**: v2 `kind: sandbox` kits are
all sbx-launched agents, and this is what asks the host to launch the agent
rather than let the image entrypoint become PID 1, and to honor the identity the
image config declares. Never on a mixin — `validate.go` rejects it outright as
workload-only, so this is a build failure rather than a declaration a host
quietly ignores.

The kit side of the claim is real: the image must ship `/bin/sh` and `/bin/bash`
the declared user can execute and declare a non-empty `user` that resolves in
`/etc/passwd`. Naming an absolute shipped file in `BASH_ENV` is a **SHOULD** on
the capability page rather than a MUST, and `kit-tck` warns rather than failing
when it is missing.

### port@1

One entry per v2 port. `name:` and `container:` carry over; v2 `protocol: tcp`
becomes `transport: tcp`. A kit cannot pin a host port.

### agent-sessions@1

Workload-only by convention — unlike `sbx@1`, `validate.go` has no kind check
for it, so a mixin declaring it is accepted and simply describes something a
mixin does not own. Add it only where the v2 kit's `testdata/tck.yaml` recorded
a working non-interactive invocation in `promptArgs`. Translate it into the
verb tails: `promptArgs: ["-p"]` → `prompt: ["-p", "{{.Prompt}}"]`, plus
`continue` and `resume` where the agent supports them, and `list`, which is a
complete command rather than a tail. Where `promptArgs` was deliberately
omitted, omit the capability — do not invent flags.

### agent-interactive-sessions@1

The interactive sibling of `agent-sessions@1`: the verb tails a host that puts
a person at the terminal launches, run with a terminal attached. An agent whose
CLI has both a headless and an interactive mode declares **both** capabilities;
a headless-only agent declares only `agent-sessions@1`. Same rule as there: add
only the verbs you have verified against the tool's real CLI (its `--help` or
official docs), never the verbs a pattern suggests. Fields: `prompt` (must
reference `{{.Prompt}}`; start a TUI session seeded with a prompt, which is
usually a positional argument or `-i`, not the headless `-p`), `resume` (must
reference `{{.SessionID}}`), `continue`, `sessionPicker` and `list`.

Presence is the semantics here, unlike `agent-sessions@1`: a present key means
supported, an absent key means unsupported, and a present empty list means the
launch argv alone. Leave a verb out when the agent has no such operation rather
than writing `[]`. `newSession` is the exception: omit it when a bare launch
opens the TUI, because omitted it defaults to the `lifecycle@1` interactive
launch (the launch argv plus `interactive`, or the launch argv alone). When a
kit states both `newSession` and `lifecycle@1.interactive` they must be the
same argv, an explicit `[]` included, so a v2 `sandbox.command.interactive`
becomes `lifecycle@1` `interactive` and `newSession` stays omitted. `list` is the same command as in
`agent-sessions@1`; repeat it there verbatim, and omit it where the agent has no
command that prints session ids one per line.

### Bundled skills

`agent-skill@1` declares a skill carried by the Kit: `path` is an absolute
image directory containing `SKILL.md` and supporting files. Its basename
becomes the directory name in the agent's skill store, unless `config.name`
overrides it. The entry's `name` is still only a display label.

Declare `agent-skills@1` on the agent-bearing Kit to identify its discovery
directory. The runtime links or otherwise exposes every selected
`agent-skill@1` bundle at every selected path. The same declaration permits
host-shared content when available and enabled; there is no separate
directory capability. A required bundled skill without a destination
fails, and conflicting effective names cannot silently replace one another.

### agent-skills@1

Only where the v2 kit already declares or documents the agent's skills
directory. Do not invent a path. `mode` defaults to `readonly`, and the
effective access is the narrower of the host's setting and the kit's.

Missing or empty host skills, or disabled host sharing, do not make this
request unsatisfiable, even when required. Bundled skills remain available
independently of host sharing. Keep bundled source directories separate
from discovery paths and declare them with `agent-skill@1`; the runtime
combines the sources while honoring access bounds and collision rules.

### Do not add on speculation

`privileged@1`, `resources@1`, `kit-registry@1` and `usb-device@1` belong in a
migrated kit only if the v2 kit declared the equivalent. A v2 Dockerfile's
`LABEL com.docker.sandboxes.start-docker="true"` stays a label in the recipe —
it is not a v3 capability.

`ssh-agent@1` has no v2 counterpart to migrate: agent forwarding was a
runtime setting (`ssh.agentForwardingEnabled`), not something a v2 kit
declared, so a migration never adds it. Adding it is a new grant of every
key the user's agent holds, which is a decision for the kit's author, not
a translation.

## Mixin variants

A workload kit's `-mixin` sibling declares the same credentials, network policy,
volumes, hooks, args and provides, minus what only the kit that owns the
environment can carry:

- no `sbx@1`, `agent-sessions@1` or `agent-interactive-sessions@1`; an agent
  mixin keeps its explicit
  `agent-context@1` directory and filename, while tool mixins carry bodies only
- no `ENTRYPOINT` — the base workload's launch command stays, and the user runs
  the tool from the shell. Say so in the descriptor's header comment.
- `displayName: <Name> (mixin)`, and a description that says to layer it onto a
  shell base and run the tool.

v2 `environment.variables` becomes `ENV` in a mixin too. Env is one of the
**additive** image-config fields that merge at assembly, so a mixin's `ENV`
does reach the composed image; only the contract fields the workload anchors —
entrypoint, cmd, user, workdir — are ignored. Put it on the recipe's **final**
stage, since a build stage's config is discarded.

Prefer it over an `/etc/profile.d/<kit>-env.sh` drop, which only a login shell
sources: `sbx@1` starts the agent under `bash` through `BASH_ENV` because
profile and rc files do not run for it, so a profile.d value reaches a terminal
the user opens and not the agent. Use profile.d for a variable two kits might
each want to set differently — a genuine conflict fails the composition — or
one that only makes sense interactively.

The recipe is an overlay, not a root filesystem:

```dockerfile
# syntax=docker/dockerfile:1
FROM <the workload's base> AS build
# the same install the workload does, landed under /out
RUN ...

# The overlay: lands on any base.
FROM scratch
COPY --from=build /out /
ENV IS_SANDBOX=1
```

Where an install resists relocation — `uv tool install`, npm global installs —
use the workload's own base as a build stage, run the unmodified install, then
`COPY --from=build` the specific resulting paths (`/usr/local/bin/...`,
`/home/agent/.local/...`, `/opt/...`) into the `scratch` overlay, and comment
on why that shape was chosen.

**Apt packages are not in that list.** Selected paths leave the dpkg state and
the shared-library closure behind, so an apt install has to stay a create-time
hook however tempting the copy-out looks — the same rule the lifecycle section
above states.

**Everything an overlay has to get right about ownership is in
[RECIPES.md](../create-kit-v3/RECIPES.md#ownership)** — the `/home` and
`/home/agent` invariants, the `COPY --chown` and `chown -R` traps that break
them, the foreign uids that ride along in npm and PyPI tarballs, the
virtualenv that only travels with its interpreter, and the installer that
relocates a launcher without its payload. Read it before writing a mixin
recipe; it is the same rulebook whether the kit is new or migrated, and the
export-and-audit command lives there too.

Two of those have a specifically migration-shaped trap. A v2 install hook ran
against the **real** base, so a package tree's foreign owner and a venv's
absolute interpreter path were both harmless — the install happened where the
paths resolved. Turning that hook into overlay content moves it onto an
unknown base, where neither holds. A hook that worked for years is not
evidence that its baked equivalent will.

`examples/devin-mixin/` and `examples/docker-agent-mixin/` show the copy-out
pattern; `examples/claude-mixin/` shows the single-binary case.

A kit's build context is rooted at its own directory and a `dockerfile:` path
may not escape it, so a mixin **cannot** reach an asset sitting in the
workload's directory. A shipped script the two shapes share has to be copied
into both, and the copies then have to move together — say so in a comment in
each, because nothing enforces it.

## Gotcha checklist

Before calling a migration done:

- [ ] no `name:` field, and `kind:` says `workload`, never `sandbox`
- [ ] `version:`, `sourceUrl:` and `licenses:` are present even though the v2
      kit wrote none of them — nothing fails without them and the published
      artifact is what loses
- [ ] every hook that reads a variable declares it in `env:`
- [ ] every credential that was effectively optional in v2 sets `optional: true`
- [ ] `git-identity@1` reflects an actual attribution need, with its
      permission widening and required/optional behavior accounted for
- [ ] every inject domain appears in the same phase's allow list
- [ ] the install/runtime phase split loses no host from the v2 list
- [ ] every mixin declaring a context `filename:` also declares its
      explicit discovery `directory:`
- [ ] no `sbx@1`, `agent-sessions@1` or `agent-interactive-sessions@1` on a mixin
- [ ] the mixin's `ENV` is on the recipe's final stage, and profile.d is used
      only for values that collide or that only a shell needs
- [ ] entrypoint, env, user and workdir live in the recipe, not the descriptor
- [ ] every volume declares a `size`
- [ ] v2 comments carried across, deltas marked `# MIGRATION NOTE:`
- [ ] every `contentFile:` names a file that exists, and every staged
      `*-context.md` is referenced by a descriptor
- [ ] a v2 `agentInstructions.content` body still reaches the agent, as a
      staged file or inline `content:` — a dropped body breaks nothing and is
      invisible
- [ ] the recipe's `ENTRYPOINT` (plus any `CMD`) reproduces the v2
      `sandbox.entrypoint` and `sandbox.command.default` exactly

The orphan case is worth scripting, because nothing else sees it: a
`contentFile` naming a missing file fails at the build, but a context body
nobody references fails at nothing at all and silently never reaches the agent.

The frontend trims a leading `./` and accepts a quoted value, so `./name`,
`name` and `"./name"` are the same reference. Match all three or the audit
reports an ORPHAN for a file that is referenced:

```sh
for y in */*.yaml; do d=$(dirname "$y")
  rg -o 'contentFile: *["'\'']?\.?/?[^"'\'' ]+' "$y" \
    | sed 's|.*contentFile: *||; s|["'\'']||g; s|^\./||' | while read -r f; do
    [ -f "$d/$f" ] || echo "MISSING: $y -> $f"
  done
done
for m in */*-context.md; do d=$(dirname "$m"); b=$(basename "$m")
  rg -q "contentFile: *[\"']?(\./)?$b" "$d"/*.yaml || echo "ORPHAN: $m"
done
```
