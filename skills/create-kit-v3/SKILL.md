---
name: create-kit-v3
description: >-
  Authors a new Docker sandbox kit against the v3 descriptor — choosing workload
  or mixin, writing the descriptor and its content recipe, declaring the
  capabilities it needs, pinning the tool version, and verifying the result with
  docker buildx, sbx and kit-tck. Use when creating a new kit from scratch,
  adding a `-mixin` variant, packaging a CLI or agent as a sandbox kit, or
  asking what a kit descriptor should contain.
---

# Create a v3 kit

A kit is one OCI image. Its layers are the content and its manifest annotation
carries the **descriptor**: what the kit offers, what it needs from the host as
typed capability requests, and what it needs from other kits. An engine that
does not read the annotation runs it as an ordinary image.

To migrate an existing v2 `spec.yaml` kit rather than write a new one, use the
`migrate-kit-to-v3` skill instead; its `FIELD-MAPPING.md` is also the best
field-by-field reference if you are unsure what a given declaration means.

## Decide the kind first

Everything else follows from this.

| | `kind: workload` | `kind: mixin` |
|---|---|---|
| Layers are | a root filesystem | an overlay landing on a workload |
| Per composition | exactly one | zero or more |
| Owns | entrypoint, env, user, workdir, a legacy context profile | env, labels, ports and volumes, which merge; an agent mixin can select an explicit context profile |
| Must have content | yes | no — may be declaration-only |

Write a **workload** when you own the environment the agent runs in. Write a
**mixin** when you add a tool, a credential or a policy to somebody else's.
Most new kits should be mixins, and a tool worth shipping as a workload is
usually worth shipping as both — that is what the `claude`/`claude-mixin` pair
in the examples is.

A mixin cannot set `ENTRYPOINT`, `CMD`, `USER` or `WORKDIR` and have it take
effect: the workload anchors the composition and owns those contract fields.
The **additive** fields do merge — env, labels, ports and volumes — so a
mixin's `ENV` reaches the composed image, and `PATH` is appended rather than
replaced.

Prefer `ENV` for static environment. It is what the agent process sees, and
`sbx@1` launches the agent under `bash` via `BASH_ENV` precisely because
profile and rc files do not run for it — so an `/etc/profile.d/<kit>-env.sh`
drop reaches a terminal the user opens and misses the agent itself. Reach for
profile.d only when a value is likely to collide, because two mixins setting
one variable to different values is a hard composition failure, or when the
value genuinely only makes sense in an interactive shell.

## Layout

The default authoring form is a companion pair, found by filename stem:

```text
<kit>/
  <kit>.yaml           # the descriptor; first line `# syntax=docker/sandbox-kit:3`
  <kit>.dockerfile     # the content recipe (omit for a declaration-only mixin)
  <kit>-context.md     # agent-context body, referenced as contentFile:
  README.md
```

No `dockerfile:` field is needed — the stem convention finds it. Three other
forms exist (an inline `build:` block, a `# kit:` comment descriptor inside a
Dockerfile, and a `kind: set` list of other kits); see SPEC-v3 §3.

## Descriptor skeleton

```yaml
# syntax=docker/sandbox-kit:3
schemaVersion: "3"
kind: mixin
displayName: GitHub CLI
description: gh, installed from the official release tarball
sourceUrl: https://github.com/cli/cli
licenses: [MIT]

# One value drives the install, the provide, the published version and the tag.
version: "${{ kit.args.version }}"

args:
  version:
    default: "2.98.0"
    pattern: '^[0-9]+\.[0-9]+\.[0-9]+$'
    description: GitHub CLI release to install
    buildArg: GH_VERSION

provides: ["gh@${{ kit.args.version }}"]
# No requires: this overlay ships a release tarball and needs nothing from the
# workload. Add entries only for what the composed runtime must already have —
# see below, and note the comment under `requires` about refusing to compose.

capabilities:
  - type: com.docker.sandbox/network-policy@1
    config:
      runtime:
        allow: [github.com, api.github.com]

  - type: com.docker.sandbox/agent-context@1
    config:
      contentFile: ./gh-context.md
```

Every key is `lowerCamelCase` with acronyms title-cased (`sourceUrl`,
`apiKey`). Decoding is **strict**: an unrecognized key anywhere is an error,
which is deliberate — a misspelled key silently ignored would be a policy
silently absent.

