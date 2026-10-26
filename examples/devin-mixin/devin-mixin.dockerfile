# syntax=docker/dockerfile:1
# Cognition publishes no pinnable public install channel for the devin
# CLI, so the template image is the distribution: the overlay copies the
# binary the template ships. The kit's version: field speaks for the
# artifact, not for a binary version the channel cannot pin.
# The hardened dhi.io/sbx-templates mirror carries every other
# agent template but not devin, so this one stays on the public
# tag until it is mirrored.
FROM docker/sandbox-templates:devin-docker AS build
USER root
# The template's devin command is a wrapper; devin-cli points at the
# versioned payload. Dereference it so the overlay is independent of
# the template's home directory and launcher.
RUN mkdir -p /out/usr/local/bin \
 && cp -L /home/agent/.local/bin/devin-cli /out/usr/local/bin/devin \
 && chown 0:0 /out/usr/local/bin/devin \
 && chmod 0755 /out/usr/local/bin/devin

FROM scratch
COPY --from=build /out /
