# Introducing Kits

A Kit packages a piece of a working environment — a tool, an agent, a
service — so that a runtime can install it, grant it what it needs, and
combine it with other Kits without knowing anything about it in advance.

This is the worked tour: a real Kit, what its capabilities ask for, and
how a set composes. The concepts behind it — what a Kit is, the two kinds,
and the tenets that decided the design — are on the
[README](../README.md), and the normative rules are in
[SPEC-v3.md](spec/SPEC-v3.md).

## What a Kit looks like

A Kit is two files: the declarations and the recipe that produces the
content. Here is a mixin that adds the GitHub CLI to whatever workload it
lands on.

```yaml
# gh.yaml
# syntax=docker/sandbox-kit:3
schemaVersion: "3"
displayName: GitHub CLI
description: gh from nixpkgs, pinned by commit, as a self-contained overlay
sourceUrl: https://github.com/cli/cli
licenses: [MIT]

kind: mixin

provides: ["gh@2.72.0"]

capabilities:
  - type: com.docker.sandbox/network-policy@1
    config:
      runtime:
        allow: [github.com, api.github.com, uploads.github.com]

  - type: com.docker.sandbox/credential@1
    optional: true
    description: GitHub API access for gh
    config:
      service: github
      phase: runtime
      apiKey:
        name: GH_TOKEN
        proxyManaged: true
        inject:
          - {domain: api.github.com, header: Authorization, format: "Bearer %s"}

  - type: com.docker.sandbox/agent-context@1
    config:
      contentFile: ./gh-context.md
```

Note what is *absent*: no top-level identity name, no image reference, no
entrypoint, no env. A Kit's identity is the reference you consume it by,
and its runtime contract is the image config — where images already keep
those things. Capability-entry names are display labels, not Kit identity.
`provides` states matchable identity, which is a different question from
"what is this file called".

Decoding is strict: any unrecognized field is an error. A misspelled key
would otherwise be a policy silently absent.

The content is an ordinary Dockerfile, named by the same filename stem. It
builds `gh` from a pinned nixpkgs commit and copies the result — the binary
plus its complete closure — into an empty image:

```dockerfile
# gh.dockerfile
FROM nixos/nix:2.35.2 AS build
WORKDIR /src
COPY <<'EOF' flake.nix
{
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/ac62194c3917d5f474c1a844b6fd6da2db95077d";
  outputs = { self, nixpkgs }:
    let
      forAll = f: nixpkgs.lib.genAttrs [ "x86_64-linux" "aarch64-linux" ]
        (system: f nixpkgs.legacyPackages.${system});
    in {
      packages = forAll (pkgs: { default = pkgs.gh; });
    };
}
EOF
RUN nix --extra-experimental-features 'nix-command flakes' build .
RUN mkdir -p /out/nix/store /out/usr/local/bin \
 && cp -a $(nix-store --query --requisites result) /out/nix/store/ \
 && ln -s "$(readlink -f result)/bin/gh" /out/usr/local/bin/gh

# The overlay: gh plus its pinned closure, landing on any base.
FROM scratch
COPY --from=build /out /
ENTRYPOINT ["gh"]
CMD ["--help"]
```

Three things about this recipe are worth copying. It ends at `FROM scratch`,
so the layers are purely the overlay — nothing from the build stage travels,
and the mixin composes onto any workload without dragging a second base
filesystem behind it. It depends on nothing in that workload: the nix
closure carries every library `gh` needs, so the mixin cannot be broken by
the distribution underneath it.

And it sets an entrypoint despite being an overlay, because a Kit is still
an image: `docker run` on this mixin alone runs `gh --help`. Composition
ignores those fields — the workload anchors the runtime contract — so
declaring them costs nothing and makes the artifact useful on its own.

