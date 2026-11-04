# syntax=docker/dockerfile:1
# gcloud is a Python application and its release tarball bundles an
# interpreter only for x86_64, so nix is the version authority here as it
# is for gh: the pinned nixpkgs commit fixes the gcloud release and the
# closure carries its own Python, landing on any base. Nothing from the
# build stage reaches the overlay but the closure.
FROM --platform=$BUILDPLATFORM nixos/nix:2.35.2 AS build
ARG TARGETARCH
WORKDIR /src
# Pin a full commit hash; the descriptor's `provides` reports the gcloud
# version it resolves to. This is the same nixos-unstable head gh pins.
COPY <<'EOF' flake.nix
{
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/3ec636362e1816bc2f0888fa0fa2c69f1bc9e504";
  outputs = { self, nixpkgs }:
    let
      forAll = f: nixpkgs.lib.genAttrs [ "x86_64-linux" "aarch64-linux" ]
        (system: f nixpkgs.legacyPackages.${system});
    in {
      packages = forAll (pkgs: { default = pkgs.google-cloud-sdk; });
    };
}
EOF
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
# /etc/sbx/gcp receives the descriptor's lifecycle file, which the runtime
# writes as the agent user; the parent stays root's.
RUN mkdir -p /out/nix/store /out/usr/local/bin /out/etc/sbx \
 && install -d -o 1000 -g 1000 /out/etc/sbx/gcp \
 && cp -a $(nix-store --query --requisites result) /out/nix/store/ \
 && for bin in gcloud gsutil bq; do \
      ln -s "$(readlink -f result)/bin/$bin" /out/usr/local/bin/$bin; \
    done

# The overlay: gcloud plus its closure, landing on any base.
FROM scratch
ARG GCP_PROJECT
COPY --from=build /out /
# The credential configuration the descriptor's lifecycle file writes.
# Both gcloud and Application Default Credentials read it, so a Python
# or Go program in the sandbox federates like the CLI does.
ENV GOOGLE_APPLICATION_CREDENTIALS=/etc/sbx/gcp/credentials.json \
    CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE=/etc/sbx/gcp/credentials.json \
    CLOUDSDK_CORE_PROJECT=${GCP_PROJECT} \
    CLOUDSDK_CORE_DISABLE_USAGE_REPORTING=true \
    CLOUDSDK_COMPONENT_MANAGER_DISABLE_UPDATE_CHECK=true
# gcloud ships its own CA bundle and would not trust the sandbox proxy
# that terminates TLS toward the token relay; the system bundle carries
# the public roots and the proxy CA the runtime installs.
ENV CLOUDSDK_CORE_CUSTOM_CA_CERTS_FILE=/etc/ssl/certs/ca-certificates.crt
ENTRYPOINT ["gcloud"]
CMD ["--help"]
