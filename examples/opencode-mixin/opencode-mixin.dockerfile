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

# Retain the Node runtime this mixin supplies for integrations. Both
# binaries use glibc, matching the trixie floor of these workloads; the
# frontend exports only the delta over this base.
FROM dhi.io/node:24-debian13-dev AS runtime
FROM dhi.io/debian-base:trixie-dev
COPY --from=runtime /usr/bin/node /usr/local/bin/node
COPY --from=build /out /
