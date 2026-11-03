# syntax=docker/dockerfile:1
FROM ubuntu:24.04

# A custom workload supplies the platform floor as well as the engine.
# Packages come from Ubuntu and Docker's signed Ubuntu repository; their
# actual versions become derived deb/ provides when the Kit is built.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      bash ca-certificates curl git iproute2 iptables procps sudo \
 && install -m 0755 -d /etc/apt/keyrings \
 && curl -fsSL https://download.docker.com/linux/ubuntu/gpg \
      -o /etc/apt/keyrings/docker.asc \
 && chmod 0644 /etc/apt/keyrings/docker.asc \
 && printf 'deb [arch=%s signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu noble stable\n' \
      "$(dpkg --print-architecture)" > /etc/apt/sources.list.d/docker.list \
 && apt-get update \
 && apt-get install -y --no-install-recommends \
      docker-ce docker-ce-cli containerd.io \
      docker-buildx-plugin docker-compose-plugin \
 && rm -rf /var/lib/apt/lists/*

# Ubuntu already reserves uid/gid 1000. Rename that account instead of
# leaving a second home or choosing a different identity for agent mixins.
RUN usermod --login agent --home /home/agent --move-home ubuntu \
 && groupmod --new-name agent ubuntu \
 && usermod --shell /bin/bash --append --groups docker,sudo agent \
 && printf 'agent ALL=(ALL) NOPASSWD:ALL\n' > /etc/sudoers.d/agent \
 && chmod 0440 /etc/sudoers.d/agent \
 && install -d -o agent -g agent /home/agent/workspace \
 && mkdir -p /usr/local/share/ca-certificates \
 && touch /etc/sandbox-persistent.sh \
 && printf '. /etc/sandbox-persistent.sh\n' > /etc/profile.d/sandbox-persistent.sh

# sbx 0.50.1 predates dockerd@1 and starts the engine for this label.
# The runtime owns daemon startup; bash is the user's launch command.
LABEL com.docker.sandboxes.start-docker="true"
ENV BASH_ENV=/etc/sandbox-persistent.sh
USER agent
WORKDIR /home/agent/workspace
ENTRYPOINT ["bash"]
CMD ["-l"]
