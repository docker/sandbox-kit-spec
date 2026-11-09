FROM python:3.13-alpine
RUN apk add --no-cache bash curl && adduser -D -u 1000 agent && mkdir -p /etc/docker && chown agent:agent /etc/docker
COPY --chmod=0755 dockerd.py /usr/local/bin/dockerd
USER agent
WORKDIR /home/agent
CMD ["sleep", "infinity"]
