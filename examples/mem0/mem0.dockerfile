# syntax=docker/dockerfile:1
# The dashboard is distributed as source beside the Mem0 server. Build
# both from the release tag so its API and browser contracts stay paired.
FROM node:24-trixie-slim AS upstream
USER root
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates git \
    && rm -rf /var/lib/apt/lists/*
ARG MEM0_VERSION=2.2.1
RUN git clone --branch "v${MEM0_VERSION}" --depth 1 \
      https://github.com/mem0ai/mem0.git /src \
    && test "$(sed -n 's/^version = "\(.*\)"/\1/p' /src/pyproject.toml)" = "${MEM0_VERSION}"
WORKDIR /src/server/dashboard
RUN npm install --global pnpm@10.34.2 \
    && pnpm install --frozen-lockfile
ENV NEXT_TELEMETRY_DISABLED=1 \
    NEXT_PUBLIC_API_URL=NEXT_PUBLIC_API_URL \
    NEXT_PUBLIC_INSTANCE_NAME=NEXT_PUBLIC_INSTANCE_NAME
RUN pnpm build

# A service needs Python, PostgreSQL and Node, not an agent or Docker
# engine. Keep all three in one image because a Kit has one workload.
FROM python:3.12-slim-trixie
USER root
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
      bsdextrautils hostname \
    && apt-get install -y --no-install-recommends \
      bash ca-certificates curl nodejs passwd postgresql-17 \
      postgresql-17-pgvector util-linux \
    && rm -rf /var/lib/apt/lists/* \
    # The minimal image omits generated common-* files, but the runtime
    # uses su for startup hooks and /etc/pam.d/su includes those files.
    && DEBIAN_FRONTEND=noninteractive pam-auth-update --force --enable unix \
    && groupadd --gid 1000 agent \
    && useradd --uid 1000 --gid 1000 --create-home --shell /bin/bash agent \
    && install -d -o agent -g agent -m 0700 /var/lib/mem0 \
    && install -d -o agent -g agent /home/agent/workspace \
    && install -d /usr/local/share/ca-certificates \
    && install -o agent -g agent -m 0644 /dev/null /etc/sandbox-persistent.sh

ARG MEM0_VERSION=2.2.1
ENV PATH=/opt/mem0/venv/bin:/usr/lib/postgresql/17/bin:${PATH}
RUN python -m venv /opt/mem0/venv
COPY --from=upstream /src/server/requirements.txt /tmp/mem0-requirements.txt
RUN sed -i '/^pydantic\[/d' /tmp/mem0-requirements.txt \
    # MCP 2.3 needs Pydantic 2.12; the server's FastAPI and Mem0 ranges
    # support it, despite upstream requirements pinning 2.10.4 exactly.
    && pip install --no-cache-dir \
      "mem0ai==${MEM0_VERSION}" mcp==2.3.0 \
      "pydantic[email]==2.12.5" -r /tmp/mem0-requirements.txt \
    && python -c 'import importlib.metadata, sys; assert importlib.metadata.version("mem0ai") == sys.argv[1]' "${MEM0_VERSION}" \
    && pip check \
    && rm /tmp/mem0-requirements.txt

COPY --chown=agent:agent --from=upstream /src/server/*.py /opt/mem0/server/
COPY --chown=agent:agent --from=upstream /src/server/alembic.ini /opt/mem0/server/
COPY --chown=agent:agent --from=upstream /src/server/alembic /opt/mem0/server/alembic
COPY --chown=agent:agent --from=upstream /src/server/routers /opt/mem0/server/routers
COPY --chown=agent:agent --from=upstream /src/server/dashboard/public /opt/mem0/dashboard/public
COPY --chown=agent:agent --from=upstream /src/server/dashboard/.next/standalone /opt/mem0/dashboard
COPY --chown=agent:agent --from=upstream /src/server/dashboard/.next/static /opt/mem0/dashboard/.next/static
COPY --chown=agent:agent src/app.py src/mcp_tools.py /opt/mem0/server/
COPY --chown=agent:agent --chmod=0755 src/start.sh /opt/mem0/start.sh

ENV PGDATA=/var/lib/mem0/postgres \
    POSTGRES_HOST=127.0.0.1 \
    POSTGRES_PORT=5432 \
    POSTGRES_DB=postgres \
    POSTGRES_USER=agent \
    POSTGRES_COLLECTION_NAME=memories \
    APP_DB_NAME=mem0_app \
    HISTORY_DB_PATH=/var/lib/mem0/history.db \
    DASHBOARD_URL=http://127.0.0.1:3000 \
    NEXT_PUBLIC_API_URL=http://127.0.0.1:8000 \
    API_INTERNAL_URL=http://127.0.0.1:8000 \
    NEXT_PUBLIC_INSTANCE_NAME=Mem0 \
    MEM0_TELEMETRY=false \
    BASH_ENV=/etc/sandbox-persistent.sh \
    PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1
USER agent
WORKDIR /home/agent/workspace
EXPOSE 3000 8000
# The runtime starts the stack through lifecycle@1. Plain docker run
# remains an interactive shell for inspection.
ENTRYPOINT ["bash"]
CMD ["-l"]
