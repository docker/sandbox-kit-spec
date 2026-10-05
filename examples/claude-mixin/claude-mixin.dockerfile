# syntax=docker/dockerfile:1
# Claude Code binary plus the Agent SDK helper that agent-sessions list
# runs. The shell base ships no Node, so the overlay carries node and the
# SDK the same way the claude-acp mixin does. dhi.io/node:24-debian13-dev
# is the hardened Node image: debian13 is trixie, matching the glibc floor
# of the bases these examples compose onto, and the -dev variant carries
# npm.
FROM dhi.io/node:24-debian13-dev AS sdk
ARG CLAUDE_VERSION
# The SDK's patch tracks Claude Code's: 2.1.N → 0.3.N. --omit=optional
# drops the vendored native binary — CLAUDE_CODE_EXECUTABLE / PATH point
# at the claude this kit installs, and a second copy would diverge.
RUN sdk_version="0.3.${CLAUDE_VERSION##*.}" \
 && mkdir -p /opt/claude-sessions \
 && npm install --omit=optional --no-fund --no-audit --cache /tmp/npm-cache \
      --prefix /opt/claude-sessions \
      "@anthropic-ai/claude-agent-sdk@${sdk_version}" \
 && rm -rf /tmp/npm-cache /root/.npm

FROM dhi.io/debian-base:trixie-dev AS claude
ARG CLAUDE_VERSION
ARG TARGETARCH
RUN apt-get update && apt-get install -y --no-install-recommends curl ca-certificates
# Anthropic publishes a standalone native binary per platform. Fetch it
# directly: the install.sh path downloads the same file and then runs
# `claude install` (Bun), which aborts under QEMU on arm64 cross-builds.
RUN case "$TARGETARCH" in \
      amd64) platform=linux-x64 ;; \
      arm64) platform=linux-arm64 ;; \
      *) echo "unsupported TARGETARCH: $TARGETARCH" >&2; exit 1 ;; \
    esac \
 && mkdir -p /out/usr/local/bin \
 && curl -fsSL "https://downloads.claude.ai/claude-code-releases/${CLAUDE_VERSION}/${platform}/claude" \
      -o /out/usr/local/bin/claude \
 && chmod 0755 /out/usr/local/bin/claude

# The final stage is the diff base, never exported content: the overlay
# is exactly the delta over dhi.io/debian-base:trixie-dev (claude, node,
# the SDK tree, the list script). Node is dynamically linked and the
# overlay carries no libc — it composes onto bases whose glibc is at
# least trixie's, which every base workload in these examples satisfies.
FROM dhi.io/debian-base:trixie-dev
COPY --from=claude /out /
COPY --from=sdk /usr/bin/node /usr/local/bin/node
COPY --from=sdk /opt/claude-sessions /opt/claude-sessions
COPY scripts/interactive-sessions.mjs /opt/claude-sessions/interactive-sessions.mjs
RUN chmod 0755 /opt/claude-sessions/interactive-sessions.mjs
# Lifecycle hook the settings seed registers for Stop/Notification: it
# reports the event to sbx Desktop through the MCP gateway and is a
# silent no-op when no desktop server is loaded. See the script header.
COPY scripts/sbx-agent-hook.sh /usr/local/bin/sbx-agent-hook
RUN chmod 0755 /usr/local/bin/sbx-agent-hook
