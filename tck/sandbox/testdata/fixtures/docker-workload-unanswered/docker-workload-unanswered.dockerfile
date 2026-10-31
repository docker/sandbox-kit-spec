# The stand-in runs and never binds /var/run/docker.sock, so GET /_ping
# never answers however long the runtime waits.
FROM python:3.13-alpine
RUN adduser -D -u 1000 agent
COPY --chmod=0755 <<'DOCKERD' /usr/local/bin/dockerd
#!/bin/sh
exec sleep infinity
DOCKERD
USER agent
WORKDIR /home/agent
CMD ["sleep", "infinity"]