## Capabilities

Use an optional entry-level `name` for a short display label and
`description` for its explanation. Names may contain spaces and need
not be unique; they do not change identity, permissions, or merge keys.
When entries merge, the first nonempty label in contribution order wins.
This label is separate from config fields such as `apiKey.name`.

A capability is a typed request the host answers. `optional: true` means the
kit degrades without it; the default is required, which fails resolution
closed. Read the page for each type you emit — each is normative for its config
and for what a runtime must do. The ones you will reach for, and the rule most
often got wrong:

| Type | Use it for | Easy to get wrong |
|---|---|---|
| `network-policy@1` | egress by host | It is **phase-scoped**: an absent phase grants nothing. Hosts your install hooks reach go in `install`, hosts the running agent (or a startup hook) reaches go in `runtime`, hosts both reach go in both. |
| `network-policy@2` | egress bounded by HTTP method and path | Same phases, plus entries that grant only matching requests. Pick `@2` when a host should carry one API and not the rest; stay on `@1` when host alone is the grant. **Exclusive with `@1`** — declaring both is an error, and a bounded allow entry must name its hosts literally. `gh` and `hello` use `@2`. **Verify the bound you rely on**: declare the tightest correct policy, but confirm against your target runtime which parts it enforces before treating one as a security boundary — a refusal is a `403` from the boundary, which an origin can also return, so test a case the origin would allow. |
| `credential@1` | one service's auth | Entries are **required by default** — add `optional: true` unless the kit genuinely cannot run unauthenticated. `phase` accepts a single phase or `[install, runtime]` with shared configuration. Every `inject[].domain` must appear in every listed phase's allow list, matched **exactly**: a `*.example.com` wildcard does not satisfy `api.example.com`. |
| `ssh-agent@1` | git over SSH, SSH commit signing | Set `unrestricted: false` to bound it. `sign: [git]` is all commit signing needs; `authenticate: [git@github.com]` is a login to one server. An entry with `unrestricted: true` (the default) signs anything with every key the user's agent holds — declare that only when a client cannot bind sessions (OpenSSH can), and prefer `credential@1` with HTTPS when a token will do. `optional: true` unless the kit cannot work without it: many users have no agent running. A hook that uses it declares `SSH_AUTH_SOCK` in `env:`, and only hooks of the granted phase get it. `authenticate` grants a signature, not a connection: the server must also be reachable under the network policy. `phase` accepts a single phase or `[install, runtime]` with shared rules. Commit signing needs git config too (`gpg.format=ssh`, `gpg.ssh.defaultKeyCommand="ssh-add -L"`). |
| `lifecycle@1` | install/startup hooks, staged files | Hook environments are **deny-by-default**. Declare every variable in `env:`, including ones only a child process reads — `curl`, `pip` and `npm` need `HTTP_PROXY`/`HTTPS_PROXY`, and `docker` needs `DOCKER_HOST`. |
| `volume@1` | persistent paths | Declare needed capacity with `size`; omission leaves it unspecified and conflicts with another Kit's explicit request. Retained volume settings cannot change on recreation. |
| `host-mount@1` | host-shared caches, datasets, or artifacts | Declare only the absolute, canonical in-container `path` and optional octal `mode`. The runtime owns the host location. Data is shared across sandboxes of the declaring Kit, survives sandbox removal, and is visible to the host user. Use an optional group to couple cache setup with the mount; see `examples/shared-cache`. |
| `agent-context@1` | instructions the agent reads | A workload can supply the legacy workspace-sibling `filename`. An agent workload or mixin supplies `filename` plus an absolute `directory` at its discovery location; this overrides the legacy fallback. Different explicit destinations each get a profile, so agent mixins stack. Tool mixins contribute bodies alone. Use `contentFile:` for a static body, but inline `content:` when the body interpolates an arg — a staged body is never arg-expanded. |
| `long-running@1` | workloads or service mixins that outlive client sessions | Config-less; a request from any Kit applies to the whole sandbox. A background hook or published port does not prevent session auto-stop. Required by default; use `optional: true` only if auto-stop is tolerable. This does not request restart after failure. |
| `git-identity@1` | commits attributed to the runtime-provided user | Config-less; requests only user.name and user.email, not a configuration-source mount. Requires permission; optional when missing identity is tolerable. Signing/authentication are separate grants. |
| `sbx@1` | "launch this as an agent" | Workload-only, config-less — and enforced: a mixin declaring it fails validation. |
| `agent-skill@1` | one bundled skill | `path` names the image directory containing `SKILL.md` and supporting files. The basename is the discovery name unless `config.name` overrides it; the entry-level `name` remains only a display label. |
| `agent-skills@1` | where an agent discovers skills | Declare on the agent workload or agent mixin. The runtime links or otherwise exposes every selected `agent-skill@1` bundle here, and includes host-shared skills when available and enabled. Missing host skills never block startup; `mode` bounds host-store access only. |
| `acp@1` | discover and launch an ACP adapter | Declare on the Kit that installs the adapter, workload or mixin. `agent` is the unversioned provides name of its harness; another composed Kit may provide it. `command` is a complete non-TTY stdio command, never an argv tail. `env` overlays the session environment; positive `protocolVersion` defaults to 1. Use `optional: true` for older hosts and keep the verified CLI fallbacks. Protocol capabilities come from initialize; session choices come from session responses and updates. Different configs for one normalized agent conflict. |
| `agent-sessions@1` | the headless verbs a harness drives (run one prompt, continue, resume, list) | Argv tails appended to the launch argv; `prompt` must reference `{{.Prompt}}` and `resume` `{{.SessionID}}`. **Most agent Kits declare both this and `agent-interactive-sessions@1`**; an agent with no interactive mode declares only this. Verify every flag against the tool's real CLI — never invent one. |
| `agent-interactive-sessions@1` | the TUI verbs a human-facing host launches (seeded prompt, continue, resume, session picker, list) | The sibling of `agent-sessions@1` for an agent whose CLI has an interactive mode; an agent that also has a headless mode declares both, and one with only a TUI declares just this. **Presence is meaning**: a present key is supported, an absent key is not, and for `continue`, `newSession` and `sessionPicker` `[]` is the launch argv alone (`prompt` and `resume` carry their placeholders, so they are never empty). `newSession` is the exception: omit it when a bare launch opens the TUI, because it then defaults to `lifecycle@1`'s `interactive` launch; if you state both `newSession` and `interactive` they must be the same argv, an explicit `[]` included. `list` is the same command as in `agent-sessions@1`. Declare only verbs you verified. |
| `port@1`, `resources@1`, `privileged@1` | inbound ports, limits, elevation | Do not declare on speculation; `privileged@1` is the largest widening available. |

