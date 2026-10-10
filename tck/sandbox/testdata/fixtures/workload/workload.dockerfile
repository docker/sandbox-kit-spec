# The suite execs commands rather than inspecting the runtime, so the
# workload ships the probes those checks call. They live here, in the kit
# under test, because the contract passes exec argv through unmodified: an
# adapter cannot be expected to provide them.
#
# Each probe carries its mode on the COPY rather than earning it from a
# later RUN chmod: the platform floor runs builds as the unprivileged
# agent user, which cannot chmod what COPY laid down as root, so a build
# step would fail on every conforming base image.
FROM docker/sandbox-templates:claude-code-docker

# Reports whether a URL is reachable. Exit 0 is reachable; 7 is a refusal
# at the connection boundary, which is what an egress policy produces;
# anything else is a transport failure the caller should not read as
# policy.
# Exit 0 when the ORIGIN answered, 7 when blocked by policy, curl's own
# status for anything else. Reaching the origin is proven by content, not
# by status: the fixture hosts are the IANA example domains, whose stable
# page is a known marker, so a policy proxy answering 200 or 403 with its
# own body still reads as blocked — an HTTP status alone cannot tell the
# boundary's answer from the origin's. DNS failure (curl 6) and a drop
# until timeout (28) map to blocked: both are policy mechanisms runtimes
# really use, and every deny assertion runs after an allowed-host probe
# succeeded in the same sandbox, ruling out a general outage. The residual
# ambiguity — the origin itself failing — is exactly an origin outage, and
# no probe against a public origin can see through that.
COPY --chmod=0755 <<'PROBE' /usr/local/bin/kit-tck-probe
#!/bin/sh
body=$(curl --silent --max-time 10 "$1")
status=$?
case "$status" in
0)
  case "$body" in
  *"Example Domain"*) exit 0 ;;
  *) exit 7 ;;
  esac
  ;;
6 | 7 | 28) exit 7 ;;
*) exit "$status" ;;
esac
PROBE

# The same observation for one HTTP request rather than a connection:
# `kit-tck-http-probe METHOD URL`. Exit 7 means the boundary answered 403,
# 0 means the request reached the origin, and anything else is a failure
# to observe either.
#
# Exit 7 is reserved for that 403 alone, because the capability page
# requires an HTTP refusal to carry it. Two weaker readings both pass a
# non-conforming runtime: counting every 4xx would accept the origin's own
# 404 or 405 from a runtime enforcing nothing, and counting a dropped or
# timed-out connection would accept a runtime that refuses by hanging up,
# which the page rules out precisely because the sandbox cannot tell it
# from a broken network. Any other status means the request got through,
# whatever the origin then made of it.
#
# Curl's own exit 7 (failed to connect) is remapped so it cannot be read
# as this probe's sentinel; 101 is outside curl's range.
COPY --chmod=0755 <<'HTTPPROBE' /usr/local/bin/kit-tck-http-probe
#!/bin/sh
# Reached-the-origin is proven by the IANA page's content, the same marker
# kit-tck-probe uses: a 403 alone could be the origin's own answer (POST
# against a static site), and counting it as the boundary's refusal would
# certify a runtime enforcing no HTTP policy at all. The residual is an
# origin that fails while policy is absent, which no probe against a
# public origin can see through.
# Exit 0: a completed response other than 403 — the boundary let the
# request through (the fixture paths do not exist at the origin, so origin
# content cannot be the through-signal here; a 404 is the origin's answer
# arriving). Exit 7: an observed 403, the refusal status the page
# requires; a boundary answering anything else satisfies no refusal
# assertion. Exit 101: connection refused. The one ambiguity — the origin
# itself answering 403 — is attributed by the same-method control probe
# each refusal check runs against an allowed host first: if that origin
# family 403s the method, the control fails loudly instead of certifying.
code=$(curl --silent --max-time 10 --output /dev/null \
  --write-out '%{http_code}' --request "$1" "$2")
status=$?
if [ "$status" -ne 0 ]; then
  [ "$status" -eq 7 ] && exit 101
  exit "$status"
fi
[ "$code" = "403" ] && exit 7
exit 0
HTTPPROBE

# A probe operating on the mounted path, so host sharing is observed
# through real filesystem operations rather than an adapter assertion.
COPY --chmod=0755 <<'HOSTMOUNT' /usr/local/bin/kit-tck-host-mount
#!/bin/sh
set -eu
operation="$1"; root="$2"; name="${3:-}"; value="${4:-}"
case "$operation" in
write) printf '%s' "$value" >"$root/$name" ;;
read) cat "$root/$name" ;;
absent) test ! -e "$root/$name" ;;
writable) printf '%s' "$value" >"$root/$name"; id -u ;;
mode) stat -c '%a' "$root" ;;
set-mode) chmod "$value" "$root" ;;
*) exit 1 ;;
esac
HOSTMOUNT

# Writes a file, so volume persistence is observable without a shell
# redirect the adapter would have to quote.
COPY --chmod=0755 <<'WRITE' /usr/local/bin/kit-tck-write
#!/bin/sh
mkdir -p "$(dirname "$1")" && printf '%s' "$2" > "$1"
WRITE

