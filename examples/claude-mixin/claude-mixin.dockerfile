# syntax=docker/dockerfile:1
FROM dhi.io/debian-base:trixie-dev AS build
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

# The mixin owns no session control surface, so it carries only the
# Claude binary and Desktop hook; the workload carries the listing SDK.
COPY scripts/sbx-agent-hook.sh /out/usr/local/bin/sbx-agent-hook
RUN chmod 0755 /out/usr/local/bin/sbx-agent-hook && chown 0:0 /out/usr/local/bin/sbx-agent-hook
FROM scratch
COPY --from=build /out /
