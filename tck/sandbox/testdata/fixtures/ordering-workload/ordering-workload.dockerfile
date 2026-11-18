FROM python:3.13-alpine
RUN apk add --no-cache bash curl git ca-certificates && adduser -D -u 1000 agent \
    && mkdir -p /home/agent/workspace && chown agent:agent /home/agent/workspace \
    && touch /etc/sandbox-persistent.sh
COPY --chmod=0755 dockerd.py /usr/local/bin/dockerd
COPY --chmod=0755 probe.py /usr/local/bin/kit-tck-ordering
COPY --chmod=0644 daemon.json /etc/docker/daemon.json
ENV HOME=/home/agent BASH_ENV=/etc/sandbox-persistent.sh
USER agent
WORKDIR /home/agent/workspace
ENTRYPOINT ["python3", "/usr/local/bin/kit-tck-ordering", "workload"]