# Reports the memory limit the sandbox runs under, across cgroup v2 and v1.
COPY --chmod=0755 <<'MEM' /usr/local/bin/kit-tck-memory-limit
#!/bin/sh
if [ -r /sys/fs/cgroup/memory.max ]; then cat /sys/fs/cgroup/memory.max
elif [ -r /sys/fs/cgroup/memory/memory.limit_in_bytes ]; then cat /sys/fs/cgroup/memory/memory.limit_in_bytes
else echo unknown; fi
MEM

# Reports whether the sandbox runs elevated, by testing the bit directly in
# the bounding capability set (the script explains why not the effective
# one). `capsh --print` also lists capabilities it
# does NOT have — negated in the IAB set — so grepping its output for a
# name reports yes in an unprivileged sandbox. Reading /proc also avoids
# depending on capsh being installed at all.
COPY --chmod=0755 <<'PRIV' /usr/local/bin/kit-tck-privileged
#!/bin/sh
# The BOUNDING set, not the effective one: a sandbox running as a
# non-root user has no effective capabilities whether or not it is
# elevated, so reading CapEff reports "no" for a privileged sandbox.
#
# One capability is not evidence of the platform's privileged mode — an
# ordinary container started with --cap-add SYS_ADMIN has that bit while
# keeping the rest of the non-privileged boundary. Elevation is judged by
# the combination no default profile produces: CAP_SYS_ADMIN (21) plus
# CAP_SYS_MODULE (16) and CAP_SYS_RAWIO (17), which no runtime grants by
# default, and seccomp mode 0, which privileged mode implies and the
# default filter (mode 2) rules out.
bnd=$(awk '/^CapBnd:/ {print $2}' /proc/self/status)
seccomp=$(awk '/^Seccomp:/ {print $2}' /proc/self/status)
[ -n "$bnd" ] || { echo unknown; exit 0; }
dec=$(printf '%d' "0x$bnd" 2>/dev/null) || { echo unknown; exit 0; }
has() { [ $(( (dec >> $1) & 1 )) -eq 1 ]; }
if has 21 && has 16 && has 17 && [ "${seccomp:-0}" = "0" ]; then echo yes; else echo no; fi
PRIV

# Observes the SSH agent a sandbox was given, one agent-protocol request
# at a time: the script's docstring lists what it sends. It speaks the
# protocol itself rather than through ssh-add because the checks need
# requests ssh-add cannot make on demand — a namespaced signature, a login
# after a session binding, a bare request of any type — and it prints the
# data and signature of every signing request so the suite verifies them
# itself.
COPY --chmod=0755 kit-tck-ssh-agent.py /usr/local/bin/kit-tck-ssh-agent

# A bare `bash` reads stdin, and the contract promises the adapter neither
# a TTY nor anything on it, so it can exit at EOF — leaving a
# process-based runtime with an already-stopped sandbox and failing every
# exec check for reasons that have nothing to do with capabilities. The
# fixture waits instead, and waits without spinning.
# The suite's checks read the agent-context profile beside the workspace,
# so the workload pins its workdir where the contract says the suite will
# look: a runtime placing the profile beside the workload's own workdir
# writes it as this directory's sibling, which is exactly where the suite
# reads.
# The canary the inject-only check compares by value: the image owns it,
# so it is identical in every composition, and a runtime smuggling a
# credential into an existing variable is judged against a variable the
# suite controls rather than every stable name (runtime-owned variables
# may legitimately vary with the kit set).
ENV KIT_TCK_CANARY=image-baseline
# Distinct from the files fixture's argument export and create-time override.
ENV KIT_GREETING=image-greeting

# Capture the entrypoint's own environment before it starts waiting: exec
# commands can have different credential and SSH grants from the original
# workload. Publish the credential record atomically so an empty value is
# evidence of absence, not a read before the write completed.
COPY --chmod=0755 <<'ENTRY' /usr/local/bin/kit-tck-workload-entrypoint
#!/bin/sh
printf '%s' "${KIT_TCK_PHASE_TOKEN:-}" > /var/tmp/kit-tck-workload-credential.tmp
mv /var/tmp/kit-tck-workload-credential.tmp /var/tmp/kit-tck-workload-credential
printf '%s' "${SSH_AUTH_SOCK:-}" > /var/tmp/kit-tck-workload-ssh-sock
kit-tck-ssh-agent observe > /var/tmp/kit-tck-workload-ssh-sock-proof.tmp 2>&1 || true
mv /var/tmp/kit-tck-workload-ssh-sock-proof.tmp /var/tmp/kit-tck-workload-ssh-sock-proof
exec sleep infinity
ENTRY

WORKDIR /home/agent/workspace
ENTRYPOINT ["kit-tck-git-identity", "workload", "/usr/local/bin/kit-tck-workload-entrypoint"]

COPY --chmod=0755 kit-tck-git-identity /usr/local/bin/kit-tck-git-identity
RUN git config --global user.name "Image Author" \
 && git config --global user.email image@example.invalid \
 && git config --global alias.kit-tck-guest status

# The image marker distinguishes a fresh empty mount from copying image
# contents into it. The unmounted path remains writable for the control.
COPY --chmod=0755 kit-tck-volume /usr/local/bin/kit-tck-volume
RUN mkdir -p /var/tmp/kit-tck-volume /var/tmp/kit-tck-volume-other \
 && printf image > /var/tmp/kit-tck-volume/image-marker
