# Red Hat UBI with Docker

A shell workload built from
`registry.access.redhat.com/ubi9/ubi:9.6`, with Docker Engine, Buildx and
Compose installed from Docker's signed RHEL 9 package repository.
It is the RPM-based counterpart to
[`ubuntu-docker`](../ubuntu-docker/README.md).

The recipe supplies bash, curl, git, a CA store, and the `agent` user at
uid 1000. The Kit version and default publication tag follow the UBI
release, `9.6`. Docker package versions follow Docker's repository.

## Run

From the repository root, with Docker Buildx and a signed-in `sbx`:

```sh
docker buildx create --name kit-builder --driver docker-container --bootstrap
BUILDX_BUILDER=kit-builder sbx run --name ubi-docker ./examples/ubi-docker .
```

Reuse the builder if it already exists. The container driver preserves
the Kit's OCI annotations during the local source build. No registry
publication is needed.

Inside the sandbox:

```sh
id
cat /etc/redhat-release
docker version
docker buildx version
docker compose version
docker run --rm hello-world
printf 'FROM busybox:stable\nRUN echo built-in-sandbox\n' | docker build -t smoke -
```

The shell runs as `agent`; passwordless `sudo` is available. Install
additional packages with `sudo dnf install <package>`. The runtime's
normal session-based auto-stop policy applies. Stop it explicitly with
`sbx stop ubi-docker`, or remove it with `sbx rm ubi-docker`.

## Engine startup and compatibility

Installing Docker packages does not start a daemon under `sbx`, which
does not boot the image's systemd services. The runtime starts the
engine separately from the user's bash entrypoint.

The descriptor requests `dockerd@1` as optional for runtimes such as
sbx 0.50.1 that predate the type. The image also carries
`com.docker.sandboxes.start-docker=true`, and the descriptor requests
`privileged@1` for that fallback. A runtime that honors neither the
capability nor the label will not start an engine.

When `dockerd@1` is granted, `/var/lib/docker` is private persistent
storage across stop/start and recreation. The legacy fallback does not
promise recreation persistence. Removing the sandbox deletes its engine
data. Do not add a separate volume at `/var/lib/docker` alongside
`dockerd@1`; that is a conflicting storage request.

The frontend currently derives package provides only from dpkg and apk
databases. This RPM workload publishes no derived package provides;
mixins that require `deb/` or `apk/` packages will not resolve against it.

The runtime egress list covers UBI packages, Docker's package repository
and Docker Hub. Builds that download from other sites need those hosts
added to the policy. Building this Kit uses the builder's network access.

## Proxy certificate trust

sbx injects its proxy CA under `/usr/local/share/ca-certificates` and sets
TLS variables such as `NODE_EXTRA_CA_CERTS` to
`/etc/ssl/certs/ca-certificates.crt`. UBI uses `/etc/pki` instead. The
recipe links the injected-certificate directory to UBI's native anchors
and exposes its CA bundle at the path the runtime expects. An
`update-ca-certificates` adapter runs UBI's `update-ca-trust extract`.

This lets Node-based tools, including agent mixins, trust the proxy while
retaining certificate verification. Rebuild the Kit and recreate an
existing sandbox to pick up these image changes.

## Build and validate

```sh
task kit:build KIT=ubi-docker -- --builder kit-builder \
  --output type=oci,dest=/tmp/ubi-docker-layout,tar=false
task tck:kit:layout DIR=/tmp/ubi-docker-layout TAG=9.6
```
