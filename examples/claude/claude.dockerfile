# v2's sandbox.image, as content. The template carries the platform floor
# (bash, agent user, git, CA store) and a claude install; the build
# re-pins claude to the release this kit publishes, so the provide cannot
# claim a version the image does not ship.
#
# Anthropic's install.sh downloads this same binary and then runs
# `claude install` to wire the symlink. That second step is a Bun runtime,
# which aborts under QEMU when cross-building arm64, so the kit fetches
# the platform binary directly into the layout install.sh would have left.
FROM dhi.io/sbx-templates:claude-code-docker
ARG CLAUDE_VERSION
ARG TARGETARCH
USER root
RUN case "$TARGETARCH" in \
      amd64) platform=linux-x64 ;; \
      arm64) platform=linux-arm64 ;; \
      *) echo "unsupported TARGETARCH: $TARGETARCH" >&2; exit 1 ;; \
    esac \
 && mkdir -p /home/agent/.local/share/claude/versions /home/agent/.local/bin \
 && curl -fsSL "https://downloads.claude.ai/claude-code-releases/${CLAUDE_VERSION}/${platform}/claude" \
      -o "/home/agent/.local/share/claude/versions/${CLAUDE_VERSION}" \
 && chmod 0755 "/home/agent/.local/share/claude/versions/${CLAUDE_VERSION}" \
 && ln -sfn "/home/agent/.local/share/claude/versions/${CLAUDE_VERSION}" /home/agent/.local/bin/claude \
 && chown -R agent:agent /home/agent/.local

# agent-sessions list reads interactive sessions through the Agent SDK.
# The SDK's patch tracks Claude Code's (2.1.N → 0.3.N). --omit=optional
# drops the vendored native binary so this kit keeps one claude (above).
# The script lives beside node_modules so ESM resolution finds the package
# without NODE_PATH (which ESM ignores).
RUN sdk_version="0.3.${CLAUDE_VERSION##*.}" \
 && mkdir -p /opt/claude-sessions \
 && npm install --omit=optional --no-fund --no-audit --cache /tmp/npm-cache \
      --prefix /opt/claude-sessions \
      "@anthropic-ai/claude-agent-sdk@${sdk_version}" \
 && rm -rf /tmp/npm-cache /root/.npm
COPY scripts/interactive-sessions.mjs /opt/claude-sessions/interactive-sessions.mjs
RUN chmod 0755 /opt/claude-sessions/interactive-sessions.mjs

# Lifecycle hook the settings seed registers for Stop/Notification: it
# reports the event to sbx Desktop through the MCP gateway and is a
# silent no-op when no desktop server is loaded. See the script header.
COPY scripts/sbx-agent-hook.sh /usr/local/bin/sbx-claude-hook
RUN chmod 0755 /usr/local/bin/sbx-claude-hook

# v2's environment.variables, in the slot OCI already owns for static env.
ENV IS_SANDBOX=1
USER agent
WORKDIR /home/agent/workspace
# v2's sandbox.entrypoint.
ENTRYPOINT ["claude", "--dangerously-skip-permissions"]
