# Inspecting a Kit's source

A published Kit's build provenance can identify its source repository,
commit, and build directory, even when its Docker Hub page has no source
link. Start with the attestation, then check the optional `sourceUrl`
descriptor field and the recipe carried by the artifact. These are
existing mechanisms; no additional descriptor field is needed.

For a repeatable inspection, use a digest reference such as
`docker.io/sbx/neovim@sha256:<digest>` in place of the tag in the commands
below. `latest` and other tags can move.

## Find the source through build provenance

BuildKit can publish an in-toto attestation with a SLSA provenance
predicate. It can be embedded as an attestation manifest in the image
index, discoverable through OCI referrers, or available through both.
Neither discovery route requires the descriptor's `sourceUrl` field.

### Quick view with Docker Scout

If Docker Scout is installed, its CVE report also summarizes available
provenance information:

```sh
docker scout cves sbx/neovim:latest
```

Look for the source repository and revision in the image details. This
command also performs vulnerability analysis. Use the commands below to
inspect the provenance fields directly or discover referrers. See the
[Docker Scout CVE command reference][scout-cves] for its options.

[scout-cves]: https://docs.docker.com/reference/cli/docker/scout/cves/

### Attestations in the image index

Docker Buildx can read embedded provenance. With `jq` installed, select
the platform and the source fields from the SLSA predicate:

```sh
docker buildx imagetools inspect docker.io/sbx/neovim:latest \
  --format '{{json .Provenance}}' |
  jq '.["linux/amd64"].SLSA | {
    vcs: .runDetails.metadata.buildkit_metadata.vcs,
    configSource: .buildDefinition.externalParameters.configSource
  }'
```

The inspected Neovim artifact records:

```json
{
  "vcs": {
    "localdir:context": "neovim",
    "localdir:dockerfile": "neovim",
    "revision": "317a118e6839dfe5bf23f0fef075c67dd15e71b7",
    "source": "https://github.com/docker/sbx-kits-contrib"
  },
  "configSource": {
    "path": "neovim.yaml"
  }
}
```

That gives the [Kit's build directory at the recorded commit][neovim-source],
including its descriptor, Dockerfile, and helper scripts. Both published
platforms record the same source. This example was read from image index
`sha256:8412632ad7cfaacc2d5ca35010b8e484e74ece55768a4e7c9230ba2b8e212052`.

[neovim-source]: https://github.com/docker/sbx-kits-contrib/tree/317a118e6839dfe5bf23f0fef075c67dd15e71b7/neovim

These paths describe BuildKit's SLSA v1 predicate. Older SLSA v0.2
predicates use `invocation.configSource` and
`metadata["https://mobyproject.org/buildkit@v1#metadata"].vcs` instead.
For Git build contexts, also inspect the source URI and commit in the
predicate's resolved dependencies (`materials` in v0.2). Available fields
depend on the builder and its source inputs; local builds can omit VCS
metadata. For local contexts, the `vcs` values are client-supplied hints;
BuildKit does not verify that the files match the recorded commit. See
[BuildKit's provenance documentation][buildkit-provenance].

[buildkit-provenance]: https://github.com/moby/buildkit/blob/master/docs/attestations/slsa-provenance.md

### Attestations through OCI referrers

With [regctl](https://regclient.org/cli/regctl/), discover artifacts that
refer to the selected platform manifest:

```sh
regctl artifact list docker.io/sbx/neovim:latest --platform linux/amd64
```

Check the platform digest as well as the image index: an attestation can
refer to a platform manifest while the index has no referrers. Neovim's
amd64 attestation is both embedded in the index and discoverable this way.

Download the BuildKit attestation's payloads into an empty directory,
then select the SLSA statement. The same attestation can carry an SBOM,
so selecting by predicate type matters:

```sh
mkdir neovim-attestations
regctl artifact get --subject docker.io/sbx/neovim:latest \
  --platform linux/amd64 \
  --filter-artifact-type application/vnd.docker.attestation.manifest.v1+json \
  --output neovim-attestations
jq 'select(.predicateType | startswith("https://slsa.dev/provenance/")) |
  {subject, predicateType, vcs: .predicate.runDetails.metadata.buildkit_metadata.vcs}' \
  neovim-attestations/*
```

The statement's `subject` identifies the image digest it describes. Other
publishers may use different artifact types or signed envelopes; inspect
the referrers list before choosing a filter or a verification tool.
Finding no provenance in one location does not establish that it is
absent from the other.

## Find the declared source URL

The optional `sourceUrl` descriptor field is another discovery route.
The frontend copies it to `org.opencontainers.image.source`. A registry's
web page may not display that annotation.

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
field is absent or points only to the upstream project, check provenance
for the Kit's build sources. If neither mechanism records them, ask the
publisher for the link. A registry namespace alone does not establish a
Git repository.

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

A source URL is a publisher's claim. Provenance records a build's source
and output relationship, but fetching it does not authenticate that claim.
BuildKit provenance is not automatically signed. Check the statement's
subject against the image digest being assessed, then verify any
signature and builder identity using the publisher's verification
procedure. The discovery commands above do not perform that verification.

The `vnd.docker.sandbox.kit.built-by` annotation identifies the
**frontend** build, and its revision names the frontend's source commit.
It does not identify the Kit's source revision.