For ACP adapters, verify the launch settings preserve the interactive
entrypoint's permission posture and that stdin EOF and SIGTERM cancel
running turns, persist resumable sessions, and exit within five seconds.
`optional: true` does not exempt an adapter from these guarantees.
ACP 1 background completion depends on negotiated extensions. ACP 2
declarations require standard terminal lifecycle updates and sub-agent
completion updates; a package release number is not evidence that the
adapter speaks protocol version 2.

Host sharing is a separate grant from `volume@1`, including when the
in-container path stays the same. Do not supply a host path or assume a
Linux bind mount: runtimes may use VM filesystem sharing. Host-shared
directories may have weaker filesystem semantics than private volumes.
Concurrent writers coordinate access themselves. The initial `mode`
does not reset existing directory permissions on later creates.
Two host mounts at one path, or a host mount and a volume there, conflict.
Matching volume requests across Kits merge on the cleaned destination;
different sizes, modes, or backing kinds conflict. Sizes compare by byte
value (suffixes use powers of 1024), modes by octal value, and omitted
`tmpfs` is false. Argument-backed sizes and modes remain strings, including
byte sizes such as `1024` and unpadded octal modes such as `755`.
An unspecified size or mode differs from an explicit
value. Duplicate destinations within one declaration block are rejected.
When a published set re-exports a volume's text input, publication keeps
the requests separate until create expands and selects them; matching
concrete settings then merge. Do not pin a create-phase size merely to
avoid comparing its placeholder during publication.

Block-volume data belongs to the sandbox instance, independently of the
Kits that requested it. Restart and recreation retain it, including when
mixins or the workload change. Removing a declaration leaves its storage
retained and unmounted; restoring the path reattaches it. Recreation with
different settings for a retained destination refuses before replacing
the container. New paths start empty before hooks; reattachment preserves
contents and permissions. Removing the instance deletes all its volumes;
creating another with the same name starts fresh. Tmpfs is empty after
stop or recreation. Host mounts retain their separate sharing contract.

