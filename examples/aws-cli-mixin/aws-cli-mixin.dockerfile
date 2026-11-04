# syntax=docker/dockerfile:1
# The AWS CLI is a Python application whose official installer bakes
# absolute paths, so nix is the version authority here as it is for gh:
# the pinned nixpkgs commit fixes the release and the closure carries its
# own Python, landing on any base. curl rides in the same closure so the
# credential process depends on nothing the base may lack but sh and sed.
FROM --platform=$BUILDPLATFORM nixos/nix:2.35.2 AS build
ARG TARGETARCH
WORKDIR /src
# Pin a full commit hash; the descriptor's `provides` reports the awscli2
# version it resolves to. This is the same nixos-unstable head gh pins.
COPY <<'EOF' flake.nix
{
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/3ec636362e1816bc2f0888fa0fa2c69f1bc9e504";
  outputs = { self, nixpkgs }:
    let
      forAll = f: nixpkgs.lib.genAttrs [ "x86_64-linux" "aarch64-linux" ]
        (system: f nixpkgs.legacyPackages.${system});
    in {
      packages = forAll (pkgs: {
        default = pkgs.buildEnv { name = "aws-cli-env"; paths = [ pkgs.awscli2 pkgs.curl ]; };
      });
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
# aws lands on PATH; curl stays under libexec so the base's own curl, when
# it has one, is not shadowed. /etc/sbx/aws receives the descriptor's
# lifecycle file, which the runtime writes as the agent user; the parent
# stays root's.
RUN mkdir -p /out/nix/store /out/usr/local/bin /out/usr/local/libexec/sbx-aws /out/etc/sbx \
 && install -d -o 1000 -g 1000 /out/etc/sbx/aws \
 && cp -a $(nix-store --query --requisites result) /out/nix/store/ \
 && ln -s "$(readlink -f result/bin/aws)" /out/usr/local/bin/aws \
 && ln -s "$(readlink -f result/bin/curl)" /out/usr/local/libexec/sbx-aws/curl

# The overlay: the AWS CLI, curl, and the credential process, landing on
# any base.
FROM scratch
COPY --from=build /out /
# The credential process the descriptor's profile names. It fetches the
# sandbox's workload token from the identity service relay, where the
# sandbox proxy adds the bearer header, and assumes the role with it.
# AssumeRoleWithWebIdentity is unsigned, so the inner aws call runs with
# the profile hidden and signing off: it must not resolve credentials,
# which would recurse into this process. The guard makes a misconfigured
# call fail instead of loop.
COPY --chmod=755 <<'EOF' /usr/local/bin/sbx-aws-credentials
#!/bin/sh
set -eu
role="${1:?usage: sbx-aws-credentials <role-arn> <ledger-host> [region]}"
ledger="${2:?usage: sbx-aws-credentials <role-arn> <ledger-host> [region]}"
region="${3:-${AWS_REGION:-${AWS_DEFAULT_REGION:-us-east-1}}}"
if [ -n "${SBX_AWS_CREDENTIALS_INFLIGHT:-}" ]; then
  echo "sbx-aws-credentials: recursive credential resolution" >&2
  exit 1
fi
export SBX_AWS_CREDENTIALS_INFLIGHT=1
session="sbx-${SANDBOX_ID:-$(hostname)}"
token="$(/usr/local/libexec/sbx-aws/curl -fsS --cacert /etc/ssl/certs/ca-certificates.crt "https://${ledger}/workload/token" \
  | sed -n 's/.*"value":"\([^"]*\)".*/\1/p')"
if [ -z "$token" ]; then
  echo "sbx-aws-credentials: no workload token from ${ledger}; is the sandbox identity registered?" >&2
  exit 1
fi
unset AWS_PROFILE
export AWS_CONFIG_FILE=/dev/null AWS_SHARED_CREDENTIALS_FILE=/dev/null
exec /usr/local/bin/aws --region "$region" --no-sign-request sts assume-role-with-web-identity \
  --role-arn "$role" --role-session-name "$session" --web-identity-token "$token" \
  --duration-seconds 3600 --output json \
  --query '{Version: `1`, AccessKeyId: Credentials.AccessKeyId, SecretAccessKey: Credentials.SecretAccessKey, SessionToken: Credentials.SessionToken, Expiration: Credentials.Expiration}'
EOF
# The profile the descriptor's lifecycle file writes; the CLI and every
# SDK read it, so a Python or Go program in the sandbox assumes the role
# like the CLI does. The system bundle carries the public roots and the
# proxy CA the runtime installs.
ENV AWS_CONFIG_FILE=/etc/sbx/aws/config \
    AWS_CA_BUNDLE=/etc/ssl/certs/ca-certificates.crt \
    AWS_PAGER=
ENTRYPOINT ["aws"]
CMD ["help"]
