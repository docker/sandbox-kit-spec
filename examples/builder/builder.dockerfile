# syntax=dhi/build:2-debian13

# The kit's content recipe is a DHI build definition, not a Dockerfile: the
# kit frontend forwards it to the frontend this syntax line names, the way
# it forwards any foreign frontend. The DHI frontend assembles the rootfs
# from DHI deb packages directly — no apt, no dpkg, no package manager in
# the final image — and squashes the result.

name: Kit Builder
# The DHI frontend requires a namespaced image name; without a slash the
# build fails with a bare exit code 2.
image: docker/sbx-kit-builder
output: squashed
platforms:
    - linux/amd64
    - linux/arm64

contents:
    repositories:
        - deb [signed-by=/usr/share/keyrings/dhi-deb-main.gpg] http://dhi.io/deb/debian/main trixie main
    keyring:
        - https://dhi.io/keyring/dhi-deb-gpg.D46852F6925E9F71.key
    packages:
        - base-files
        - bash
        - ca-certificates
        - containerd.io
        - coreutils
        - curl
        - dash
        - docker-buildx-plugin
        - docker-ce
        - docker-ce-cli
        - findutils
        - git
        - grep
        - gzip
        - iproute2
        # DHI supplies nft-backed aliases. Restating them under paths
        # follows the existing links and collides with their target.
        - iptables
        - libc-bin
        - libpam-modules
        - libpam-runtime
        - mount
        - netbase
        - procps
        - psmisc
        - sed
        - tar
        - util-linux

accounts:
    root: true
    run-as: agent
    users:
        - name: agent
          uid: 1000
          gid: 1000
    groups:
        - name: agent
          gid: 1000
          members:
            - agent
        - name: docker
          gid: 999
          members:
            - agent

work-dir: /home/agent/workspace

environment:
    # Baked into the image config so every exec session — login shell or
    # not — selects the container-driver builder without relying on
    # `docker buildx use` state, which is per-user and endpoint-keyed.
    BUILDX_BUILDER: sbx-builder
    PATH: /usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

paths:
    - type: directory
      path: /home/agent/workspace
      uid: 1000
      gid: 1000
      mode: "0755"
    # The sandbox daemon's kit registry facade serves the engine store
    # over plain HTTP on the host loopback (fixed port, see
    # DefaultKitRegistryAddr); from inside the sandbox that endpoint is
    # host.docker.internal. dockerd refuses plain-HTTP registries unless
    # they are declared insecure, so bake the declaration in. A daemon
    # running with a non-default facade port simply doesn't match, and
    # kit builds fall back to archive delivery.
    - type: directory
      path: /etc/docker
      uid: 0
      gid: 0
      mode: "0755"
    - type: file
      path: /etc/docker/daemon.json
      content: |
        {
          "insecure-registries": ["host.docker.internal:5411"]
        }
      uid: 0
      gid: 0
      mode: "0644"
    # The sandbox runtime's CA installer writes the egress proxy's MITM CA
    # to /usr/local/share/ca-certificates/proxy-ca.crt before merging it
    # into the trust bundle. Without this directory the installer fails
    # silently and every TLS connection through the proxy — including the
    # inner dockerd's registry pulls — dies with an unknown-authority error.
    - type: directory
      path: /usr/local/share/ca-certificates
      uid: 0
      gid: 0
      mode: "0755"
    # The sandbox durable-startup dispatcher runs hooks as root via
    # `su -s /bin/sh -c '...' <user>`. The hardened base's PAM defaults
    # prompt even root for a password, which hangs every startup hook —
    # so ship the same permissive PAM stack the DHI sandbox templates use,
    # with pam_rootok letting root su without a password.
    - type: directory
      path: /etc/pam.d
      uid: 0
      gid: 0
      mode: "0755"
    - type: file
      path: /etc/pam.d/common-auth
      content: |
        auth    [success=1 default=ignore]    pam_unix.so nullok
        auth    requisite                      pam_deny.so
        auth    required                       pam_permit.so
      uid: 0
      gid: 0
      mode: "0644"
    - type: file
      path: /etc/pam.d/common-account
      content: |
        account [success=1 new_authtok_reqd=done default=ignore]    pam_unix.so
        account requisite                      pam_deny.so
        account required                       pam_permit.so
      uid: 0
      gid: 0
      mode: "0644"
    - type: file
      path: /etc/pam.d/common-session
      content: |
        session [default=1]                    pam_permit.so
        session requisite                      pam_deny.so
        session required                       pam_permit.so
        session required                       pam_unix.so
      uid: 0
      gid: 0
      mode: "0644"
    - type: file
      path: /etc/pam.d/common-session-noninteractive
      content: |
        session [default=1]                    pam_permit.so
        session requisite                      pam_deny.so
        session required                       pam_permit.so
        session required                       pam_unix.so
      uid: 0
      gid: 0
      mode: "0644"
    - type: file
      path: /etc/pam.d/common-password
      content: |
        password [success=1 default=ignore]    pam_unix.so obscure yescrypt
        password requisite                     pam_deny.so
        password required                      pam_permit.so
      uid: 0
      gid: 0
      mode: "0644"
    - type: file
      path: /etc/pam.d/su
      content: |
        auth    sufficient    pam_rootok.so
        @include common-auth
        @include common-account
        @include common-session
      uid: 0
      gid: 0
      mode: "0644"

annotations:
    org.opencontainers.image.description: DHI-based Docker-in-Docker builder for sandbox kits

# Transition: runtimes that predate dockerd@1 start the engine for this label.
labels:
    com.docker.sandboxes.start-docker: "true"

# No tini: under sbx the daemon's init is PID 1 and the interactive attach
# re-execs entrypoint+cmd as an ordinary process, where tini only warns
# that it is not PID 1. Plain `docker run` gets bash as PID 1, which is
# fine for debugging.
cmd:
    - bash
    - -l
