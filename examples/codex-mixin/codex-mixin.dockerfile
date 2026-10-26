# syntax=docker/dockerfile:1
FROM dhi.io/debian-base:trixie-dev AS build
ARG CODEX_VERSION
ARG TARGETARCH
RUN apt-get update && apt-get install -y --no-install-recommends curl ca-certificates
# Codex is carried as the complete package its release publishes,
# codex-package-<triple>.tar.gz: bin/codex and bin/codex-code-mode-host
# beside codex-resources/ (bwrap, zsh, the voice libraries), codex-path/rg,
# and the codex-package.json manifest. The CLI will not start its
# background server — which the interactive TUI uses by default — without
# that layout around it ("this CLI has no complete local package"), so the
# bare codex-<triple>.tar.gz binary is not enough. This is the asset
# upstream's install.sh unpacks; the script itself is not run, because it
# installs under $CODEX_HOME, which is the agent's state, not image
# content. The binary locates the package from its own resolved path, so
# one link on PATH is enough.
RUN case "$TARGETARCH" in \
      amd64) target=x86_64-unknown-linux-musl ;; \
      arm64) target=aarch64-unknown-linux-musl ;; \
      *) echo "unsupported TARGETARCH: $TARGETARCH" >&2; exit 1 ;; \
    esac \
 && release="https://github.com/openai/codex/releases/download/rust-v${CODEX_VERSION}" \
 && asset="codex-package-${target}.tar.gz" \
 && mkdir -p /tmp/codex /out/opt/codex /out/usr/local/bin \
 && cd /tmp/codex \
 && curl -fsSLO "${release}/${asset}" \
 && curl -fsSLO "${release}/codex-package_SHA256SUMS" \
 && grep "  ${asset}\$" codex-package_SHA256SUMS | sha256sum -c - \
 && tar -xzf "${asset}" -C /out/opt/codex \
 && ln -s /opt/codex/bin/codex /out/usr/local/bin/codex \
 && rm -rf /tmp/codex \
 # Release tarballs carry the CI runner's uid; as image content on an
 # unknown base that uid may be a real account, so the tree goes to root.
 && chown -R 0:0 /out \
 # The pin is a claim about content: re-read it from the tree that ships,
 # in both the manifest the CLI trusts and the binary itself.
 && grep -q "\"version\": \"${CODEX_VERSION}\"" /out/opt/codex/codex-package.json \
 && /out/opt/codex/bin/codex --version | grep -q "${CODEX_VERSION}"

# `notify` hook the config seed points Codex at: it reports a completed
# turn to Narwhal through the MCP gateway and is a silent no-op when
# no desktop server is loaded. Rides the overlay like the binaries above.
COPY scripts/sbx-agent-hook.sh /out/usr/local/bin/sbx-codex-hook
RUN chmod 0755 /out/usr/local/bin/sbx-codex-hook && chown 0:0 /out/usr/local/bin/sbx-codex-hook

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

# The overlay: the package under /opt/codex, its link on PATH, and
# the profile.d exports, landing on any base.
FROM scratch
COPY --from=build /out /
# ACP starts Codex directly, without sourcing a login profile.
ENV CODEX_HOME=/home/agent/.codex
