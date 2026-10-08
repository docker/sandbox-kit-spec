---
name: migrate-kit-to-v3
description: >-
  Migrates a Docker sandbox kit from the v2 `spec.yaml` grammar to the v3 kit
  descriptor, then builds it with docker buildx, runs it with sbx, and verifies
  it with the kit-tck conformance suite. Use when migrating or porting a kit to
  schemaVersion 3, when converting a v2 spec.yaml / sandbox.image / setup hooks
  / permissions block into v3 capabilities, when adding a `-mixin` variant of a
  workload kit, or when asked how to build, run, or conformance-check a v3 kit.
---

# Migrate a kit to v3

A v2 kit is one `spec.yaml` plus a `Dockerfile` that builds a named image. A v3
kit is one OCI image: the descriptor declares what the kit needs as typed
capabilities and rides in a manifest annotation, the image config carries the
runtime contract (entrypoint, env, user, workdir), and the layers carry the
content.

## Tooling

Three published tools, nothing repo-local:

- **`docker buildx`** builds kits. Nothing to install for the kit frontend —
  BuildKit pulls `docker/sandbox-kit:3` from the descriptor's `# syntax=` line.
- **`sbx`** runs them. Install the stable CLI from
  [Docker Docs](https://docs.docker.com/ai/sandboxes/install/) /
  [sbx-releases](https://github.com/docker/sbx-releases); current releases
  support Kits v3 in local and cloud mode.
- **`kit-tck`** judges conformance:
  `go install github.com/docker/sandbox-kit-spec/v3/cmd/kit-tck@latest`, or take
  an archive from the
  [releases page](https://github.com/docker/sandbox-kit-spec/releases). (A
  `go install` of an untagged ref reports `dev`; `@v3.x.y` and the release
  archives both carry the real version, which `internal/version` reads from
  the build info.)

  **This repository is private, so both routes need access to it.** The public
  module proxy cannot serve it — `proxy.golang.org` answers 404 — so `go
  install` resolves direct and needs a git credential plus
  `GOPRIVATE=github.com/docker/*`. In CI that means a token with read access,
  and a fork's token does not have one: a fork can still validate a descriptor
  by building it, since the frontend validates during the build, but it cannot
  run `kit-tck`. If the repository becomes public, that whole constraint
  disappears.

## Authorities

In precedence order:

1. [`spec/`](https://github.com/docker/sandbox-kit-spec/tree/main/spec) — the Go
   implementation. Where docs and code disagree, code wins.
2. [SPEC-v3.md](https://github.com/docker/sandbox-kit-spec/blob/main/docs/spec/SPEC-v3.md)
   — the grammar (§3 authoring forms, §4 top-level fields, §5
   provides/requires, §6 args, §7 capabilities, §8 launch modes).
3. [capability pages](https://github.com/docker/sandbox-kit-spec/tree/main/docs/spec/capabilities/com.docker.sandbox)
   — one per capability type, normative for its config schema and runtime
   behavior.
4. [examples](https://github.com/docker/sandbox-kit-spec/tree/main/examples) —
   worked kits. `claude` and `claude-mixin` are the migration of a real v2 kit
   into both shapes; read them first.

Working inside the spec repo, all four are also on disk at `spec/`,
`docs/spec/` and `examples/`.

## Workflow

Track progress with this checklist:

```text
- [ ] 1. Read the v2 kit end to end, including its README and testdata
- [ ] 2. Write the v3 descriptor, recipe, and context file
- [ ] 3. Add the -mixin variant (workload kits only)
- [ ] 4. Validate the descriptor (fails in seconds on a bad field)
- [ ] 5. Build the kit
- [ ] 6. Run it with sbx and exercise the agent
- [ ] 7. Verify with kit-tck
- [ ] 8. Publish, and delete the CI that published the v2 pair
```

### 1. Read the v2 kit first

Read `spec.yaml`, `Dockerfile`, `README.md` and `testdata/tck.yaml` before
writing anything. v2 kits carry their reasoning in comments, and that prose is
the most valuable thing to carry across. `testdata/tck.yaml` is the only record
of whether a working non-interactive invocation was ever established
(`promptArgs`), which decides whether the migrated kit declares
`agent-sessions@1`. It says nothing about interactive verbs: declare
`agent-interactive-sessions@1` beside it only for verbs you have verified
against the tool's real CLI, never from a pattern.

### 2. Write the v3 files

For a kit named `<kit>`, migrating in place:

| v2 | v3 |
|---|---|
| `<kit>/spec.yaml` | `<kit>/<kit>.yaml`, first line `# syntax=docker/sandbox-kit:3` |
| `<kit>/Dockerfile` | `<kit>/<kit>.dockerfile` — found by filename stem, so no `dockerfile:` field |
| `agentInstructions.content` | `<kit>/<kit>-context.md`, referenced as `contentFile: ./<kit>-context.md` |
| `<kit>/testdata/tck.yaml` | delete — v2-only harness |
| `<kit>/.dockerignore` | **keep and audit.** BuildKit still applies it to a v3 kit's build context, so deleting it puts back whatever it was excluding — secrets and large generated files included. Drop only the entries that named v2 files. |
| `<kit>/<kit>_tck_test.go` | delete — it loads the v2 `spec.yaml` through `tck.NewSuiteFromDir(".")` and cannot compile once that file is gone |

The complete field-by-field mapping, the capability rules, and the gotcha list
are in [FIELD-MAPPING.md](FIELD-MAPPING.md). Read it before writing the
descriptor.

Volume declarations at one cleaned path merge across Kits only when their
expanded settings match. Equivalent byte-size and octal-mode spellings
match; unspecified size or mode differs from an explicit value. Block
storage belongs to the sandbox instance: recreation retains it across
composition changes, unselected paths remain unmounted until requested
again, and incompatible settings refuse recreation. Instance removal
deletes all its volumes; another create with the same name starts fresh.
Fresh storage starts empty before hooks. Host mounts still have one
Kit owner and conflict with volumes at the same destination.

Migrate faithfully: preserve every declared host, credential, hook, volume,
port, env var and instruction, and keep base images verbatim. Where v3 cannot
express something, or where a v2 declaration turns out to be dead config, mark
it with an inline `# MIGRATION NOTE:` comment rather than dropping it silently
— the `claude` example shows the convention.

Faithful does not mean literal in one respect: a v2 install **hook** is often a
v2 limitation rather than a v3 requirement, because a v2 mixin had no way to
ship content. Decide per hook whether it becomes a layer — the rule, and the
five cases where a hook is still correct, are in
[FIELD-MAPPING.md](FIELD-MAPPING.md#lifecycle1).

### 3. Add the `-mixin` variant

Every workload kit gets a sibling `<kit>-mixin/` holding `<kit>-mixin.yaml`,
`<kit>-mixin.dockerfile` and a context file. Name that last one
`<kit>-context.md`, which is what six of the seven example mixins do — the
staged name only has to avoid `kit.yaml` and `kit.dockerfile`, so the `-mixin-`
infix buys nothing and the repository is already near-unanimous. The mixin
declares the
same credentials, network policy, volumes and hooks, minus what only the kit
that owns the environment can carry. See the
[mixin variants](FIELD-MAPPING.md#mixin-variants) section for the exact
subtractions and the overlay recipe patterns.

### 4. Validate the descriptor

The frontend decodes and validates the descriptor **before** it builds any
content, so an export-less build is the fast loop *while the descriptor is
wrong*:

```sh
cd <kit> && docker buildx build . -f <kit>.yaml --output type=cacheonly
```

A malformed descriptor fails in about a second, naming the offending field and
its line. Be clear about what `cacheonly` does, though: it suppresses the
**export**, not the build. Once the descriptor is valid the frontend goes on to
solve the whole recipe — downloads, installs and all — so this is fail-fast for
bad input rather than a validation-only step. Iterate here until it is
clean: a malformed descriptor still costs only the second it takes to
reject, even though a valid one costs the whole recipe.

Pass build-phase args by the **kit's** arg name, not the `buildArg` name the
recipe sees, and supply anything declared `required` or validation fails:

```sh
docker buildx build . -f <kit>.yaml --build-arg version=2.99.0 --output type=cacheonly
```

Because the recipe is solved too, a bad `FROM` or `COPY --from` surfaces here
rather than waiting for step 5 — you just do not get an image out of it.

### 5. Build the kit

Swap the export for `--load` to put an ordinary tagged image in the local
store:

```sh
docker buildx build . -f <kit>.yaml -t <kit>-kit:<tag> --load
```

`--load` is not optional on the `docker-container` driver, which is what
`buildx create` gives you: without an output the result stays in the builder's
cache and `docker run <kit>-kit:<tag>` reports no such image.

To judge the artifact with `kit-tck` without a registry, export an OCI layout
directory instead:

```sh
docker buildx build . -f <kit>.yaml -t <kit>-kit:<tag> \
  --output type=oci,dest=/tmp/<kit>-layout,tar=false
```

A workload's recipe must build on a base carrying the platform floor — `bash`,
the `agent` user (uid 1000), `git`, a CA store — which the hardened
`dhi.io/sbx-templates:*` images carry. A bare distro base builds fine and
fails at agent launch.

### 6. Run it with sbx

Point `sbx` at the kit directory — the runtime builds source-form kits on
demand, keyed by source hash, so this needs no registry and no push:

```sh
sbx run ./<kit> .                            # a workload kit
sbx run ./<workload> --kit ./<kit>-mixin .   # a mixin, composed onto a workload
```

A mixin cannot run alone; compose it onto the migrated workload or onto a shell
workload. Pass kit args with `--kit-arg name=value` (or `--kit-arg
kit.name=value` to target one kit), and bind a credential the kit declares with
`sbx secret set <service>` before expecting authenticated calls to work. (The
`-g` flag older docs show is deprecated; global is the default now.)

Inside the sandbox, the kit is self-describing — use it to check that what you
declared is what arrived:

```sh
cat /usr/share/sandbox/kit/<kit>/kit.yaml        # the published descriptor
cat /usr/share/sandbox/kit/<kit>/kit.dockerfile  # the recipe that built it
cat /var/log/sbx-kit-startup.log                 # startup hook output
```

Then exercise the kit for real: run the agent's own version command, confirm
install hooks left what they should, confirm a declared volume is writable by
`agent`, and confirm an undeclared host is refused while a declared one is not.

`sbx kit inspect ./<kit>` builds the source kit and prints its resolved
declarations (kind, network counts, credentials, args) without starting a
sandbox. Add `--kit-arg` to preview how args resolve. The first source build
creates the shared builder sandbox and is slow; `sbx kit builder status` shows
it and `sbx kit builder rm` reclaims the cache.

For the published path instead of the local loop, push the kit as an ordinary
image and run it by reference — a kit image that only exists in the local
Docker store cannot run, because the runtime resolves kit images from
registries:

```sh
docker buildx build . -f <kit>.yaml --push -t docker.io/<you>/sbx-kit-<kit>:<tag> \
  --platform linux/amd64,linux/arm64 --provenance=true
sbx run docker.io/<you>/sbx-kit-<kit>:<tag> .
```

### 7. Verify with kit-tck

`kit-tck` judges an artifact's annotations, layers, staged sources and image
config, with every check linked to the clause it enforces. The same checks run
inside the frontend during a build, so running them here is how an artifact
changed by an exporter or a registry on its way out gets judged — and how a kit
this frontend did not build gets judged at all.

```sh
kit-tck validate --layout /tmp/<kit>-layout <tag>     # the OCI layout from step 5
kit-tck validate docker.io/<you>/sbx-kit-<kit>:<tag>  # a published kit
```

For the layout form, `<tag>` is the tag **alone** as the layout records it
(`1.0.0`), not the full `<kit>-kit:1.0.0` reference the build was tagged with.

Add `--verbose` to list the checks that passed, `--format json` for every check
with its spec link, and `--plain-http` for a registry served over HTTP.

A published kit built on a **multi-node** builder warns that the index carries
no kit annotations. That is expected, not a defect: a multi-node build merges
per-node results into a fresh index client-side, which dissolves them, and
§9.3 requires consumers to fall back to the platform manifest, which does carry
them. The verdict is still `✓ conforms`. A single-node build shows no warning.

Runtime conformance is a separate suite, for people implementing a runtime
rather than authoring a kit: `kit-tck runtime --adapter <path>` drives hundreds
of sandbox lifecycles against an adapter implementing
[conformance.md](https://github.com/docker/sandbox-kit-spec/blob/main/docs/spec/conformance.md).
It runs long — the bare command defaults `--timeout` to 30m and fails when that
expires, so a real run needs something like `--timeout 2h` — and migrating a kit
does not need it at all.

### 8. Publish

One artifact means one push. A v2 kit pointed `sandbox.image` at an image
published separately from the kit itself, so shipping a change meant building
and pushing both and keeping the reference between them honest. In v3 the
recipe's `FROM` builds the content, the frontend annotates it, and the result
in the registry is the whole kit:

```sh
docker buildx build . -f <kit>.yaml --platform linux/amd64,linux/arm64 --push \
  -t <registry>/sbx-kit-<kit>:<version> -t <registry>/sbx-kit-<kit>:latest
```

**Audit the CI that published the v2 pair.** A pipeline built around two
artifacts does not fail once there is only one — it keeps pushing an image
nothing references now that `sandbox.image` is gone, and the step that packed
the kit has nothing left to pack. Both halves get deleted, not rewired.

**Check what the existing tag actually names.** A repo publishing a family of
kits into one repository tags them by *name* (`…/sbx-kits:<kit>`), which is a
tag that says nothing about which build it points at. Carried into v3 unchanged
and paired with a descriptor that never set `version:`, it yields a published
kit with no version anywhere in it. Add `<kit>-<version>` beside the bare name,
and set `version:` regardless — the joined tag is not version-shaped, so it
cannot stand in for the field.

Signing is optional, unchanged by the migration, and described with the rest
of the publish flow in
[create-kit-v3](../create-kit-v3/SKILL.md#publish).

## What actually catches bugs

Each check below caught real defects in a migration of 87 kits that the
cheaper checks above it did not. They are ordered by what they cost.

1. **Validation** catches malformed descriptors, and the **build** catches more
   than you would guess: `readContextFile` and `RequireAuthoredProvides` both
   run before the content loop, so a `contentFile:` naming a missing file and
   an authored `deb/` provide fail in step 4, not later. What neither sees is a
   `*-context.md` no descriptor references, or a v2 instruction body that was
   dropped — both fail at nothing at all. Script those two file-level audits;
   they take seconds and they found a kit whose instructions would silently
   never have reached the agent.
2. **Building** catches recipes. It does not prove the content works.
3. **Reading the exported layer** catches ownership, in **two** parts. Export
   with `--output type=oci,dest=<dir>,tar=false` and run
   `kit-tck validate --layout <dir> <tag>`: `overlay-home-ownership` fails an
   overlay whose `/home` is not root's or whose `/home/agent` is not uid
   1000's, which is what six of the migrated overlays got wrong. Then count
   every other owner:
   `for b in <dir>/blobs/sha256/*; do tar --numeric-owner -tvf "$b"; done | awk '{print ($2 ~ /\//) ? $2 : $3"/"$4}' | sort | uniq -c`.
   Every count must be `0/0` or `1000/1000`; that is what found four overlays
   shipping files owned by package publishers' uids. The awk reads GNU tar's
   joined `0/0` field or bsdtar's split pair, because a pipeline written for
   either silently reports the other's size and date.
4. **Composing the overlay and running the tool** catches the rest. Three
   mixins shipped dangling symlinks whose build-time `test -x` passed because
   the real tree was still present *in the build stage*: the installer had
   relocated a launcher but not its payload. `kit-tck`'s
   `overlay-links-resolve` now warns about those, but only the composition
   shows whether a link the overlay cannot resolve by itself finds its target
   on the base. Compose it through
   the assembler, which is what merges the overlay's `ENV` and `PATH` and puts
   it on a base carrying the platform floor:
   `sbx run ./<workload> --kit ./<kit> --detached --name t .`, then
   `sbx exec t tool --version`. Use `sbx exec` and not `sbx run … -- tool`:
   arguments after `--` are **agent** arguments and never run the binary. The
   `--load` plus throwaway `COPY --from=<overlay> / /` trick is faster and
   finds the same dangling symlinks, but it transfers files only — the image
   config is dropped, so a mixin relying on its own `ENV` fails there for a
   reason the assembler would not produce. Full detail in
   [Verifying an overlay](../create-kit-v3/RECIPES.md#verifying-an-overlay).
5. **`kit-tck`** judges the published artifact — see step 7.

When you change ownership, re-run step 4, not just step 3. A `chown` that fixes
the numbers can still break the tool.

## Known gaps

- `sbx kit validate` does not accept a v3 **source** kit: its load path has no
  kit builder configured. Validate with the build in step 4, and inspect the
  built artifact with `sbx kit inspect`.
- A migration strands every script, workflow and test that globs the old
  layout. Audit them as part of the work: discovery globbing `*/spec.yaml`
  returns nothing, so CI passes by building nothing at all, which is the worst
  failure mode available.

## Migration conventions

These are the conventions the `sbx-kits-contrib` v3 migration followed. Keep
them unless the task says otherwise:

- Migrate in place and delete the v2 `spec.yaml` and `Dockerfile` once the v3
  pair exists.
- Keep each kit's base images verbatim; a grammar migration is not the moment
  to re-point a base.
- Keep `README.md`, updating the filenames and any v2 grammar it quotes; keep
  `README.image.md`; delete `testdata/tck.yaml`. Keep `.dockerignore` — it
  still governs the v3 build context — and prune only the entries that named
  v2 files.
- Heavily commented YAML is the house style. Carry the v2 comments across —
  they are the reasoning behind the declarations.

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
