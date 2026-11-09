FROM alpine:3.22
RUN apk add --no-cache bash curl git ca-certificates && adduser -D -u 1000 agent
COPY --chmod=0755 probe /usr/local/bin/kit-tck-open-url
COPY --chmod=0755 entrypoint /usr/local/bin/kit-tck-open-url-entrypoint
USER agent
WORKDIR /home/agent
ENTRYPOINT ["kit-tck-open-url-entrypoint"]