## Bundled skills

Use `agent-skill@1` to package a skill as a mixin; see
`examples/review-skill`. Ship the whole directory under a Kit-specific
prefix such as `/usr/share/example-skills/review`, and declare that
absolute source path. A source path must be literal after publishing;
use a build-phase argument if it varies. Supporting scripts and references retain their
relative paths. An optional `config.name` overrides the source basename
at discovery destinations; it does not rename or rewrite the source.

Agent Kits declare `agent-skills@1` at their discovery paths. The runtime
links or otherwise exposes every selected `agent-skill@1` bundle at each
path, even when the host store is missing, empty, or disabled. The same
declaration permits host sharing when available, bounded by `mode` and
host policy; no second directory declaration is needed. Runtime assembly
of these sources is implementation-specific. Different source paths
claiming one effective name conflict at composition, and an existing
destination entry makes a skill request unsatisfiable. Without
any selected destination, a required skill request fails; optional
requests are skipped and recorded. Registration does not execute scripts.

## Versions, provides and requires

**Pin the tool, and say so once.** Declare a build-phase `version` arg, wire it
through to the installer, and reference it from both `provides` and the
top-level `version:` — publishing expands all of it, so one value drives the
install, the matchable capability, `org.opencontainers.image.version` and the
published tag.

**A pinned provide is a claim about content, so make the build enforce it.**
Add a step that re-reads the installed version and fails on mismatch. Pinning
the provide without pinning the install is worse than floating: it asserts a
version the content may not have.

**An unversioned provide is not a resting place.** It resolves by falling
through: an explicit `@version` wins, else a version-shaped consumption
reference, else the descriptor's `version:` — and `:latest` is not
version-shaped. So an unversioned provide under `version: "1.0.0"` publishes
`<tool>@1.0.0`, the kit's release number wearing the tool's name, which a
lower-bound constraint will not match. With no `version:` at all it does not
publish: `RequireVersionedProvides` fails the build. Pin the version, or drop
the provide — a kit with no `provides` publishes fine, and offering nothing
matchable is honest when the kit cannot know what it installed.

**`requires` is a closed-set check**: a name nothing in the composition
provides makes your kit refuse to compose *anywhere*, which is worse than
saying nothing. It constrains the composed **runtime** set, not your builder —
a mixin that only touches apt inside a build stage needs no `deb/apt`, and
adding one there rejects every Alpine or distroless workload that could
otherwise have run the shipped binary perfectly well.

Two things follow, and both cut against the instinct to list whatever a
lifecycle hook shells out to:

**Never require the platform floor.** §12 lets kit content assume `bash` and
`sh`, `curl`, `git`, a populated CA store, and the `agent` user at uid 1000.
A hook running `curl` or `git` has declared nothing by doing so. Worse,
`requires: ["deb/curl"]` refuses every workload without a dpkg database —
an Alpine or Wolfi base publishes `apk/` names — so the entry rules out bases
that were always going to satisfy it.

**A conditional dependency cannot be expressed here, so do not try.** There is
no either/or in `requires`: an entry is a hard precondition on every
composition. A hook that reaches for `apt-get` *only when the tool it installs
is missing* works fine on a base that already ships the tool, and
`requires: ["deb/apt"]` converts "degrades on some bases" into "refuses on
them". Let the hook probe and fail with an actionable message, and say in a
comment that the silence is deliberate — otherwise the next reader adds the
entry back.

The test is not "what do my hooks run" but **"what must already be present, on
every base, for this kit to work at all"**. Usually that is nothing.

Where a requirement is real, `deb/` names are the right vocabulary and
invented ones are wrong: publishing derives a `deb/<pkg>` provide from a
**workload's** dpkg database, so `requires: ["deb/apt"]`, `["deb/jq"]` or
`["deb/docker-ce"]` resolve against any Debian-based workload. On a
multi-platform workload the derived set is the **intersection** — §9.6 emits a
package only where every published platform agrees on its normalized name and
version — so check each arch, not just your own
(`docker run --rm --platform linux/arm64 <base> dpkg-query -W -f='${Version} ${Status}\n' <pkg>`),
and never *author* a `deb/` provide — the frontend refuses it.

