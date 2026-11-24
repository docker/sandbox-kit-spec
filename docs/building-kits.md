# Building and publishing Kits

This page covers building Kits with Docker Buildx, publishing them, and
checking the result. The [README](../README.md#how-to-get-started) has
the short path from a published Kit to your own; this page is the detail
behind it.

## Editor support

`schema/kit.schema.json` is the descriptor grammar as a JSON Schema, for
validation and completion in editors. To use it with
yaml-language-server, add a modeline on the descriptor's second line
(the `# syntax=` line must stay first):

```yaml
# syntax=docker/sandbox-kit:3
# yaml-language-server: $schema=https://raw.githubusercontent.com/docker/sandbox-kit-spec/main/schema/kit.schema.json
```

## Where a Kit's content comes from

A Kit pairs its declarations with a content recipe in one of four
ways ([SPEC-v3 §3](spec/SPEC-v3.md#3-authoring-forms)):

- a companion `<stem>.dockerfile` next to the `<stem>.yaml` descriptor,
  or a recipe the descriptor names with a `dockerfile:` field (a path
  relative to the descriptor's directory)
- an inline `build:` block in the descriptor with literal Dockerfile
  text (`examples/motd` shows this single-file form)
- a comment descriptor: a Dockerfile that carries its declarations in a
  `# kit:` comment block, so one file is both (`examples/gh-comment`)
- a `kits:` list naming other Kits (`examples/team`, a `kind: set`; see
  [Publishing a set as one Kit](#publishing-a-set-as-one-kit))

`build:`, `dockerfile:`, and `kits:` are mutually exclusive, and a
comment descriptor must not declare `build:` or `dockerfile:`, because
the file already is the recipe. A `dockerfile:` path that does not exist
is an error. A `kind: mixin` Kit with no recipe is declaration-only.

The frontend finds the companion `claude.dockerfile` by naming
convention, or the file a `dockerfile:` field names, and builds it
through `dockerfile.v0`, honoring the companion's own `# syntax=`
directive if it names a different frontend.
It then validates the descriptor against the resulting image, stages
guidance content into the image, and attaches the published descriptor
as a manifest annotation. A `kind: mixin` descriptor with no companion
produces a declaration-only image whose single layer carries the
published descriptor.

A workload Kit's companion must build on a base that provides the
runtime's platform floor: bash, the `agent` user (uid 1000), git, and a
CA store. The hardened `dhi.io/sbx-templates:*` images carry it, and the
workloads under `examples/` build on them, except `devin`, `wordpress`,
and [`mem0`](../examples/mem0/README.md), whose recipes explain why. Mem0
supplies the floor itself, around a Python memory service with
host-shared storage. A Kit built on a bare distro image builds fine but
fails at agent launch.

## Building with a container builder

Create a named container builder once, then select it explicitly when
you export and publish Kits:

```sh
docker buildx create --name kit-builder --driver docker-container --bootstrap
docker buildx build --builder kit-builder . -f claude.yaml \
  -t docker.io/me/claude-kit:2.1.0 \
  --output type=oci,dest=/tmp/claude-kit-layout,tar=false
```

The descriptor lives in manifest annotations. A push through the
`docker` driver with the classic image store can drop those annotations
and publish an image that consumers don't recognize as a Kit. The
container builder avoids that round trip through the store. A build or
push that succeeds doesn't prove the exported artifact kept its
descriptor, so validate the OCI layout with
`kit-tck validate --layout /tmp/claude-kit-layout 2.1.0`.

Build-phase args are passed by their Kit-arg name and validated before
the Dockerfile sees them under the declared `buildArg` name. `hello`
declares `greeting` with `buildArg: GREETING`, and its recipe writes
`hello ${GREETING}` to `/etc/motd`. From `examples/hello`:

```sh
docker buildx build --builder kit-builder . -f hello.yaml \
  --build-arg greeting=there -t hello-kit:dev --output type=cacheonly
```

## Multi-platform builds

Multi-platform builds produce an image index with the descriptor
annotation on every platform manifest:

```sh
docker buildx build --builder kit-builder . -f claude.yaml \
  --platform linux/amd64,linux/arm64 --provenance=mode=min --push \
  -t docker.io/me/claude-kit:2.1.0
kit-tck validate docker.io/me/claude-kit:2.1.0
```

Validate after publishing too: the frontend's build-time checks cannot
see metadata that an exporter or registry drops. From this repository,
`task tck:kit REF=docker.io/me/claude-kit:2.1.0` runs the same artifact
checks.

The frontend also copies annotations onto the image index for
multi-platform builds and for builds that request provenance
explicitly. Single-platform builds with Buildx's default provenance can
produce an unannotated index, because the frontend cannot tell whether
the selected exporter will keep that attestation. Pass
`--provenance=mode=min` when you want index annotations. They are an
optimization: a Kit whose platform manifest carries the descriptor is
valid without them, and consumers fall back to that manifest, as
[SPEC-v3 §9.3](spec/SPEC-v3.md#93-annotations) requires. An OCI
layout's outer `index.json` catalog is separate from the image index
referenced inside it.

## Publishing a set as one Kit

A composition worth sharing doesn't have to stay a command line. A
`kind: set` descriptor names other Kits in `kits:`. The frontend
resolves them, checks that the set is coherent, and merges their layers
and declarations into one ordinary Kit:

```yaml
# syntax=docker/sandbox-kit:3
schemaVersion: "3"
kind: set
displayName: Team environment
version: "1.0.0"
kits:
  - ref: docker.io/me/sbx-kit-shell:1.0.0
  - ref: docker.io/me/sbx-kit-gh:2.72.0
```

```sh
# The registry the set is pushed to and the one it resolves its Kits
# from are separate answers: the first names where this artifact goes,
# the second is baked into the references it merges.
task kit:push KIT=team TAG=1.0.0 REGISTRY=$KIT_REGISTRY \
  BUILD_ARGS="--build-arg registry=$KIT_REGISTRY"
sbx run $KIT_REGISTRY/sbx-kit-team:1.0.0 .
```

The result is a Kit like any other. Nothing in the runtime knows it was
a set, and `kind: set` never reaches consumers: publishing derives
`workload` or `mixin` from the Kits it lists. The `kits:` list stays in
the published descriptor, pinned by digest, so the artifact records what
it was built from. Inside the sandbox, `ls /usr/share/sandbox/kit/`
lists the staged sources of each listed Kit. They must be published
references, because the set has to be resolvable from its manifest
alone, not from the directory it was written in. See
[§3.4](spec/SPEC-v3.md#34-kit-set) for the grammar and
[§9.5](spec/SPEC-v3.md#95-merging-a-set) for what the merge does with
each declaration.

When a recipe builds `FROM` another Kit, publication removes inherited
`/usr/share/sandbox/kit/*/kit.yaml` files before staging the new Kit's
own descriptor. Inherited guidance, recipes, and payload files stay. A
set build keeps the staged sources of all its listed Kits. Images
published earlier with inherited descriptors must be rebuilt with the
updated frontend before they can be listed in a set.

## Local builds with sbx

When `sbx run` is pointed at Kit directories, local builds from a recipe
use host Docker Buildx when Docker is available, and a dedicated builder
sandbox otherwise. Declaration-only mixins need neither.
`SBX_KIT_BUILDER=host` or `SBX_KIT_BUILDER=sandbox` picks one
explicitly. `sbx kit builder status`, history, and removal cover only
the sandbox backend; removing it does not free the host builder's cache.

The host builder needs OCI export support. If a local source build or
`sbx kit inspect ./hello` fails because the selected builder cannot
export OCI, use `SBX_KIT_BUILDER=sandbox`, or create the container
builder from [Building with a container
builder](#building-with-a-container-builder) and select it with
`BUILDX_BUILDER=kit-builder`. The `docker` driver can export OCI with
the containerd image store; the classic image store does not have the
same exporter support.

Created with `--kit-arg buildkitExpose=true`, the builder sandbox also
publishes BuildKit so the host's own `docker buildx` can attach to it:

```sh
# After the builder sandbox is up, read the published buildkit port from
# `sbx kit builder status`, then:
docker buildx create --name sbx-remote --driver remote \
  tcp://127.0.0.1:<host-port> --use
cd claude && docker buildx build --builder sbx-remote . -f claude.yaml \
  --output type=cacheonly
```

That endpoint builds as root and takes no credential, and `port@1` only
says a host binding *should* be loopback. So it is off unless you ask
for it, and you should only turn it on where you know the runtime binds
to loopback.

The in-sandbox BuildKit port is 3330; `--kit-arg buildkitPort=…`
changes it. The builder declares `dockerd@1`, so on a runtime that
grants it, the engine store under `/var/lib/docker` is storage the
runtime keeps private to the sandbox across restarts and re-creation,
and it doubles as the build cache. The request is optional, and the
recipe keeps the `com.docker.sandboxes.start-docker` label, so a runtime
that predates `dockerd@1` still starts the engine. On such a runtime the
store sits on the sandbox's writable layer, and the build cache is lost
when the sandbox is re-created. A runtime that honors neither the type
nor the label starts no engine.

## Checking a Kit with kit-tck

`kit-tck validate` judges a published Kit against the artifact rules in
[conformance.md](spec/conformance.md). The same checks run inside the
frontend during `docker buildx build`, so a Kit built here cannot be
published malformed. Running them against a published artifact catches
anything the exporter or the registry did to it, and covers Kits this
frontend did not build.

A registry on loopback is reached over plain HTTP automatically, so a
throwaway `registry:2` works as a target while iterating. For a
TLS-less registry anywhere else, pass `--plain-http`:
`kit-tck validate --plain-http <ref>`. An artifact that never reached a
registry is checked in place from the `--output type=oci` directory:

```sh
task tck:kit:layout DIR=/tmp/out TAG=sha256:...
```

Every finding names the statement it checked and links to where the
specification says it, and the run ends with a tally of what was
checked. Passing checks are not printed unless you add `--verbose`.
`--format json` emits the same run as data, with each finding's spec
link, for a pipeline that annotates results. Color follows the terminal
and `NO_COLOR`; `--color` overrides both. The runtime suite reports the
same way.

```sh
kit-tck validate docker.io/me/sbx-kit-gh:1.0.0 --verbose
kit-tck validate docker.io/me/sbx-kit-gh:1.0.0 --format json
```

`kit-tck inspect` reads a Kit without checking it. It prints the
descriptor from the manifest annotation and the content recipe staged at
`/usr/share/sandbox/kit/<stem>/kit.dockerfile`, however that recipe was
written. It takes the same `--layout` and `--plain-http` flags as
`validate`.

```sh
kit-tck inspect docker.io/me/sbx-kit-gh:1.0.0                  # both, as YAML and Dockerfile
kit-tck inspect docker.io/me/sbx-kit-gh:1.0.0 --dockerfile > gh.dockerfile
kit-tck inspect docker.io/me/sbx-kit-gh:1.0.0 --format json
```

To find a Kit's source repository and commit, inspect its build
provenance in the image index or through OCI referrers. The optional
`sourceUrl` field and the `org.opencontainers.image.source` annotation
can also point to its sources.
[Inspecting a Kit's source](inspecting-kits.md) has the commands and
guidance for publishers.

## The frontend image

`# syntax=docker/sandbox-kit:3` resolves to
[`docker/sandbox-kit`](https://hub.docker.com/r/docker/sandbox-kit) on
Docker Hub. BuildKit pulls and caches it on the first build that names
it. Every release also publishes its exact version, such as
`docker/sandbox-kit:3.0.0-m.13`. Pin a release tag to control frontend
upgrades, or a digest to pin exact content.
[RELEASES.md](../RELEASES.md#frontend-image) says when the floating `:3`
tag moves.
