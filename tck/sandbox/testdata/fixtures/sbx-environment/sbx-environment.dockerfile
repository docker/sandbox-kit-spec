FROM docker/sandbox-templates:claude-code-docker
USER root
RUN groupadd --gid 1234 sbxagent \
 && useradd --create-home --uid 1234 --gid 1234 --shell /bin/bash sbxagent \
 && mkdir -p /home/sbxagent/.config \
 && chown -R sbxagent:sbxagent /home/sbxagent \
 && touch /etc/sbx-env-persistent.sh
COPY --chmod=755 probe.py /usr/local/bin/kit-tck-sbx-env
# Different values at each input layer expose incorrect precedence.
ENV BASH_ENV=/etc/sbx-env-persistent.sh WORKSPACE_DIR=/image/workspace \
    SANDBOX_NAME=image-name SANDBOX_ID=image-id IS_SANDBOX=image-marker
USER sbxagent
WORKDIR /home/sbxagent/project
ENTRYPOINT ["kit-tck-sbx-env", "launch"]
