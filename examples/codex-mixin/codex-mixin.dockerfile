# syntax=docker/dockerfile:1
FROM dhi.io/debian-base:trixie-dev AS build
ARG CODEX_VERSION
ARG TARGETARCH
RUN apt-get update && apt-get install -y --no-install-recommends curl ca-certificates
# Codex is carried as the platform half of its npm package: a
# vendor/<triple>/ tree with bin/codex and bin/codex-code-mode-host beside
# codex-resources/ (bwrap, zsh, rg, the voice libraries). That layout is
# what the CLI calls a complete local package, and it will not start its
# background server — which the interactive TUI uses by default — from a
# binary without it: "this CLI has no complete local package". The GitHub
# release tarball is exactly such a bare binary, so the overlay ships the
# package tree instead, fetched from the npm registry as a plain tarball
# (no node needed here or on the base), and puts symlinks on PATH. The
# binary finds its siblings through its own resolved path, so a link is
# enough; codex spawns codex-code-mode-host that way for Code Mode.
RUN case "$TARGETARCH" in \
      amd64) npmarch=x64;   target=x86_64-unknown-linux-musl ;; \
      arm64) npmarch=arm64; target=aarch64-unknown-linux-musl ;; \
      *) echo "unsupported TARGETARCH: $TARGETARCH" >&2; exit 1 ;; \
    esac \
 && mkdir -p /out/opt/codex /out/usr/local/bin \
 && curl -fsSL "https://registry.npmjs.org/@openai/codex/-/codex-${CODEX_VERSION}-linux-${npmarch}.tgz" \
      | tar -xz -C /out/opt/codex --strip-components=1 \
 && for bin in codex codex-code-mode-host; do \
      test -x "/out/opt/codex/vendor/${target}/bin/${bin}" \
      && ln -s "/opt/codex/vendor/${target}/bin/${bin}" "/out/usr/local/bin/${bin}"; \
    done \
 # npm tarballs preserve the packer's uid; as image content on an unknown
 # base that uid may be a real account, so the tree is handed to root.
 && chown -R 0:0 /out \
 # The pin is a claim about content: re-read it from the tree that ships.
 && "/out/opt/codex/vendor/${target}/bin/codex" --version | grep -q "${CODEX_VERSION}"

# The v2 kit's environment.variables block has no v3 field; the image
# config owns runtime env. An ENV on the final stage would reach the
# composed image — assembly merges a mixin's env (SPEC-v3 §10) — but a
# merged key is first-writer-owned, so a base setting BROWSER to anything
# else would refuse the composition. These exports ride the overlay
# instead, sourced by the base workload's login shell, where they apply
# over the base's values; the cost is that a process started outside a
# login shell does not see them. GIT_TERMINAL_PROMPT=0 keeps git from
# hanging on an interactive credential prompt in a headless sandbox.
RUN mkdir -p /out/etc/profile.d && cat > /out/etc/profile.d/codex-env.sh <<'EOF'
export BROWSER=xdg-open
export IS_SANDBOX=1
export GIT_TERMINAL_PROMPT=0
EOF

# The overlay: the package tree under /opt/codex, its links on PATH, and
# the profile.d exports, landing on any base.
FROM scratch
COPY --from=build /out /
# ACP starts Codex directly, without sourcing a login profile.
ENV CODEX_HOME=/home/agent/.codex
