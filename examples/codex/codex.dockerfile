# v2's sandbox.image, as content. The template carries the platform floor
# and a codex install; the build replaces that codex with the release this
# kit publishes, so the provide cannot claim a version the image does not
# ship.
#
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
FROM dhi.io/debian-base:trixie-dev AS codex
ARG CODEX_VERSION
ARG TARGETARCH
RUN apt-get update && apt-get install -y --no-install-recommends curl ca-certificates
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
 # Release tarballs carry the CI runner's uid, which may be a real account
 # in the template, so the tree goes to root.
 && chown -R 0:0 /out \
 && grep -q "\"version\": \"${CODEX_VERSION}\"" /out/opt/codex/codex-package.json \
 && /out/opt/codex/bin/codex --version | grep -q "${CODEX_VERSION}"

FROM dhi.io/sbx-templates:codex-docker
ARG CODEX_VERSION
# The template's codex is an npm global, and its npm bin directory comes
# before /usr/local/bin on PATH, so leaving it would shadow the pinned
# package. Uninstalled as agent, not root: the template hands the npm
# prefix to the agent so it can add globals at run time, and a root-run
# npm would take that back for every path it touches.
RUN npm uninstall -g --no-fund --no-audit @openai/codex \
 && ! test -e /usr/local/share/npm-global/bin/codex
USER root
COPY --from=codex /out/ /
# `notify` hook the config seed points Codex at: it reports a completed
# turn to Narwhal through the MCP gateway and is a silent no-op when
# no desktop server is loaded.
COPY scripts/sbx-agent-hook.sh /usr/local/bin/sbx-codex-hook
RUN chmod 0755 /usr/local/bin/sbx-codex-hook \
 # The pin is a claim about content: judge it the way the entrypoint
 # will, through PATH. Every codex start, --version included, writes
 # $CODEX_HOME/tmp; the template sets HOME=/home/agent, so without a
 # scratch CODEX_HOME this root-run check would leave a root-owned
 # ~/.codex that the agent's config seed cannot write into.
 && test "$(command -v codex)" = /usr/local/bin/codex \
 && scratch=$(mktemp -d) \
 && CODEX_HOME="$scratch" codex --version | grep -q "${CODEX_VERSION}" \
 && rm -rf "$scratch" \
 && ! test -e /home/agent/.codex
USER agent
# v2's environment.variables, in the slot OCI already owns for static env.
ENV IS_SANDBOX=1 BROWSER=xdg-open CODEX_HOME=/home/agent/.codex GIT_TERMINAL_PROMPT=0
WORKDIR /home/agent/workspace
# v2's sandbox.entrypoint.
ENTRYPOINT ["codex", "--dangerously-bypass-approvals-and-sandbox"]
