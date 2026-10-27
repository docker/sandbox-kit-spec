# syntax=docker/dockerfile:1
# Why not copy /usr/local out of the official python image: that interpreter
# links the build image's own libssl, libffi and libsqlite, so it breaks on a
# base with different ones. python-build-standalone is built to be relocated
# and is what `uv python install` fetches, so the build stage uses uv to get
# it and the overlay ships the tree. Bump the pins together with `provides`
# in python.yaml.
FROM ghcr.io/astral-sh/uv:0.13.6 AS uv
FROM ghcr.io/astral-sh/uv:0.13.6-debian-slim AS build
ENV UV_PYTHON_INSTALL_DIR=/opt/uv-python
RUN uv --version | grep -q '^uv 0\.13\.6 ' \
 && uv python install 3.14.10 \
 && mv "$(dirname "$(dirname "$(uv python find 3.14.10)")")" /opt/python \
 && /opt/python/bin/python3 -c 'import sys, ssl, sqlite3, ctypes, venv; assert sys.version.split()[0] == "3.14.10", sys.version'

# The overlay: one tree under /opt and two binaries, nothing under /home/agent.
FROM scratch
COPY --from=build /opt/python /opt/python
COPY --from=uv /uv /uvx /usr/local/bin/

# PATH merges by appending, so a workload's own python3 keeps precedence.
ENV PATH=/opt/python/bin
