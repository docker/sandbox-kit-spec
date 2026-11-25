FROM alpine:3.22
RUN apk add --no-cache bash curl git ca-certificates && adduser -D -u 1000 agent
COPY --chmod=0755 probe /usr/local/bin/kit-tck-clipboard
COPY --chmod=0755 entrypoint /usr/local/bin/kit-tck-clipboard-entrypoint
# COPY --chmod applies its mode to the parents it creates, and a 0644
# directory cannot be traversed by the agent user; create it first.
RUN mkdir -p /usr/share/kit-tck
COPY --chmod=0644 clipboard.png /usr/share/kit-tck/clipboard.png
USER agent
WORKDIR /home/agent
ENTRYPOINT ["kit-tck-clipboard-entrypoint"]
