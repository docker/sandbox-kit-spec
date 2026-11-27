FROM alpine:3.22 AS build
RUN apk add --no-cache gcc musl-dev wayland-dev
COPY probe.c /src/probe.c
RUN cc -Wall -Wextra -Werror /src/probe.c -lwayland-client -o /kit-tck-display

FROM alpine:3.22
RUN apk add --no-cache bash ca-certificates curl git wayland-libs-client && adduser -D -u 1000 agent
COPY --from=build /kit-tck-display /usr/local/bin/kit-tck-display
USER agent
WORKDIR /home/agent
ENTRYPOINT ["sleep", "infinity"]
