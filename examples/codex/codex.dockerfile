# v2's sandbox.image, as content. The template carries the platform floor
# and a codex install; the build re-pins codex to the release this kit
# publishes, so the provide cannot claim a version the image does not ship.
#
# The re-pin goes through npm, the way the template installed it, and not
# by dropping the release tarball's binary into /usr/local/bin. Codex
# starts a background server by default and, to do so, has to be running
# from what it calls a complete local package: the npm layout, with the
# codex.js launcher and a platform package carrying vendor/<target>/bin/
# (codex, codex-code-mode-host) and codex-resources/ (bwrap, zsh, rg, the
# voice libraries). A lone binary has none of that beside it and exits
# with "this CLI has no complete local package". The platform package is
# an optional dependency npm selects for the stage's own architecture,
# which under a multi-platform build is the target's.
FROM dhi.io/sbx-templates:codex-docker
ARG CODEX_VERSION
# As agent, not root: the template hands the npm prefix to the agent so
# it can add globals at run time, and a root-run install would take that
# back for every path it touches. The cache lands outside the image.
#
# `notify` hook the config seed points Codex at: it reports a completed
# turn to sbx Desktop through the MCP gateway and is a silent no-op when
# no desktop server is loaded. The template already runs as agent, so the
# root-owned install needs an explicit root stage.
USER root
COPY scripts/sbx-agent-hook.sh /usr/local/bin/sbx-agent-hook
RUN chmod 0755 /usr/local/bin/sbx-agent-hook
USER agent
# Installing over the template's existing platform package can leave npm
# extracting into directories it just removed, losing the native binary.
# Remove that package first so the pin is a clean install on every base.
RUN npm uninstall -g --no-fund --no-audit @openai/codex \
 && npm install -g --no-fund --no-audit --cache /tmp/npm-cache \
      "@openai/codex@${CODEX_VERSION}" \
 && rm -rf /tmp/npm-cache \
 # The pin is a claim about content: judge it the way the entrypoint
 # will, through PATH, and make sure the platform package came along.
 && test "$(command -v codex)" = /usr/local/share/npm-global/bin/codex \
 && codex --version | grep -q "${CODEX_VERSION}" \
 && test -x /usr/local/share/npm-global/lib/node_modules/@openai/codex/node_modules/@openai/codex-linux-*/vendor/*/bin/codex-code-mode-host
# v2's environment.variables, in the slot OCI already owns for static env.
ENV IS_SANDBOX=1 BROWSER=xdg-open CODEX_HOME=/home/agent/.codex GIT_TERMINAL_PROMPT=0
WORKDIR /home/agent/workspace
# v2's sandbox.entrypoint.
ENTRYPOINT ["codex", "--dangerously-bypass-approvals-and-sandbox"]
