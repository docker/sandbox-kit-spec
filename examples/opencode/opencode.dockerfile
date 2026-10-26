# syntax=docker/dockerfile:1
# OpenCode's installer executes its native Bun launcher, which fails
# under emulation. Fetch the target binary package with native npm and
# skip install hooks instead. The x64 baseline runs without AVX2.
FROM --platform=$BUILDPLATFORM dhi.io/node:24-debian13-dev AS build
ARG OPENCODE_VERSION
ARG TARGETARCH
RUN case "$TARGETARCH" in \
      amd64) package=opencode-linux-x64-baseline ;; \
      arm64) package=opencode-linux-arm64 ;; \
      *) echo "unsupported TARGETARCH: $TARGETARCH" >&2; exit 1 ;; \
    esac \
 && mkdir -p /tmp/opencode /out/usr/local/bin \
 && cd /tmp/opencode \
 && archive=$(npm pack --ignore-scripts --silent "${package}@${OPENCODE_VERSION}") \
 && node -e 'const fs = require("node:fs"); fs.writeFileSync("opencode.tar", require("node:zlib").gunzipSync(fs.readFileSync(process.argv[1])))' "$archive" \
 && tar -xf opencode.tar \
 && node -e 'if (require("./package/package.json").version !== process.env.OPENCODE_VERSION) throw new Error("OpenCode package version differs from the Kit pin")' \
 && cp package/bin/opencode /out/usr/local/bin/opencode \
 && chmod 0755 /out/usr/local/bin/opencode

# The template supplies the platform floor and agent configuration.
FROM dhi.io/sbx-templates:opencode-docker
USER root
# Remove the template's older package and launcher before copying the
# pinned binary. npm runs with root's HOME so the agent keeps ownership
# of every path below its own home.
RUN export HOME=/root \
 && npm uninstall -g opencode-ai \
 && rm -rf /root/.npm /root/.cache /root/.config /root/.local \
 && ! find /home/agent -uid 0 | grep -q .
COPY --from=build /out/usr/local/bin/opencode /usr/local/bin/opencode
USER agent
WORKDIR /home/agent/workspace
ENTRYPOINT ["opencode"]
