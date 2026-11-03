# syntax=docker/dockerfile:1
FROM registry.access.redhat.com/ubi9/ubi:9.6

# A custom workload supplies the platform floor as well as the engine.
# UBI ships curl-minimal; allow dnf to replace it with the full curl.
RUN dnf install -y --allowerasing \
      bash ca-certificates curl git iproute iptables procps-ng \
      shadow-utils sudo util-linux \
 && curl -fsSL https://download.docker.com/linux/rhel/docker-ce.repo \
      -o /etc/yum.repos.d/docker-ce.repo \
 && dnf --releasever=9 install -y \
      docker-ce docker-ce-cli containerd.io \
      docker-buildx-plugin docker-compose-plugin \
 && dnf clean all \
 && rm -rf /var/cache/dnf

RUN groupadd --gid 1000 agent \
 && useradd --uid 1000 --gid agent --groups docker,wheel \
      --create-home --shell /bin/bash agent \
 && printf 'agent ALL=(ALL) NOPASSWD:ALL\n' > /etc/sudoers.d/agent \
 && chmod 0440 /etc/sudoers.d/agent \
 && install -d -o agent -g agent /home/agent/workspace \
 && mkdir -p /usr/local/share/ca-certificates \
 && touch /etc/sandbox-persistent.sh \
 && printf '. /etc/sandbox-persistent.sh\n' > /etc/profile.d/sandbox-persistent.sh

# Older runtimes start Docker for this label instead of dockerd@1.
# The runtime owns daemon startup; bash is the user's launch command.
LABEL com.docker.sandboxes.start-docker="true"
ENV BASH_ENV=/etc/sandbox-persistent.sh
USER agent
WORKDIR /home/agent/workspace
ENTRYPOINT ["bash"]
CMD ["-l"]