## Content recipes

Recipe patterns for both kinds, including the ownership rules an overlay must
satisfy, are in [RECIPES.md](RECIPES.md). Read it before writing a mixin:
overlay ownership is the single most common way a working-looking kit is
broken.

## Build, run, verify

```sh
# 1. validate — the descriptor is checked before any content is built, so this
#    fails in a second on a bad field. cacheonly drops the EXPORT, not the
#    build: once the descriptor is valid the whole recipe still solves.
cd <kit> && docker buildx build . -f <kit>.yaml --output type=cacheonly

# 2. build, exporting a layout so kit-tck can judge it without a registry
docker buildx build . -f <kit>.yaml -t <kit>:<version> \
  --output type=oci,dest=/tmp/<kit>-layout,tar=false

# 3. conformance — NB the tag alone, not <kit>:<version>
kit-tck validate --layout /tmp/<kit>-layout <version>

# 4. run it, no registry needed
sbx run ./<kit> .                          # a workload
sbx run ./<workload> --kit ./<kit> .       # a mixin, composed
sbx kit inspect ./<kit>                    # resolved declarations, no sandbox
```

Inside a sandbox the kit is self-describing: `/usr/share/sandbox/kit/<kit>/kit.yaml`
is the published descriptor, `kit.dockerfile` the recipe, and
`/var/log/sbx-kit-startup.log` the startup hook output.

