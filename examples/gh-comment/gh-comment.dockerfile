# syntax=docker/sandbox-kit:3
#
# The gh kit in the comment-descriptor form (SPEC-v3 §3): one valid
# Dockerfile whose declarations ride the `# kit:` block below, so the file
# is both the descriptor and the content recipe. examples/gh is the same
# kit as a companion pair; the two share the nixpkgs pin and its provide,
# and a bump moves both.
#
# kit:
#   schemaVersion: "3"
#   displayName: GitHub CLI
#   description: gh from nixpkgs, pinned by commit, as a self-contained overlay
#   sourceUrl: https://github.com/cli/cli
#   licenses: [MIT]
#
#   kind: mixin
#
#   # The nixpkgs commit below is the version authority: gh's version
#   # follows from it, a bump is editing the pin, and this kit has no
#   # version arg at all.
#   provides: ["gh@2.103.2"]
#
#   capabilities:
#     - type: com.docker.sandbox/network-policy@2
#       config:
#         runtime:
#           allow:
#             - github.com
#             - hosts: [api.github.com]
#               methods: [GET, HEAD, POST, PATCH, PUT, DELETE]
#             - hosts: [uploads.github.com]
#               methods: [POST, PUT]
#           deny:
#             # Deletes under the repository tree stay out of the injected
#             # token's reach; deny wins over the verb granted above.
#             - hosts: [api.github.com]
#               methods: [DELETE]
#               paths: [/repos/**]
#
#     - type: com.docker.sandbox/credential@1
#       description: GitHub API access for gh
#       optional: true
#       config:
#         service: github
#         phase: [runtime]
#         apiKey:
#           name: GH_TOKEN
#           proxyManaged: true
#           inject:
#             - {domain: api.github.com, header: Authorization, format: "Bearer %s"}
#             - {domain: uploads.github.com, header: Authorization, format: "Bearer %s"}
#             - {domain: github.com, header: Authorization, format: "Bearer %s"}
#
#     # Offer both versions during the runtime rollout; select one.
#     - type: com.docker.sandbox/agent-context@1
#       optional: true
#       config: &agent-context
#         content: |
#           # GitHub CLI
#
#           `gh` is on PATH at `/usr/local/bin/gh`.
#
#           - Authentication is proxy-managed: `GH_TOKEN` is set to a sentinel and the
#             real credential is injected on requests to github.com, api.github.com,
#             and uploads.github.com. Never print or copy the token; it is not the
#             real value anyway.
#           - Do not run `gh auth login` — authentication already works.
#           - Prefer `gh` over raw git for GitHub operations: `gh pr create`,
#             `gh issue list`, `gh run watch`, `gh api` for anything without a
#             dedicated subcommand.
#
#     - type: com.docker.sandbox/agent-context@2
#       optional: true
#       config: *agent-context

# The second stanza names the content's own frontend; everything from it
# down is what the kit frontend hands on as the recipe.
# syntax=docker/dockerfile:1
# Nix is the version authority for this kit (see the pinned input below),
# and the hardened catalog carries no nix toolchain, so this build stage
# stays on the upstream image. Nothing from it reaches the overlay: the
# final stage is scratch and copies only the closure nix produced.
# Nix crashes under amd64 emulation on arm64 builders. Evaluate natively
# and select the target package explicitly; its cached closure carries
# the target architecture without running target binaries here.
FROM --platform=$BUILDPLATFORM nixos/nix:2.35.2 AS build
ARG TARGETARCH
WORKDIR /src
# Pin a full commit hash — a branch name would float. This one is the
# nixos-unstable branch head at the time of writing.
COPY <<'EOF' flake.nix
{
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/31ecbaca1bdefa45dffc409f0250bd8039ea0010";
  outputs = { self, nixpkgs }:
    let
      forAll = f: nixpkgs.lib.genAttrs [ "x86_64-linux" "aarch64-linux" ]
        (system: f nixpkgs.legacyPackages.${system});
    in {
      packages = forAll (pkgs: { default = pkgs.gh; });
    };
}
EOF
# A builder behind a TLS proxy can supply its CA as the optional nix-ca
# secret. It stays in this step and out of the published closure.
RUN --mount=type=secret,id=nix-ca \
    if [ -f /run/secrets/nix-ca ]; then \
      export NIX_SSL_CERT_FILE=/run/secrets/nix-ca; \
    fi \
 && case "$TARGETARCH" in \
      amd64) system=x86_64-linux ;; \
      arm64) system=aarch64-linux ;; \
      *) echo "unsupported TARGETARCH: $TARGETARCH" >&2; exit 1 ;; \
    esac \
 && nix --extra-experimental-features 'nix-command flakes' build \
      ".#packages.${system}.default"
RUN mkdir -p /out/nix/store /out/usr/local/bin \
 && cp -a $(nix-store --query --requisites result) /out/nix/store/ \
 && ln -s "$(readlink -f result)/bin/gh" /out/usr/local/bin/gh

# The overlay: gh plus its pinned closure, landing on any base.
FROM scratch
COPY --from=build /out /
ENTRYPOINT ["gh"]
CMD ["--help"]
