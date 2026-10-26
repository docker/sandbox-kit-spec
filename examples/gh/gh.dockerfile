# syntax=docker/dockerfile:1
# Nix is the version authority for this kit (see the pinned input below),
# and the hardened catalog carries no nix toolchain, so this build stage
# stays on the upstream image. Nothing from it reaches the overlay: the
# final stage is scratch and copies only the closure nix produced.
FROM nixos/nix:2.35.2 AS build
WORKDIR /src
# The pinned commit in the input URL is the version authority, and the
# descriptor's `provides` reports what it resolves to. Pin a full commit
# hash — a branch name would float. This one is the nixos-unstable
# branch head at the time of writing.
COPY <<'EOF' flake.nix
{
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/3ec636362e1816bc2f0888fa0fa2c69f1bc9e504";
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
