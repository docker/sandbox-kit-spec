# Releases

Several version axes move independently in this repository; confusing them
is the main hazard. This page says what each one means and what forces it
to change. Schema-3 frontend releases and the Go module major share the
`v3.*` git tag namespace: the module path is
`github.com/docker/sandbox-kit-spec/v3`.

## Version axes

| Axis | Where it lives | Moves when |
|---|---|---|
| Module / schema-3 release | git tags `v3.X.Y` (+ Hub `docker/sandbox-kit:3.X.Y`) | A publish of the Go packages (`spec`, `resolve`, `assemble`, `fetch`, `tck`), the schema-3 BuildKit frontend, and `kit-tck` binaries |
| Schema version | `spec.SchemaVersion`, the descriptor's `schemaVersion: "3"` | The descriptor grammar changes shape incompatibly |
| Capability version | the `@N` in `com.docker.sandbox/<name>@N` | That capability's config schema or observable behavior changes after it has shipped, subject to the draft-stage exception below |
| Frontend floating tag | `docker/sandbox-kit:3` | Tracks the highest stable schema-3 frontend release |

A kit's own `version:` and its `provides` entries are a further axis, but
they belong to kit authors rather than to this repository;
[SPEC-v3 §5.2](docs/spec/SPEC-v3.md#52-versions) governs them.

`v3.*` git tags **are** Go module versions for
`github.com/docker/sandbox-kit-spec/v3`. Consumers import packages as
`github.com/docker/sandbox-kit-spec/v3/spec` (and siblings) and resolve
them with `go get github.com/docker/sandbox-kit-spec/v3@v3.X.Y`. The same
tag also publishes the frontend image and attaches `kit-tck` release
assets.

## Module tags

Tag when the Go packages (and the matching frontend / `kit-tck` release)
should be consumable at a new version:
`git tag -a v3.X.Y && git push origin v3.X.Y`. Prefer annotated tags.
Pre-release suffixes (`v3.0.0-m.2`, `v3.0.0-rc.1`) are valid module
versions and trigger the release workflow without moving floating Hub
`:3`.

## Schema version

`schemaVersion: "3"` names the generation of the descriptor grammar.
Decoding is strict ([SPEC-v3 §1.2](docs/spec/SPEC-v3.md#12-strict-decoding)),
in two passes with two mechanisms. The descriptor itself is YAML-decoded
with `KnownFields(true)` (`spec.Decode`), which rejects an unrecognized
top-level field. A capability's `config` survives that pass as a plain
map and is decoded per type later, with `DisallowUnknownFields`
(`spec.DecodeCapabilityConfig`), which rejects an unrecognized key in a
well-known type's config. Config for a type the reader does not know is
the exception at both steps — it stays an opaque map, which is what lets
third-party types travel.

For anything the grammar defines, then, the compatibility question is not
"would an old reader ignore this?" — it would not; it would refuse the
document.

What that buys is a grammar where a descriptor is either understood
completely or rejected loudly. What it costs is that **adding a field is
not free**: a descriptor using one cannot be read by a frontend or
runtime built before it. Adding a field to a capability's config moves
that capability's version, which is the fine-grained lever; moving
`schemaVersion` is for changes that lever cannot express — a top-level
field added, renamed, or removed, or a different meaning for one that
stays. An addition counts: strict decoding means an older reader refuses
a descriptor that uses it.

Moving it is expensive: the frontend image tag follows, every `#
syntax=docker/sandbox-kit:N` line in the wild points at the old one, and
`spec.SchemaVersion` gates decoding. Treat it as a new specification
document (`docs/spec/SPEC-v4.md`) rather than an edit to the current one.

## Capability versions

Each capability type addresses its config schema and observable behavior
by version, so **the version moves when that contract changes** — the rule
[SPEC-v3 §7](docs/spec/SPEC-v3.md#7-capabilities) states — and the old
version stays published: `network-policy@1` and `@2` both exist, and a
descriptor states one of them.

"Changes" is not only "gains a field an old runtime would reject on
decode". A field whose meaning, default, or permitted values change is
worse, because an old runtime accepts it and then enforces the wrong
policy — a silent misreading of a permission grant rather than a loud
failure. Both move the version.

A capability that has never appeared in a tagged release may still
change in place. Once shipped, the versioning rule applies, subject to
the draft-stage exception below.

The specification is currently an early draft. During this phase,
maintainers may approve in-place capability contract changes even when
the capability appeared in a milestone pre-release tag. Those tags do
not freeze the draft contracts: consumers need the matching draft
specification and implementation. A pull request making such a change
records the maintainer-approved exception and its compatibility impact.

The exception ends with the first stable specification release. After
that, changes to shipped capability meanings, defaults, permitted
values, reconciliation, or persistent-state transitions require a new
capability version, and the old contract remains published.

Adding a whole new capability type is additive and moves nothing.

## Frontend image

Pushing a `v3.X.Y` (or `v3.X.Y-rc.N`) tag runs the release workflow, which
publishes `docker/sandbox-kit:3.X.Y` and, when that tag is the highest
stable `v3.*.*` on the remote, also moves floating `docker/sandbox-kit:3`.
Main commits publish `docker/sandbox-kit:<short-sha>` only.

To publish by hand:

```sh
FRONTEND_PUSH_OK=1 task frontend:push FRONTEND_VERSION=3.0.0
# also move floating :3 (only when this is the intended tip):
FRONTEND_PUSH_OK=1 FRONTEND_PROMOTE_MAJOR=1 \
  task frontend:push FRONTEND_VERSION=3.0.0
```

`FRONTEND_PUSH_OK` is a precondition, not decoration: these tags are the
syntax references descriptors resolve, so publishing is never one
forgotten flag away. Kits name the floating major in their `# syntax=`
line, so `:3` must keep building every descriptor of that generation —
rebuild and promote it whenever the grammar gains something kits may
use, and never repoint it at a frontend that would reject an older v3
descriptor.

## Build stamps

Both binaries carry the tag they were built from and the commit beside
it, in `internal/version`. Neither can read git for itself — GoReleaser
links `kit-tck` from a tagged checkout, and the frontend is linked inside
a Docker build whose context excludes `.git` — so the values are handed
down as linker flags, from `{{.Version}}`/`{{.FullCommit}}` in
[.goreleaser.yaml](.goreleaser.yaml) and from the `VERSION` / `REVISION`
build args in [Dockerfile](Dockerfile). A build nobody stamped says
`dev`, which is what a local `docker build` or `task kit:dev` produces.

The stamp is a reflection of the module tag, not a fourth axis: nothing
here moves on its own. Where it shows up:

| Surface | Form |
|---|---|
| `kit-tck version`, report header, `--format json` | `3.0.0-m.5 (2f9a1c4e)`; the JSON envelope keeps `version` and `revision` apart |
| Frontend build progress | `[internal] load kit descriptor <file> · sandbox-kit 3.0.0-m.5 (2f9a1c4e)` |
| Every kit the frontend publishes | the `vnd.docker.sandbox.kit.built-by` annotation ([SPEC-v3 §9.3](docs/spec/SPEC-v3.md#93-annotations)) |

`frontend:push` stamps the same string it tags the image with, so a
frontend can never report a release it was not published as — the rule
kit tags already follow. Only `kit-tck` feeds its version to spec links,
and it passes the tag alone: a revision is not a ref, and a URL built
from one resolves to nothing.

## Conformance suites and releases

`task tck:runtime ADAPTER=<path>` judges a runtime through its adapter,
and `task tck:kit REF=<ref>` judges a published artifact; `task test:tck`
runs the same runtime suite against the repository's fake adapter, which
is how the suite itself is kept honest. All are versioned with the module
rather than separately — a release of the Go packages is also the release of the
conformance suites, and a suite that gains a check can fail a runtime
that passed the previous tag. That is intended: the check reflects a duty
the specification already stated.
