# An MCP server Kit's image is the server, not a sandbox: no platform
# floor, no agent user, no shell. The hardened Node runtime image is
# enough, and it already runs as an unprivileged user.
FROM dhi.io/node:24-debian13
WORKDIR /app
COPY scripts/server.mjs /app/server.mjs
ENTRYPOINT ["node", "/app/server.mjs"]
CMD []
