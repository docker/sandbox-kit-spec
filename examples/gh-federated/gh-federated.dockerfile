# syntax=docker/dockerfile:1
# Nix is the version authority for gh, as in the plain gh kit; curl rides
# in the same closure so the helpers need nothing from the base. Nothing
# from the build stage reaches the overlay but the closure.
FROM --platform=$BUILDPLATFORM nixos/nix:2.35.2 AS build
ARG TARGETARCH
WORKDIR /src
# The same nixos-unstable head the gh kit pins; `provides` reports the gh
# version it resolves to.
COPY <<'EOF' flake.nix
{
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/31ecbaca1bdefa45dffc409f0250bd8039ea0010";
  outputs = { self, nixpkgs }:
    let
      forAll = f: nixpkgs.lib.genAttrs [ "x86_64-linux" "aarch64-linux" ]
        (system: f nixpkgs.legacyPackages.${system});
    in {
      packages = forAll (pkgs: {
        default = pkgs.buildEnv { name = "gh-federated-env"; paths = [ pkgs.gh pkgs.curl ]; };
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
# The real gh and curl stay under libexec: /usr/local/bin/gh is the
# wrapper that exchanges first. /etc/sbx/github receives the descriptor's
# lifecycle file, which the runtime writes as the agent user.
RUN mkdir -p /out/nix/store /out/usr/local/bin /out/usr/local/libexec/sbx-gh /out/etc/sbx \
 && install -d -o 1000 -g 1000 /out/etc/sbx/github \
 && cp -a $(nix-store --query --requisites result) /out/nix/store/ \
 && ln -s "$(readlink -f result/bin/gh)" /out/usr/local/libexec/sbx-gh/gh \
 && ln -s "$(readlink -f result/bin/curl)" /out/usr/local/libexec/sbx-gh/curl

# The overlay: gh, curl, and the exchange helpers, landing on any base.
FROM scratch
COPY --from=build /out /

# sbx-gh-token prints a GitHub installation token for one owner/repo
# scope, exchanging the sandbox's workload token with Octo STS and caching
# the result per scope until shortly before it expires. The cache lives in
# a directory only this user can read; the installation token is the
# credential the Trust Policy granted, so it is handled like one.
COPY --chmod=755 <<'EOF' /usr/local/bin/sbx-gh-token
#!/bin/sh
set -eu
. /etc/sbx/github/sts.env
scope="${1:-${SBX_GH_SCOPE:-}}"
if [ -z "$scope" ]; then
  echo "sbx-gh-token: no repository scope; pass owner/repo, or set the kit's scope arg" >&2
  exit 1
fi
cache="${XDG_RUNTIME_DIR:-/tmp}/sbx-gh-$(id -u)"
mkdir -p "$cache" && chmod 700 "$cache"
entry="$cache/$(printf '%s' "$scope" | tr '/' '_')"
now=$(date +%s)
if [ -f "$entry" ]; then
  read -r expires token < "$entry" || true
  if [ -n "${token:-}" ] && [ "${expires:-0}" -gt $((now + 300)) ]; then
    printf '%s\n' "$token"
    exit 0
  fi
fi
curl=/usr/local/libexec/sbx-gh/curl
ca=/etc/ssl/certs/ca-certificates.crt
workload="$($curl -fsS --cacert "$ca" "https://${SBX_GH_LEDGER}/workload/token?name=github" \
  | sed -n 's/.*"value":"\([^"]*\)".*/\1/p')"
if [ -z "$workload" ]; then
  echo "sbx-gh-token: no workload token from ${SBX_GH_LEDGER}; is the sandbox identity registered?" >&2
  exit 1
fi
response="$($curl -sS --cacert "$ca" -w '\n%{http_code}' -H "Authorization: Bearer ${workload}" \
  "https://${SBX_GH_STS}/sts/exchange?scope=${scope}&identity=${SBX_GH_IDENTITY}")"
status="$(printf '%s\n' "$response" | tail -n 1)"
body="$(printf '%s\n' "$response" | sed '$d')"
if [ "$status" != "200" ]; then
  echo "sbx-gh-token: Octo STS refused the exchange for ${scope} (identity ${SBX_GH_IDENTITY}): HTTP ${status} ${body}" >&2
  exit 1
fi
token="$(printf '%s' "$body" | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')"
if [ -z "$token" ]; then
  echo "sbx-gh-token: Octo STS returned no token: ${body}" >&2
  exit 1
fi
# GitHub App installation tokens live one hour; the response's expiry
# is authoritative when it parses, the hour is the floor otherwise.
expiry="$(printf '%s' "$body" | sed -n 's/.*"expiry":"\([^"]*\)".*/\1/p')"
expires=$(date -u -d "$expiry" +%s 2>/dev/null || echo $((now + 3600)))
umask 077
printf '%s %s\n' "$expires" "$token" > "$entry.tmp" && mv "$entry.tmp" "$entry"
printf '%s\n' "$token"
EOF

# The gh wrapper: resolve the repository the way gh will (--repo, GH_REPO,
# then the working tree's origin), exchange for it, and run gh with the
# installation token. GH_TOKEN is set for this process only.
COPY --chmod=755 <<'EOF' /usr/local/bin/gh
#!/bin/sh
set -eu
scope="${GH_REPO:-}"
prev=""
for arg in "$@"; do
  case "$prev" in
    -R|--repo) scope="$arg" ;;
  esac
  case "$arg" in
    --repo=*) scope="${arg#--repo=}" ;;
    -R*) [ ${#arg} -gt 2 ] && scope="${arg#-R}" ;;
  esac
  prev="$arg"
done
case "$scope" in
  https://github.com/*) scope="${scope#https://github.com/}" ;;
  github.com/*) scope="${scope#github.com/}" ;;
esac
scope="${scope%.git}"
if [ -z "$scope" ]; then
  url="$(git remote get-url origin 2>/dev/null || true)"
  case "$url" in
    https://github.com/*) scope="${url#https://github.com/}" ;;
    git@github.com:*) scope="${url#git@github.com:}" ;;
    ssh://git@github.com/*) scope="${url#ssh://git@github.com/}" ;;
  esac
  scope="${scope%.git}"
fi
if [ -z "$scope" ]; then
  # Outside a repository gh has no target of its own; the kit's default
  # scope becomes the one gh addresses as well as the one exchanged for.
  . /etc/sbx/github/sts.env
  scope="${SBX_GH_SCOPE:-}"
  [ -z "$scope" ] || export GH_REPO="$scope"
fi
if [ -n "$scope" ]; then
  GH_TOKEN="$(sbx-gh-token "$scope")"
  export GH_TOKEN
  unset GITHUB_TOKEN
fi
exec /usr/local/libexec/sbx-gh/gh "$@"
EOF

# The git credential helper. useHttpPath makes git hand over the
# repository path, so the exchange is scoped to the remote being pushed
# to. Only `get` answers; git's store and erase are no-ops because the
# cache is keyed by scope and expiry, not by git's view of success.
COPY --chmod=755 <<'EOF' /usr/local/bin/git-credential-sbx-sts
#!/bin/sh
set -eu
[ "${1:-}" = "get" ] || exit 0
host="" path=""
while IFS='=' read -r key value; do
  case "$key" in
    host) host="$value" ;;
    path) path="$value" ;;
    "") break ;;
  esac
done
[ "$host" = "github.com" ] || exit 0
scope="${path%.git}"
scope="${scope#/}"
case "$scope" in
  */*) scope="$(printf '%s' "$scope" | cut -d/ -f1-2)" ;;
  *) exit 0 ;;
esac
token="$(sbx-gh-token "$scope")" || exit 0
printf 'username=x-access-token\npassword=%s\n' "$token"
EOF

# Revoke every cached installation token at GitHub and forget it.
COPY --chmod=755 <<'EOF' /usr/local/bin/sbx-gh-revoke
#!/bin/sh
set -eu
cache="${XDG_RUNTIME_DIR:-/tmp}/sbx-gh-$(id -u)"
[ -d "$cache" ] || exit 0
for entry in "$cache"/*; do
  [ -f "$entry" ] || continue
  read -r _ token < "$entry" || true
  if [ -n "${token:-}" ]; then
    /usr/local/libexec/sbx-gh/curl -sS -o /dev/null --cacert /etc/ssl/certs/ca-certificates.crt \
      -X DELETE -H "Authorization: Bearer ${token}" https://api.github.com/installation/token || true
  fi
  rm -f "$entry"
done
EOF

ENTRYPOINT ["gh"]
CMD ["--help"]