The pinned commit is also why the descriptor declares no version argument.
That pin is the version authority — bumping `gh` means editing it — and
`provides: ["gh@2.72.0"]` reports the result. Kits that *are* configurable
declare `args`, which can be exposed to the build and expanded into fields
like `provides`; see [§6](spec/SPEC-v3.md#6-args).

## Capabilities: one list for everything the Kit cannot supply

A Kit declares what it needs from its host as typed, versioned requests:

```yaml
capabilities:
  - type: com.docker.sandbox/credential@1
    optional: true
    description: GitHub API access for gh
    config:
      service: github
      phase: runtime
```

Resource grants (volumes, ports, devices) and engine-executed behaviors
(lifecycle hooks, agent context) go through the same list, so a host answers
the whole ask through one mechanism — or refuses the parts it does not
understand. A `required` request that cannot be met fails resolution closed;
an `optional` one is skipped and recorded.

The `@1` names the version of that type's *capability contract*: its config
schema and observable behavior, including composition rules and permitted
transitions against persistent state. Capability types can evolve without
a descriptor grammar bump. Well-known types are strictly decoded and
documented one page each under
[spec/capabilities/](spec/capabilities/com.docker.sandbox); unknown types are
carried opaquely so a host can support its own.

## Composing Kits

You launch a set: one workload plus any number of mixins. A conforming
runtime treats that set as **closed** — every `requires` must be satisfied
from within the set or resolution fails, nothing is fetched implicitly. It
fails on `conflicts`, orders composition by the dependency graph rather than
the order you happened to pass flags in, and insists on exactly one
workload.

Because the result is a pure function of the resolved set, it can be locked,
reproduced, and cached as a single assembled image — and a set worth keeping
can be published as one Kit instead of a command line (below).

## Sharing a whole set

A set you have to retype is not really shareable, so a Kit can take its
content from other Kits instead of from a Dockerfile:

```yaml
# team-claude.yaml
# syntax=docker/sandbox-kit:3
schemaVersion: "3"
kind: set
displayName: Team Claude environment
version: "1.0.0"

kits:
  - ref: docker.io/dockerdev/sbx-kit-shell:1.0.0
  - ref: docker.io/dockerdev/sbx-kit-claude-mixin:2.1.6
    args:
      version: "2.1.6"
  - ref: docker.io/dockerdev/sbx-kit-gh:2.72.0
```

Building this resolves each one, checks the set is coherent, and merges
their layers and declarations into **one ordinary Kit** — so what you
publish is a Kit like any other, and `sbx run <your-set> .` needs no new
machinery to run it. Their network rules union, their hooks concatenate in
dependency order, and their guidance becomes one document. Two of them
asking for incompatible things fails the build rather than picking a
winner.

`kind: set` never reaches a consumer: publishing derives `workload` or
`mixin` from the Kits it lists, because a merged set really is one or the
other. What survives is the `kits:` list, pinned by digest — the record of
how the content was produced, the way an inline `build:` block is.

The trade is worth knowing. A merged set is a *pinned artifact*: one
reference, one digest, one pull, and bumping one of its Kits means
republishing it. A set is also not a way to hide what is inside — each of
their staged sources rides along in the filesystem, and the permission
surface is the union of what they ask for, gated as usual.

## Ways to author one

The descriptor is YAML; the content recipe is ordinary Dockerfile text —
or, for a set, a list of kits. How you connect them is your choice:

1. **Companion pair** — `gh.yaml` beside `gh.dockerfile`, matched by filename
   stem. Dockerfile tooling keeps working on a file that is still just a
   Dockerfile.
2. **Inline `build:` block** — the Dockerfile text embedded in the
   descriptor, so a Kit is a single file.
3. **Comment descriptor** — a Dockerfile carrying its declarations in a
   `# kit:` comment block, so the file is both.
4. **Kit set** — `kits:`, above.

All of them produce ordinary Kit images. A BuildKit frontend, dispatched by
the `# syntax=docker/sandbox-kit:3` line, validates the descriptor, builds
the content, and publishes both as one image.

## Where to go next

- [SPEC-v3.md](spec/SPEC-v3.md) — the normative specification.
- [spec/capabilities/](spec/capabilities/com.docker.sandbox) — one page per
  well-known capability type, normative for runtimes implementing it.
- [`examples/`](../examples) — working Kits, from `hello` (the smallest
  possible workload) through `shell` plus agent mixins.
- [README](../README.md) — building and running Kits locally.
