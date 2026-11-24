![Docker Sandbox Kit Specification](docs/assets/banner.png)

# Docker Sandbox Kit Specification v3

Kits let you hand someone an agent environment as a single image: the
agent, its tools, its skills and instructions, and exactly what it may
touch, from the credentials it uses to the hosts it can reach. Run it
with `sbx run`, layer more Kits onto it, and push it to any registry like
an ordinary image.

A Dockerfile can't carry that authority. Today it lives in `docker run`
flags, Compose files, CI configs, and people's memory. A Kit declares it
in the image's manifest, under the same digest as the content, so when
the next version of an agent asks for another credential or another
host, the change shows up in a diff and can be stopped for review.

**Dockerfiles made software reproducible. Kits make authority reproducible.**

This repository holds the Kit v3 specification, the BuildKit frontend
that builds Kits, and the conformance suites that check a published Kit
or a runtime that claims to support Kits.
[docs/design.md](docs/design.md) has the full argument and the tenets
behind the design.

> [!IMPORTANT]
> **This specification is experimental.** It will change as implementers
> find gaps. Changes are meant to be additive: a capability contract
> that has to change ships as a new version (`@2` next to `@1`), and
> both stay published, so Kits that resolve today should keep resolving.
> A final version is planned for **Q4 2026**. If a Kit you want to write
> cannot be expressed, or a runtime duty is stated in a way you cannot
> implement or check, please
> [open an issue](https://github.com/docker/sandbox-kit-spec/issues/new/choose).

## What a Kit is

A Kit packages one piece of a working environment, such as a tool, an
agent, or a service, so that a runtime can install it, grant it what it
needs, and combine it with other Kits without knowing anything about it
in advance.

A Kit is a single OCI image. The manifest annotation
`vnd.docker.sandbox.kit.descriptor` carries the Kit's **declarations**,
and the layers carry its **content**. There is no Kit media type,
artifact type, or sidecar file, so a Kit pulls with `docker pull`, works
as a `FROM` base, and any registry stores it correctly.

| Kind | Its layers are | Per composition |
|---|---|---|
| `workload` | A root filesystem. The image config supplies entrypoint, cmd, env, user, workdir. | Exactly one |
| `mixin` | An overlay that lands on the workload's filesystem. May carry no content at all and only declare. | Zero or more |

A workload is the thing that runs. Mixins add to it: a CLI, a credential
binding, a network rule, or context for an agent.

## How to get started

Running Kits takes the `sbx` CLI, which supports Kits v3 in both local
and cloud mode. The frontend needs no install: BuildKit pulls
[`docker/sandbox-kit:3`](https://hub.docker.com/r/docker/sandbox-kit)
when it reads a descriptor's `# syntax=` line.

**1. Install `sbx`.** [Docker Docs](https://docs.docker.com/ai/sandboxes/install/)
and [sbx-releases](https://github.com/docker/sbx-releases) have the
details.

```sh
# macOS
brew trust docker/tap
brew install docker/tap/sbx

# Windows
winget install -h Docker.sbx

# Ubuntu (sbx only; or use SBX=1 with get.docker.com for Engine + sbx)
curl -fsSL https://get.docker.com | sudo REPO_ONLY=1 sh
sudo apt install docker-sbx
sudo usermod -aG kvm $USER
# re-login (or: newgrp kvm) so /dev/kvm is usable before sbx run

# Or download platform artifacts from
# https://github.com/docker/sbx-releases/releases
sbx login
```

**2. Run a published Kit.** This composes a minimal workload with a
mixin that adds Claude Code:

```sh
sbx run docker/sbx-kit-shell:1.0.0 --kit docker/sbx-kit-claude-mixin:2.1.281 .
# same references in cloud (no local workspace path):
# sbx --cloud run docker/sbx-kit-shell:1.0.0 --kit docker/sbx-kit-claude-mixin:2.1.281
```

More v3 Kits are on
[Docker Hub](https://hub.docker.com/search?type=sbx_kit&badges=verified_publisher)
under the `docker` org. Kits under `sbx/*` there are the older v2 line;
don't mix the two.

**3. Run a Kit from this checkout.** Point `sbx run` at Kit directories
and the runtime builds them on demand, with no registry and no push:

```sh
cd examples
sbx run ./hello --kit ./gh .
```

`hello` is the smallest workload Kit: an agent environment with
guidance and a network policy. `gh` is a mixin that adds the GitHub CLI
as a pinned Nix closure. Inside the sandbox, `gh --version` works, and
`/usr/share/sandbox/kit/hello/` holds the published `kit.yaml` and the
`kit.dockerfile` recipe that produced the content. With a `github`
secret set on the host (`sbx secret set github`), `gh api user`
authenticates through the proxy, and the container only ever sees a
sentinel token.

Edit `hello/hello.yaml` (say, add an allow entry) and run it again. Only
`hello` rebuilds. If the build fails because the builder cannot export
OCI, set `SBX_KIT_BUILDER=sandbox`;
[Local builds with sbx](docs/building-kits.md#local-builds-with-sbx)
explains the options.

**4. Share it.** The runtime resolves Kit images from registries, so an
image that exists only in Docker Desktop's local store cannot run. Push
to a namespace you own:

```sh
export KIT_REGISTRY=docker.io/<your-hub-username>
docker login
task kit:push KIT=hello TAG=1.0.0 REGISTRY=$KIT_REGISTRY
sbx run $KIT_REGISTRY/sbx-kit-hello:1.0.0 .
```

`task kit:build KIT=hello` builds without pushing, which is enough to
check that a descriptor validates.

## Where to go next

- [docs/kit-intro.md](docs/kit-intro.md): a worked tour of a real Kit,
  its capabilities, and how Kits compose.
- [docs/building-kits.md](docs/building-kits.md): building with Buildx,
  base images, multi-platform builds, publishing a set as one Kit, and
  checking a Kit with `kit-tck`.
- [docs/spec/SPEC-v3.md](docs/spec/SPEC-v3.md): the normative
  specification, with one page per capability type.
- [`examples/`](examples): working Kits, including
  [`ubuntu-docker`](examples/ubuntu-docker/README.md) and
  [`ubi-docker`](examples/ubi-docker/README.md), workloads with Docker
  Engine, Buildx and Compose.
- [`skills/`](skills/README.md): agent skills for writing a v3 Kit and
  migrating a v2 one.

## Conformance

`kit-tck validate` checks that an artifact is a conforming Kit.
`kit-tck runtime` checks that a runtime behaves as the capability pages
require, through an adapter: an executable, in any language, that
implements the [adapter verbs](docs/spec/conformance.md#22-verbs).
[docs/spec/conformance.md](docs/spec/conformance.md) defines what each
suite means. Each
[GitHub Release](https://github.com/docker/sandbox-kit-spec/releases)
has `kit-tck` binaries attached, and from this checkout `task` runs the
suites with `go run`:

```sh
task tck:kit REF=docker.io/me/sbx-kit-gh:1.0.0   # is this artifact a conforming Kit?
task tck:runtime ADAPTER=./my-adapter            # does this runtime behave as the pages require?
```

[Checking a Kit with kit-tck](docs/building-kits.md#checking-a-kit-with-kit-tck)
covers output formats, local layouts, and `kit-tck inspect`.

## Development

```sh
task validate   # gofmt + go vet
task test       # both modules and the containerd 2.3 consumer check
```

See [CONTRIBUTING.md](.github/CONTRIBUTING.md) for the repository
layout, working on the frontend, and changing the grammar.

## License

Licensed under the [Apache License, Version 2.0](LICENSE).
