FROM docker/sandbox-templates:claude-code-docker
COPY --chmod=0755 probe /usr/local/bin/kit-tck-bundled
COPY --chmod=0755 entrypoint /usr/local/bin/kit-tck-bundled-entrypoint
ENTRYPOINT ["/usr/local/bin/kit-tck-bundled-entrypoint"]
