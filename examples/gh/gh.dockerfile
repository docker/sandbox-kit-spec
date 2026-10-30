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
# The pinned commit in the input URL is the version authority, and the
# descriptor's `provides` reports what it resolves to. Pin a full commit
# hash — a branch name would float. This one is the nixos-unstable
# branch head at the time of writing.
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
