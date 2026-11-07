# Inspecting a Kit's source

A Kit can advertise its source repository through the existing optional
`sourceUrl` descriptor field. The frontend copies that value to the
standard OCI manifest annotation `org.opencontainers.image.source`.
No additional descriptor field is needed. A registry's web page may not
display the annotation, so a missing link on Docker Hub does not by itself
mean the published Kit has no source URL.

## Find the declared source URL

Use `kit-tck inspect` to read the published descriptor:

```sh
kit-tck inspect docker.io/sbx/neovim:latest --descriptor
```

Look for `sourceUrl`. This reads manifest metadata without downloading
the Kit's layers. To inspect a particular platform, add
`--platform linux/amd64`. See the [README](../README.md#conformance) for
the conformance tools and their other inspection options.

Generic registry tools can read the source annotation too. For example,
with [regctl](https://regclient.org/cli/regctl/) and `jq` installed:

```sh
regctl manifest get docker.io/sbx/neovim:latest \
  --platform linux/amd64 --format raw-body |
  jq -r '.annotations["org.opencontainers.image.source"] // "No source URL declared"'
```

Selecting a platform matters: a multi-platform image index can omit
annotations even when its platform manifests carry them.

`sourceUrl` is optional and can link to a source repository or
documentation. Some Kits link to the upstream tool's project rather than
the repository containing the Kit's Dockerfile and helper scripts. If the
field is absent or points only to the upstream project, it does not
establish where the Kit's build sources live. There is no general mapping
from a registry namespace to a Git repository; ask the publisher for that
link.

## Read the recipe carried by the artifact

Kits stage their published descriptor and content recipe in their own
filesystem. `kit-tck inspect` can download and print the recipe without
running the Kit:

```sh
kit-tck inspect docker.io/sbx/neovim:latest --dockerfile
```

Without `--descriptor` or `--dockerfile`, it prints both. Inside a sandbox
that composes the Kit, the files are under
`/usr/share/sandbox/kit/<stem>/kit.yaml` and `kit.dockerfile`.
Declaration-only Kits and merged sets have no content recipe of their
own; older or nonconforming artifacts may not stage one.

The staged recipe does not necessarily include every build input. For
example, a Dockerfile's `COPY` can refer to helper scripts that the build
does not preserve as source files. The publisher's repository is the
place to inspect those inputs.

## Publish a useful source link

To help people inspect how a Kit is built, set `sourceUrl` to the
directory containing its descriptor, Dockerfile, and helper scripts:

```yaml
sourceUrl: https://github.com/example/kits/tree/v1.0.0/neovim
```

For a repository containing several Kits, a directory link is more useful
than its root. Prefer a full commit ID in the URL when publishing a
specific build, so the link continues to show the same source after a
branch or tag moves. Link upstream product documentation separately in
the Kit's README. Include the build-source link in the registry README
too, so people browsing Docker Hub can find it without a CLI.

The field and its OCI annotation are described in
[SPEC-v3 §4](spec/SPEC-v3.md#4-top-level-fields) and
[§9.3](spec/SPEC-v3.md#93-annotations). Updating the descriptor takes a new
build and publication to update the artifact's annotation.

## Distinguish a source link from build provenance

A source URL is a publisher's claim. Even a commit-specific link does
not prove that an artifact was built from that commit. To establish that
relationship, inspect build provenance tied to the artifact's digest and
verify its signature and builder identity against a publisher you trust.
Provenance availability and verification depend on the publishing
workflow; a URL alone cannot supply them.

Use a digest reference such as `docker.io/sbx/neovim@sha256:<digest>` when
assessing a particular artifact: `latest` and other tags can move. The
`vnd.docker.sandbox.kit.built-by` annotation identifies the **frontend**
build, and its revision names the frontend's source commit. It does not
identify the Kit's source revision.
