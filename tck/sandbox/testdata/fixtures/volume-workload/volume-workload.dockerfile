FROM docker/sandbox-templates:claude-code-docker
COPY --chmod=0755 kit-tck-volume /usr/local/bin/kit-tck-volume
RUN mkdir -p /var/tmp/kit-tck-volume /var/tmp/kit-tck-volume-other \
 && printf replacement > /var/tmp/kit-tck-volume/image-marker
ENTRYPOINT ["sleep", "infinity"]
