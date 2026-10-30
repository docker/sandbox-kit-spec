# The sandbox's launch is the server, and no agent is ever launched in
# it, so the platform floor §3.5 asks of agent workloads buys nothing
# here. The hardened Node runtime image is enough, and it already runs
# as an unprivileged user.
FROM dhi.io/node:24-debian13
WORKDIR /app
COPY scripts/server.mjs /app/server.mjs
ENTRYPOINT ["node", "/app/server.mjs"]
CMD []
