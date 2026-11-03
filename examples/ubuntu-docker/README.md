# Ubuntu with Docker

A shell workload built from `ubuntu:24.04`, with Docker Engine, Buildx
and Compose installed from Docker's signed Ubuntu package repository.
It illustrates the custom-image setup discussed in
[sbx-releases#806](https://github.com/docker/sbx-releases/issues/806).
The recipe supplies the runtime tools, CA store and `agent` user at uid
1000 that the sandbox templates normally provide.

## Run

From the repository root, with Docker Buildx and a signed-in `sbx`:

```sh
docker buildx create --name kit-builder --driver docker-container --bootstrap
BUILDX_BUILDER=kit-builder sbx run --name ubuntu-docker ./examples/ubuntu-docker .
```

Reuse the builder if it already exists. The container driver preserves
the Kit's OCI annotations during the local source build. No registry
publication is needed.

Inside the sandbox:

```sh
id
docker version
docker buildx version
docker compose version
docker run --rm hello-world
printf 'FROM busybox:stable\nRUN echo built-in-sandbox\n' | docker build -t smoke -
```

The shell runs as `agent`; passwordless `sudo` is available. The runtime's
normal session-based auto-stop policy applies. Stop it explicitly with
`sbx stop ubuntu-docker`, or remove it with `sbx rm ubuntu-docker`.

## Engine startup and compatibility

Installing Docker packages does not start a daemon under `sbx`: the
runtime launches the workload command separately from its init process,
and does not boot Ubuntu's systemd services. Keep `bash` as the image's
entrypoint and let the runtime start Docker.

The descriptor requests `dockerd@1` as optional because sbx 0.50.1 rejects
that type when it is required. For that runtime the image also carries
`com.docker.sandboxes.start-docker=true`, and the descriptor requests
`privileged@1`. This is the same transition used by the `builder`
example. A runtime that honors neither the capability nor the label
will not start an engine. `privileged@1` alone does not request startup.

On a runtime granting `dockerd@1`, `/var/lib/docker` is private persistent
storage across stop/start and recreation. The legacy fallback does not
promise recreation persistence. Removing the sandbox deletes its engine
data. Do not add a separate volume at `/var/lib/docker` alongside
`dockerd@1`; that is a conflicting storage request.

The runtime egress list covers Ubuntu packages, Docker's package
repository and Docker Hub. Builds that download from other sites need
those hosts added to the policy. This list governs sandbox runtime
traffic; building this Kit uses the builder's own network access.

## Build and validate

```sh
task kit:build KIT=ubuntu-docker -- --builder kit-builder \
  --output type=oci,dest=/tmp/ubuntu-docker-layout,tar=false
task tck:kit:layout DIR=/tmp/ubuntu-docker-layout TAG=1.0.0
```

The Kit release is `1.0.0`; Ubuntu and Docker package versions are read
from the built image's dpkg database. Rebuilding takes current packages
from those repositories rather than claiming a fixed Docker version.
