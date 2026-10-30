# A workload without the start-docker label the conventional base carries,
# so an engine that starts here was started for a reason the suite chose.
# The dockerd on PATH is a stand-in: the checks need records of when it
# ran and what storage it saw, which a real engine does not leave.
FROM python:3.13-alpine
RUN apk add --no-cache bash curl git ca-certificates && adduser -D -u 1000 agent
COPY --chmod=0755 dockerd.py /usr/local/bin/dockerd
# dockerd refuses to start when a directive comes from both a flag and
# this file, so a runtime passing --host or --data-root here would break
# a real engine. The values are the ones the capability promises anyway.
COPY --chmod=0644 <<'CONFIG' /etc/docker/daemon.json
{"hosts": ["unix:///var/run/docker.sock"], "data-root": "/var/lib/docker"}
CONFIG
# Reports whether the stand-in engine has started, and marks the writable
# layer so a check can tell that recreation discarded it.
COPY --chmod=0755 <<'PROBE' /usr/local/bin/kit-tck-docker
#!/bin/sh
case "$1" in
started) test -e /var/tmp/kit-tck-docker/boot ;;
mark) : > /var/tmp/kit-tck-docker-scratch ;;
marked) test -e /var/tmp/kit-tck-docker-scratch ;;
*) exit 1 ;;
esac
PROBE
# The workload fixture's elevation probe, repeated here so dockerd@1 is
# judged on an image without the start-docker label. The workload
# fixture's copy explains the bits it reads.
COPY --chmod=0755 <<'PRIV' /usr/local/bin/kit-tck-privileged
#!/bin/sh
bnd=$(awk '/^CapBnd:/ {print $2}' /proc/self/status)
seccomp=$(awk '/^Seccomp:/ {print $2}' /proc/self/status)
[ -n "$bnd" ] || { echo unknown; exit 0; }
dec=$(printf '%d' "0x$bnd" 2>/dev/null) || { echo unknown; exit 0; }
has() { [ $(( (dec >> $1) & 1 )) -eq 1 ]; }
if has 21 && has 16 && has 17 && [ "${seccomp:-0}" = "0" ]; then echo yes; else echo no; fi
PRIV
USER agent
WORKDIR /home/agent
CMD ["sleep", "infinity"]
