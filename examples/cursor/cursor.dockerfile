# v2's sandbox.image, as content. The template carries the platform floor
# and the cursor-agent install; the workload publishes it as-is. Cursor's
# installer has no version pin, so the kit's version: field is the semver
# spelling of the cursor-agent this template build carries
# (2026.10.22-27dcf5f is 2026.10.22). The template tag moves on every
# refresh, so the base is pinned by digest and the build asserts the
# agent it carries: otherwise a refresh would publish a different binary
# under an unchanged kit tag. Move the digest, CURSOR_AGENT_BUILD, and
# version: together.
FROM dhi.io/sbx-templates:cursor-agent-docker@sha256:c2fce9d7c903a7421ecc9a4a8ce3498767f99f1ceee2fb8cb181113c94d0e4e8
ARG CURSOR_AGENT_BUILD=2026.10.22-27dcf5f
ARG BUILDARCH
ARG TARGETARCH
# Check the installed release on every target. QEMU cannot map the amd64
# SQLite addon on 16 KiB arm64 hosts, so execute the CLI only on native
# builds. A scratch HOME keeps the version check out of the agent's state.
RUN test "$(readlink -f "$(command -v cursor-agent)")" = \
      "/home/agent/.local/share/cursor-agent/versions/${CURSOR_AGENT_BUILD}/cursor-agent" \
 && if [ "$BUILDARCH" = "$TARGETARCH" ]; then \
      scratch=$(mktemp -d) \
      && got=$(HOME="$scratch" cursor-agent --version) \
      && rm -rf "$scratch" \
      && { [ "$got" = "${CURSOR_AGENT_BUILD}" ] || { echo "cursor-agent is $got, expected ${CURSOR_AGENT_BUILD}" >&2; exit 1; }; }; \
    fi
# v2's environment.variables. AGENT_CLI_CREDENTIAL_STORE=memory keeps
# cursor from validating a file-backed sentinel and re-prompting login;
# auth bootstraps through the environment when host OAuth creds exist.
ENV IS_SANDBOX=1 AGENT_CLI_CREDENTIAL_STORE=memory
# The ACP launcher acp@1 names: cursor-agent's ACP server keeps running
# after stdin closes, so the wrapper follows the close with SIGTERM, which
# it does stop on.
USER root
COPY scripts/acp-graceful.mjs /usr/local/bin/acp-graceful
RUN chmod 0755 /usr/local/bin/acp-graceful
USER agent
WORKDIR /home/agent/workspace
# v2's sandbox.entrypoint.
ENTRYPOINT ["cursor-agent", "--yolo"]