**A build is not proof the kit works.** For a mixin especially, compose the
built overlay onto a bare base and run the tool. An overlay shipping a dangling
symlink — which happens whenever an installer relocates a launcher but not its
payload and the build-stage `test -x` passes because the payload is still
there — draws a `kit-tck` warning in step 3, but only the composition proves
whether the base supplies the target. Details and the ownership audit are in
[RECIPES.md](RECIPES.md#verifying-an-overlay).

## Publish

**Publishing is the build.** A kit is an OCI artifact and the frontend has
already written its annotations, staged sources and config, so the thing in
the registry is the kit — there is no pack step, no sidecar artifact and no
`kit push` subcommand to look for. Add `--push` to the build that produced the
kit you verified:

```sh
docker buildx build . -f <kit>.yaml --platform linux/amd64,linux/arm64 --push \
  -t <registry>/<kit>:<version> -t <registry>/<kit>:latest \
  --metadata-file /tmp/<kit>-push.json
```

**Push both platforms in one invocation.** One build writes the index
consumers resolve through; two single-platform builds pushed to the same tag
replace each other, leaving a tag that serves whichever ran last and silently
fails for everyone on the other architecture.

Tag the version and `latest` together. `<version>` is the descriptor's
expanded `version:`, so where a `version` arg drives the install it also names
the tag, and the tag says exactly what the image contains.

**Where several kits share one repository, the version belongs in the tag.**
A repository per kit is the simple case; a repository holding a family of them
distinguishes kits *by tag*, which leaves `<kit>:<version>` nowhere to put the
version. Join them and keep the bare name as the moving tag:

```sh
docker buildx build . -f <kit>.yaml --platform linux/amd64,linux/arm64 --push \
  -t <registry>/<kits-repo>:<kit>-<version> \
  -t <registry>/<kits-repo>:<kit>
```

Publishing only the bare name leaves consumers no way to ask for a particular
build, or to notice they were moved onto a different one. Reference the
immutable tag from anything that has to keep working — and note that a
version-shaped *tag* is also one of the inputs that answers an unversioned
`provides` entry, which `<kit>-<version>` is not. That is a reason to state
`version:` in the descriptor rather than leaning on how you tagged.

Signing is optional and orthogonal. A signature is stored as its own object in
the repository rather than as part of the image, so it changes neither the
kit's digest nor its annotations, and a signed kit's `kit-tck` verdict is the
one it already had:

```sh
digest=$(jq -r '."containerimage.digest"' /tmp/<kit>-push.json)
cosign sign --yes <registry>/<kit>@"$digest"
```

**Sign the digest, never the tag.** `--metadata-file` reports the digest the
push actually produced; a tag is mutable, so a signature naming one attests to
whatever it happened to point at.

For a multi-platform build that digest is the **index's**, and signing it
signs the index alone — the per-platform manifests beneath it carry no
signature of their own, so a consumer verifying a platform digest directly
finds nothing. Add `--recursive` to sign each discrete image as well, or say
plainly that only the index is signed and expect verification to name the
index.

In CI, keyless signing avoids managing a key at all: grant the job
`id-token: write` and cosign takes its identity from the OIDC token. The
matching verification names the identity rather than a public key, and for
GitHub Actions that identity is the **workflow**, not the repository:

```sh
cosign verify <registry>/<kit>@"$digest" \
  --certificate-identity 'https://github.com/<org>/<repo>/.github/workflows/publish.yml@refs/tags/<tag>' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

**Name the whole identity, not a prefix of it.** An identity regexp like
`^https://github\.com/<org>/` accepts a certificate from *any* workflow in
*any* repository in the organization, so any job anywhere in the org with
`id-token: write` can mint something that passes — and verification then
proves only that the signature came from somebody in your org, which is not
the question being asked. The signature is only evidence of provenance when
the identity pins the repository, the workflow file and the ref. Where a tag
varies, keep the rest exact and vary only that part:

```sh
  --certificate-identity-regexp '^https://github\.com/<org>/<repo>/\.github/workflows/publish\.yml@refs/tags/'
```

## Tooling

- **`docker buildx`** — nothing to install for the frontend; BuildKit pulls
  `docker/sandbox-kit:3` from the `# syntax=` line.
- **`sbx`** — install the stable CLI from
  [Docker Docs](https://docs.docker.com/ai/sandboxes/install/) /
  [sbx-releases](https://github.com/docker/sbx-releases); current releases
  support Kits v3 in local and cloud mode. Note `sbx kit validate` does
  **not** accept a v3 source kit; `sbx kit inspect` does.
- **`kit-tck`** — `go install github.com/docker/sandbox-kit-spec/v3/cmd/kit-tck@latest`.
- **`cosign`** — only if you sign. Nothing in the kit grammar requires it and
  no consumer needs it to run a kit.

## Reference

- [SPEC-v3.md](https://github.com/docker/sandbox-kit-spec/blob/main/docs/spec/SPEC-v3.md)
  — the grammar. §3 authoring forms, §5 provides/requires, §6 args,
  §7 capabilities, §9.6 derived provides.
- [capability pages](https://github.com/docker/sandbox-kit-spec/tree/main/docs/spec/capabilities/com.docker.sandbox)
  — normative per type.
- [examples](https://github.com/docker/sandbox-kit-spec/tree/main/examples) —
  `gh` for a self-contained tool mixin, `hello` for the smallest workload,
  `claude` and `claude-mixin` for one agent in both shapes, `motd` for the
  single-file inline form, `team` for a set.
- Where the docs and the Go implementation in `spec/` disagree, the code wins.

## Coupled optional features

Use a `group` when skipping a capability also needs to skip its hooks,
files, or guidance. Put `optional: true` on the group, never on its
members (even `optional: false` is invalid). Groups are nonempty and
cannot nest; required and one-member groups are valid. See
`examples/optional-cache/optional-cache.yaml`.

The selection API includes every member or none, before composition.
A runtime supplies decisions on expanded entries; the API owns atomic
selection and ordering. Validate all member configs even in skipped
groups. Selected entries must still satisfy cross-entry rules, and a
composition conflict is an error rather than a reason to skip another
group. Singleton arity is per declaration block; lifecycle lists from
selected blocks concatenate, and duplicate file paths are errors.

Selection lasts for one sandbox installation, including restarts.
Recreation selects afresh. A failing selected hook is an execution
failure, not optional unavailability. Skipping a group does not remove
old files from reused volumes, omit image layers, or suppress argument
environment exports. Group names are display labels, not merge keys.

Groups extend the unfinished schema-3 grammar in place. Descriptors
using them require a reader implementing this extension; older strict
readers reject them.

## Final environment references

Use `${{ kit.env.HOME }}` in capability configuration strings when a path
depends on the final container environment. Image defaults compose first,
argument `env` exports replace them, and runtime environment overrides win
last. Expansion happens before validation and selection, including inside
groups. Missing names fail; empty values remain empty. This is structural
string substitution, not shell evaluation: `$HOME`, `${HOME}`, and `~/`
are unchanged. Do not use environment references in mapping keys or Kit
metadata, and do not pass Kit placeholders inside argument or environment
values. Persist the expanded descriptor for restart.
